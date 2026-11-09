//go:build acceptance

// Package recovery proves what a snapshot can bring back. No verb restores one
// yet, so the gate drives the snapshot package directly, with real git.
package recovery

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/snapshot"
)

func TestMain(m *testing.M) {
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Exit(m.Run())
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=a", "-c", "user.email=a@b.c"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRestoreNeedsNoSourceRepository takes a snapshot of a worktree holding
// every kind of unpublished state, destroys the repository, and restores.
func TestRestoreNeedsNoSourceRepository(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, source, "init", "-q", "-b", "work")
	write(t, source, ".gitignore", ".env\n*.db\n")
	write(t, source, "app.txt", "committed\n")
	write(t, source, "staged.txt", "staged base\n")
	git(t, source, "add", ".")
	git(t, source, "commit", "-q", "-m", "start")
	head := git(t, source, "rev-parse", "HEAD")

	write(t, source, "staged.txt", "staged change\n")
	git(t, source, "add", "staged.txt")
	files := map[string]string{
		"app.txt":    "edited, never committed\n",
		"new.txt":    "untracked\n",
		".env":       "TOKEN=only-copy\n",
		"data.db":    "rows only here\n",
		".gitignore": ".env\n*.db\n",
		"staged.txt": "staged change\n",
	}
	write(t, source, "app.txt", files["app.txt"])
	write(t, source, "new.txt", files["new.txt"])
	write(t, source, ".env", files[".env"])
	write(t, source, "data.db", files["data.db"])

	dest := filepath.Join(root, "kept", "snapshot")
	ctx := context.Background()
	if _, err := snapshot.Take(ctx, snapshot.Options{Worktree: source, Dest: dest, Task: "task", Op: "op"}); err != nil {
		t.Fatalf("take: %v", err)
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}

	if _, err := snapshot.Verify(ctx, dest); err != nil {
		t.Fatalf("verify without the source: %v", err)
	}
	target := filepath.Join(root, "restored")
	if err := snapshot.Restore(ctx, dest, target); err != nil {
		t.Fatalf("restore without the source: %v", err)
	}

	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(target, name))
		if err != nil {
			t.Errorf("%s did not come back: %v", name, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if got := git(t, target, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD = %s, want %s", got, head)
	}
	if got := git(t, target, "show", ":staged.txt"); got != "staged change" {
		t.Errorf("staged blob = %q", got)
	}
	if got := git(t, target, "show", "HEAD:staged.txt"); got != "staged base" {
		t.Errorf("committed blob = %q", got)
	}
	if out := git(t, target, "fsck", "--full"); strings.Contains(out, "missing") {
		t.Errorf("objects are missing:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(target, ".git", "objects", "info", "alternates")); !os.IsNotExist(err) {
		t.Errorf("the restored repository still borrows objects: %v", err)
	}
}
