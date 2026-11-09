package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/ledger"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/wtree"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

func latestStart(t *testing.T, h *harness, repo string) *ops.Operation {
	t.Helper()
	starts, err := (&routeService{d: h.d}).starts(route.UpKey(repo, "harbor"))
	if err != nil || len(starts) == 0 {
		t.Fatalf("starts: %v %v", starts, err)
	}
	return starts[len(starts)-1]
}

// A second caller of a verb that is mid-step gets its progress, not a second run.
func TestABusyVerbReportsItsProgress(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	repo, _ := upNoAdjutant(t, h, c)
	h.stop()
	entered, release := make(chan struct{}), make(chan struct{})
	h.cfg.RouteTeardown = func(context.Context, string, string) error {
		close(entered)
		<-release
		return nil
	}
	h.start()
	c = routeClient(t, h)
	ctx := context.Background()
	id := &corev1.RouteId{Value: "harbor"}
	first := make(chan error, 1)
	go func() {
		_, err := c.Route.Pause(ctx, &daemonv1.RouteServicePauseRequest{Route: id, Repo: repo, Teardown: true})
		first <- err
	}()
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the teardown step never ran")
	}
	again, err := c.Route.Pause(ctx, &daemonv1.RouteServicePauseRequest{Route: id, Repo: repo, Teardown: true})
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if again.GetResult().GetState() != string(ops.OpRunning) {
		t.Fatalf("%+v", again.GetResult())
	}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

// Another operation on the route's lock refuses the verb and journals nothing.
func TestALockedRouteRefusesAVerb(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	repo, _ := upNoAdjutant(t, h, c)
	ctx := context.Background()
	plan, err := route.PlanControl(ctx, route.ControlRequest{Verb: route.VerbPause, Start: latestStart(t, h, repo), Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	plan.Spec.Key, plan.Spec.Kind = "another-operation", "route.other"
	if _, err := h.d.ops.Submit(ctx, plan.Spec); err != nil {
		t.Fatal(err)
	}
	_, err = c.Route.Pause(ctx, &daemonv1.RouteServicePauseRequest{Route: &corev1.RouteId{Value: "harbor"}, Repo: repo})
	if codeOf(err) != rpc.FailedPrecondition {
		t.Fatalf("got %v", err)
	}
}

// An unfinished resume can restart agents, so finishing it needs a real caller.
func TestAnUnfinishedResumeNeedsAVerifiedCaller(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	repo, _ := upNoAdjutant(t, h, c)
	ctx := context.Background()
	id := &corev1.RouteId{Value: "harbor"}
	if _, err := c.Route.Pause(ctx, &daemonv1.RouteServicePauseRequest{Route: id, Repo: repo}); err != nil {
		t.Fatal(err)
	}
	plan, err := route.PlanControl(ctx, route.ControlRequest{Verb: route.VerbResume, From: route.ControlPaused, Start: latestStart(t, h, repo), Generation: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.d.ops.Submit(ctx, plan.Spec); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Route.Resume(ctx, &daemonv1.RouteServiceResumeRequest{Route: id, Repo: repo, Caller: &daemonv1.UpCaller{Pid: 1}}); err == nil {
		t.Fatal("a caller that is not the peer finished the resume")
	}
	res, err := c.Route.Resume(ctx, &daemonv1.RouteServiceResumeRequest{Route: id, Repo: repo, Caller: &daemonv1.UpCaller{Pid: int64(os.Getpid())}})
	if err != nil || res.GetResult().GetState() != string(ops.OpDone) || res.GetResult().GetControl() != route.ControlRunning {
		t.Fatalf("%v %v", res, err)
	}
}

func TestDriveControlOfAnUnknownOperation(t *testing.T) {
	h := newHarness(t)
	_, err := (&routeService{d: h.d}).driveControl(context.Background(), controlCall{verb: route.VerbPause}, &ops.Operation{ID: "absent"})
	if codeOf(err) != rpc.Internal {
		t.Fatalf("got %v", err)
	}
}

// A task recorded on the route is stopped, and another route's is not. The
// worktree layer does not record a task's route yet, so the test writes it.
func TestRouteTeardownStopsTheRoutesTasks(t *testing.T) {
	folder := filepath.Join(t.TempDir(), ".herdsman", "harbor-2026-11-09")
	s, repo, worktree, common := verbRepo(t)
	ctx := context.Background()
	state, err := wtree.OpenState(common)
	if err != nil {
		t.Fatal(err)
	}
	_, err = state.DB().ExecContext(ctx, `INSERT INTO tasks (task, route, created_at) VALUES (?, ?, 1)`, "task", folder)
	state.Close()
	if err != nil {
		t.Fatal(err)
	}
	w := setupLedger(t)
	useLedger(t, w.service)
	w.pids = []ledger.Process{{Pid: 5, Group: 5, Parent: 1, Start: ledgerStart, Command: "server"}}
	if _, err := s.ResourceAdd(ctx, WorktreeResourceAddRequest{Dir: worktree, Execution: "zz", Kind: ledger.KindProcess, ID: "5"}); err != nil {
		t.Fatal(err)
	}
	if err := teardownRoute(s)(ctx, repo, folder+"-another"); err != nil || len(w.calls) != 0 {
		t.Fatalf("another route's teardown: %v, %v", err, w.calls)
	}
	if err := teardownRoute(s)(ctx, repo, folder); err != nil {
		t.Fatal(err)
	}
	if len(w.calls) == 0 {
		t.Fatal("the route's task was not stopped")
	}
}
