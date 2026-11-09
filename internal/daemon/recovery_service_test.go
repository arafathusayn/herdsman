package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/snapshot"
	"github.com/arafathusayn/herdsman/sdk/client"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

func recoveryRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(shortDir(t), "state")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func dialOps(t *testing.T, root string) daemonv1.OpsService {
	t.Helper()
	sdk, err := client.Dial(client.Options{Socket: SocketPath(root)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sdk.Close)
	return sdk.Ops
}

func wantFailure(t *testing.T, err error, code rpc.Code) *rpc.Error {
	t.Helper()
	var failure *rpc.Error
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("err = %v, want code %v", err, code)
	}
	return failure
}

func TestExportThroughTheSDKImportsIntoAnEmptyState(t *testing.T) {
	root := recoveryRoot(t)
	_, stop := serving(t, root, false)
	defer stop()
	dest := filepath.Join(t.TempDir(), "export")
	res, err := dialOps(t, root).Export(context.Background(), &daemonv1.ExportRequest{Dest: dest})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetOperationId() == "" || res.GetDest() != dest {
		t.Fatalf("%v", res)
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
	root := recoveryRoot(t)
	_, stop := serving(t, root, false)
	defer stop()
	_, err := dialOps(t, root).Undo(context.Background(), &daemonv1.UndoRequest{Snapshot: "nope"})
	wantFailure(t, err, rpc.NotFound)
}

func TestExportOverAnExistingFolderIsRefused(t *testing.T) {
	root := recoveryRoot(t)
	_, stop := serving(t, root, false)
	defer stop()
	_, err := dialOps(t, root).Export(context.Background(), &daemonv1.ExportRequest{Dest: t.TempDir()})
	wantFailure(t, err, rpc.FailedPrecondition)
}

func TestInspectRefusesEveryRecoveryChange(t *testing.T) {
	root := recoveryRoot(t)
	_, stop := serving(t, root, true)
	defer stop()
	sdk := dialOps(t, root)
	dest := filepath.Join(t.TempDir(), "export")
	_, err := sdk.Export(context.Background(), &daemonv1.ExportRequest{Dest: dest})
	if f := wantFailure(t, err, rpc.FailedPrecondition); !strings.Contains(f.Message, "inspect mode") {
		t.Fatal(f)
	}
	if _, serr := os.Stat(dest); !os.IsNotExist(serr) {
		t.Fatalf("inspect exported: %v", serr)
	}
	_, err = sdk.Undo(context.Background(), &daemonv1.UndoRequest{Snapshot: "x"})
	wantFailure(t, err, rpc.FailedPrecondition)
	_, err = sdk.Import(context.Background(), &daemonv1.ImportRequest{Source: "/x", StateDir: "/y"})
	wantFailure(t, err, rpc.FailedPrecondition)
	if _, err := sdk.Records(context.Background(), &daemonv1.RecordsRequest{}); err != nil {
		t.Fatalf("inspect serves reads: %v", err)
	}
}

func TestImportOverTheSDK(t *testing.T) {
	root := recoveryRoot(t)
	_, stop := serving(t, root, false)
	defer stop()
	sdk := dialOps(t, root)
	dest := filepath.Join(t.TempDir(), "export")
	if _, err := sdk.Export(context.Background(), &daemonv1.ExportRequest{Dest: dest}); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "imported")
	res, err := sdk.Import(context.Background(), &daemonv1.ImportRequest{Source: dest, StateDir: state})
	if err != nil || res.GetStateDir() != state {
		t.Fatalf("%v %v", res, err)
	}
	for _, bad := range []string{"", root, filepath.Join(root, "inner")} {
		_, err := sdk.Import(context.Background(), &daemonv1.ImportRequest{Source: dest, StateDir: bad})
		wantFailure(t, err, rpc.InvalidArgument)
	}
}

func TestImportAccountFillsTheNamedStateAndTheDefault(t *testing.T) {
	root := recoveryRoot(t)
	_, stop := serving(t, root, false)
	dest := filepath.Join(t.TempDir(), "export")
	if _, err := dialOps(t, root).Export(context.Background(), &daemonv1.ExportRequest{Dest: dest}); err != nil {
		t.Fatal(err)
	}
	stop()
	state := filepath.Join(t.TempDir(), "imported")
	got, err := ImportAccount(context.Background(), dest, state)
	if err != nil || got != state {
		t.Fatalf("%q %v", got, err)
	}
	fallback := filepath.Join(t.TempDir(), "fallback")
	t.Setenv("HERDSMAN_STATE_DIR", fallback)
	got, err = ImportAccount(context.Background(), dest, "")
	if err != nil || got != fallback {
		t.Fatalf("%q %v", got, err)
	}
}

func TestImportRelocatesARouteCreatedThroughTheDaemon(t *testing.T) {
	root := recoveryRoot(t)
	c, stop := serving(t, root, false)
	if _, err := c.CreateRoute(context.Background(), CreateRequest{Repo: "repo", Route: "r1"}); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "export")
	if _, err := dialOps(t, root).Export(context.Background(), &daemonv1.ExportRequest{Dest: dest}); err != nil {
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
		_, err := dialOps(t, h.ph).Export(context.Background(), &daemonv1.ExportRequest{Dest: filepath.Join(t.TempDir(), "export")})
		done <- err
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := c.CreateRoute(ctx, CreateRequest{Repo: "repo", Route: "r1"})
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
	if _, err := c.CreateRoute(context.Background(), CreateRequest{Repo: "repo", Route: "r1"}); err != nil {
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

// linkedTree is a worktree on feat/x holding an ignored .env.
func linkedTree(t *testing.T) (repo, tree string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(shortDir(t))
	if err != nil {
		t.Fatal(err)
	}
	repo, tree = filepath.Join(base, "repo"), filepath.Join(base, "tree")
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
	return repo, tree
}

func TestUndoThroughTheSDKRestoresARealSnapshot(t *testing.T) {
	h := newHarness(t)
	repo, tree := linkedTree(t)
	op, err := h.d.recovery.Snapshot(context.Background(), ops.SnapshotRequest{Task: "t1", Worktree: tree})
	if err != nil {
		t.Fatal(err)
	}
	git(t, repo, "worktree", "remove", "--force", tree)
	git(t, repo, "branch", "-D", "feat/x")
	res, err := dialOps(t, h.ph).Undo(context.Background(), &daemonv1.UndoRequest{Snapshot: op.ID})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetTarget() != tree || !res.GetOriginal() || res.GetOperationId() == "" {
		t.Fatalf("%v", res)
	}
	got, err := os.ReadFile(filepath.Join(tree, ".env"))
	if err != nil || string(got) != "SECRET=1\n" {
		t.Fatalf("%q %v", got, err)
	}
	if head := git(t, tree, "symbolic-ref", "HEAD"); head != "refs/heads/feat/x" {
		t.Fatalf("HEAD = %s", head)
	}
}

// TestExportGateThroughTheSDK is the export gate read only through the
// client: what the barrier saw equals what the imported daemon serves.
func TestExportGateThroughTheSDK(t *testing.T) {
	h := newHarness(t)
	h.stop()
	var clock atomic.Int64
	clock.Store(time.Now().Unix())
	h.cfg.Now = func() time.Time { return time.Unix(clock.Add(1), 0) }
	h.cfg.Retention = ops.Retention{Undo: 40 * time.Second}
	var mu sync.Mutex
	var seen *daemonv1.RecordsResponse
	h.cfg.OpsFault = func(p string) {
		if p != "export:barrier" {
			return
		}
		got, err := dialOps(t, h.ph).Records(context.Background(), &daemonv1.RecordsRequest{})
		if err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		seen = got
		mu.Unlock()
	}
	h.start()
	_, tree := linkedTree(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	var made, swept atomic.Int64
	go func() {
		defer wg.Done()
		for i := 0; ctx.Err() == nil; i++ {
			if _, err := h.d.recovery.Snapshot(ctx, ops.SnapshotRequest{Task: "t" + strconv.Itoa(i), Worktree: tree}); err != nil {
				if ctx.Err() == nil {
					t.Errorf("snapshot: %v", err)
				}
				return
			}
			made.Add(1)
			clock.Add(10)
		}
	}()
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			pruned, err := h.d.recovery.Sweep(ctx)
			if err != nil {
				if ctx.Err() == nil {
					t.Errorf("sweep: %v", err)
				}
				return
			}
			swept.Add(int64(len(pruned)))
			time.Sleep(5 * time.Millisecond)
		}
	}()
	source := dialOps(t, h.ph)
	for i := 0; i < 3; i++ {
		time.Sleep(80 * time.Millisecond)
		dest := filepath.Join(t.TempDir(), "export")
		if _, err := source.Export(context.Background(), &daemonv1.ExportRequest{Dest: dest}); err != nil {
			t.Fatalf("export %d: %v", i, err)
		}
		mu.Lock()
		want := seen
		mu.Unlock()
		state := filepath.Join(shortDir(t), "s")
		if err := ops.Import(context.Background(), dest, state); err != nil {
			t.Fatalf("import %d: %v", i, err)
		}
		_, stopImported := serving(t, state, false)
		got, err := dialOps(t, state).Records(context.Background(), &daemonv1.RecordsRequest{})
		if err != nil {
			stopImported()
			t.Fatal(err)
		}
		if !proto.Equal(got, want) {
			stopImported()
			t.Fatalf("export %d: the imported daemon serves %d records and %d snapshots, the barrier saw %d and %d",
				i, len(got.GetRecords()), len(got.GetSnapshots()), len(want.GetRecords()), len(want.GetSnapshots()))
		}
		for _, s := range got.GetSnapshots() {
			if _, err := snapshot.Verify(context.Background(), filepath.Join(state, "snapshots", s.GetId())); err != nil {
				stopImported()
				t.Fatalf("export %d: snapshot %s: %v", i, s.GetId(), err)
			}
		}
		stopImported()
	}
	cancel()
	wg.Wait()
	if made.Load() == 0 || swept.Load() == 0 {
		t.Fatalf("the loops did no work: %d made, %d pruned", made.Load(), swept.Load())
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

func TestHeldDuringExportCoversOnlyMessageWriters(t *testing.T) {
	base := rpc.BasePath + daemonv1.MessageServiceName + "/"
	for path, want := range map[string]bool{
		base + "Send":        true,
		base + "CreateRoute": true,
		base + "SomeNewCall": true,
		base + "Status":      false,
		base + "Wait":        false,
		rpc.BasePath + daemonv1.OpsServiceName + "/Undo": false,
		rpc.BasePath + daemonv1.RouteServiceName + "/Up": false,
	} {
		if got := heldDuringExport(path); got != want {
			t.Errorf("%s: %v, want %v", path, got, want)
		}
	}
}
