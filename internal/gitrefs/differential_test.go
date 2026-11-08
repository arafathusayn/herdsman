package gitrefs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// gitBinary is resolved once, so that a test can empty PATH around the reader
// and still run the real git for the expected side of a comparison.
var gitBinary = func() string {
	path, err := exec.LookPath("git")
	if err != nil {
		panic(err)
	}
	return path
}()

var objectFormats = []string{"sha1", "sha256"}

const forEachRefFormat = "--format=%(refname)%00%(objectname)%00%(symref)"

func gitEnv() []string {
	env := []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return env
}

func runGitErr(dir string, args ...string) (string, error) {
	cmd := exec.Command(gitBinary, append([]string{"-C", dir}, args...)...)
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), fmt.Errorf("git %v: %w: %s", args, err, ee.Stderr)
	}
	return string(out), err
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGitErr(dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func initRepo(t *testing.T, format string, extra ...string) string {
	t.Helper()
	dir := filepath.Join(tempDir(t), "repo")
	args := append([]string{"init", "-q", "-b", "main", "--object-format=" + format}, extra...)
	runGit(t, filepath.Dir(dir), append(args, dir)...)
	return dir
}

func commit(t *testing.T, dir, message string) string {
	t.Helper()
	runGit(t, dir, "commit", "-q", "--allow-empty", "-m", message)
	return strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
}

func newRepo(t *testing.T, format string) string {
	t.Helper()
	dir := initRepo(t, format)
	commit(t, dir, "first")
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// withoutForks runs fn with a PATH that holds no git, so any fork fails.
func withoutForks(t *testing.T, fn func()) {
	t.Helper()
	old := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir())
	defer os.Setenv("PATH", old)
	fn()
}

func wantRefs(t *testing.T, dir string) []Ref {
	t.Helper()
	got, err := parseForEachRef(runGit(t, dir, "for-each-ref", forEachRefFormat))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// assertMatches compares the reader, with no git on PATH, against
// for-each-ref, worktree list and rev-parse, run from dir.
func assertMatches(t *testing.T, dir string) {
	t.Helper()
	reader, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var refs []Ref
	var heads []Head
	var refsErr, headsErr error
	withoutForks(t, func() {
		refs, refsErr = reader.Refs(context.Background())
		heads, headsErr = reader.Heads(context.Background())
	})
	if refsErr != nil || headsErr != nil {
		t.Fatalf("reader forked or failed: refs %v, heads %v", refsErr, headsErr)
	}
	if reason := reader.FallbackReason(); reason != "" {
		t.Fatalf("fell back: %s", reason)
	}
	if want := wantRefs(t, dir); !sameRefs(refs, want) {
		t.Fatalf("refs differ\n got %+v\nwant %+v", refs, want)
	}
	want, err := parseWorktreeList(runGit(t, dir, "worktree", "list", "--porcelain", "-z"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(heads, want) {
		t.Fatalf("heads differ\n got %+v\nwant %+v", heads, want)
	}
	for _, head := range heads {
		assertRevParse(t, head)
	}
}

func sameRefs(a, b []Ref) bool {
	return len(a) == len(b) && (len(a) == 0 || reflect.DeepEqual(a, b))
}

func assertRevParse(t *testing.T, head Head) {
	t.Helper()
	if head.State == HeadBare || head.State == HeadUnborn {
		return
	}
	if _, err := os.Stat(head.Path); err != nil {
		return
	}
	if got := strings.TrimSpace(runGit(t, head.Path, "rev-parse", "HEAD")); got != head.Object {
		t.Fatalf("%s: rev-parse HEAD = %s, reader %s", head.Path, got, head.Object)
	}
	name := "HEAD"
	if head.State == HeadAttached {
		name = head.Branch
	}
	if got := strings.TrimSpace(runGit(t, head.Path, "rev-parse", "--symbolic-full-name", "HEAD")); got != name {
		t.Fatalf("%s: symbolic name = %s, reader %s", head.Path, got, name)
	}
}

func headStates(t *testing.T, dir string) []HeadState {
	t.Helper()
	reader, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	heads, err := reader.Heads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	states := make([]HeadState, len(heads))
	for i, head := range heads {
		states[i] = head.State
	}
	return states
}

func TestDifferential(t *testing.T) {
	scenarios := map[string]func(t *testing.T, format string){
		"unborn head, no refs": func(t *testing.T, format string) {
			dir := initRepo(t, format)
			assertMatches(t, dir)
			if got := headStates(t, dir); !reflect.DeepEqual(got, []HeadState{HeadUnborn}) {
				t.Fatalf("states = %v", got)
			}
		},
		"detached head": func(t *testing.T, format string) {
			dir := newRepo(t, format)
			runGit(t, dir, "checkout", "-q", "--detach")
			assertMatches(t, dir)
			if got := headStates(t, dir); !reflect.DeepEqual(got, []HeadState{HeadDetached}) {
				t.Fatalf("states = %v", got)
			}
		},
		"linked worktrees, per-worktree refs": func(t *testing.T, format string) {
			dir := newRepo(t, format)
			root := filepath.Dir(dir)
			attached := filepath.Join(root, "b-attached")
			detached := filepath.Join(root, "a-detached")
			orphan := filepath.Join(root, "c-orphan")
			runGit(t, dir, "worktree", "add", "-q", "-b", "side", attached)
			runGit(t, dir, "worktree", "add", "-q", "--detach", detached)
			runGit(t, dir, "worktree", "add", "-q", "--orphan", "-b", "unborn", orphan)
			oid := commit(t, attached, "side work")
			runGit(t, dir, "update-ref", "refs/worktree/main-only", oid)
			runGit(t, attached, "update-ref", "refs/worktree/attached-only", oid)
			runGit(t, attached, "update-ref", "refs/bisect/bad", oid)
			runGit(t, attached, "symbolic-ref", "refs/worktree/sym", "refs/worktree/attached-only")
			runGit(t, detached, "update-ref", "refs/rewritten/detached-only", oid)
			for _, wt := range []string{dir, attached, detached, orphan} {
				assertMatches(t, wt)
			}
			want := []HeadState{HeadAttached, HeadDetached, HeadAttached, HeadUnborn}
			if got := headStates(t, attached); !reflect.DeepEqual(got, want) {
				t.Fatalf("states = %v, want %v", got, want)
			}
			assertMatches(t, filepath.Join(dir, ".git", "worktrees", "b-attached"))
			assertMatches(t, filepath.Join(dir, ".git"))
		},
		"worktree with relative paths": func(t *testing.T, format string) {
			dir := newRepo(t, format)
			wt := filepath.Join(filepath.Dir(dir), "relative")
			runGit(t, dir, "worktree", "add", "-q", "--relative-paths", "-b", "rel", wt)
			assertMatches(t, dir)
			assertMatches(t, wt)
		},
		"pruned worktree entries are still listed or skipped like git": func(t *testing.T, format string) {
			dir := newRepo(t, format)
			wt := filepath.Join(filepath.Dir(dir), "gone")
			runGit(t, dir, "worktree", "add", "-q", "-b", "gone", wt)
			if err := os.RemoveAll(wt); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(dir, ".git", "worktrees", "stray-file"), "x")
			assertMatches(t, dir)
		},
		"bare repository with a linked worktree": func(t *testing.T, format string) {
			src := newRepo(t, format)
			bare := filepath.Join(filepath.Dir(src), "bare.git")
			runGit(t, src, "clone", "-q", "--bare", src, bare)
			wt := filepath.Join(filepath.Dir(src), "from-bare")
			runGit(t, bare, "worktree", "add", "-q", "-b", "wt", wt)
			assertMatches(t, bare)
			assertMatches(t, wt)
		},
		"loose over packed": func(t *testing.T, format string) {
			dir := newRepo(t, format)
			runGit(t, dir, "branch", "packed-only")
			runGit(t, dir, "branch", "both")
			runGit(t, dir, "tag", "-a", "-m", "annotated", "v1")
			runGit(t, dir, "tag", "light")
			runGit(t, dir, "pack-refs", "--all", "--prune")
			newer := commit(t, dir, "second")
			runGit(t, dir, "update-ref", "refs/heads/both", newer)
			runGit(t, dir, "update-ref", "refs/notes/x", newer)
			assertMatches(t, dir)
		},
		"deleted packed ref": func(t *testing.T, format string) {
			dir := newRepo(t, format)
			runGit(t, dir, "branch", "doomed")
			runGit(t, dir, "pack-refs", "--all", "--prune")
			runGit(t, dir, "branch", "-q", "-D", "doomed")
			assertMatches(t, dir)
		},
		"packed entries outside the usual namespaces": func(t *testing.T, format string) {
			dir := newRepo(t, format)
			wt := filepath.Join(filepath.Dir(dir), "wt")
			runGit(t, dir, "worktree", "add", "-q", "--detach", wt)
			oid := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
			writeFile(t, filepath.Join(dir, ".git/packed-refs"), "# pack-refs with: peeled fully-peeled sorted \n"+
				oid+" outside/refs\n"+oid+" refs/heads/packed\n"+oid+" refs/worktree/packed\n")
			assertMatches(t, dir)
			assertMatches(t, wt)
		},
		"head on a packed branch": func(t *testing.T, format string) {
			dir := newRepo(t, format)
			runGit(t, dir, "pack-refs", "--all", "--prune")
			assertMatches(t, dir)
		},
		"symbolic refs": func(t *testing.T, format string) {
			dir := newRepo(t, format)
			runGit(t, dir, "branch", "packed-target")
			runGit(t, dir, "pack-refs", "--all", "--prune")
			runGit(t, dir, "symbolic-ref", "refs/heads/to-packed", "refs/heads/packed-target")
			runGit(t, dir, "symbolic-ref", "refs/heads/alias", "refs/heads/main")
			runGit(t, dir, "symbolic-ref", "refs/heads/chain", "refs/heads/alias")
			runGit(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/absent")
			assertMatches(t, dir)
		},
		"symref chain limit": func(t *testing.T, format string) {
			dir := newRepo(t, format)
			heads := filepath.Join(dir, ".git", "refs", "heads")
			names := []string{"s1", "s2", "s3", "s4", "s5", "s6"}
			for i, name := range names[:len(names)-1] {
				writeFile(t, filepath.Join(heads, name), "ref: refs/heads/"+names[i+1]+"\n")
			}
			writeFile(t, filepath.Join(heads, "s6"), "ref:   refs/heads/main  \n")
			writeFile(t, filepath.Join(heads, "cycle-a"), "ref: refs/heads/cycle-b\n")
			writeFile(t, filepath.Join(heads, "cycle-b"), "ref: refs/heads/cycle-a\n")
			writeFile(t, filepath.Join(heads, "bad-target"), "ref: refs/heads/no..good\n")
			assertMatches(t, dir)
		},
		"broken and odd loose files": func(t *testing.T, format string) {
			dir := newRepo(t, format)
			oid := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
			runGit(t, dir, "branch", "shadowed")
			runGit(t, dir, "pack-refs", "--all", "--prune")
			heads := filepath.Join(dir, ".git", "refs", "heads")
			writeFile(t, filepath.Join(heads, "shadowed"), "not an object id\n")
			writeFile(t, filepath.Join(heads, "empty"), "")
			writeFile(t, filepath.Join(heads, "null"), strings.Repeat("0", len(oid))+"\n")
			writeFile(t, filepath.Join(heads, "short"), oid[:20]+"\n")
			writeFile(t, filepath.Join(heads, "long"), oid+"ff\n")
			writeFile(t, filepath.Join(heads, "upper"), strings.ToUpper(oid)+"\n")
			writeFile(t, filepath.Join(heads, "trailing"), oid+" extra data\n")
			writeFile(t, filepath.Join(heads, "no-newline"), oid)
			writeFile(t, filepath.Join(heads, "branch.lock"), oid+"\n")
			writeFile(t, filepath.Join(heads, ".hidden"), oid+"\n")
			writeFile(t, filepath.Join(heads, "bad..name"), oid+"\n")
			writeFile(t, filepath.Join(heads, "dir.lock", "inner"), oid+"\n")
			if err := os.MkdirAll(filepath.Join(heads, "empty-dir", "nested"), 0o755); err != nil {
				t.Fatal(err)
			}
			missing := strings.Repeat("ab", len(oid)/2)
			writeFile(t, filepath.Join(heads, "missing-object"), missing+"\n")
			assertMatches(t, dir)
		},
		"unusual branch names": func(t *testing.T, format string) {
			dir := newRepo(t, format)
			for _, name := range []string{"feature/deep/er", "a-b", "a/b", "ünï", "@x", "z.y"} {
				runGit(t, dir, "branch", name)
			}
			assertMatches(t, dir)
		},
	}
	for name, scenario := range scenarios {
		for _, format := range objectFormats {
			t.Run(name+"/"+format, func(t *testing.T) { scenario(t, format) })
		}
	}
}

// TestDifferentialConcurrentWrites runs the reader while git moves, creates,
// deletes and packs refs. A ref git never deletes must be visible in every
// snapshot, whatever pack-refs is doing to its loose file.
func TestDifferentialConcurrentWrites(t *testing.T) {
	for _, format := range objectFormats {
		t.Run(format, func(t *testing.T) {
			dir := newRepo(t, format)
			var history []string
			for i := range 6 {
				history = append(history, commit(t, dir, "c"+strconv.Itoa(i)))
			}
			position := map[string]int{}
			for i, oid := range history {
				position[oid] = i
			}
			stable := map[string]string{}
			for i := range 12 {
				name := fmt.Sprintf("refs/heads/stable-%02d", i)
				oid := history[i%len(history)]
				runGit(t, dir, "update-ref", name, oid)
				stable[name] = oid
			}
			runGit(t, dir, "update-ref", "refs/heads/moving", history[0])
			runGit(t, dir, "symbolic-ref", "refs/heads/moving-alias", "refs/heads/moving")

			done := make(chan error, 1)
			var finished atomic.Bool
			go func() {
				defer finished.Store(true)
				for round := 1; round < 40; round++ {
					steps := [][]string{
						{"update-ref", "refs/heads/moving", history[min(round/7+1, len(history)-1)]},
						{"update-ref", "refs/heads/temporary", history[0]},
						{"pack-refs", "--all", "--prune"},
						{"update-ref", "-d", "refs/heads/temporary"},
					}
					for _, args := range steps {
						if _, err := runGitErr(dir, args...); err != nil {
							done <- err
							return
						}
					}
				}
				done <- nil
			}()

			reader, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			snapshots, last := 0, 0
			for !finished.Load() || snapshots == 0 {
				refs, err := reader.Refs(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				snapshots++
				seen := map[string]Ref{}
				for _, ref := range refs {
					seen[ref.Name] = ref
				}
				for name, oid := range stable {
					if seen[name].Object != oid {
						t.Fatalf("snapshot %d: %s = %q, want %s", snapshots, name, seen[name].Object, oid)
					}
				}
				moved, ok := position[seen["refs/heads/moving"].Object]
				if !ok || moved < last {
					t.Fatalf("snapshot %d: moving went to %q (index %d, last %d)", snapshots, seen["refs/heads/moving"].Object, moved, last)
				}
				last = moved
				alias := seen["refs/heads/moving-alias"]
				if alias.Symref != "refs/heads/moving" || position[alias.Object] < moved {
					t.Fatalf("snapshot %d: alias = %+v, moving index %d", snapshots, alias, moved)
				}
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if reason := reader.FallbackReason(); reason != "" {
				t.Fatalf("fell back during writes: %s", reason)
			}
			assertMatches(t, dir)
		})
	}
}
