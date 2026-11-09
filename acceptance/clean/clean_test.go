//go:build acceptance

// Package clean is the real-platform gate for freeing a task: real git, real
// process listing, a real snapshot, and a real restore.
package clean

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon"
	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/wtree"
)

type gate struct {
	t    *testing.T
	root string
	main string
	tree string
	svc  *daemon.WorktreeService
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func put(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newGate(t *testing.T) *gate {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := &gate{t: t, root: root, main: filepath.Join(root, "repo"), tree: filepath.Join(root, "tree")}
	if err := os.Mkdir(g.main, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, g.main, "init", "-q", "-b", "main")
	put(t, filepath.Join(g.main, ".gitignore"), ".env\n*.sqlite\nbig.bin\n")
	run(t, g.main, "add", ".")
	run(t, g.main, "commit", "-qm", "base")
	run(t, g.main, "worktree", "add", "-q", "-b", "feat/a", g.tree)
	put(t, filepath.Join(g.tree, "a.txt"), "a")
	run(t, g.tree, "add", ".")
	run(t, g.tree, "commit", "-qm", "work")
	put(t, filepath.Join(g.tree, ".env"), "TOKEN=kept")
	put(t, filepath.Join(g.tree, "data/app.sqlite"), "database bytes")
	state, err := wtree.OpenState(filepath.Join(g.main, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err := state.PutWorktree(context.Background(), wtree.Worktree{Task: "a", Branch: "feat/a", Path: g.tree, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	g.svc = &daemon.WorktreeService{StateDir: filepath.Join(root, "state"), Repo: "acme/repo", GH: filepath.Join(root, "gh"), Approvals: filepath.Join(root, "approvals")}
	put(t, g.svc.GH, "#!/bin/sh\necho '[]'\n")
	if err := os.Chmod(g.svc.GH, 0o700); err != nil {
		t.Fatal(err)
	}
	return g
}

func (g *gate) clean(req daemon.WorktreeCleanRequest) daemon.WorktreeCleanReport {
	g.t.Helper()
	req.Dir, req.Tasks = g.main, []string{"a"}
	resp, err := g.svc.Clean(context.Background(), req)
	if err != nil {
		g.t.Fatal(err)
	}
	return resp.Reports[0]
}

func (g *gate) recovery() *ops.Recovery {
	g.t.Helper()
	db, err := store.Open(filepath.Join(g.svc.StateDir, "daemon", "account.db"), store.Writer, store.KindAccount)
	if err != nil {
		g.t.Fatal(err)
	}
	g.t.Cleanup(func() { db.Close() })
	rec := ops.NewRecovery(ops.RecoveryOptions{StateDir: g.svc.StateDir})
	rec.Attach(ops.NewEngine(db, ops.Options{Effects: rec.Effects()}))
	return rec
}

func (g *gate) assertTreeKept() {
	g.t.Helper()
	for _, name := range []string{"a.txt", ".env", "data/app.sqlite"} {
		if _, err := os.Lstat(filepath.Join(g.tree, name)); err != nil {
			g.t.Fatalf("%s was removed: %v", name, err)
		}
	}
	if run(g.t, g.main, "for-each-ref", "refs/heads/feat/a") == "" {
		g.t.Fatal("the branch was deleted")
	}
}

func TestIgnoredFilesSurviveCleanAndComeBackWithUndo(t *testing.T) {
	g := newGate(t)
	run(t, g.main, "merge", "-q", "--ff-only", "feat/a")
	report := g.clean(daemon.WorktreeCleanRequest{})
	if report.Error != "" {
		t.Fatal(report.Error)
	}
	if _, err := os.Lstat(g.tree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the tree is still there: %v", err)
	}
	if !report.BranchDeleted {
		t.Fatalf("%+v", report)
	}
	result, err := g.recovery().Undo(context.Background(), ops.UndoRequest{Snapshot: report.Snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if result.Target != g.tree {
		t.Fatalf("restored to %s", result.Target)
	}
	for name, want := range map[string]string{".env": "TOKEN=kept", "data/app.sqlite": "database bytes", "a.txt": "a"} {
		got, err := os.ReadFile(filepath.Join(g.tree, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
	if run(t, g.main, "for-each-ref", "refs/heads/feat/a") == "" {
		t.Fatal("undo did not bring the branch back")
	}
}

func TestAFailedSnapshotRefusesDeletion(t *testing.T) {
	g := newGate(t)
	run(t, g.main, "merge", "-q", "--ff-only", "feat/a")
	// A file in the unmerged state cannot be captured, so the snapshot fails.
	head := run(t, g.tree, "rev-parse", "HEAD")
	blob := run(t, g.tree, "rev-parse", "HEAD:a.txt")
	cmd := exec.Command("git", "update-index", "--index-info")
	cmd.Dir = g.tree
	cmd.Stdin = strings.NewReader("100644 " + blob + " 1\ta.txt\n100644 " + blob + " 2\ta.txt\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	_ = head
	report := g.clean(daemon.WorktreeCleanRequest{Force: true})
	if report.Error == "" {
		t.Fatal("clean did not refuse")
	}
	g.assertTreeKept()
}

func TestAnUncapturedPathRefusesDeletionUntilNamed(t *testing.T) {
	g := newGate(t)
	g.svc.FileCap = 64
	run(t, g.main, "merge", "-q", "--ff-only", "feat/a")
	put(t, filepath.Join(g.tree, "big.bin"), strings.Repeat("x", 128))
	report := g.clean(daemon.WorktreeCleanRequest{})
	if !strings.Contains(report.Error, "big.bin") {
		t.Fatalf("%+v", report)
	}
	g.assertTreeKept()
	report = g.clean(daemon.WorktreeCleanRequest{Named: []string{"big.bin"}})
	if report.Error != "" || strings.Join(report.Lost, ",") != "big.bin" {
		t.Fatalf("%+v", report)
	}
}

func TestARunningTaskRefusesDeletion(t *testing.T) {
	g := newGate(t)
	run(t, g.main, "merge", "-q", "--ff-only", "feat/a")
	sleeper := exec.Command("sleep", "120")
	sleeper.Dir = g.tree
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() }()
	report := g.clean(daemon.WorktreeCleanRequest{})
	if !strings.Contains(report.Error, "still runs") {
		t.Fatalf("%+v", report)
	}
	g.assertTreeKept()
}

func TestTheBranchStaysWithoutProofOfIntegration(t *testing.T) {
	g := newGate(t)
	run(t, g.main, "update-ref", "refs/remotes/origin/feat/a", run(t, g.tree, "rev-parse", "HEAD"))
	report := g.clean(daemon.WorktreeCleanRequest{})
	if report.Error != "" || report.BranchDeleted {
		t.Fatalf("%+v", report)
	}
	if run(t, g.main, "for-each-ref", "refs/heads/feat/a") == "" {
		t.Fatal("an unproven branch was deleted")
	}
}
