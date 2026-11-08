package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/ops"
)

func recoveryRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(shortDir(t), "state")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestExportThroughTheDaemonImportsIntoAnEmptyState(t *testing.T) {
	root := recoveryRoot(t)
	c, stop := serving(t, root, false)
	defer stop()
	dest := filepath.Join(t.TempDir(), "export")
	var op ops.Operation
	if _, err := c.Do(context.Background(), "POST", "/v1/export", ExportRequest{Dest: dest}, &op); err != nil {
		t.Fatal(err)
	}
	if op.State != ops.OpDone {
		t.Fatalf("export ended %s: %s", op.State, op.Reason)
	}
	state := filepath.Join(t.TempDir(), "imported")
	if err := ops.Import(context.Background(), dest, state); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state, "daemon", "account.db")); err != nil {
		t.Fatal(err)
	}
}

func TestUndoOfAnUnknownSnapshotIsNotFound(t *testing.T) {
	c, stop := serving(t, recoveryRoot(t), false)
	defer stop()
	var res ops.UndoResult
	_, err := c.Do(context.Background(), "POST", "/v1/undo", ops.UndoRequest{Snapshot: "nope"}, &res)
	var e *APIError
	if !errors.As(err, &e) || e.Code != "not_found" {
		t.Fatalf("err = %v", err)
	}
}

func TestExportOverAnExistingFolderIsRefused(t *testing.T) {
	c, stop := serving(t, recoveryRoot(t), false)
	defer stop()
	var op ops.Operation
	_, err := c.Do(context.Background(), "POST", "/v1/export", ExportRequest{Dest: t.TempDir()}, &op)
	var e *APIError
	if !errors.As(err, &e) || e.Code != "failed_precondition" {
		t.Fatalf("err = %v", err)
	}
}

func TestInspectRefusesUndoAndExport(t *testing.T) {
	c, stop := serving(t, recoveryRoot(t), true)
	defer stop()
	dest := filepath.Join(t.TempDir(), "export")
	var op ops.Operation
	_, err := c.Do(context.Background(), "POST", "/v1/export", ExportRequest{Dest: dest}, &op)
	var e *APIError
	if !errors.As(err, &e) || !strings.Contains(e.Message, "inspect mode") {
		t.Fatalf("err = %v", err)
	}
	if _, serr := os.Stat(dest); !os.IsNotExist(serr) {
		t.Fatalf("inspect exported: %v", serr)
	}
}

func TestRecoveryCallImportsInThisProcess(t *testing.T) {
	root := recoveryRoot(t)
	c, stop := serving(t, root, false)
	dest := filepath.Join(t.TempDir(), "export")
	var op ops.Operation
	if _, err := c.Do(context.Background(), "POST", "/v1/export", ExportRequest{Dest: dest}, &op); err != nil {
		t.Fatal(err)
	}
	stop()
	state := filepath.Join(t.TempDir(), "imported")
	raw, _ := json.Marshal(ImportRequest{Source: dest, StateDir: state})
	out, err := RecoveryCaller("test")(context.Background(), "Import", raw)
	if err != nil {
		t.Fatal(err)
	}
	var got ImportResponse
	if err := json.Unmarshal(out, &got); err != nil || got.StateDir != state {
		t.Fatalf("%s %v", out, err)
	}
	if _, err := RecoveryCaller("test")(context.Background(), "Nope", raw); err == nil {
		t.Fatal("an unknown method was accepted")
	}
}

func TestImportRelocatesARouteCreatedThroughTheDaemon(t *testing.T) {
	root := recoveryRoot(t)
	c, stop := serving(t, root, false)
	var created CreateResult
	if _, err := c.Do(context.Background(), "POST", "/v1/routes", CreateRequest{Repo: "repo", Route: "r1"}, &created); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "export")
	var op ops.Operation
	if _, err := c.Do(context.Background(), "POST", "/v1/export", ExportRequest{Dest: dest}, &op); err != nil {
		t.Fatal(err)
	}
	stop()
	state := filepath.Join(t.TempDir(), "imported")
	if err := ops.Import(context.Background(), dest, state); err != nil {
		t.Fatal(err)
	}
	rf, err := ReadRoutes(state)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rf.Routes["r1"].Inbox, filepath.Join(state, "repo-r1"); got != want {
		t.Fatalf("inbox = %s, want %s", got, want)
	}
}

func TestExportHoldsBackRouteChangesUntilItEnds(t *testing.T) {
	h := newHarness(t)
	h.stop()
	entered, release := make(chan struct{}), make(chan struct{})
	h.cfg.OpsFault = func(p string) {
		if p == "export:barrier" {
			close(entered)
			<-release
		}
	}
	h.start()
	c := h.client()
	done := make(chan error, 1)
	go func() {
		var op ops.Operation
		_, err := c.Do(context.Background(), "POST", "/v1/export", ExportRequest{Dest: filepath.Join(t.TempDir(), "export")}, &op)
		done <- err
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var created CreateResult
	_, err := c.Do(ctx, "POST", "/v1/routes", CreateRequest{Repo: "repo", Route: "r1"}, &created)
	time.Sleep(100 * time.Millisecond) // the server sees the client leave
	close(release)
	if exportErr := <-done; exportErr != nil {
		t.Fatal(exportErr)
	}
	if err == nil {
		t.Fatal("a route was created during the export")
	}
	rf, rerr := ReadRoutes(h.ph)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if _, ok := rf.Routes["r1"]; ok {
		t.Fatal("the abandoned request created the route after the export")
	}
	if _, err := c.Do(context.Background(), "POST", "/v1/routes", CreateRequest{Repo: "repo", Route: "r1"}, &created); err != nil {
		t.Fatalf("after the export: %v", err)
	}
}

func git(t *testing.T, dir string, args ...string) string {
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

func TestUndoThroughTheDaemonRestoresARealSnapshot(t *testing.T) {
	h := newHarness(t)
	base, err := filepath.EvalSymlinks(shortDir(t))
	if err != nil {
		t.Fatal(err)
	}
	repo, tree := filepath.Join(base, "repo"), filepath.Join(base, "tree")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q", "-b", "main")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "first")
	git(t, repo, "worktree", "add", "-q", "-b", "feat/x", tree)
	if err := os.WriteFile(filepath.Join(tree, ".env"), []byte("SECRET=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, ".gitignore"), []byte(".env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	op, err := h.d.recovery.Snapshot(context.Background(), ops.SnapshotRequest{Task: "t1", Worktree: tree})
	if err != nil {
		t.Fatal(err)
	}
	git(t, repo, "worktree", "remove", "--force", tree)
	git(t, repo, "branch", "-D", "feat/x")
	target := filepath.Join(base, "restored")
	var res ops.UndoResult
	if _, err := h.client().Do(context.Background(), "POST", "/v1/undo", ops.UndoRequest{Snapshot: op.ID, Target: target}, &res); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(target, ".env"))
	if err != nil || string(got) != "SECRET=1\n" {
		t.Fatalf("%q %v", got, err)
	}
	if head := git(t, target, "symbolic-ref", "HEAD"); head != "refs/heads/feat/x" {
		t.Fatalf("HEAD = %s", head)
	}
}

func TestTheHourlySweepPrunesExpiredSnapshots(t *testing.T) {
	saved := sweepEvery
	t.Cleanup(func() { sweepEvery = saved })
	sweepEvery = 10 * time.Millisecond
	h := newHarness(t)
	dir := filepath.Join(shortDir(t), "old")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := h.d.ops.Read(func(c store.Conn) error {
		_, err := c.Exec("INSERT INTO snapshots(id, task, dir, repo, size, created) VALUES ('old','t',?,'',1,1)", dir)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		list, err := h.d.recovery.Snapshots()
		if err != nil {
			t.Fatal(err)
		}
		if len(list) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still held: %+v", list)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("folder stayed: %v", err)
	}
}
