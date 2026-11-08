package snapshot

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata golden files")

func TestMain(m *testing.M) {
	flag.Parse()
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Exit(m.Run())
}

func runGitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeT(t *testing.T, root, rel, body string, mode fs.FileMode) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(full, mode); err != nil {
		t.Fatal(err)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitT(t, root, "init", "-q", "--initial-branch=feature/work")
	return root
}

// dirtyRepo builds the acceptance tree: committed, staged, unstaged, deleted,
// untracked, ignored, rebuildable and over-cap files.
func dirtyRepo(t *testing.T) string {
	t.Helper()
	root := newRepo(t)
	writeT(t, root, ".gitignore", ".env\n*.db\nnode_modules/\nhuge.ignored\n", 0o644)
	writeT(t, root, ".gitattributes", "crlf.txt text eol=crlf\n", 0o644)
	writeT(t, root, "a.txt", "committed\n", 0o644)
	writeT(t, root, "dir/b.txt", "b\n", 0o644)
	writeT(t, root, "exec.sh", "#!/bin/sh\n", 0o755)
	writeT(t, root, "gone.txt", "will be deleted\n", 0o644)
	writeT(t, root, "staged.txt", "v1\n", 0o644)
	writeT(t, root, "crlf.txt", "one\r\n", 0o644)
	writeT(t, root, "bigtracked.bin", strings.Repeat("t", 2000), 0o644)
	if err := os.Symlink("a.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	runGitT(t, root, "add", "-A")
	runGitT(t, root, "commit", "-qm", "first")
	// Staged, then changed again: the two trees must differ.
	writeT(t, root, "staged.txt", "v2 staged\n", 0o644)
	runGitT(t, root, "add", "staged.txt")
	writeT(t, root, "staged.txt", "v3 unstaged\n", 0o644)
	writeT(t, root, "a.txt", "committed and edited\n", 0o644)
	// LF in a file git would convert: the bytes must survive unfiltered.
	writeT(t, root, "crlf.txt", "one\ntwo\n", 0o644)
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	writeT(t, root, "new file.txt", "untracked\n", 0o640)
	writeT(t, root, "quo\"te", "quoted\n", 0o644)
	writeT(t, root, "nl\nname", "newline\n", 0o644)
	writeT(t, root, ".env", "SECRET=1\n", 0o600)
	writeT(t, root, "data.db", "\x00\x01binary\xff", 0o640)
	if err := os.Symlink("/etc/hosts", filepath.Join(root, "abs.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "emptydir"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeT(t, root, "node_modules/x/y.js", "cache\n", 0o644)
	writeT(t, root, "huge.ignored", strings.Repeat("i", 2000), 0o644)
	writeT(t, root, "huge.txt", strings.Repeat("u", 2000), 0o644)
	writeT(t, root, "bigtracked.bin", strings.Repeat("w", 2000), 0o644)
	return root
}

func takeT(t *testing.T, root, task, op string) (string, *Manifest) {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "snap")
	m, err := Take(context.Background(), testOptions(root, dest, task, op))
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	return dest, m
}

func testOptions(root, dest, task, op string) Options {
	return Options{
		Worktree: root, Dest: dest, Task: task, Op: op,
		Rebuildable: []string{"node_modules"}, FileCap: 1024,
	}
}

type fileState struct {
	mode  fs.FileMode
	body  string
	mtime int64
}

// readTree captures every path outside .git, skipping the named paths.
func readTree(t *testing.T, root string, skip ...string) map[string]fileState {
	t.Helper()
	out := map[string]fileState{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		for _, s := range append([]string{".git"}, skip...) {
			if rel == s {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		st := fileState{mode: info.Mode(), mtime: info.ModTime().UnixNano()}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			st.body, err = os.Readlink(p)
			st.mode, st.mtime = fs.ModeSymlink, 0
		case info.IsDir():
			entries, _ := os.ReadDir(p)
			if len(entries) != 0 {
				return nil
			}
			st.mtime = 0
		default:
			var raw []byte
			raw, err = os.ReadFile(p)
			st.body = string(raw)
		}
		out[rel] = st
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func compareTrees(t *testing.T, want, got map[string]fileState) {
	t.Helper()
	for p, w := range want {
		g, ok := got[p]
		if !ok {
			t.Errorf("missing after restore: %q", p)
			continue
		}
		if g != w {
			t.Errorf("%q differs: want %+v, got %+v", p, w, g)
		}
	}
	for p := range got {
		if _, ok := want[p]; !ok {
			t.Errorf("unexpected after restore: %q", p)
		}
	}
}

func TestDirtyTreeRestoresByteForByteWithoutSource(t *testing.T) {
	root := dirtyRepo(t)
	skip := []string{"node_modules", "huge.ignored", "huge.txt", "bigtracked.bin"}
	want := readTree(t, root, skip...)
	wantCached := runGitT(t, root, "diff", "--cached", "--name-status")
	wantHead := strings.TrimSpace(runGitT(t, root, "rev-parse", "HEAD"))
	wantStaged := runGitT(t, root, "show", ":staged.txt")

	dest, m := takeT(t, root, "task-1", "op-1")
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(t.TempDir(), "restored")
	if err := Restore(context.Background(), dest, target); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	compareTrees(t, want, readTree(t, target, skip...))

	if got := strings.TrimSpace(runGitT(t, target, "rev-parse", "HEAD")); got != wantHead {
		t.Errorf("HEAD = %s, want %s", got, wantHead)
	}
	if got := strings.TrimSpace(runGitT(t, target, "symbolic-ref", "HEAD")); got != "refs/heads/feature/work" {
		t.Errorf("branch = %s", got)
	}
	if got := runGitT(t, target, "diff", "--cached", "--name-status"); got != wantCached {
		t.Errorf("staged state: want %q, got %q", wantCached, got)
	}
	if got := runGitT(t, target, "show", ":staged.txt"); got != wantStaged {
		t.Errorf("staged blob: want %q, got %q", wantStaged, got)
	}
	if m.StagedTree == m.WorkingTree {
		t.Error("staged and working trees must differ")
	}
	if m.Head != wantHead || m.Branch != "refs/heads/feature/work" {
		t.Errorf("manifest head/branch: %s %s", m.Head, m.Branch)
	}
}

func TestSnapshotFolderIsPrivate(t *testing.T) {
	root := dirtyRepo(t)
	dest, _ := takeT(t, root, "t", "o")
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("folder mode = %o", info.Mode().Perm())
	}
	for _, name := range []string{ManifestName, BundleName, PreservedName} {
		fi, err := os.Stat(filepath.Join(dest, name))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o", name, fi.Mode().Perm())
		}
	}
	if _, err := os.Stat(filepath.Join(dest, workDirName)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("work folder left behind: %v", err)
	}
}

func excludedReason(m *Manifest, p string) string {
	for _, e := range m.Excluded {
		if string(e.Path) == p {
			return e.Reason
		}
	}
	return ""
}

func captured(m *Manifest, p string) bool {
	for _, f := range m.Files {
		if string(f.Path) == p {
			return true
		}
	}
	return false
}

func TestFileOverCapIsListedNotCaptured(t *testing.T) {
	root := dirtyRepo(t)
	dest, m := takeT(t, root, "t", "o")
	for _, p := range []string{"huge.ignored", "huge.txt", "bigtracked.bin"} {
		if got := excludedReason(m, p); got != ReasonOverCap {
			t.Errorf("%s reason = %q", p, got)
		}
		if captured(m, p) {
			t.Errorf("%s is captured", p)
		}
	}
	target := filepath.Join(t.TempDir(), "r")
	if err := Restore(context.Background(), dest, target); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"huge.ignored", "huge.txt", "bigtracked.bin"} {
		if _, err := os.Lstat(filepath.Join(target, p)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s restored: %v", p, err)
		}
	}
}

func TestFileExactlyAtCapIsCaptured(t *testing.T) {
	root := newRepo(t)
	writeT(t, root, "exact.txt", strings.Repeat("e", 1024), 0o644)
	_, m := takeT(t, root, "t", "o")
	if !captured(m, "exact.txt") {
		t.Fatal("a file at the cap must be captured")
	}
}

func TestDefaultFileCapApplies(t *testing.T) {
	root := newRepo(t)
	writeT(t, root, "f", "x", 0o644)
	opts := testOptions(root, filepath.Join(t.TempDir(), "s"), "t", "o")
	opts.FileCap = 0
	m, err := Take(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if m.FileCap != DefaultFileCap {
		t.Fatalf("cap = %d", m.FileCap)
	}
}

func TestRebuildableIsListedOnce(t *testing.T) {
	root := dirtyRepo(t)
	writeT(t, root, "node_modules/z.js", "more\n", 0o644)
	_, m := takeT(t, root, "t", "o")
	count := 0
	for _, e := range m.Excluded {
		if e.Reason == ReasonRebuildable {
			count++
			if string(e.Path) != "node_modules" || e.Pattern != "node_modules" {
				t.Errorf("exclusion = %+v", e)
			}
		}
	}
	if count != 1 {
		t.Fatalf("rebuildable listed %d times", count)
	}
}

func TestUntrackedRebuildableIsCapturedTrackedIsKept(t *testing.T) {
	root := newRepo(t)
	writeT(t, root, ".gitignore", "build/*.o\n", 0o644)
	writeT(t, root, "build/tracked.sh", "keep\n", 0o644)
	runGitT(t, root, "add", "-A")
	runGitT(t, root, "commit", "-qm", "x")
	writeT(t, root, "build/untracked.txt", "work\n", 0o644)
	writeT(t, root, "docs/build/notes.md", "notes\n", 0o644)
	writeT(t, root, "build/ignored.o", "drop\n", 0o644)
	opts := testOptions(root, filepath.Join(t.TempDir(), "s"), "t", "o")
	opts.Rebuildable = []string{"build"}
	m, err := Take(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"build/tracked.sh", "build/untracked.txt", "docs/build/notes.md"} {
		if !captured(m, keep) {
			t.Errorf("%s must be captured", keep)
		}
	}
	if captured(m, "build/ignored.o") || excludedReason(m, "build") != ReasonRebuildable {
		t.Errorf("an ignored match must be excluded: %+v", m.Excluded)
	}
	if excludedReason(m, "docs/build") != "" {
		t.Errorf("an untracked path must not be excluded: %+v", m.Excluded)
	}
}

func TestMatchRebuildable(t *testing.T) {
	cases := []struct {
		patterns []string
		rel      string
		prefix   string
		ok       bool
	}{
		{[]string{"node_modules"}, "node_modules/a/b.js", "node_modules", true},
		{[]string{"node_modules"}, "web/node_modules/a.js", "web/node_modules", true},
		{[]string{"*.pyc"}, "a/b.pyc", "a/b.pyc", true},
		{[]string{"web/dist"}, "web/dist/a.js", "web/dist", true},
		{[]string{"web/dist"}, "other/web/dist/a.js", "", false},
		{[]string{"build"}, "builder/a", "", false},
		{nil, "a", "", false},
	}
	for _, c := range cases {
		prefix, _, ok := matchRebuildable(c.patterns, c.rel)
		if ok != c.ok || prefix != c.prefix {
			t.Errorf("%v %q: got %q %v", c.patterns, c.rel, prefix, ok)
		}
	}
}

func TestDefaultRebuildableIsACopy(t *testing.T) {
	first := DefaultRebuildable()
	if len(first) == 0 {
		t.Fatal("no defaults")
	}
	first[0] = "changed"
	if DefaultRebuildable()[0] == "changed" {
		t.Fatal("defaults are shared")
	}
}

func TestOptionsAreValidated(t *testing.T) {
	root := newRepo(t)
	cases := map[string]func(*Options){
		"bad task":   func(o *Options) { o.Task = "a b" },
		"empty op":   func(o *Options) { o.Op = "" },
		"dest":       func(o *Options) { o.Dest = "" },
		"cap":        func(o *Options) { o.FileCap = -1 },
		"glob":       func(o *Options) { o.Rebuildable = []string{"["} },
		"empty":      func(o *Options) { o.Rebuildable = []string{""} },
		"absolute":   func(o *Options) { o.Rebuildable = []string{"/etc"} },
		"parent":     func(o *Options) { o.Rebuildable = []string{"../x"} },
		"not a repo": func(o *Options) { o.Worktree = t.TempDir() },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			opts := testOptions(root, filepath.Join(t.TempDir(), "s"), "t", "o")
			change(&opts)
			if _, err := Take(context.Background(), opts); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestDestMustNotExist(t *testing.T) {
	root := newRepo(t)
	dest := t.TempDir()
	marker := filepath.Join(dest, "keep")
	writeT(t, dest, "keep", "x", 0o644)
	_, err := Take(context.Background(), testOptions(root, dest, "t", "o"))
	if !errors.Is(err, ErrDestExists) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("an existing folder must be left alone")
	}
}

func TestSameOpTwiceIsRefusedAndKeepsTheFirst(t *testing.T) {
	root := newRepo(t)
	writeT(t, root, "f", "x", 0o644)
	_, first := takeT(t, root, "t", "o")
	_, err := Take(context.Background(), testOptions(root, filepath.Join(t.TempDir(), "s2"), "t", "o"))
	if err == nil {
		t.Fatal("expected a refusal")
	}
	got := strings.TrimSpace(runGitT(t, root, "rev-parse", first.SnapshotRef))
	if got != first.SnapshotCommit {
		t.Fatalf("ref moved: %s", got)
	}
}

func TestSnapshotRefAndUserIndexUntouched(t *testing.T) {
	root := dirtyRepo(t)
	indexPath := filepath.Join(root, ".git", "index")
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	_, m := takeT(t, root, "task", "op")
	after, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("Take changed the user's index")
	}
	if m.SnapshotRef != "refs/herdsman/snapshot/task/op" {
		t.Errorf("ref = %s", m.SnapshotRef)
	}
	if got := strings.TrimSpace(runGitT(t, root, "rev-parse", m.SnapshotRef)); got != m.SnapshotCommit {
		t.Errorf("ref points at %s", got)
	}
	for name, want := range map[string]string{"staged": m.StagedTree, "working": m.WorkingTree} {
		if got := strings.TrimSpace(runGitT(t, root, "rev-parse", m.SnapshotCommit+":"+name)); got != want {
			t.Errorf("%s tree: %s want %s", name, got, want)
		}
	}
	if m.StartedAt.IsZero() || m.FinishedAt.Before(m.StartedAt) {
		t.Errorf("times: %v %v", m.StartedAt, m.FinishedAt)
	}
}

func TestUnbornBranchRestores(t *testing.T) {
	root := newRepo(t)
	writeT(t, root, "staged", "s\n", 0o644)
	runGitT(t, root, "add", "staged")
	writeT(t, root, "untracked", "u\n", 0o644)
	dest, m := takeT(t, root, "t", "o")
	if m.Head != "" {
		t.Fatalf("head = %q", m.Head)
	}
	target := filepath.Join(t.TempDir(), "r")
	if err := Restore(context.Background(), dest, target); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runGitT(t, target, "symbolic-ref", "HEAD")); got != "refs/heads/feature/work" {
		t.Errorf("branch = %s", got)
	}
	if got := runGitT(t, target, "ls-files", "--stage"); !strings.Contains(got, "staged") || strings.Contains(got, "untracked") {
		t.Errorf("index = %s", got)
	}
	if _, err := os.Stat(filepath.Join(target, "untracked")); err != nil {
		t.Error(err)
	}
}

func TestDetachedHeadRestores(t *testing.T) {
	root := newRepo(t)
	writeT(t, root, "f", "1\n", 0o644)
	runGitT(t, root, "add", "-A")
	runGitT(t, root, "commit", "-qm", "1")
	writeT(t, root, "f", "2\n", 0o644)
	runGitT(t, root, "commit", "-qam", "2")
	runGitT(t, root, "checkout", "-q", "--detach", "HEAD~1")
	head := strings.TrimSpace(runGitT(t, root, "rev-parse", "HEAD"))
	dest, m := takeT(t, root, "t", "o")
	if m.Branch != "" {
		t.Fatalf("branch = %q", m.Branch)
	}
	target := filepath.Join(t.TempDir(), "r")
	if err := Restore(context.Background(), dest, target); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runGitT(t, target, "rev-parse", "HEAD")); got != head {
		t.Errorf("HEAD = %s", got)
	}
	cmd := exec.Command("git", "symbolic-ref", "-q", "HEAD")
	cmd.Dir = target
	if err := cmd.Run(); err == nil {
		t.Error("HEAD must stay detached")
	}
}

func TestPathTypeChangesAreCaptured(t *testing.T) {
	root := newRepo(t)
	writeT(t, root, "file-then-dir", "x\n", 0o644)
	writeT(t, root, "dir-then-file/inner", "y\n", 0o644)
	runGitT(t, root, "add", "-A")
	runGitT(t, root, "commit", "-qm", "x")
	if err := os.Remove(filepath.Join(root, "file-then-dir")); err != nil {
		t.Fatal(err)
	}
	writeT(t, root, "file-then-dir/now", "z\n", 0o644)
	if err := os.RemoveAll(filepath.Join(root, "dir-then-file")); err != nil {
		t.Fatal(err)
	}
	writeT(t, root, "dir-then-file", "w\n", 0o644)
	want := readTree(t, root)
	dest, _ := takeT(t, root, "t", "o")
	target := filepath.Join(t.TempDir(), "r")
	if err := Restore(context.Background(), dest, target); err != nil {
		t.Fatal(err)
	}
	compareTrees(t, want, readTree(t, target))
}

func TestSubmoduleAndNestedRepositoriesAreListed(t *testing.T) {
	upstream := newRepo(t)
	writeT(t, upstream, "u", "u\n", 0o644)
	runGitT(t, upstream, "add", "-A")
	runGitT(t, upstream, "commit", "-qm", "u")
	upstreamHead := strings.TrimSpace(runGitT(t, upstream, "rev-parse", "HEAD"))

	root := newRepo(t)
	writeT(t, root, ".gitignore", "ignored-repo/\n", 0o644)
	runGitT(t, root, "-c", "protocol.file.allow=always", "submodule", "--quiet", "add", upstream, "sub")
	runGitT(t, root, "add", "-A")
	runGitT(t, root, "commit", "-qm", "with sub")
	// A gitlink with no checkout: registered, but nothing on disk to read.
	runGitT(t, root, "update-index", "--add", "--cacheinfo", "160000,"+upstreamHead+",absent")
	for _, nested := range []string{"nested", "ignored-repo"} {
		dir := filepath.Join(root, nested)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		runGitT(t, dir, "init", "-q")
		writeT(t, dir, "inner", "i\n", 0o644)
		runGitT(t, dir, "add", "-A")
		runGitT(t, dir, "commit", "-qm", "inner")
	}
	dest, m := takeT(t, root, "t", "o")
	if excludedReason(m, "sub") != ReasonSubmodule {
		t.Errorf("excluded: %+v", m.Excluded)
	}
	for _, nested := range []string{"nested", "ignored-repo"} {
		if excludedReason(m, nested) != ReasonNestedRepository {
			t.Errorf("%s: %+v", nested, m.Excluded)
		}
	}
	byPath := map[string]Submodule{}
	for _, s := range m.Submodules {
		byPath[string(s.Path)] = s
	}
	if byPath["sub"].Head != upstreamHead || byPath["sub"].IndexCommit != upstreamHead {
		t.Errorf("sub = %+v, want %s", byPath["sub"], upstreamHead)
	}
	if byPath["absent"].Head != "" || byPath["absent"].IndexCommit != upstreamHead {
		t.Errorf("absent = %+v", byPath["absent"])
	}
	target := filepath.Join(t.TempDir(), "r")
	if err := Restore(context.Background(), dest, target); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(target, "sub"))
	if err != nil || !info.IsDir() {
		t.Fatalf("submodule folder: %v", err)
	}
	if got := runGitT(t, target, "ls-files", "--stage", "sub"); !strings.HasPrefix(got, "160000 "+upstreamHead) {
		t.Errorf("gitlink = %s", got)
	}
}

func TestSpecialFilesAreListed(t *testing.T) {
	root := newRepo(t)
	writeT(t, root, "f", "x\n", 0o644)
	runGitT(t, root, "add", "-A")
	runGitT(t, root, "commit", "-qm", "x")
	if err := os.Remove(filepath.Join(root, "f")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"f", "pipe"} {
		if out, err := exec.Command("mkfifo", filepath.Join(root, name)).CombinedOutput(); err != nil {
			t.Fatalf("mkfifo: %v %s", err, out)
		}
	}
	_, m := takeT(t, root, "t", "o")
	for _, name := range []string{"f", "pipe"} {
		if excludedReason(m, name) != ReasonSpecial {
			t.Errorf("%s: %+v", name, m.Excluded)
		}
	}
}

func TestVerifyPasses(t *testing.T) {
	root := dirtyRepo(t)
	dest, m := takeT(t, root, "t", "o")
	got, err := Verify(context.Background(), dest)
	if err != nil {
		t.Fatal(err)
	}
	if got.SnapshotCommit != m.SnapshotCommit {
		t.Fatal("manifest differs")
	}
}

func artifactOf(t *testing.T, err error) string {
	t.Helper()
	var ve *VerifyError
	if !errors.As(err, &ve) {
		t.Fatalf("not a VerifyError: %v", err)
	}
	if !strings.Contains(err.Error(), ve.Artifact) {
		t.Fatalf("message does not name %s: %v", ve.Artifact, err)
	}
	return ve.Artifact
}

func flipByte(t *testing.T, p string, at int) {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if at < 0 {
		at += len(raw)
	}
	raw[at] ^= 0xff
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func sha(t *testing.T, p string) (string, int64) {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), int64(len(raw))
}

func rewriteManifest(t *testing.T, dest string, change func(*Manifest)) {
	t.Helper()
	m, err := ReadManifest(dest)
	if err != nil {
		t.Fatal(err)
	}
	change(m)
	if err := writeManifest(dest, m); err != nil {
		t.Fatal(err)
	}
}

func TestCorruptBundleFailsByName(t *testing.T) {
	root := dirtyRepo(t)
	dest, _ := takeT(t, root, "t", "o")
	flipByte(t, filepath.Join(dest, BundleName), -30)
	_, err := Verify(context.Background(), dest)
	if got := artifactOf(t, err); got != BundleName {
		t.Fatalf("artifact = %s", got)
	}
	target := filepath.Join(t.TempDir(), "r")
	if err := Restore(context.Background(), dest, target); artifactOf(t, err) != BundleName {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a failed verification must write nothing")
	}
}

// A bundle whose checksum was forged must still fail on its own content.
func TestCorruptBundleWithMatchingChecksumFailsByName(t *testing.T) {
	root := dirtyRepo(t)
	dest, _ := takeT(t, root, "t", "o")
	p := filepath.Join(dest, BundleName)
	flipByte(t, p, -30)
	sum, size := sha(t, p)
	rewriteManifest(t, dest, func(m *Manifest) { m.Bundle.SHA256, m.Bundle.Size = sum, size })
	_, err := Verify(context.Background(), dest)
	if got := artifactOf(t, err); got != BundleName {
		t.Fatalf("artifact = %s", got)
	}
}

func TestCorruptPreservedFailsByName(t *testing.T) {
	root := dirtyRepo(t)
	dest, _ := takeT(t, root, "t", "o")
	flipByte(t, filepath.Join(dest, PreservedName), 600)
	_, err := Verify(context.Background(), dest)
	if got := artifactOf(t, err); got != PreservedName {
		t.Fatalf("artifact = %s", got)
	}
}

func TestVerifyFindsManifestProblems(t *testing.T) {
	cases := map[string]struct {
		change   func(*Manifest)
		artifact string
	}{
		"staged tree":  {func(m *Manifest) { m.StagedTree = strings.Repeat("0", len(m.StagedTree)) }, BundleName},
		"working tree": {func(m *Manifest) { m.WorkingTree = strings.Repeat("0", len(m.WorkingTree)) }, BundleName},
		"commit":       {func(m *Manifest) { m.SnapshotCommit = strings.Repeat("0", len(m.SnapshotCommit)) }, BundleName},
		"extra file": {func(m *Manifest) {
			m.Files = append(m.Files, FileRecord{Path: "ghost", Mode: 0o100644, Source: SourceTree})
		}, BundleName},
		"mode": {func(m *Manifest) {
			for i := range m.Files {
				if m.Files[i].Source == SourceTree {
					m.Files[i].Mode = 0o100755 ^ m.Files[i].Mode ^ 0o100644
					return
				}
			}
		}, BundleName},
		"extra preserved": {func(m *Manifest) {
			m.Files = append(m.Files, FileRecord{Path: "ghost", Mode: 0o100644, Source: SourcePreserved})
		}, PreservedName},
		"preserved mode": {func(m *Manifest) {
			for i := range m.Files {
				if m.Files[i].Source == SourcePreserved && string(m.Files[i].Path) == ".env" {
					m.Files[i].Mode = 0o100644
				}
			}
		}, PreservedName},
		"unsafe path": {func(m *Manifest) {
			m.Files = append(m.Files, FileRecord{Path: "../x", Mode: 0o100644, Source: SourceTree})
		}, ManifestName},
		"bad head":          {func(m *Manifest) { m.Head = "not-an-id" }, ManifestName},
		"bad commit id":     {func(m *Manifest) { m.StagedTree = "--flag" }, ManifestName},
		"no head or branch": {func(m *Manifest) { m.Head, m.Branch = "", "" }, ManifestName},
		"malformed branch":  {func(m *Manifest) { m.Branch = "refs/heads/a b" }, ManifestName},
		"bad branch":        {func(m *Manifest) { m.Branch = "refs/tags/x" }, ManifestName},
		"format":            {func(m *Manifest) { m.ObjectFormat = "md5" }, ManifestName},
	}
	root := dirtyRepo(t)
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			runGitT(t, root, "update-ref", "-d", "refs/herdsman/snapshot/t/o")
			dest, _ := takeT(t, root, "t", "o")
			rewriteManifest(t, dest, c.change)
			_, err := Verify(context.Background(), dest)
			if got := artifactOf(t, err); got != c.artifact {
				t.Fatalf("artifact = %s: %v", got, err)
			}
		})
	}
}

func TestVerifyMissingAndForeignInputs(t *testing.T) {
	ctx := context.Background()
	if _, err := Verify(ctx, filepath.Join(t.TempDir(), "absent")); artifactOf(t, err) != ManifestName {
		t.Fatal(err)
	}
	root := newRepo(t)
	writeT(t, root, "f", "x\n", 0o644)

	dest, _ := takeT(t, root, "a", "1")
	if err := os.Remove(filepath.Join(dest, BundleName)); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(ctx, dest); artifactOf(t, err) != BundleName {
		t.Fatal(err)
	}

	dest, _ = takeT(t, root, "a", "2")
	if err := os.Remove(filepath.Join(dest, PreservedName)); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(ctx, dest); artifactOf(t, err) != PreservedName {
		t.Fatal(err)
	}

	dest, _ = takeT(t, root, "a", "3")
	if err := os.Chmod(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(ctx, dest); artifactOf(t, err) != FolderArtifact {
		t.Fatal(err)
	}

	dest, _ = takeT(t, root, "a", "4")
	rewriteManifest(t, dest, func(m *Manifest) { m.Schema = "2.0.0" })
	if _, err := Verify(ctx, dest); artifactOf(t, err) != ManifestName {
		t.Fatal(err)
	}

	dest, _ = takeT(t, root, "a", "5")
	writeT(t, dest, ManifestName, "{not json", 0o600)
	if _, err := Verify(ctx, dest); artifactOf(t, err) != ManifestName {
		t.Fatal(err)
	}
}

type tarEntry struct {
	name, link, body string
	flag             byte
	mode             int64
}

func writeHostileTar(t *testing.T, dest string, entries []tarEntry) {
	t.Helper()
	p := filepath.Join(dest, PreservedName)
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Linkname: e.link, Typeflag: e.flag, Mode: e.mode, Size: int64(len(e.body)), Format: tar.FormatGNU}
		if err := w.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	sum, size := sha(t, p)
	rewriteManifest(t, dest, func(m *Manifest) {
		m.Preserved.SHA256, m.Preserved.Size = sum, size
		var files []FileRecord
		for _, f := range m.Files {
			if f.Source != SourcePreserved {
				files = append(files, f)
			}
		}
		for _, e := range entries {
			mode := uint32(0o100000 | e.mode)
			switch e.flag {
			case tar.TypeSymlink:
				mode = 0o120000
			case tar.TypeDir:
				mode = uint32(0o040000 | e.mode)
			}
			files = append(files, FileRecord{Path: Path(strings.TrimSuffix(e.name, "/")), Mode: mode, Source: SourcePreserved})
		}
		m.Files = files
	})
}

func TestHostileTarCannotEscape(t *testing.T) {
	outsideBase := t.TempDir()
	outside := filepath.Join(outsideBase, "pwned")
	reg := func(name string) tarEntry {
		return tarEntry{name: name, body: "x", flag: tar.TypeReg, mode: 0o644}
	}
	cases := map[string][]tarEntry{
		"absolute":           {reg(outside)},
		"dotdot":             {reg("../pwned")},
		"inner dotdot":       {reg("a/../../pwned")},
		"git dir":            {reg(".git/hooks/post-checkout")},
		"git dir any case":   {reg(".GIT/config")},
		"symlink then write": {{name: "esc", link: outsideBase, flag: tar.TypeSymlink}, reg("esc/pwned")},
		"hard link":          {{name: "hl", link: "/etc/hosts", flag: tar.TypeLink}},
		"device":             {{name: "dev", flag: tar.TypeChar, mode: 0o644}},
		"duplicate":          {reg("twice"), reg("twice")},
		"empty name":         {reg(".")},
	}
	root := newRepo(t)
	writeT(t, root, "f", "x\n", 0o644)
	i := 0
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			i++
			dest, _ := takeT(t, root, "hostile", fmt.Sprintf("o%d", i))
			writeHostileTar(t, dest, entries)
			_, err := Verify(context.Background(), dest)
			if artifactOf(t, err) != PreservedName {
				t.Fatalf("verify: %v", err)
			}
			target := filepath.Join(t.TempDir(), "r")
			if err := Restore(context.Background(), dest, target); err == nil {
				t.Fatal("restore must refuse")
			}
			if _, err := os.Lstat(outside); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("wrote outside: %v", err)
			}
			if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("partial restore left behind: %v", err)
			}
		})
	}
}

// The tree holds a symlink out of the folder and the tar writes through it:
// each source alone looks valid, so verification must check them combined.
func TestTreeSymlinkCannotCarryATarWrite(t *testing.T) {
	outsideBase := t.TempDir()
	root := newRepo(t)
	if err := os.Symlink(outsideBase, filepath.Join(root, "lnk")); err != nil {
		t.Fatal(err)
	}
	dest, _ := takeT(t, root, "t", "o")
	writeHostileTar(t, dest, []tarEntry{{name: "lnk/pwned", body: "x", flag: tar.TypeReg, mode: 0o644}})
	_, err := Verify(context.Background(), dest)
	if artifactOf(t, err) != PreservedName || !errors.Is(err, ErrUnsafePath) || !strings.Contains(err.Error(), "lnk/pwned") {
		t.Fatalf("verify: %v", err)
	}
	target := filepath.Join(t.TempDir(), "r")
	err = Restore(context.Background(), dest, target)
	if !errors.Is(err, ErrUnsafePath) || !strings.Contains(err.Error(), "lnk/pwned") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outsideBase, "pwned")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("wrote through the symlink")
	}
}

func TestPreservedSymlinkWithOutsideTargetRestores(t *testing.T) {
	root := dirtyRepo(t)
	dest, _ := takeT(t, root, "t", "o")
	target := filepath.Join(t.TempDir(), "r")
	if err := Restore(context.Background(), dest, target); err != nil {
		t.Fatal(err)
	}
	got, err := os.Readlink(filepath.Join(target, "abs.db"))
	if err != nil || got != "/etc/hosts" {
		t.Fatalf("link = %q %v", got, err)
	}
}

func TestRestoreRefusesOccupiedTarget(t *testing.T) {
	root := newRepo(t)
	writeT(t, root, "f", "x\n", 0o644)
	dest, _ := takeT(t, root, "t", "o")
	occupied := t.TempDir()
	writeT(t, occupied, "keep", "mine", 0o644)
	if err := Restore(context.Background(), dest, occupied); !errors.Is(err, ErrTargetOccupied) {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(occupied, "keep")); string(got) != "mine" {
		t.Fatal("existing file changed")
	}
	file := filepath.Join(t.TempDir(), "a-file")
	writeT(t, filepath.Dir(file), "a-file", "x", 0o644)
	if err := Restore(context.Background(), dest, file); !errors.Is(err, ErrTargetOccupied) {
		t.Fatalf("err = %v", err)
	}
}

func TestRestoreIntoEmptyFolderKeepsItsMode(t *testing.T) {
	root := newRepo(t)
	if err := os.Chmod(root, 0o750); err != nil {
		t.Fatal(err)
	}
	writeT(t, root, "f", "x\n", 0o644)
	dest, _ := takeT(t, root, "t", "o")
	empty := t.TempDir()
	if err := Restore(context.Background(), dest, empty); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(empty)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o750 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(empty, "f")); err != nil {
		t.Fatal(err)
	}
}

func TestSafeRel(t *testing.T) {
	good := []string{"a", "a/b", ".env", "a/.gitignore", "..a", "a..", "dir/"}
	bad := []string{"", ".", "..", "/a", "a/../b", "a//b", "./a", "a/.git/b", ".Git", "a\x00b"}
	for _, p := range good {
		if err := safeRel(p); err != nil {
			t.Errorf("%q: %v", p, err)
		}
	}
	for _, p := range bad {
		if !errors.Is(safeRel(p), ErrUnsafePath) {
			t.Errorf("%q accepted", p)
		}
	}
}

func TestPathJSONKeepsInvalidUTF8(t *testing.T) {
	for _, in := range []Path{"plain", "unié", "bad\xff\xfe"} {
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		var out Path
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if out != in {
			t.Errorf("%q round-tripped to %q via %s", in, out, raw)
		}
	}
	var out Path
	for _, bad := range []string{`42`, `{"base64":"***"}`} {
		if err := json.Unmarshal([]byte(bad), &out); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestCQuote(t *testing.T) {
	cases := map[string]string{
		"plain":       "plain",
		"new\nl":      `"new\012l"`,
		"cr\r":        `"cr\015"`,
		"t\tb":        `"t\011b"`,
		"d\x7f":       `"d\177"`,
		`q"t`:         `"q\"t"`,
		`back\sl`:     `"back\\sl"`,
		`"lead`:       `"\"lead"`,
		"sp ace":      "sp ace",
		"caf\xc3\xa9": "caf\xc3\xa9",
	}
	for in, want := range cases {
		if got := cQuote(in); got != want {
			t.Errorf("%q: got %s want %s", in, got, want)
		}
	}
}

func goldenManifest() *Manifest {
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	return &Manifest{
		Schema: SchemaVersion, Task: "task", Op: "op",
		StartedAt: at, FinishedAt: at.Add(time.Second),
		Worktree: "/repo", WorktreeMode: 0o755, ObjectFormat: "sha1",
		Head: strings.Repeat("a", 40), Branch: "refs/heads/main",
		SnapshotRef: "refs/herdsman/snapshot/task/op", SnapshotCommit: strings.Repeat("b", 40),
		StagedTree: strings.Repeat("c", 40), WorkingTree: strings.Repeat("d", 40),
		Bundle:      Artifact{Name: BundleName, SHA256: strings.Repeat("e", 64), Size: 10},
		Preserved:   Artifact{Name: PreservedName, SHA256: strings.Repeat("f", 64), Size: 20},
		FileCap:     DefaultFileCap,
		Rebuildable: []string{"node_modules"},
		Files: []FileRecord{
			{Path: ".env", Mode: 0o100600, Size: 3, MTimeNS: 1, Source: SourcePreserved},
			{Path: "a", Mode: 0o100644, Perm: 0o644, Size: 2, MTimeNS: 2, Source: SourceTree},
		},
		Submodules: []Submodule{{Path: "sub", IndexCommit: strings.Repeat("1", 40), Head: strings.Repeat("2", 40)}},
		Excluded: []Exclusion{
			{Path: "huge", Reason: ReasonOverCap, Size: 99},
			{Path: "node_modules", Reason: ReasonRebuildable, Pattern: "node_modules"},
		},
	}
}

// The manifest is a stored artifact: this golden pins its wire form.
func TestManifestGolden(t *testing.T) {
	raw, err := marshalManifest(goldenManifest())
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join("testdata", "manifest.golden.json")
	if *updateGolden {
		if err := os.WriteFile(p, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, want) {
		t.Fatalf("manifest form changed:\n%s", raw)
	}
}

// failGit installs a git that fails when its arguments contain pattern and
// runs the real git otherwise, after any number of matching calls to skip.
func failGit(t *testing.T, pattern string) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "git")
	body := fmt.Sprintf("#!/bin/sh\ncase \" $* \" in\n*%q*) echo planted failure >&2; exit 1;;\nesac\nexec %q \"$@\"\n", pattern, real)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	old := gitBinary
	gitBinary = script
	t.Cleanup(func() { gitBinary = old })
}

func TestTakeStopsOnEveryGitFailureAndCleansUp(t *testing.T) {
	patterns := []string{
		"--show-object-format", "--git-path index", "ls-files -z -s", "ls-files -z --others --exclude-standard",
		"--others --ignored", "init -q --bare", "hash-object --no-filters", "hash-object -w --stdin", "write-tree", "update-index",
		"mktree", "commit-tree", "update-ref refs/herdsman", "bundle create", "bundle verify", "fetch", "ls-tree -r", "cat-file -e",
	}
	for _, pattern := range patterns {
		t.Run(pattern, func(t *testing.T) {
			root := dirtyRepo(t)
			failGit(t, pattern)
			dest := filepath.Join(t.TempDir(), "snap")
			_, err := Take(context.Background(), testOptions(root, dest, "t", "o"))
			if err == nil {
				t.Fatal("expected an error")
			}
			if _, statErr := os.Lstat(dest); !errors.Is(statErr, fs.ErrNotExist) {
				t.Errorf("dest left behind: %v", statErr)
			}
			gitBinary = "git"
			cmd := exec.Command("git", "rev-parse", "-q", "--verify", "refs/herdsman/snapshot/t/o")
			cmd.Dir = root
			if cmd.Run() == nil {
				t.Error("snapshot ref left behind")
			}
		})
	}
}

func TestTakeRefusesUnmergedIndex(t *testing.T) {
	root := newRepo(t)
	writeT(t, root, "c", "base\n", 0o644)
	runGitT(t, root, "add", "-A")
	runGitT(t, root, "commit", "-qm", "base")
	runGitT(t, root, "checkout", "-qb", "other")
	writeT(t, root, "c", "other\n", 0o644)
	runGitT(t, root, "commit", "-qam", "other")
	runGitT(t, root, "checkout", "-q", "feature/work")
	writeT(t, root, "c", "mine\n", 0o644)
	runGitT(t, root, "commit", "-qam", "mine")
	cmd := exec.Command("git", "merge", "other")
	cmd.Dir = root
	_ = cmd.Run()
	_, err := Take(context.Background(), testOptions(root, filepath.Join(t.TempDir(), "s"), "t", "o"))
	if !errors.Is(err, ErrUnmerged) || !strings.Contains(err.Error(), "c") {
		t.Fatalf("err = %v", err)
	}
}

func TestRestoreStopsOnEveryGitFailure(t *testing.T) {
	patterns := []string{"init -q --template=", "symbolic-ref HEAD", "update-ref", "ls-tree -r -z --full-tree", "cat-file --batch", "read-tree"}
	root := dirtyRepo(t)
	dest, _ := takeT(t, root, "t", "o")
	for _, pattern := range patterns {
		t.Run(pattern, func(t *testing.T) {
			failGit(t, pattern)
			target := filepath.Join(t.TempDir(), "r")
			if err := Restore(context.Background(), dest, target); err == nil {
				t.Fatal("expected an error")
			}
			if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("target left behind: %v", err)
			}
		})
	}
}

// fakeGit runs a shell action instead of git when the arguments contain
// pattern, from its nth matching call onward.
func fakeGit(t *testing.T, pattern string, nth int, action string) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "git")
	count := filepath.Join(dir, "count")
	body := fmt.Sprintf("#!/bin/sh\ncase \" $* \" in\n*%q*)\necho x >> %q\nif [ \"$(wc -l < %q)\" -ge %d ]; then %s; fi;;\nesac\nexec %q \"$@\"\n",
		pattern, count, count, nth, action, real)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	old := gitBinary
	gitBinary = script
	t.Cleanup(func() { gitBinary = old })
}

func TestTakeRejectsWhatGitMisreports(t *testing.T) {
	cases := map[string]struct{ pattern, action string }{
		"no toplevel":     {"--show-toplevel", "exit 0"},
		"unresolved head": {" -q ", "exit 1"},
		"short hashing":   {"hash-object --no-filters", "exit 0"},
		"ghost untracked": {"ls-files -z --others --exclude-standard", `printf 'ghost\000'; exit 0`},
		"ghost ignored":   {"--others --ignored", `printf 'ghost\000'; exit 0`},
		"unknown format":  {"--show-object-format", "echo md5; exit 0"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			root := dirtyRepo(t)
			fakeGit(t, c.pattern, 1, c.action)
			if _, err := Take(context.Background(), testOptions(root, filepath.Join(t.TempDir(), "s"), "t", "o")); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func unreadable(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
}

func TestTakeStopsOnUnreadablePaths(t *testing.T) {
	t.Run("tracked", func(t *testing.T) {
		root := newRepo(t)
		writeT(t, root, "d/f", "x\n", 0o644)
		runGitT(t, root, "add", "-A")
		runGitT(t, root, "commit", "-qm", "x")
		unreadable(t, filepath.Join(root, "d"))
		if _, err := Take(context.Background(), testOptions(root, filepath.Join(t.TempDir(), "s"), "t", "o")); !errors.Is(err, fs.ErrPermission) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("untracked folder", func(t *testing.T) {
		root := newRepo(t)
		writeT(t, root, "d/f", "x\n", 0o644)
		unreadable(t, filepath.Join(root, "d"))
		if _, err := Take(context.Background(), testOptions(root, filepath.Join(t.TempDir(), "s"), "t", "o")); !errors.Is(err, fs.ErrPermission) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("index is a folder", func(t *testing.T) {
		root := newRepo(t)
		if err := os.Mkdir(filepath.Join(root, ".git", "index"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := Take(context.Background(), testOptions(root, filepath.Join(t.TempDir(), "s"), "t", "o")); err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestTakeDestFailures(t *testing.T) {
	root := newRepo(t)
	base := t.TempDir()
	writeT(t, base, "file", "x", 0o644)
	if _, err := Take(context.Background(), testOptions(root, filepath.Join(base, "file", "s"), "t", "o")); err == nil {
		t.Fatal("parent is a file")
	}
	locked := filepath.Join(base, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	if _, err := Take(context.Background(), testOptions(root, filepath.Join(locked, "s"), "t", "o")); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("err = %v", err)
	}
}

func TestRestoreTargetFailures(t *testing.T) {
	root := newRepo(t)
	writeT(t, root, "f", "x\n", 0o644)
	dest, _ := takeT(t, root, "t", "o")
	ctx := context.Background()
	base := t.TempDir()
	writeT(t, base, "file", "x", 0o644)
	if err := Restore(ctx, dest, filepath.Join(base, "file", "r")); err == nil {
		t.Fatal("parent is a file")
	}
	shut := filepath.Join(base, "shut")
	if err := os.Mkdir(shut, 0o755); err != nil {
		t.Fatal(err)
	}
	unreadable(t, shut)
	if err := Restore(ctx, dest, shut); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("unreadable target: %v", err)
	}
	locked := filepath.Join(base, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	if err := Restore(ctx, dest, filepath.Join(locked, "r")); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("locked parent: %v", err)
	}
	if err := Restore(ctx, dest, filepath.Join(locked, "a", "r")); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("locked grandparent: %v", err)
	}
}

func TestRelativeSnapshotFolder(t *testing.T) {
	root := newRepo(t)
	writeT(t, root, "f", "x\n", 0o644)
	dest, _ := takeT(t, root, "t", "o")
	base := t.TempDir()
	t.Chdir(filepath.Dir(dest))
	if err := Restore(context.Background(), filepath.Base(dest), filepath.Join(base, "r")); err != nil {
		t.Fatalf("relative snapshot folder: %v", err)
	}
}

func TestVerifyCannotMakeScratchRepository(t *testing.T) {
	root := newRepo(t)
	writeT(t, root, "f", "x\n", 0o644)
	dest, _ := takeT(t, root, "t", "o")
	t.Setenv("TMPDIR", filepath.Join(dest, "absent"))
	if _, err := Verify(context.Background(), dest); artifactOf(t, err) != BundleName {
		t.Fatal(err)
	}
}

func TestVerifyErrorUnwraps(t *testing.T) {
	root := newRepo(t)
	writeT(t, root, "f", "x\n", 0o644)
	dest, _ := takeT(t, root, "t", "o")
	writeHostileTar(t, dest, []tarEntry{{name: "../x", body: "x", flag: tar.TypeReg, mode: 0o644}})
	if _, err := Verify(context.Background(), dest); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("err = %v", err)
	}
}

func TestRestoreStopsOnBadObjectStream(t *testing.T) {
	root := dirtyRepo(t)
	dest, _ := takeT(t, root, "t", "o")
	cases := map[string]struct {
		pattern string
		nth     int
		action  string
	}{
		"second fetch": {"fetch", 2, "exit 1"},
		"junk header":  {"cat-file --batch", 1, "cat >/dev/null; echo junk; exit 0"},
		"short body":   {"cat-file --batch", 1, `cat >/dev/null; printf 'aa blob 100\nabc'; exit 0`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fakeGit(t, c.pattern, c.nth, c.action)
			target := filepath.Join(t.TempDir(), "r")
			if err := Restore(context.Background(), dest, target); err == nil {
				t.Fatal("expected an error")
			}
			if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("target left behind: %v", err)
			}
		})
	}
}

func TestManifestFileFailures(t *testing.T) {
	if err := writeManifest(filepath.Join(t.TempDir(), "absent"), goldenManifest()); err == nil {
		t.Fatal("missing folder")
	}
	dir := t.TempDir()
	writeT(t, dir, ManifestName+"/inner", "x", 0o644)
	if err := writeManifest(dir, goldenManifest()); err == nil {
		t.Fatal("manifest name is a folder")
	}
	if _, _, err := checksum(dir); err == nil {
		t.Fatal("checksum of a folder")
	}
}

func openRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root, dir
}

func TestPreservedFailures(t *testing.T) {
	dir := t.TempDir()
	garbage := filepath.Join(dir, "garbage")
	writeT(t, dir, "garbage", strings.Repeat("g", 100), 0o600)
	missing := filepath.Join(dir, "missing")
	m := &Manifest{}
	if err := checkPreserved(missing, m); err == nil {
		t.Error("missing archive")
	}
	if err := checkPreserved(garbage, m); err == nil {
		t.Error("garbage archive")
	}
	root, rootDir := openRoot(t)
	if err := unpackPreserved(root, missing); err == nil {
		t.Error("unpack missing")
	}
	if err := unpackPreserved(root, garbage); err == nil {
		t.Error("unpack garbage")
	}
	if err := writePreserved(dir, garbage, nil); err == nil {
		t.Error("existing archive must not be overwritten")
	}
	for i, rec := range []FileRecord{
		{Path: "absent", Mode: modeSymlink},
		{Path: "absent", Mode: 0o100644},
	} {
		if err := writePreserved(dir, filepath.Join(dir, fmt.Sprintf("out%d", i)), []FileRecord{rec}); err == nil {
			t.Errorf("record %d", i)
		}
	}
	writeT(t, rootDir, "taken", "x", 0o644)
	if err := os.Mkdir(filepath.Join(rootDir, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, hdr := range []*tar.Header{
		{Name: "../up", Typeflag: tar.TypeReg},
		{Name: "dir/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "taken", Typeflag: tar.TypeReg, Mode: 0o644},
	} {
		if err := unpackEntry(root, hdr, strings.NewReader("")); err == nil {
			t.Errorf("%s accepted", hdr.Name)
		}
	}
	if err := writeNew(root, "new", iotestErrReader{}, 0o644); err == nil {
		t.Error("read failure must surface")
	}
	if err := makeSubmoduleFolder(root, "../sub"); err == nil {
		t.Error("unsafe submodule path")
	}
}

type iotestErrReader struct{}

func (iotestErrReader) Read([]byte) (int, error) { return 0, errors.New("planted read failure") }

func TestWriteBlobFailures(t *testing.T) {
	root, rootDir := openRoot(t)
	writeT(t, rootDir, "taken", "x", 0o644)
	header := "aa blob 1\n"
	cases := map[string]struct {
		row    treeRow
		stream io.Reader
	}{
		"unsafe":      {treeRow{mode: "100644", path: "../x"}, strings.NewReader(header + "x\n")},
		"link taken":  {treeRow{mode: "120000", path: "taken"}, strings.NewReader(header + "x\n")},
		"file taken":  {treeRow{mode: "100644", path: "taken"}, strings.NewReader(header + "x\n")},
		"link unread": {treeRow{mode: "120000", path: "n"}, io.MultiReader(strings.NewReader(header), iotestErrReader{})},
		"wrong kind":  {treeRow{mode: "100644", path: "m"}, strings.NewReader("aa tree 1\nx\n")},
		"no header":   {treeRow{mode: "100644", path: "m"}, strings.NewReader("")},
	}
	for name, c := range cases {
		if err := writeBlob(root, bufio.NewReader(c.stream), c.row); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestApplyMetadataFailures(t *testing.T) {
	root, _ := openRoot(t)
	for _, f := range []FileRecord{{Path: "absent", Mode: 0o100644, Perm: 0o644}, {Path: "absent", Mode: 0o100600}} {
		if err := applyMetadata(root, &Manifest{Files: []FileRecord{f}}); err == nil {
			t.Errorf("%+v accepted", f)
		}
	}
}

func TestSubmoduleHeadFailureStopsTake(t *testing.T) {
	upstream := newRepo(t)
	writeT(t, upstream, "u", "u\n", 0o644)
	runGitT(t, upstream, "add", "-A")
	runGitT(t, upstream, "commit", "-qm", "u")
	root := newRepo(t)
	runGitT(t, root, "-c", "protocol.file.allow=always", "submodule", "--quiet", "add", upstream, "sub")
	failGit(t, "rev-parse HEAD")
	if _, err := Take(context.Background(), testOptions(root, filepath.Join(t.TempDir(), "s"), "t", "o")); err == nil {
		t.Fatal("expected an error")
	}
}

func TestUnmergedSubmoduleLineStopsScan(t *testing.T) {
	c := &capture{ctx: context.Background(), root: t.TempDir(), excluded: map[string]Exclusion{}, opaque: map[string]bool{}}
	unreadable(t, c.root)
	if err := c.scanTracked([]string{"100644 aa 0\tf"}); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("err = %v", err)
	}
}

func TestWhitespaceInRepositoryPathIsKept(t *testing.T) {
	base := t.TempDir()
	plain, spaced := filepath.Join(base, "repo"), filepath.Join(base, "repo ")
	for _, root := range []string{plain, spaced} {
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
		runGitT(t, root, "init", "-q")
	}
	writeT(t, plain, "wrong", "wrong\n", 0o644)
	writeT(t, spaced, "right", "right\n", 0o644)
	_, m := takeT(t, spaced, "t", "o")
	if !strings.HasSuffix(m.Worktree, "repo ") {
		t.Fatalf("worktree = %q", m.Worktree)
	}
	if !captured(m, "right") || captured(m, "wrong") {
		t.Fatalf("captured the wrong repository: %+v", m.Files)
	}
}

func TestReplacementObjectsAreIgnored(t *testing.T) {
	root := newRepo(t)
	writeT(t, root, "f", "original\n", 0o644)
	runGitT(t, root, "add", "-A")
	runGitT(t, root, "commit", "-qm", "x")
	original := strings.TrimSpace(runGitT(t, root, "rev-parse", "HEAD:f"))
	writeT(t, root, "evil-source", "replaced\n", 0o644)
	replacement := strings.TrimSpace(runGitT(t, root, "hash-object", "-w", "evil-source"))
	if err := os.Remove(filepath.Join(root, "evil-source")); err != nil {
		t.Fatal(err)
	}
	runGitT(t, root, "replace", original, replacement)
	dest, _ := takeT(t, root, "t", "o")
	target := filepath.Join(t.TempDir(), "r")
	if err := Restore(context.Background(), dest, target); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(target, "f"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original\n" {
		t.Fatalf("restored %q", got)
	}
}

func TestVerifyResolvesRecordedHead(t *testing.T) {
	root := newRepo(t)
	writeT(t, root, "f", "x\n", 0o644)
	runGitT(t, root, "add", "-A")
	runGitT(t, root, "commit", "-qm", "x")
	for name, pick := range map[string]func(*Manifest) string{
		"absent": func(m *Manifest) string { return strings.Repeat("0", len(m.Head)) },
		"a tree": func(m *Manifest) string { return m.StagedTree },
	} {
		t.Run(name, func(t *testing.T) {
			runGitT(t, root, "update-ref", "-d", "refs/herdsman/snapshot/h/o")
			dest, _ := takeT(t, root, "h", "o")
			rewriteManifest(t, dest, func(m *Manifest) { m.Head = pick(m) })
			if _, err := Verify(context.Background(), dest); artifactOf(t, err) != BundleName {
				t.Fatal(err)
			}
		})
	}
}

func TestVerifyRejectsConflictingLayouts(t *testing.T) {
	reg := func(name string) tarEntry { return tarEntry{name: name, body: "x", flag: tar.TypeReg, mode: 0o644} }
	cases := map[string][]tarEntry{
		"same path as a tree file":  {reg("f")},
		"beneath a tree file":       {reg("f/under")},
		"folder above a tree file":  {{name: "d/", flag: tar.TypeDir, mode: 0o755}},
		"beneath an archive file":   {reg("a"), reg("a/b")},
		"beneath an archive folder": {{name: "e/", flag: tar.TypeDir, mode: 0o755}, reg("e/b")},
	}
	root := newRepo(t)
	writeT(t, root, "f", "x\n", 0o644)
	writeT(t, root, "d/inner", "y\n", 0o644)
	i := 0
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			i++
			dest, _ := takeT(t, root, "layout", fmt.Sprintf("o%d", i))
			writeHostileTar(t, dest, entries)
			if _, err := Verify(context.Background(), dest); artifactOf(t, err) != PreservedName {
				t.Fatal(err)
			}
		})
	}
}

func swap[T any](t *testing.T, target *T, value T) {
	t.Helper()
	old := *target
	*target = value
	t.Cleanup(func() { *target = old })
}

var errPlanted = errors.New("planted failure")

func failChmod(t *testing.T, match string) {
	swap(t, &osChmod, func(name string, mode fs.FileMode) error {
		if strings.Contains(name, match) {
			return errPlanted
		}
		return os.Chmod(name, mode)
	})
}

func failSync(t *testing.T, match string) {
	swap(t, &syncPath, func(name string) error {
		if strings.HasSuffix(name, match) {
			return errPlanted
		}
		return syncToDisk(name)
	})
}

func failRemoveAll(t *testing.T, match string) {
	swap(t, &osRemoveAll, func(name string) error {
		if strings.HasSuffix(name, match) {
			return errPlanted
		}
		return os.RemoveAll(name)
	})
}

func failOpen(t *testing.T, match string) {
	swap(t, &osOpen, func(name string) (*os.File, error) {
		if strings.HasSuffix(name, match) {
			return nil, errPlanted
		}
		return os.Open(name)
	})
}

func TestTakeStopsOnEveryPlantedFailureAndCleansUp(t *testing.T) {
	cases := map[string]func(*testing.T){
		"chmod folder":   func(t *testing.T) { failChmod(t, "deep/snap") },
		"chmod bundle":   func(t *testing.T) { failChmod(t, BundleName) },
		"sync bundle":    func(t *testing.T) { failSync(t, BundleName) },
		"sync preserved": func(t *testing.T) { failSync(t, PreservedName) },
		"sync manifest":  func(t *testing.T) { failSync(t, ManifestName+".tmp") },
		"sync folder":    func(t *testing.T) { failSync(t, "deep/snap") },
		"sync parent":    func(t *testing.T) { failSync(t, "created/deep") },
		"sync ancestor":  func(t *testing.T) { failSync(t, "created") },
		"remove work":    func(t *testing.T) { failRemoveAll(t, workDirName) },
		"sum bundle":     func(t *testing.T) { failOpen(t, BundleName) },
		"sum preserved":  func(t *testing.T) { failOpen(t, PreservedName) },
		"read ignored":   func(t *testing.T) { failOpen(t, "data.db") },
		"make work": func(t *testing.T) {
			swap(t, &osMkdir, func(string, fs.FileMode) error { return errPlanted })
		},
		"publish": func(t *testing.T) {
			swap(t, &osRename, func(string, string) error { return errPlanted })
		},
		"read link": func(t *testing.T) {
			swap(t, &osReadlink, func(string) (string, error) { return "", errPlanted })
		},
		"resolve dest": func(t *testing.T) {
			swap(t, &osAbs, func(string) (string, error) { return "", errPlanted })
		},
		"inspect empty folder": func(t *testing.T) {
			swap(t, &osLstat, func(name string) (fs.FileInfo, error) {
				if strings.HasSuffix(name, "emptydir") {
					return nil, errPlanted
				}
				return os.Lstat(name)
			})
		},
	}
	for name, install := range cases {
		t.Run(name, func(t *testing.T) {
			root := dirtyRepo(t)
			base := t.TempDir()
			dest := filepath.Join(base, "created", "deep", "snap")
			install(t)
			_, err := Take(context.Background(), testOptions(root, dest, "t", "o"))
			if !errors.Is(err, errPlanted) {
				t.Fatalf("err = %v", err)
			}
			if _, statErr := os.Lstat(dest); !errors.Is(statErr, fs.ErrNotExist) {
				t.Errorf("dest left behind: %v", statErr)
			}
			cmd := exec.Command("git", "rev-parse", "-q", "--verify", "refs/herdsman/snapshot/t/o")
			cmd.Dir = root
			if cmd.Run() == nil {
				t.Error("snapshot ref left behind")
			}
		})
	}
}

func TestTakeReportsCleanupFailures(t *testing.T) {
	t.Run("folder", func(t *testing.T) {
		root := dirtyRepo(t)
		failChmod(t, BundleName)
		swap(t, &osRemoveAll, func(string) error { return errors.New("cleanup failed") })
		_, err := Take(context.Background(), testOptions(root, filepath.Join(t.TempDir(), "snap"), "t", "o"))
		if !errors.Is(err, errPlanted) || !strings.Contains(err.Error(), "cleanup failed") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("ref", func(t *testing.T) {
		root := dirtyRepo(t)
		failChmod(t, BundleName)
		failGit(t, "update-ref -d")
		_, err := Take(context.Background(), testOptions(root, filepath.Join(t.TempDir(), "snap"), "t", "o"))
		if !errors.Is(err, errPlanted) || !strings.Contains(err.Error(), "update-ref -d") {
			t.Fatalf("err = %v", err)
		}
	})
}

// The data must be durable before the manifest claims it is complete.
func TestPublicationOrder(t *testing.T) {
	root := dirtyRepo(t)
	base := t.TempDir()
	dest := filepath.Join(base, "created", "deep", "snap")
	var events []string
	swap(t, &syncPath, func(name string) error {
		events = append(events, "sync "+strings.TrimPrefix(name, base))
		return syncToDisk(name)
	})
	swap(t, &osRename, func(from, to string) error {
		events = append(events, "rename "+filepath.Base(to))
		return os.Rename(from, to)
	})
	if _, err := Take(context.Background(), testOptions(root, dest, "t", "o")); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"sync /created/deep/snap/" + BundleName,
		"sync /created/deep/snap/" + PreservedName,
		"sync /created/deep/snap/" + ManifestName + ".tmp",
		"rename " + ManifestName,
		"sync /created/deep/snap",
		"sync /created/deep",
		"sync /created",
		"sync ",
	}
	if strings.Join(events, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events:\n%s", strings.Join(events, "\n"))
	}
}

func TestRestoreStopsOnEveryPlantedFailure(t *testing.T) {
	root := newRepo(t)
	writeT(t, root, "f", "x\n", 0o644)
	dest, _ := takeT(t, root, "t", "o")
	cases := map[string]func(*testing.T){
		"open root": func(t *testing.T) {
			swap(t, &osOpenRoot, func(string) (*os.Root, error) { return nil, errPlanted })
		},
		"chmod": func(t *testing.T) { failChmod(t, ".herdsman-restore") },
		"rmdir": func(t *testing.T) {
			swap(t, &osRmdir, func(string) error { return errPlanted })
		},
		"rename": func(t *testing.T) {
			swap(t, &osRename, func(string, string) error { return errPlanted })
		},
		"resolve": func(t *testing.T) {
			swap(t, &osAbs, func(string) (string, error) { return "", errPlanted })
		},
	}
	for name, install := range cases {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			target := filepath.Join(parent, "r")
			install(t)
			if err := Restore(context.Background(), dest, target); !errors.Is(err, errPlanted) {
				t.Fatalf("err = %v", err)
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatalf("left behind: %v %v", entries, err)
			}
		})
	}
}

func TestVerifyPlantedFailures(t *testing.T) {
	root := newRepo(t)
	writeT(t, root, "f", "x\n", 0o644)
	dest, _ := takeT(t, root, "t", "o")
	t.Run("resolve", func(t *testing.T) {
		swap(t, &osAbs, func(string) (string, error) { return "", errPlanted })
		if _, err := Verify(context.Background(), dest); artifactOf(t, err) != ManifestName {
			t.Fatal(err)
		}
	})
	t.Run("stat", func(t *testing.T) {
		swap(t, &osStat, func(string) (fs.FileInfo, error) { return nil, errPlanted })
		if _, err := Verify(context.Background(), dest); artifactOf(t, err) != FolderArtifact {
			t.Fatal(err)
		}
	})
}

func TestFirstExistingAndSyncToDisk(t *testing.T) {
	if got := firstExisting("/herdsman-absent-folder/x"); got != "/" {
		t.Fatalf("got %q", got)
	}
	if err := syncToDisk(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("missing path")
	}
}

func TestCheckLayoutDirect(t *testing.T) {
	m := &Manifest{Files: []FileRecord{{Path: "f", Source: SourceTree}}}
	for _, links := range [][]string{{"../x"}, {"f"}} {
		if err := checkLayout(m, links); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("%v: %v", links, err)
		}
	}
	if err := checkLayout(m, []string{"sub"}); err != nil {
		t.Error(err)
	}
}

// Verification checks names; this holds for what only the filesystem knows.
func TestPrepareParentsRefusesRealSymlinks(t *testing.T) {
	root, dir := openRoot(t)
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, "lnk")); err != nil {
		t.Fatal(err)
	}
	if err := prepareParents(root, "lnk/pwned"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("err = %v", err)
	}
	long := strings.Repeat("n", 300) + "/f"
	if err := prepareParents(root, long); err == nil || errors.Is(err, ErrUnsafePath) {
		t.Fatalf("err = %v", err)
	}
}

func TestAddPreservedRejectsUnencodableHeader(t *testing.T) {
	dir := t.TempDir()
	rec := FileRecord{Path: "a\x00b", Mode: 0o100644}
	if err := writePreserved(dir, filepath.Join(dir, "out"), []FileRecord{rec}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestWriteWorkingTreeStopsOnOccupiedGitlink(t *testing.T) {
	repo := newRepo(t)
	info := "160000 " + strings.Repeat("a", 40) + "\tsub\n"
	env := filepath.Join(t.TempDir(), "index")
	cmd := exec.Command("git", "update-index", "--add", "--index-info")
	cmd.Dir, cmd.Stdin = repo, strings.NewReader(info)
	cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+env)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	write := exec.Command("git", "write-tree")
	write.Dir, write.Env = repo, append(os.Environ(), "GIT_INDEX_FILE="+env)
	tree, err := write.Output()
	if err != nil {
		t.Fatal(err)
	}
	root, dir := openRoot(t)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkingTree(context.Background(), root, repo, strings.TrimSpace(string(tree))); err == nil {
		t.Fatal("expected an error")
	}
}

func TestUnpackStopsOnOccupiedPath(t *testing.T) {
	root, rootDir := openRoot(t)
	writeT(t, rootDir, "taken", "mine", 0o644)
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	if err := w.WriteHeader(&tar.Header{Name: "taken", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeT(t, dir, "a.tar", buf.String(), 0o600)
	if err := unpackPreserved(root, filepath.Join(dir, "a.tar")); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(rootDir, "taken")); string(got) != "mine" {
		t.Fatal("existing file changed")
	}
}

// Verification reads the archive once; the unpack is the next open.
func TestRestoreStopsWhenTheArchiveCannotBeUnpacked(t *testing.T) {
	root := newRepo(t)
	writeT(t, root, "f", "x\n", 0o644)
	dest, _ := takeT(t, root, "t", "o")
	opens := 0
	swap(t, &osOpen, func(name string) (*os.File, error) {
		if strings.HasSuffix(name, PreservedName) {
			if opens++; opens == 2 {
				return nil, errPlanted
			}
		}
		return os.Open(name)
	})
	target := filepath.Join(t.TempDir(), "r")
	if err := Restore(context.Background(), dest, target); !errors.Is(err, errPlanted) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("target left behind: %v", err)
	}
}

func restoreAndCompare(t *testing.T, root string) {
	t.Helper()
	want := readTree(t, root)
	dest, _ := takeT(t, root, "t", "o")
	target := filepath.Join(t.TempDir(), "r")
	if err := Restore(context.Background(), dest, target); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	compareTrees(t, want, readTree(t, target))
}

func TestCarriageReturnNames(t *testing.T) {
	t.Run("pair", func(t *testing.T) {
		root := newRepo(t)
		writeT(t, root, "x\r", "the CR file\n", 0o644)
		writeT(t, root, "x", "a different file\n", 0o644)
		restoreAndCompare(t, root)
	})
	t.Run("Icon alone", func(t *testing.T) {
		root := newRepo(t)
		writeT(t, root, "Icon\r", "folder icon\n", 0o644)
		restoreAndCompare(t, root)
	})
}

func TestEveryControlCharacterInNames(t *testing.T) {
	root := newRepo(t)
	writeT(t, root, ".gitignore", "ignored*\n", 0o644)
	add := func(name string) { writeT(t, root, name, "content of "+name+"\n", 0o644) }
	for c := 1; c < 128; c++ {
		if c >= 0x20 && c != 0x7f {
			continue
		}
		ch := string(rune(c))
		add(fmt.Sprintf("mid%d%send", c, ch))
		add(fmt.Sprintf("tail%d%s", c, ch))
		add(fmt.Sprintf("%slead%d", ch, c))
		add(fmt.Sprintf("ignored%d%s", c, ch))
	}
	for _, name := range []string{" lead space", "trail space ", `back\slash`, `"quoted"`, `tail\`, "dir\r/inner\n"} {
		add(name)
	}
	restoreAndCompare(t, root)
}

func TestTrackedPathUnderSymlinkParentIsDeleted(t *testing.T) {
	for shape, ignore := range map[string]string{"untracked link": "", "ignored link": "d\n"} {
		t.Run(shape, func(t *testing.T) {
			outside := t.TempDir()
			writeT(t, outside, "f", "OUTSIDE SECRET\n", 0o644)
			writeT(t, outside, "deep/g", "OUTSIDE SECRET\n", 0o644)
			root := newRepo(t)
			writeT(t, root, "d/f", "inside\n", 0o644)
			writeT(t, root, "d/deep/g", "inside\n", 0o644)
			runGitT(t, root, "add", "-A")
			runGitT(t, root, "commit", "-qm", "x")
			if ignore != "" {
				writeT(t, root, ".gitignore", ignore, 0o644)
			}
			if err := os.RemoveAll(filepath.Join(root, "d")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(root, "d")); err != nil {
				t.Fatal(err)
			}
			dest, m := takeT(t, root, "t", "o")
			if captured(m, "d/f") || captured(m, "d/deep/g") {
				t.Fatalf("read through the symlink: %+v", m.Files)
			}
			if !captured(m, "d") {
				t.Fatalf("the link itself must be captured: %+v", m.Files)
			}
			target := filepath.Join(t.TempDir(), "r")
			if err := Restore(context.Background(), dest, target); err != nil {
				t.Fatal(err)
			}
			if got, err := os.Readlink(filepath.Join(target, "d")); err != nil || got != outside {
				t.Fatalf("link = %q %v", got, err)
			}
			if got := runGitT(t, target, "ls-tree", "-r", "--name-only", m.WorkingTree); strings.Contains(got, "d/") {
				t.Fatalf("working tree holds %q", got)
			}
		})
	}
}

func TestUnreadableParentOfTrackedPathStopsTake(t *testing.T) {
	root := newRepo(t)
	writeT(t, root, "a/b/c/f", "x\n", 0o644)
	runGitT(t, root, "add", "-A")
	runGitT(t, root, "commit", "-qm", "x")
	unreadable(t, filepath.Join(root, "a"))
	if _, err := Take(context.Background(), testOptions(root, filepath.Join(t.TempDir(), "s"), "t", "o")); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("err = %v", err)
	}
}

func TestNestedGitFoldersAreListed(t *testing.T) {
	root := newRepo(t)
	writeT(t, root, "broken/.git/data", "only copy\n", 0o644)
	writeT(t, root, "broken/kept.txt", "kept\n", 0o644)
	if err := os.MkdirAll(filepath.Join(root, "hollow", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeT(t, root, "hollow/kept.txt", "kept\n", 0o644)
	dest, m := takeT(t, root, "t", "o")
	for _, p := range []string{"broken/.git", "hollow/.git"} {
		if excludedReason(m, p) != ReasonNestedRepository {
			t.Errorf("%s: %+v", p, m.Excluded)
		}
	}
	for _, p := range []string{"broken/kept.txt", "hollow/kept.txt"} {
		if !captured(m, p) {
			t.Errorf("%s not captured: %+v", p, m.Files)
		}
	}
	if err := Restore(context.Background(), dest, filepath.Join(t.TempDir(), "r")); err != nil {
		t.Fatal(err)
	}
}

// A case-sensitive system lists such a path as an ordinary file.
func TestGitNamedComponentIsOpaqueInAnyCase(t *testing.T) {
	c := &capture{ctx: context.Background(), root: t.TempDir(), excluded: map[string]Exclusion{}, opaque: map[string]bool{}}
	if err := c.addOther("a/.GIT/x", nil, &c.tree, recordForTree); err != nil {
		t.Fatal(err)
	}
	if got := c.excluded["a/.GIT"]; got.Reason != ReasonNestedRepository || !c.opaque["a/.GIT"] {
		t.Fatalf("excluded = %+v", c.excluded)
	}
}

// git will not store these names in a tree. A refusal removes nothing, which
// is the safe answer for a capture that authorizes deletion.
func TestNamesGitRefusesStopTake(t *testing.T) {
	for _, name := range []string{"git~1", ".git ", `a\.git\b`} {
		t.Run(name, func(t *testing.T) {
			root := newRepo(t)
			writeT(t, root, name, "x\n", 0o644)
			dest := filepath.Join(t.TempDir(), "s")
			if _, err := Take(context.Background(), testOptions(root, dest, "t", "o")); err == nil {
				t.Fatal("expected a refusal")
			}
			if _, err := os.Lstat(dest); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("dest left behind: %v", err)
			}
		})
	}
}

func TestIgnoredFileWithoutReadPermissionStopsTake(t *testing.T) {
	root := dirtyRepo(t)
	secret := filepath.Join(root, "data.db")
	if err := os.Chmod(secret, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(secret, 0o644) })
	dest := filepath.Join(t.TempDir(), "s")
	if _, err := Take(context.Background(), testOptions(root, dest, "t", "o")); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Lstat(dest); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dest left behind: %v", err)
	}
	cmd := exec.Command("git", "rev-parse", "-q", "--verify", "refs/herdsman/snapshot/t/o")
	cmd.Dir = root
	if cmd.Run() == nil {
		t.Error("snapshot ref left behind")
	}
}
