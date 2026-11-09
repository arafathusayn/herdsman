package wtree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arafathusayn/herdsman/internal/gitrefs"
	"github.com/arafathusayn/herdsman/internal/gitrun"
)

const (
	headsPrefix   = "refs/heads/"
	remoteHeadRef = "refs/remotes/origin/HEAD"
	remotePrefix  = "refs/remotes/origin/"
)

// RefSource is the part of the ref reader that collection uses.
type RefSource interface {
	Refs(ctx context.Context) ([]gitrefs.Ref, error)
	Heads(ctx context.Context) ([]gitrefs.Head, error)
	AheadBehind(ctx context.Context, base string) (map[string]gitrefs.Divergence, error)
}

// Row is one worktree in the table. Skeleton sets the identity fields without
// starting a process; Fill sets the rest.
type Row struct {
	Task      string
	Branch    string // Short name; empty when detached or bare.
	Path      string
	Head      string // Commit; empty when unborn or bare.
	Main      bool
	Bare      bool
	Detached  bool
	Locked    bool
	IsDefault bool // The branch is the repository's default branch.

	Status        Status
	Divergence    gitrefs.Divergence // Against the default branch.
	HasDivergence bool
	Subject       string
	CommitTime    time.Time
	Err           error // What could not be filled; other cells remain valid.
}

// Marks renders the state of the worktree itself for a table cell.
func (r Row) Marks() string {
	marks := ""
	if r.Detached {
		marks += "⊘"
	}
	if r.Locked {
		marks += "⊞"
	}
	return marks
}

// Collector gathers the worktree table of one repository.
type Collector struct {
	Refs    RefSource
	State   *State
	Git     *gitrun.Runner
	Workers int // Concurrent status calls; zero means the core count.

	statusHook func() func() // Test seam: runs around each status call.
}

func (c *Collector) workers() int {
	if c.Workers > 0 {
		return c.Workers
	}
	return runtime.NumCPU()
}

// Collect returns the complete table.
func (c *Collector) Collect(ctx context.Context) ([]Row, error) {
	rows, err := c.Skeleton(ctx)
	if err != nil {
		return nil, err
	}
	if err := c.Fill(ctx, rows, nil); err != nil {
		return nil, err
	}
	return rows, nil
}

// Skeleton returns one row per worktree from the repository's files and the
// state database, starting no process unless the ref reader has to fall back.
func (c *Collector) Skeleton(ctx context.Context) ([]Row, error) {
	heads, err := c.Refs.Heads(ctx)
	if err != nil {
		return nil, fmt.Errorf("wtree: read worktrees: %w", err)
	}
	refs, err := c.Refs.Refs(ctx)
	if err != nil {
		return nil, fmt.Errorf("wtree: read refs: %w", err)
	}
	recorded, err := c.State.Worktrees(ctx)
	if err != nil {
		return nil, fmt.Errorf("wtree: read state: %w", err)
	}
	tasks := make(map[string]string, len(recorded))
	for _, worktree := range recorded {
		tasks[worktree.Path] = worktree.Task
	}
	defaultName := defaultBranch(refs)
	rows := make([]Row, 0, len(heads))
	for i, head := range heads {
		row := Row{
			Task: filepath.Base(head.Path), Path: head.Path, Head: head.Object, Main: i == 0,
			Branch: strings.TrimPrefix(head.Branch, headsPrefix),
			Bare:   head.State == gitrefs.HeadBare, Detached: head.State == gitrefs.HeadDetached,
		}
		if task, ok := tasks[head.Path]; ok {
			row.Task = task
		}
		row.IsDefault = row.Branch != "" && row.Branch == defaultName
		row.Locked = !row.Main && isLocked(head.Path)
		rows = append(rows, row)
	}
	return append(rows, c.cloneRows(ctx, recorded, rows, defaultName)...), nil
}

// cloneRows adds the recorded tasks that are repositories of their own, which
// git's worktree list cannot name. A clone that is gone or unreadable has no
// row, as a removed worktree has none.
func (c *Collector) cloneRows(ctx context.Context, recorded []Worktree, known []Row, defaultName string) []Row {
	var out []Row
	for _, worktree := range recorded {
		if slices.ContainsFunc(known, func(r Row) bool { return r.Path == worktree.Path }) {
			continue
		}
		if info, err := os.Lstat(filepath.Join(worktree.Path, ".git")); err != nil || !info.IsDir() {
			continue
		}
		reader, err := gitrefs.Open(worktree.Path)
		if err != nil {
			continue
		}
		heads, err := reader.Heads(ctx)
		if err != nil || len(heads) == 0 {
			continue
		}
		branch := strings.TrimPrefix(heads[0].Branch, headsPrefix)
		out = append(out, Row{
			Task: worktree.Task, Path: worktree.Path, Head: heads[0].Object, Branch: branch,
			Detached: heads[0].State == gitrefs.HeadDetached, IsDefault: branch != "" && branch == defaultName,
		})
	}
	return out
}

// isLocked looks for the lock file in the worktree's administrative folder.
func isLocked(path string) bool {
	data, err := os.ReadFile(filepath.Join(path, ".git"))
	if err != nil {
		return false
	}
	target, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return false
	}
	admin := strings.TrimSpace(target)
	if !filepath.IsAbs(admin) {
		admin = filepath.Join(path, admin)
	}
	_, err = os.Lstat(filepath.Join(admin, "locked"))
	return err == nil
}

// defaultBranch prefers what origin/HEAD names, then the usual local names. It
// returns "" when no local branch can be called the default.
func defaultBranch(refs []gitrefs.Ref) string {
	local := make(map[string]bool, len(refs))
	remote := ""
	for _, ref := range refs {
		if name, ok := strings.CutPrefix(ref.Name, headsPrefix); ok {
			local[name] = true
		}
		if ref.Name == remoteHeadRef {
			remote = strings.TrimPrefix(ref.Symref, remotePrefix)
		}
	}
	for _, name := range []string{remote, "main", "master"} {
		if name != "" && local[name] {
			return name
		}
	}
	return ""
}

// Fill completes rows in place. The cells shared across rows come first, each
// costing at most one process, then status runs per row on a bounded pool.
// emit, when set, is called once per finished row and never concurrently. A
// cell that fails is recorded on its row; only cancellation returns an error.
func (c *Collector) Fill(ctx context.Context, rows []Row, emit func(index int, row Row)) error {
	c.fillDivergence(ctx, rows)
	c.fillCommits(ctx, rows)

	var wg sync.WaitGroup
	var emitting sync.Mutex
	slots := make(chan struct{}, c.workers())
	for i := range rows {
		select {
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		case slots <- struct{}{}:
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			c.fillStatus(ctx, &rows[i])
			if emit != nil {
				emitting.Lock()
				defer emitting.Unlock()
				emit(i, rows[i])
			}
		}()
	}
	wg.Wait()
	return ctx.Err()
}

func (c *Collector) fillStatus(ctx context.Context, row *Row) {
	if row.Bare {
		return
	}
	if c.statusHook != nil {
		defer c.statusHook()()
	}
	result, err := c.Git.Run(ctx, row.Path, statusArgs...)
	if err != nil {
		row.Err = errors.Join(row.Err, err)
		return
	}
	status, err := ParseStatus(result.Stdout)
	if err != nil {
		row.Err = errors.Join(row.Err, err)
		return
	}
	row.Status = status
}

// fillDivergence answers from the cache when every row can be, and otherwise
// starts one process for all branches. The base is pinned to its object id,
// and a result is cached only for a branch that did not move during the call,
// so a key never holds an answer computed for other commits.
func (c *Collector) fillDivergence(ctx context.Context, rows []Row) {
	refs, err := c.Refs.Refs(ctx)
	if err != nil {
		markRows(rows, needsDivergence, err)
		return
	}
	baseName := defaultBranch(refs)
	baseObject := refObject(refs, headsPrefix+baseName)
	if baseObject == "" {
		return
	}
	keys := make([]string, len(rows))
	var wanted []string
	for i := range rows {
		if !needsDivergence(rows[i]) {
			continue
		}
		if key, err := CacheKey(rows[i].Head, baseObject); err == nil {
			keys[i] = key
			wanted = append(wanted, key)
		}
	}
	cached := c.cachedOrEmpty(ctx, CacheDivergence, wanted)
	missing := false
	for i, key := range keys {
		if key == "" {
			continue
		}
		if divergence, ok := decodeDivergence(cached[key]); ok {
			rows[i].Divergence, rows[i].HasDivergence = divergence, true
		} else {
			missing = true
		}
	}
	if !missing {
		return
	}
	computed, err := c.Refs.AheadBehind(ctx, baseObject)
	if err != nil {
		markRows(rows, func(row Row) bool { return needsDivergence(row) && !row.HasDivergence }, err)
		return
	}
	after, err := c.Refs.Refs(ctx)
	if err != nil {
		after = nil // Nothing can be shown unchanged, so nothing is cached.
	}
	fresh := map[string]string{}
	for i, key := range keys {
		if key == "" || rows[i].HasDivergence {
			continue
		}
		name := headsPrefix + rows[i].Branch
		divergence, ok := computed[name]
		if !ok {
			continue
		}
		rows[i].Divergence, rows[i].HasDivergence = divergence, true
		if refObject(after, name) == rows[i].Head {
			fresh[key] = encodeDivergence(divergence)
		}
	}
	// The cache only saves work; failing to write it loses nothing.
	_ = c.State.PutCached(ctx, CacheDivergence, fresh)
}

func needsDivergence(row Row) bool {
	return row.Branch != "" && row.Head != ""
}

func refObject(refs []gitrefs.Ref, name string) string {
	for _, ref := range refs {
		if ref.Name == name {
			return ref.Object
		}
	}
	return ""
}

func markRows(rows []Row, applies func(Row) bool, err error) {
	for i := range rows {
		if applies(rows[i]) {
			rows[i].Err = errors.Join(rows[i].Err, err)
		}
	}
}

// fillCommits reads the time and subject of the commits not yet cached, all in
// one process.
func (c *Collector) fillCommits(ctx context.Context, rows []Row) {
	seen := map[string]bool{}
	var objects []string
	for _, row := range rows {
		if isObjectID(row.Head) && !seen[row.Head] {
			seen[row.Head] = true
			objects = append(objects, row.Head)
		}
	}
	if len(objects) == 0 {
		return
	}
	known := c.cachedOrEmpty(ctx, CacheCommit, objects)
	var missing []string
	for _, object := range objects {
		if _, _, ok := decodeCommit(known[object]); !ok {
			missing = append(missing, object)
		}
	}
	if len(missing) > 0 {
		fresh, err := c.readCommits(ctx, rows, missing)
		if err != nil {
			markRows(rows, func(row Row) bool { return row.Head != "" }, err)
		}
		_ = c.State.PutCached(ctx, CacheCommit, fresh)
		for object, value := range fresh {
			known[object] = value
		}
	}
	for i := range rows {
		if when, subject, ok := decodeCommit(known[rows[i].Head]); ok {
			rows[i].CommitTime, rows[i].Subject = when, subject
		}
	}
}

func (c *Collector) readCommits(ctx context.Context, rows []Row, objects []string) (map[string]string, error) {
	args := append([]string{"log", "--no-walk=unsorted", "--format=%H%x00%ct%x00%s%x00"}, objects...)
	result, err := c.Git.Run(ctx, rows[0].Path, args...)
	if err != nil {
		return nil, err
	}
	return parseCommits(result.Stdout)
}

func (c *Collector) cachedOrEmpty(ctx context.Context, kind CacheKind, keys []string) map[string]string {
	found, err := c.State.Cached(ctx, kind, keys)
	if err != nil {
		return map[string]string{} // A cache that cannot be read is a miss.
	}
	return found
}

// parseCommits reads repeated object, time, subject fields, each ended by NUL,
// into cache values of "time NUL subject". git puts a newline between commits.
func parseCommits(data []byte) (map[string]string, error) {
	fields := bytes.Split(data, []byte{0})
	if len(bytes.TrimSpace(fields[len(fields)-1])) != 0 || (len(fields)-1)%3 != 0 {
		return nil, fmt.Errorf("wtree: unexpected log output: %q", data)
	}
	commits := make(map[string]string, (len(fields)-1)/3)
	for i := 0; i+2 < len(fields); i += 3 {
		object := strings.TrimLeft(string(fields[i]), "\n")
		commits[object] = string(fields[i+1]) + "\x00" + string(fields[i+2])
	}
	return commits, nil
}

func decodeCommit(value string) (time.Time, string, bool) {
	seconds, subject, ok := strings.Cut(value, "\x00")
	if !ok {
		return time.Time{}, "", false
	}
	unix, err := strconv.ParseInt(seconds, 10, 64)
	if err != nil {
		return time.Time{}, "", false
	}
	return time.Unix(unix, 0), subject, true
}

func encodeDivergence(d gitrefs.Divergence) string {
	return strconv.Itoa(d.Ahead) + " " + strconv.Itoa(d.Behind)
}

func decodeDivergence(value string) (gitrefs.Divergence, bool) {
	ahead, behind, ok := strings.Cut(value, " ")
	if !ok {
		return gitrefs.Divergence{}, false
	}
	a, aheadErr := strconv.Atoi(ahead)
	b, behindErr := strconv.Atoi(behind)
	if aheadErr != nil || behindErr != nil {
		return gitrefs.Divergence{}, false
	}
	return gitrefs.Divergence{Ahead: a, Behind: b}, true
}
