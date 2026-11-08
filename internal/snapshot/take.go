package snapshot

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Options says what to capture and where.
type Options struct {
	// Worktree is any path inside the worktree to capture.
	Worktree string
	// Dest is the snapshot folder. It must not exist.
	Dest string
	// Task and Op name refs/herdsman/snapshot/<task>/<op>.
	Task, Op string
	// Rebuildable lists the patterns whose matches may be left out. Nil means
	// none: only the caller's configuration grants that authority.
	Rebuildable []string
	// FileCap is the largest file captured, 0 meaning DefaultFileCap.
	FileCap int64
}

func snapshotRef(task, op string) string {
	return "refs/herdsman/snapshot/" + task + "/" + op
}

func (o *Options) check(ctx context.Context) error {
	if o.Dest == "" || o.FileCap < 0 {
		return fmt.Errorf("snapshot: invalid options: dest %q, file cap %d", o.Dest, o.FileCap)
	}
	if err := absPaths(&o.Dest); err != nil {
		return err
	}
	if err := checkRebuildable(o.Rebuildable); err != nil {
		return err
	}
	if o.FileCap == 0 {
		o.FileCap = DefaultFileCap
	}
	_, err := runGit(ctx, "", nil, nil, "check-ref-format", snapshotRef(o.Task, o.Op))
	return err
}

// makeFolder creates the private snapshot folder. The Chmod is there because
// Mkdir's mode passes through the umask.
func makeFolder(dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(dest, 0o700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s", ErrDestExists, dest)
		}
		return err
	}
	if err := osChmod(dest, 0o700); err != nil {
		return errors.Join(err, os.Remove(dest))
	}
	return nil
}

// firstExisting returns the nearest folder at or above p that exists: the
// one whose entries change when the folders below it are created.
func firstExisting(p string) string {
	for p != filepath.Dir(p) {
		if _, err := osLstat(p); err == nil {
			return p
		}
		p = filepath.Dir(p)
	}
	return p
}

// syncParents flushes the entry of dest and of each folder made above it.
func syncParents(dest, existing string) error {
	for dir := filepath.Dir(dest); ; dir = filepath.Dir(dir) {
		if err := syncPath(dir); err != nil {
			return err
		}
		if dir == existing {
			return nil
		}
	}
}

// Take captures the worktree into opts.Dest and verifies the result. On any
// failure it removes what it made, so a folder with a manifest is complete.
func Take(ctx context.Context, opts Options) (m *Manifest, err error) {
	if err := opts.check(ctx); err != nil {
		return nil, err
	}
	started := time.Now()
	root, err := gitPath(ctx, opts.Worktree, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	existing := firstExisting(filepath.Dir(opts.Dest))
	if err := makeFolder(opts.Dest); err != nil {
		return nil, err
	}
	ref := snapshotRef(opts.Task, opts.Op)
	refMade := false
	defer func() {
		if err != nil {
			err = errors.Join(err, osRemoveAll(opts.Dest))
			if refMade {
				_, refErr := runGit(context.WithoutCancel(ctx), root, nil, nil, "update-ref", "-d", ref)
				err = errors.Join(err, refErr)
			}
		}
	}()

	m = &Manifest{
		Schema: SchemaVersion, Task: opts.Task, Op: opts.Op, StartedAt: started.UTC(),
		Worktree: root, WorktreeMode: uint32(info.Mode().Perm()), SnapshotRef: ref,
		FileCap: opts.FileCap, Rebuildable: append([]string{}, opts.Rebuildable...),
	}
	if m.ObjectFormat, err = gitText(ctx, root, "rev-parse", "--show-object-format"); err != nil {
		return nil, err
	}
	// Both commands exit non-zero for a legitimate state (detached, unborn).
	m.Branch, _ = gitText(ctx, root, "symbolic-ref", "-q", "HEAD")
	m.Head, _ = gitText(ctx, root, "rev-parse", "-q", "--verify", "HEAD^{commit}")
	if m.Head == "" && m.Branch == "" {
		return nil, errors.New("snapshot: HEAD resolves to neither a commit nor a branch")
	}

	work := filepath.Join(opts.Dest, workDirName)
	if err := osMkdir(work, 0o700); err != nil {
		return nil, err
	}
	c := &capture{ctx: ctx, root: root, opts: opts, excluded: map[string]Exclusion{}, opaque: map[string]bool{}}
	stagedIndex := filepath.Join(work, "index.staged")
	if err := c.copyIndex(stagedIndex); err != nil {
		return nil, err
	}
	if err := c.scan(); err != nil {
		return nil, err
	}
	if m.StagedTree, err = c.writeTree(stagedIndex); err != nil {
		return nil, err
	}
	if m.WorkingTree, err = c.buildWorkingTree(filepath.Join(work, "index.working")); err != nil {
		return nil, err
	}
	if m.SnapshotCommit, err = c.commit(m); err != nil {
		return nil, err
	}
	if _, err := runGit(ctx, root, nil, nil, "update-ref", ref, m.SnapshotCommit, ""); err != nil {
		return nil, err
	}
	refMade = true

	bundleFile, preservedFile := filepath.Join(opts.Dest, BundleName), filepath.Join(opts.Dest, PreservedName)
	if err := createBundle(ctx, root, bundleFile, ref); err != nil {
		return nil, err
	}
	if err := osChmod(bundleFile, 0o600); err != nil {
		return nil, err
	}
	c.finish(m)
	if err := writePreserved(root, preservedFile, c.preserved); err != nil {
		return nil, err
	}
	if m.Bundle, err = artifactFor(bundleFile, BundleName); err != nil {
		return nil, err
	}
	if m.Preserved, err = artifactFor(preservedFile, PreservedName); err != nil {
		return nil, err
	}
	if err := osRemoveAll(work); err != nil {
		return nil, err
	}
	// Completion is published only once the data it describes is on disk: a
	// caller may delete the worktree as soon as Take returns.
	for _, file := range []string{bundleFile, preservedFile} {
		if err := syncPath(file); err != nil {
			return nil, err
		}
	}
	m.FinishedAt = time.Now().UTC()
	if err := writeManifest(opts.Dest, m); err != nil {
		return nil, err
	}
	if err := syncParents(opts.Dest, existing); err != nil {
		return nil, err
	}
	if _, err := Verify(ctx, opts.Dest); err != nil {
		return nil, err
	}
	return m, nil
}

// capture holds one Take's classification of the worktree.
type capture struct {
	ctx        context.Context
	root       string
	opts       Options
	tree       []FileRecord
	preserved  []FileRecord
	submodules []Submodule
	gitlinks   []string // "oid\tpath", ready for --index-info
	excluded   map[string]Exclusion
	opaque     map[string]bool // folders git owns as a unit; the walk skips them
}

// copyIndex works on a copy so the capture cannot touch the user's index. A
// new repository has no index file, which git reads as empty.
func (c *capture) copyIndex(dst string) error {
	indexPath, err := gitPath(c.ctx, c.root, "rev-parse", "--git-path", "index")
	if err != nil {
		return err
	}
	if !filepath.IsAbs(indexPath) {
		indexPath = filepath.Join(c.root, indexPath)
	}
	raw, err := os.ReadFile(indexPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return os.WriteFile(dst, raw, 0o600)
}

func (c *capture) gitList(args ...string) ([]string, error) {
	raw, err := runGit(c.ctx, c.root, nil, nil, args...)
	return splitNUL(raw), err
}

func (c *capture) scan() error {
	stage, err := c.gitList("ls-files", "-z", "-s")
	if err != nil {
		return err
	}
	if err := c.scanTracked(stage); err != nil {
		return err
	}
	untracked, err := c.gitList("ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return err
	}
	ignored, err := c.gitList("ls-files", "-z", "--others", "--ignored", "--exclude-standard")
	if err != nil {
		return err
	}
	for _, rel := range untracked {
		if err := c.addOther(rel, &c.tree, recordForTree); err != nil {
			return err
		}
	}
	for _, rel := range ignored {
		if err := c.addOther(rel, &c.preserved, recordFor); err != nil {
			return err
		}
	}
	return c.walk("")
}

func (c *capture) scanTracked(stage []string) error {
	unmerged := map[string]bool{}
	var regular []string
	for _, line := range stage {
		meta, rel, _ := strings.Cut(line, "\t")
		fields := strings.Fields(meta)
		switch {
		case fields[2] != "0":
			unmerged[rel] = true
		case fields[0] == "160000":
			if err := c.addSubmodule(rel, fields[1]); err != nil {
				return err
			}
		default:
			regular = append(regular, rel)
		}
	}
	if len(unmerged) > 0 {
		names := make([]string, 0, len(unmerged))
		for rel := range unmerged {
			names = append(names, rel)
		}
		sort.Strings(names)
		return fmt.Errorf("%w: %s", ErrUnmerged, strings.Join(names, ", "))
	}
	for _, rel := range regular {
		if err := c.addTracked(rel); err != nil {
			return err
		}
	}
	return nil
}

func (c *capture) addSubmodule(rel, indexCommit string) error {
	c.opaque[rel] = true
	c.excluded[rel] = Exclusion{Path: Path(rel), Reason: ReasonSubmodule}
	c.gitlinks = append(c.gitlinks, indexCommit+"\t"+rel)
	sub := Submodule{Path: Path(rel), IndexCommit: indexCommit}
	full := filepath.Join(c.root, rel)
	if _, err := osLstat(filepath.Join(full, ".git")); err == nil {
		head, err := gitText(c.ctx, full, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		sub.Head = head
	}
	c.submodules = append(c.submodules, sub)
	return nil
}

func gone(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

func capturable(info fs.FileInfo) bool {
	return info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0
}

// addTracked keeps a tracked file whatever its name: a rebuildable pattern
// never drops content git is tracking. A deleted file simply has no entry.
func (c *capture) addTracked(rel string) error {
	info, err := osLstat(filepath.Join(c.root, rel))
	if gone(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if capturable(info) && !c.tooBig(rel, info) {
		c.tree = append(c.tree, recordForTree(rel, info))
	}
	return nil
}

// addOther files an untracked or ignored path. `ls-files` ends a nested
// repository's name in a slash and never descends into it.
func (c *capture) addOther(entry string, into *[]FileRecord, record func(string, fs.FileInfo) FileRecord) error {
	rel := strings.TrimSuffix(entry, "/")
	if prefix, pattern, ok := matchRebuildable(c.opts.Rebuildable, rel); ok {
		c.excluded[prefix] = Exclusion{Path: Path(prefix), Reason: ReasonRebuildable, Pattern: pattern}
		return nil
	}
	if strings.HasSuffix(entry, "/") {
		c.opaque[rel] = true
		c.excluded[rel] = Exclusion{Path: Path(rel), Reason: ReasonNestedRepository}
		return nil
	}
	info, err := osLstat(filepath.Join(c.root, rel))
	if err != nil {
		return err
	}
	if !c.tooBig(rel, info) {
		*into = append(*into, record(rel, info))
	}
	return nil
}

func (c *capture) tooBig(rel string, info fs.FileInfo) bool {
	if !overCap(info.Size(), c.opts.FileCap) {
		return false
	}
	c.excluded[rel] = Exclusion{Path: Path(rel), Reason: ReasonOverCap, Size: info.Size()}
	return true
}

func recordForTree(rel string, info fs.FileInfo) FileRecord {
	mode := uint32(0o100644)
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		mode = modeSymlink
	case info.Mode().Perm()&0o100 != 0:
		mode = 0o100755
	}
	rec := FileRecord{Path: Path(rel), Mode: mode, Size: info.Size(), MTimeNS: info.ModTime().UnixNano(), Source: SourceTree}
	if mode != modeSymlink {
		rec.Perm = uint32(info.Mode().Perm())
	}
	return rec
}

// walk finds what git's listings cannot show: empty folders, kept in the
// archive, and sockets or pipes, listed because they cannot be captured.
func (c *capture) walk(rel string) error {
	entries, err := os.ReadDir(filepath.Join(c.root, rel))
	if err != nil {
		return err
	}
	if len(entries) == 0 && rel != "" {
		info, err := osLstat(filepath.Join(c.root, rel))
		if err != nil {
			return err
		}
		c.preserved = append(c.preserved, recordFor(rel, info))
		return nil
	}
	for _, entry := range entries {
		child := path.Join(rel, entry.Name())
		_, _, rebuildable := matchRebuildable(c.opts.Rebuildable, child)
		if child == ".git" || c.opaque[child] || rebuildable {
			continue
		}
		switch {
		case entry.IsDir():
			if err := c.walk(child); err != nil {
				return err
			}
		case !entry.Type().IsRegular() && entry.Type()&fs.ModeSymlink == 0:
			c.excluded[child] = Exclusion{Path: Path(child), Reason: ReasonSpecial}
		}
	}
	return nil
}

func (c *capture) writeTree(index string) (string, error) {
	out, err := runGit(c.ctx, c.root, []string{"GIT_INDEX_FILE=" + index}, nil, "write-tree")
	return strings.TrimSpace(string(out)), err
}

// buildWorkingTree stores each file's exact bytes. `update-index` and
// `git add` run clean filters and line-ending conversion, which would break
// the byte-for-byte promise, so the blobs are written with --no-filters.
func (c *capture) buildWorkingTree(index string) (string, error) {
	var regular []FileRecord
	var input strings.Builder
	for _, f := range c.tree {
		if f.Mode != modeSymlink {
			regular = append(regular, f)
			continue
		}
		target, err := osReadlink(filepath.Join(c.root, string(f.Path)))
		if err != nil {
			return "", err
		}
		out, err := runGit(c.ctx, c.root, nil, []byte(target), "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&input, "%o %s\t%s\x00", f.Mode, strings.TrimSpace(string(out)), f.Path)
	}
	var paths strings.Builder
	for _, f := range regular {
		paths.WriteString(cQuote(string(f.Path)) + "\n")
	}
	out, err := runGit(c.ctx, c.root, nil, []byte(paths.String()), "hash-object", "--no-filters", "-w", "--stdin-paths")
	if err != nil {
		return "", err
	}
	oids := strings.Fields(string(out))
	if len(oids) != len(regular) {
		return "", fmt.Errorf("snapshot: git hashed %d of %d files", len(oids), len(regular))
	}
	for i, f := range regular {
		fmt.Fprintf(&input, "%o %s\t%s\x00", f.Mode, oids[i], f.Path)
	}
	for _, link := range c.gitlinks {
		fmt.Fprintf(&input, "160000 %s\x00", link)
	}
	env := []string{"GIT_INDEX_FILE=" + index}
	if _, err := runGit(c.ctx, c.root, env, []byte(input.String()), "update-index", "--add", "-z", "--index-info"); err != nil {
		return "", err
	}
	return c.writeTree(index)
}

// commit stores both trees under one commit: a root tree with the entries
// `staged` and `working`, on top of HEAD so history stays reachable.
func (c *capture) commit(m *Manifest) (string, error) {
	listing := fmt.Sprintf("040000 tree %s\tstaged\n040000 tree %s\tworking\n", m.StagedTree, m.WorkingTree)
	raw, err := runGit(c.ctx, c.root, nil, []byte(listing), "mktree")
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", strings.TrimSpace(string(raw)), "-m", "herdsman snapshot " + m.Task + "/" + m.Op}
	if m.Head != "" {
		args = append(args, "-p", m.Head)
	}
	identity := []string{
		"GIT_AUTHOR_NAME=herdsman", "GIT_AUTHOR_EMAIL=herdsman@localhost",
		"GIT_COMMITTER_NAME=herdsman", "GIT_COMMITTER_EMAIL=herdsman@localhost",
	}
	out, err := runGit(c.ctx, c.root, identity, nil, args...)
	return strings.TrimSpace(string(out)), err
}

// finish moves the classification into the manifest in a stable order.
func (c *capture) finish(m *Manifest) {
	byPath := func(files []FileRecord) {
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	}
	byPath(c.preserved)
	m.Files = append(append([]FileRecord{}, c.tree...), c.preserved...)
	byPath(m.Files)
	m.Submodules = append([]Submodule{}, c.submodules...)
	sort.Slice(m.Submodules, func(i, j int) bool { return m.Submodules[i].Path < m.Submodules[j].Path })
	m.Excluded = make([]Exclusion, 0, len(c.excluded))
	for _, e := range c.excluded {
		m.Excluded = append(m.Excluded, e)
	}
	sort.Slice(m.Excluded, func(i, j int) bool { return m.Excluded[i].Path < m.Excluded[j].Path })
}
