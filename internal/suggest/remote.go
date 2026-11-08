package suggest

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// RemoteFolder is the folder of entries in a remote catalog's repository.
const RemoteFolder = "herdsman-suggest"

const (
	// maxGitOutput bounds what one git command may return.
	maxGitOutput = 128 << 20
	// maxDiffCells bounds the line-diff table; a larger pair of files is shown
	// as a full replacement.
	maxDiffCells = 1 << 20
)

var (
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	refPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
)

// BadSourceError is a source address, ref or commit that fails the rules before
// git is asked anything: the request is wrong, and retrying cannot help.
type BadSourceError struct{ Reason string }

func (e *BadSourceError) Error() string { return e.Reason }

func badSource(format string, args ...any) error {
	return &BadSourceError{Reason: fmt.Sprintf(format, args...)}
}

// Pin is a remote catalog fixed to one full commit id. An abbreviated id or a
// branch name is not a pin: it could come to mean other content.
type Pin struct {
	URL    string `json:"url"`
	Commit string `json:"commit"`
}

// String is the `<url>@<commit>` form the config stores.
func (p Pin) String() string { return p.URL + "@" + p.Commit }

// ParsePin reads the `<url>@<commit>` form.
func ParsePin(s string) (Pin, error) {
	i := strings.LastIndex(s, "@")
	if i < 0 {
		return Pin{}, badSource("%q is not pinned: want <url>@<full commit id>", s)
	}
	pin := Pin{URL: s[:i], Commit: s[i+1:]}
	if err := pin.validate(); err != nil {
		return Pin{}, err
	}
	return pin, nil
}

// validate holds the rule at every boundary a Pin crosses: Pin is an exported
// struct, so a value need not have come from ParsePin. Git must never see a
// revision that can come to mean other content, such as FETCH_HEAD.
func (p Pin) validate() error {
	if !commitPattern.MatchString(p.Commit) {
		return badSource("%q is not pinned: want a full commit id", p.Commit)
	}
	if !validRepoURL(p.URL, true) {
		return badSource("%q is not an https, ssh, git@ or file address", p.URL)
	}
	return nil
}

// Runner runs git with the given standard input and returns its output.
type Runner func(ctx context.Context, stdin []byte, args ...string) ([]byte, error)

// Remote fetches pinned catalogs into a cache of bare repositories. Entries
// are read as blobs through git plumbing: nothing is checked out, so no hook,
// filter, submodule or symlink from a catalog can take effect.
type Remote struct {
	CacheDir string
	Git      Runner

	mu sync.Mutex // one fetch at a time: FETCH_HEAD is per repository
}

// NewRemote returns a Remote that caches under cacheDir.
func NewRemote(cacheDir string) *Remote {
	return &Remote{CacheDir: cacheDir, Git: execGit(maxGitOutput)}
}

// execGit runs the real git, with output bounded by limit.
func execGit(limit int) Runner {
	return func(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Env = gitEnv(os.Environ())
		cmd.Stdin = bytes.NewReader(stdin)
		stdout := &cappedBuffer{limit: limit}
		var stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = stdout, &stderr
		err := cmd.Run()
		if stdout.over {
			return nil, fmt.Errorf("git %s: output is larger than %d bytes", args[0], limit)
		}
		if err != nil {
			return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return stdout.buf.Bytes(), nil
	}
}

type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
	over  bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.limit {
		c.over = true
		return 0, errors.New("output limit reached")
	}
	return c.buf.Write(p)
}

// gitEnv drops what would redirect git to another repository and sets the
// allowed transports. ext:: and the like stay off even if a URL check is
// someday missed.
func gitEnv(environ []string) []string {
	drop := []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_NAMESPACE", "GIT_PREFIX", "GIT_ALLOW_PROTOCOL", "GIT_TERMINAL_PROMPT"}
	out := make([]string, 0, len(environ)+2)
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(drop, name) {
			out = append(out, kv)
		}
	}
	return append(out, "GIT_ALLOW_PROTOCOL=file:https:ssh", "GIT_TERMINAL_PROMPT=0")
}

func (r *Remote) repoDir(url string) string {
	sum := sha256.Sum256([]byte(url))
	return filepath.Join(r.CacheDir, hex.EncodeToString(sum[:16])+".git")
}

func (r *Remote) gitDir(url string) string { return "--git-dir=" + r.repoDir(url) }

// Fetch brings ref into the cache and returns the pin it resolved to. An empty
// ref means the remote's HEAD. This and PreviewUpdate are the only calls that
// reach the network.
func (r *Remote) Fetch(ctx context.Context, url, ref string) (Pin, error) {
	pin, _, _, err := r.fetch(ctx, url, ref)
	return pin, err
}

func (r *Remote) fetch(ctx context.Context, url, ref string) (Pin, []file, []Problem, error) {
	if !validRepoURL(url, true) {
		return Pin{}, nil, nil, badSource("%q is not an https, ssh, git@ or file address", url)
	}
	if ref == "" {
		ref = "HEAD"
	}
	if !refPattern.MatchString(ref) || strings.Contains(ref, "..") {
		return Pin{}, nil, nil, badSource("%q is not a commit, tag or branch name", ref)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	commit, err := r.resolve(ctx, url, ref)
	if err != nil {
		return Pin{}, nil, nil, err
	}
	pin := Pin{URL: url, Commit: commit}
	files, problems, err := r.readCommit(ctx, pin)
	if err != nil {
		return Pin{}, nil, nil, err
	}
	return pin, files, problems, nil
}

func (r *Remote) resolve(ctx context.Context, url, ref string) (string, error) {
	if err := r.ensureRepo(ctx, url); err != nil {
		return "", err
	}
	dir := r.gitDir(url)
	if _, err := r.Git(ctx, nil, dir, "fetch", "--quiet", "--no-tags", "--no-auto-gc", "--no-recurse-submodules", "--depth=1", url, ref); err != nil {
		return "", err
	}
	out, err := r.Git(ctx, nil, dir, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(out))
	if !commitPattern.MatchString(commit) {
		return "", fmt.Errorf("git returned %q instead of a commit id", commit)
	}
	if commitPattern.MatchString(ref) && ref != commit {
		return "", fmt.Errorf("asked for %s and the remote gave %s instead", ref, commit)
	}
	// A ref per pin keeps the commit reachable, so gc cannot drop it.
	if _, err := r.Git(ctx, nil, dir, "update-ref", "refs/herdsman/pins/"+commit, commit); err != nil {
		return "", err
	}
	return commit, nil
}

func (r *Remote) ensureRepo(ctx context.Context, url string) error {
	if err := os.MkdirAll(r.CacheDir, 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(r.repoDir(url), "HEAD")); err == nil {
		return nil
	}
	format, err := r.objectFormat(ctx, url)
	if err != nil {
		return err
	}
	// An empty template: the cache holds no hooks at all. The format is always
	// named, so the user's own default cannot mismatch the remote.
	_, err = r.Git(ctx, nil, "init", "--quiet", "--bare", "--template=", "--object-format="+format, r.repoDir(url))
	return err
}

// objectFormat reads the remote's hash length from its refs. It runs from the
// cache folder so that no config of the process's working directory, which may
// be an untrusted checkout, applies to the connection.
func (r *Remote) objectFormat(ctx context.Context, url string) (string, error) {
	out, err := r.Git(ctx, nil, "-C", r.CacheDir, "ls-remote", url)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", fmt.Errorf("%s has no refs to fetch", url)
	}
	switch {
	case !commitPattern.MatchString(fields[0]):
		return "", fmt.Errorf("cannot tell the object format of %s from %q", url, fields[0])
	case len(fields[0]) == 64:
		return "sha256", nil
	}
	return "sha1", nil
}

// Source reads a pinned catalog from the cache only. A pin that is not cached
// becomes a Problem until `suggest source update` fetches it.
func (r *Remote) Source(ctx context.Context, pin Pin) Source {
	ref := SourceRef{Kind: SourceRemote, Name: pin.URL, Commit: pin.Commit}
	return Source{Ref: ref, read: func() ([]file, []Problem, error) { return r.readCommit(ctx, pin) }}
}

// readCommit lists the catalog folder of a commit and reads its entries in one
// batch.
func (r *Remote) readCommit(ctx context.Context, pin Pin) ([]file, []Problem, error) {
	if err := pin.validate(); err != nil {
		return nil, nil, err
	}
	ref := SourceRef{Kind: SourceRemote, Name: pin.URL, Commit: pin.Commit}
	dir := r.gitDir(pin.URL)
	tree, err := r.Git(ctx, nil, dir, "ls-tree", "-z", "-l", pin.Commit+":"+RemoteFolder)
	if err != nil {
		return nil, nil, fmt.Errorf("commit %s is not cached or has no %s folder: %w", pin.Commit, RemoteFolder, err)
	}
	wanted, problems, err := parseTree(ref, tree)
	if err != nil {
		return nil, nil, err
	}
	if len(wanted) == 0 {
		return nil, problems, nil
	}
	var stdin bytes.Buffer
	for _, w := range wanted {
		stdin.WriteString(w.hash + "\n")
	}
	out, err := r.Git(ctx, stdin.Bytes(), dir, "cat-file", "--batch")
	if err != nil {
		return nil, nil, err
	}
	files, err := parseBatch(wanted, out)
	if err != nil {
		return nil, nil, err
	}
	return files, problems, nil
}

type treeBlob struct{ name, hash string }

// parseTree reads `ls-tree -z -l`. Only regular files are read: a symlink is
// never followed, and an oversized blob is refused before it is loaded.
func parseTree(ref SourceRef, tree []byte) ([]treeBlob, []Problem, error) {
	var wanted []treeBlob
	var problems []Problem
	for _, record := range strings.Split(string(tree), "\x00") {
		if record == "" {
			continue
		}
		meta, name, found := strings.Cut(record, "\t")
		if !found {
			return nil, nil, fmt.Errorf("malformed git tree line %q", record)
		}
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) != 4 {
			return nil, nil, fmt.Errorf("malformed git tree line %q", record)
		}
		if len(wanted) == MaxSourceEntries {
			problems = append(problems, tooMany(ref))
			break
		}
		if fields[0] != "100644" && fields[0] != "100755" {
			problems = append(problems, Problem{Source: ref, File: name, Err: "not a regular file"})
			continue
		}
		size, err := strconv.Atoi(fields[3])
		if err != nil {
			return nil, nil, fmt.Errorf("malformed git tree line %q", record)
		}
		if size > MaxEntryBytes {
			problems = append(problems, Problem{Source: ref, File: name, Err: fmt.Sprintf("entry is larger than %d bytes", MaxEntryBytes)})
			continue
		}
		wanted = append(wanted, treeBlob{name, fields[2]})
	}
	return wanted, problems, nil
}

// parseBatch reads `cat-file --batch`: for each object a header line, its
// bytes and a newline, in the order asked for.
func parseBatch(wanted []treeBlob, out []byte) ([]file, error) {
	malformed := errors.New("malformed git batch output")
	files := make([]file, 0, len(wanted))
	for _, w := range wanted {
		eol := bytes.IndexByte(out, '\n')
		if eol < 0 {
			return nil, malformed
		}
		header := strings.Fields(string(out[:eol]))
		if len(header) != 3 || header[0] != w.hash {
			return nil, malformed
		}
		size, err := strconv.Atoi(header[2])
		if err != nil || size < 0 || eol+1+size >= len(out) {
			return nil, malformed
		}
		files = append(files, file{w.name, out[eol+1 : eol+1+size]})
		out = out[eol+1+size+1:]
	}
	return files, nil
}

// Action says what an update does to an entry.
type Action string

// The actions of an update.
const (
	ActionAdded   Action = "added"
	ActionRemoved Action = "removed"
	ActionChanged Action = "changed"
)

// Change is one entry an update would add, remove or change, with its diff.
type Change struct {
	ID     string `json:"id"`
	Action Action `json:"action"`
	Diff   string `json:"diff"`
}

// Update describes moving a pin. It changes nothing: the caller shows it and
// stores To only after that.
type Update struct {
	From     Pin       `json:"from"`
	To       Pin       `json:"to"`
	Changes  []Change  `json:"changes"`
	Problems []Problem `json:"problems,omitempty"`
}

// PreviewUpdate fetches ref and returns the entry diff from the pin to it.
func (r *Remote) PreviewUpdate(ctx context.Context, from Pin, ref string) (Update, error) {
	if err := from.validate(); err != nil {
		return Update{}, err
	}
	to, newFiles, newProblems, err := r.fetch(ctx, from.URL, ref)
	if err != nil {
		return Update{}, err
	}
	oldFiles, _, err := r.readCommit(ctx, from)
	if err != nil {
		return Update{}, err
	}
	newRef := SourceRef{Kind: SourceRemote, Name: to.URL, Commit: to.Commit}
	before, _ := parseFiles(SourceRef{Kind: SourceRemote, Name: from.URL, Commit: from.Commit}, oldFiles)
	after, bad := parseFiles(newRef, newFiles)
	return Update{From: from, To: to, Changes: diffEntries(before, after), Problems: append(newProblems, bad...)}, nil
}

func diffEntries(before, after []parsed) []Change {
	old := map[string]string{}
	for _, p := range before {
		old[p.entry.ID] = p.text
	}
	changes := []Change{}
	for _, p := range after {
		prev, existed := old[p.entry.ID]
		delete(old, p.entry.ID)
		switch {
		case !existed:
			changes = append(changes, Change{p.entry.ID, ActionAdded, diffLines("", p.text)})
		case prev != p.text:
			changes = append(changes, Change{p.entry.ID, ActionChanged, diffLines(prev, p.text)})
		}
	}
	for id, text := range old {
		changes = append(changes, Change{id, ActionRemoved, diffLines(text, "")})
	}
	slices.SortFunc(changes, func(a, b Change) int { return cmp.Compare(a.ID, b.ID) })
	return changes
}

// diffLines lists removed lines as `- ` and added lines as `+ `. The text was
// checked by ParseEntry, so it holds no control characters to replay in a
// terminal.
func diffLines(a, b string) string {
	x, y := splitLines(a), splitLines(b)
	for len(x) > 0 && len(y) > 0 && x[0] == y[0] {
		x, y = x[1:], y[1:]
	}
	for len(x) > 0 && len(y) > 0 && x[len(x)-1] == y[len(y)-1] {
		x, y = x[:len(x)-1], y[:len(y)-1]
	}
	var out strings.Builder
	emit := func(sign string, lines ...string) {
		for _, l := range lines {
			out.WriteString(sign + l + "\n")
		}
	}
	if len(x)*len(y) > maxDiffCells {
		emit("- ", x...)
		emit("+ ", y...)
		return out.String()
	}
	// table[i][j] is the longest common run of x[i:] and y[j:], in one slice.
	width := len(y) + 1
	table := make([]int32, (len(x)+1)*width)
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				table[i*width+j] = table[(i+1)*width+j+1] + 1
			} else {
				table[i*width+j] = max(table[(i+1)*width+j], table[i*width+j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(x) && j < len(y) {
		switch {
		case x[i] == y[j]:
			i, j = i+1, j+1
		case table[(i+1)*width+j] >= table[i*width+j+1]:
			emit("- ", x[i])
			i++
		default:
			emit("+ ", y[j])
			j++
		}
	}
	emit("- ", x[i:]...)
	emit("+ ", y[j:]...)
	return out.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
