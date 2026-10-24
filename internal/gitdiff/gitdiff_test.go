package gitdiff

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// repo makes a repository on main with one commit, and a feature branch
// checked out in a second worktree.
func repo(t *testing.T) (main, wt string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	main = t.TempDir()
	wt = filepath.Join(t.TempDir(), "wt")
	run(t, main, "init", "-q", "-b", "main")
	write(t, main, "keep.txt", "one\ntwo\nthree\n")
	write(t, main, "old-name.txt", "moved file\nwith lines\n")
	write(t, main, "gone.txt", "bye\n")
	run(t, main, "add", ".")
	run(t, main, "commit", "-q", "-m", "base")
	run(t, main, "worktree", "add", "-q", "-b", "feat", wt)
	return main, wt
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func byPath(files []File) map[string]File {
	m := map[string]File{}
	for _, f := range files {
		m[f.Path] = f
	}
	return m
}

// A worktree's diff holds its commits and its uncommitted and untracked
// files, measured from where it left main.
func TestWorktreeDiff(t *testing.T) {
	main, wt := repo(t)
	ctx := context.Background()
	write(t, wt, "keep.txt", "one\n2\nthree\nfour\n")
	run(t, wt, "mv", "old-name.txt", "new-name.txt")
	run(t, wt, "rm", "-q", "gone.txt")
	write(t, wt, "img.bin", "\x00\x01\x02")
	run(t, wt, "add", ".")
	run(t, wt, "commit", "-q", "-m", "work")
	write(t, wt, "keep.txt", "one\n2\nthree\nfour\nfive\n") // uncommitted
	write(t, wt, "notes/new.md", "draft\n")                 // untracked
	// main moves on; its new commit is not part of the branch's change.
	write(t, main, "main-only.txt", "x\n")
	run(t, main, "add", ".")
	run(t, main, "commit", "-q", "-m", "main moves")

	d, err := Worktree(ctx, wt, "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Base != "main" || d.Head != "feat" || d.Truncated {
		t.Fatalf("base %q head %q truncated %v", d.Base, d.Head, d.Truncated)
	}
	got := byPath(d.Files)
	want := map[string]File{
		"keep.txt":     {Path: "keep.txt", Status: "M", Add: 3, Del: 1},
		"new-name.txt": {Path: "new-name.txt", OldPath: "old-name.txt", Status: "R"},
		"gone.txt":     {Path: "gone.txt", Status: "D", Del: 1},
		"img.bin":      {Path: "img.bin", Status: "A", Binary: true},
		"notes/new.md": {Path: "notes/new.md", Status: "?"},
	}
	for p, w := range want {
		if got[p] != w {
			t.Errorf("%s = %+v, want %+v", p, got[p], w)
		}
	}
	if _, ok := got["main-only.txt"]; ok || len(got) != len(want) {
		t.Errorf("files = %+v", d.Files)
	}
	if !strings.Contains(d.Patch, "+five") || !strings.Contains(d.Patch, "rename to new-name.txt") {
		t.Errorf("patch misses the uncommitted line or the rename:\n%s", d.Patch)
	}
}

// A branch without a worktree shows only its commits.
func TestBranchDiff(t *testing.T) {
	main, wt := repo(t)
	write(t, wt, "keep.txt", "one\ntwo\nthree\nfour\n")
	run(t, wt, "commit", "-q", "-am", "work")
	write(t, wt, "keep.txt", "dirty\n") // not committed, not shown

	d, err := Branch(context.Background(), main, "feat", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != 1 || d.Files[0] != (File{Path: "keep.txt", Status: "M", Add: 1}) {
		t.Fatalf("files = %+v", d.Files)
	}
	if strings.Contains(d.Patch, "dirty") {
		t.Fatal("the branch diff shows uncommitted work")
	}
	for _, bad := range []string{"--output=/tmp/x", "nope"} {
		if _, err := Branch(context.Background(), main, bad, ""); err == nil {
			t.Errorf("Branch(%q) succeeded", bad)
		}
	}
}

func TestNotARepo(t *testing.T) {
	if _, err := Worktree(context.Background(), t.TempDir(), ""); !errors.Is(err, ErrNotRepo) {
		t.Fatalf("err = %v, want ErrNotRepo", err)
	}
}

// Reading a diff runs nothing the repository's config names.
func TestRunsNoRepoPrograms(t *testing.T) {
	_, wt := repo(t)
	marker := filepath.Join(t.TempDir(), "ran")
	hook := filepath.Join(t.TempDir(), "hook.sh")
	write(t, filepath.Dir(hook), "hook.sh", "#!/bin/sh\ntouch "+marker+"\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, wt, "config", "core.fsmonitor", hook)
	run(t, wt, "config", "diff.external", hook)
	write(t, wt, ".gitattributes", "*.txt diff=conv\n")
	run(t, wt, "config", "diff.conv.textconv", hook)
	write(t, wt, "keep.txt", "changed\n")

	if _, err := Worktree(context.Background(), wt, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a program from the repository's config ran")
	}
}

func TestOutputIsBounded(t *testing.T) {
	_, wt := repo(t)
	write(t, wt, "keep.txt", strings.Repeat("line\n", 2000))
	out, err := git(context.Background(), wt, 100, "diff", "--no-color", "HEAD", "--")
	if !errors.Is(err, errTooLong) || len(out) != 100 {
		t.Fatalf("len %d, err %v", len(out), err)
	}
}

func TestParsers(t *testing.T) {
	files := parseNameStatus("M\x00a.go\x00R087\x00x/old.go\x00x/new.go\x00A\x00b c.txt\x00")
	want := []File{{Status: "M", Path: "a.go"}, {Status: "R", OldPath: "x/old.go", Path: "x/new.go"}, {Status: "A", Path: "b c.txt"}}
	if !slices.Equal(files, want) {
		t.Fatalf("name-status = %+v", files)
	}
	stats := parseNumstat("1\t2\ta.go\x003\t0\t\x00x/old.go\x00x/new.go\x00-\t-\tb c.txt\x00")
	if stats["a.go"] != (stat{1, 2, false}) || stats["x/new.go"] != (stat{3, 0, false}) || !stats["b c.txt"].binary {
		t.Fatalf("numstat = %+v", stats)
	}
}

func TestWorktrees(t *testing.T) {
	main, wt := repo(t)
	run(t, main, "worktree", "add", "-q", "--detach", filepath.Join(t.TempDir(), "detached"))
	gone := filepath.Join(t.TempDir(), "gone")
	run(t, main, "worktree", "add", "-q", "-b", "gone", gone)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	list, err := Worktrees(context.Background(), wt) // from any work tree of the repo
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 4 || !list[0].Main || list[0].Branch != "main" || list[1].Branch != "feat" || list[2].Branch != "" || list[2].Head == "" || !list[3].Prunable {
		t.Fatalf("worktrees = %+v", list)
	}
}

func TestParseWorktreesSkipsBare(t *testing.T) {
	got := parseWorktrees("worktree /r.git\nbare\n\nworktree /w\nHEAD abc\nbranch refs/heads/x\nlocked\n\n")
	if len(got) != 1 || got[0] != (WorktreeInfo{Path: "/w", Head: "abc", Branch: "x", Locked: true}) {
		t.Fatalf("got %+v", got)
	}
}

// Prefix settings in a repository's config do not change the patch headers.
func TestPatchPrefixesIgnoreConfig(t *testing.T) {
	for _, key := range []string{"diff.noprefix", "diff.mnemonicPrefix"} {
		_, wt := repo(t)
		run(t, wt, "config", key, "true")
		write(t, wt, "keep.txt", "one\n2\nthree\n")
		d, err := Worktree(context.Background(), wt, "")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(d.Patch, "diff --git a/keep.txt b/keep.txt\n") || !strings.Contains(d.Patch, "+++ b/keep.txt\n") {
			t.Errorf("%s: patch headers:\n%s", key, d.Patch)
		}
	}
}
