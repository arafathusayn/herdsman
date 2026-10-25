package review

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// repo makes a repository with two commits and returns it and both ids.
func repo(t *testing.T) (string, string, string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "repo")
	gitT(t, base, "init", "-q", "-b", "main", dir)
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", "a")
	gitT(t, dir, "commit", "-q", "-m", "one")
	first := gitT(t, dir, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "commit", "-q", "-am", "two")
	return dir, first, gitT(t, dir, "rev-parse", "HEAD")
}

func TestNewWorktree(t *testing.T) {
	dir, first, _ := repo(t)
	wt := filepath.Join(filepath.Dir(dir), "rv")
	caches := t.TempDir()
	start := time.Now()
	r, err := Start(context.Background(), Options{Repo: dir, Worktree: wt, Head: first[:7], CacheRoot: caches})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("over 2 s")
	}
	want := Result{Worktree: wt, Head: first, GoCache: filepath.Join(caches, "rv-go-build"), StaticcheckCache: filepath.Join(caches, "rv-staticcheck")}
	if r != want {
		t.Errorf("%+v, want %+v", r, want)
	}
	if got := gitT(t, wt, "rev-parse", "HEAD"); got != first {
		t.Errorf("head %s", got)
	}
	if b := gitT(t, wt, "rev-parse", "--abbrev-ref", "HEAD"); b != "HEAD" {
		t.Errorf("not detached: %s", b)
	}
	for _, d := range []string{want.GoCache, want.StaticcheckCache} {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			t.Errorf("%s: %v", d, err)
		}
	}
	if lines := strings.Join(r.Lines(), "\n"); lines != "worktree="+wt+"\nhead="+first+"\nGOCACHE="+want.GoCache+"\nSTATICCHECK_CACHE="+want.StaticcheckCache {
		t.Errorf("lines %q", lines)
	}
}

func TestExistingCleanWorktree(t *testing.T) {
	dir, first, second := repo(t)
	wt := filepath.Join(filepath.Dir(dir), "rv")
	if _, err := Start(context.Background(), Options{Repo: dir, Worktree: wt, Head: first, CacheRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	// An ignored-free untouched worktree moves to the new head.
	r, err := Start(context.Background(), Options{Repo: dir, Worktree: wt, Head: "main", CacheRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if r.Head != second || gitT(t, wt, "rev-parse", "HEAD") != second {
		t.Errorf("%+v", r)
	}
}

func TestDirtyWorktree(t *testing.T) {
	dir, first, second := repo(t)
	wt := filepath.Join(filepath.Dir(dir), "rv")
	if _, err := Start(context.Background(), Options{Repo: dir, Worktree: wt, Head: first, CacheRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "a"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "new"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Start(context.Background(), Options{Repo: dir, Worktree: wt, Head: second, CacheRoot: t.TempDir()})
	var d *DirtyError
	if !errors.As(err, &d) || strings.Join(d.Paths, ",") != "a,new" {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(err.Error(), "has changes: a, new") {
		t.Errorf("%q", err)
	}
	if gitT(t, wt, "rev-parse", "HEAD") != first {
		t.Error("moved a dirty worktree")
	}
}

func TestBadHead(t *testing.T) {
	dir, _, _ := repo(t)
	wt := filepath.Join(filepath.Dir(dir), "rv")
	_, err := Start(context.Background(), Options{Repo: dir, Worktree: wt, Head: "nope", CacheRoot: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), `head "nope" is no commit`) {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(wt); err == nil {
		t.Error("worktree made for a bad head")
	}
}

// A folder that is not its own worktree (here a plain folder inside the
// repository, where git would find the parent) is refused.
func TestNotAWorktree(t *testing.T) {
	dir, first, _ := repo(t)
	wt := filepath.Join(dir, "sub")
	if err := os.Mkdir(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Start(context.Background(), Options{Repo: dir, Worktree: wt, Head: first, CacheRoot: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "is not a git worktree") {
		t.Fatalf("%v", err)
	}
	plain := t.TempDir()
	_, err = Start(context.Background(), Options{Repo: dir, Worktree: plain, Head: first, CacheRoot: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "is not a git worktree") {
		t.Fatalf("%v", err)
	}
}

func TestErrors(t *testing.T) {
	dir, first, _ := repo(t)
	if _, err := Start(context.Background(), Options{Repo: dir, Worktree: "", Head: first}); err == nil {
		t.Error("no worktree accepted")
	}
	if _, err := Start(context.Background(), Options{Repo: dir, Worktree: filepath.Join(dir, "x"), Head: ""}); err == nil {
		t.Error("no head accepted")
	}
	// The cache root cannot be made: a file stands in the way.
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Start(context.Background(), Options{Repo: dir, Worktree: filepath.Join(filepath.Dir(dir), "rv"), Head: first, CacheRoot: file})
	if err == nil {
		t.Error("cache under a file accepted")
	}
	// git worktree add fails: a file stands where its parent folder must be.
	_, err = Start(context.Background(), Options{Repo: dir, Worktree: filepath.Join(file, "rv"), Head: first, CacheRoot: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "git worktree:") {
		t.Errorf("worktree under a file: %v", err)
	}
	// The default repository is the working directory; the default cache root the temp root.
	t.Chdir(dir)
	root := t.TempDir()
	defaultRoot = func() string { return root }
	t.Cleanup(func() { defaultRoot = TempRoot })
	r, err := Start(context.Background(), Options{Worktree: "rv2", Head: first, CacheRoot: ""})
	if err != nil {
		t.Fatal(err)
	}
	if r.Worktree != filepath.Join(dir, "rv2") || filepath.Dir(r.GoCache) != root {
		t.Errorf("%+v", r)
	}
}

func TestTempRoot(t *testing.T) {
	r := TempRoot()
	if st, err := os.Stat(r); err != nil || !st.IsDir() {
		t.Errorf("%s: %v", r, err)
	}
}

func TestDirtyRename(t *testing.T) {
	dir, first, second := repo(t)
	wt := filepath.Join(filepath.Dir(dir), "rv")
	if _, err := Start(context.Background(), Options{Repo: dir, Worktree: wt, Head: first, CacheRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	gitT(t, wt, "mv", "a", "b")
	_, err := Start(context.Background(), Options{Repo: dir, Worktree: wt, Head: second, CacheRoot: t.TempDir()})
	var d *DirtyError
	if !errors.As(err, &d) || strings.Join(d.Paths, ",") != "b" {
		t.Fatalf("%v", err)
	}
}

// A worktree whose status git cannot read is refused, and not moved.
func TestUnreadableWorktree(t *testing.T) {
	dir, first, _ := repo(t)
	wt := filepath.Join(filepath.Dir(dir), "rv")
	if _, err := Start(context.Background(), Options{Repo: dir, Worktree: wt, Head: first, CacheRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	index := strings.TrimSpace(gitT(t, wt, "rev-parse", "--git-path", "index"))
	if !filepath.IsAbs(index) {
		index = filepath.Join(wt, index)
	}
	if err := os.WriteFile(index, []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Start(context.Background(), Options{Repo: dir, Worktree: wt, Head: first, CacheRoot: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "git status:") {
		t.Fatalf("%v", err)
	}
}
