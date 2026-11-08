package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/sdk/client"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

func routeClient(t *testing.T, h *harness) *client.Client {
	t.Helper()
	c, err := client.Dial(client.Options{Socket: SocketPath(h.ph)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func codeOf(err error) rpc.Code {
	var e *rpc.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func upRequest(repo string) *daemonv1.UpRequest {
	return &daemonv1.UpRequest{
		Route: &corev1.RouteId{Value: "harbor"}, Repo: repo, Prompt: "ship it",
		AdjutantCommand: "claude doctor && claude", OrchestratorCommand: "codex",
		Kinds: []string{"claude", "codex"}, Date: "2026-11-09", DryRun: true,
	}
}

func TestUpDryRunPlansWithoutJournaling(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	ctx := context.Background()
	repo := filepath.Join(h.ph, "proj")
	res, err := c.Route.Up(ctx, upRequest(repo))
	if err != nil {
		t.Fatal(err)
	}
	if res.GetStart().GetState() != "planned" || !strings.Contains(res.GetStart().GetPlan(), "claude doctor") {
		t.Fatalf("%+v", res.GetStart())
	}
	if res.GetStart().GetWorkspace() != "proj-harbor" {
		t.Fatalf("workspace %q", res.GetStart().GetWorkspace())
	}
	list, err := c.Route.List(ctx, &daemonv1.RouteServiceListRequest{})
	if err != nil || len(list.GetStarts()) != 0 {
		t.Fatalf("a dry run was journaled: %v %v", list, err)
	}
}

func TestUpRefusesABadRequest(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	ctx := context.Background()
	if _, err := c.Route.Up(ctx, &daemonv1.UpRequest{}); codeOf(err) != rpc.InvalidArgument {
		t.Fatalf("no route: %v", err)
	}
	q := upRequest(filepath.Join(h.ph, "proj"))
	q.AdjutantCommand = "claude; rm -rf x"
	if _, err := c.Route.Up(ctx, q); codeOf(err) != rpc.InvalidArgument {
		t.Fatalf("refused line: %v", err)
	}
	q = upRequest(filepath.Join(h.ph, "proj"))
	q.DryRun, q.Continue = false, true
	q.Caller = &daemonv1.UpCaller{Pid: int64(os.Getpid())}
	if _, err := c.Route.Up(ctx, q); codeOf(err) != rpc.FailedPrecondition {
		t.Fatalf("continue with no start: %v", err)
	}
}

func TestUpRefusesACallerThatIsNotThePeer(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	q := upRequest(filepath.Join(h.ph, "proj"))
	q.DryRun = false
	q.Caller = &daemonv1.UpCaller{Pid: int64(os.Getpid()) + 1, Tty: "forged"}
	if _, err := c.Route.Up(context.Background(), q); codeOf(err) != rpc.PermissionDenied {
		t.Fatalf("got %v", err)
	}
}

func TestVerifiedCallerIgnoresClaimedFacts(t *testing.T) {
	inspect := func(pid int) (procFacts, error) {
		switch pid {
		case 10:
			return procFacts{ppid: 20, tty: "ttys9", command: "herdsman up"}, nil
		case 20:
			return procFacts{ppid: 1, tty: "ttys9", command: "/opt/bin/claude --model x"}, nil
		}
		return procFacts{ppid: 1}, nil
	}
	tty, harness := inspectCaller(10, inspect)
	if tty != "/dev/ttys9" || harness != "claude" {
		t.Fatalf("%q %q", tty, harness)
	}
	none := func(int) (procFacts, error) { return procFacts{ppid: 1, tty: "??", command: "sshd"}, nil }
	if tty, harness = inspectCaller(10, none); tty != "" || harness != "" {
		t.Fatalf("%q %q", tty, harness)
	}
	node := func(int) (procFacts, error) {
		return procFacts{ppid: 1, tty: "ttys1", command: "node /usr/lib/node_modules/.bin/codex"}, nil
	}
	if _, harness = inspectCaller(10, node); harness != "codex" {
		t.Fatalf("a script harness was missed: %q", harness)
	}
}

func TestLatestStartIsTheNewest(t *testing.T) {
	starts := []*ops.Operation{{ID: "02B"}, {ID: "01A"}, {ID: "03C"}}
	sortStarts(starts)
	if starts[len(starts)-1].ID != "03C" || starts[0].ID != "01A" {
		t.Fatalf("%v %v %v", starts[0].ID, starts[1].ID, starts[2].ID)
	}
}

func TestGetNamesAnUnknownRoute(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	ctx := context.Background()
	_, err := c.Route.Get(ctx, &daemonv1.RouteServiceGetRequest{Route: &corev1.RouteId{Value: "nope"}, Repo: "/x"})
	if codeOf(err) != rpc.NotFound {
		t.Fatalf("got %v", err)
	}
	if _, err := c.Route.Get(ctx, &daemonv1.RouteServiceGetRequest{}); codeOf(err) != rpc.InvalidArgument {
		t.Fatalf("empty: %v", err)
	}
}

// upNoAdjutant starts a route with one agent through the service, on a fake
// herdr layout, and waits for it to finish.
func upNoAdjutant(t *testing.T, h *harness, c *client.Client) (string, *herdrtest.World) {
	t.Helper()
	world := herdrtest.ServeWorld(h.fx)
	repo := filepath.Join(h.ph, "proj")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	q := upRequest(repo)
	q.DryRun, q.NoAdjutant, q.AdjutantCommand = false, true, ""
	q.Caller = &daemonv1.UpCaller{Pid: int64(os.Getpid())}
	if _, err := c.Route.Up(ctx, q); err != nil {
		t.Fatal(err)
	}
	get := &daemonv1.RouteServiceGetRequest{Route: q.GetRoute(), Repo: repo}
	for range 500 {
		res, err := c.Route.Get(ctx, get)
		if err != nil {
			t.Fatal(err)
		}
		switch res.GetStart().GetState() {
		case "done":
			return repo, world
		case "running":
			time.Sleep(10 * time.Millisecond)
		default:
			t.Fatalf("up: %s: %s", res.GetStart().GetState(), res.GetStart().GetReason())
		}
	}
	t.Fatal("up did not finish")
	return "", nil
}

func TestEachVerbRepeatedChangesNothing(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	repo, world := upNoAdjutant(t, h, c)
	ctx := context.Background()
	id := &corev1.RouteId{Value: "harbor"}
	pause := func() *daemonv1.RouteControl {
		res, err := c.Route.Pause(ctx, &daemonv1.RouteServicePauseRequest{Route: id, Repo: repo})
		if err != nil {
			t.Fatal(err)
		}
		return res.GetResult()
	}
	if got := pause(); got.GetState() != "done" || got.GetControl() != "paused" || got.GetOperation() == "" {
		t.Fatalf("%+v", got)
	}
	h.srv.ForgetRequests()
	if got := pause(); got.GetState() != "unchanged" || got.GetControl() != "paused" {
		t.Fatalf("%+v", got)
	}
	if n := len(h.srv.Requests()); n != 0 {
		t.Fatalf("a repeated pause reached herdr %d times", n)
	}
	res, err := c.Route.Get(ctx, &daemonv1.RouteServiceGetRequest{Route: id, Repo: repo})
	if err != nil || res.GetStart().GetControl() != "paused" {
		t.Fatalf("%v %v", res, err)
	}
	stop := func() *daemonv1.RouteControl {
		res, err := c.Route.Stop(ctx, &daemonv1.RouteServiceStopRequest{Route: id, Repo: repo})
		if err != nil {
			t.Fatal(err)
		}
		return res.GetResult()
	}
	if got := stop(); got.GetState() != "done" || got.GetControl() != "stopped" {
		t.Fatalf("%+v", got)
	}
	if got := stop(); got.GetState() != "unchanged" {
		t.Fatalf("%+v", got)
	}
	if _, err := c.Route.Pause(ctx, &daemonv1.RouteServicePauseRequest{Route: id, Repo: repo}); codeOf(err) != rpc.FailedPrecondition {
		t.Fatalf("pause of a stopped route: %v", err)
	}
	if world.PaneByLabel("orchestrator") != "" {
		t.Fatal("stop left the orchestrator pane")
	}
}

func TestDeleteRefusesWhileAgentsRunAndForceStopsThem(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	repo, _ := upNoAdjutant(t, h, c)
	ctx := context.Background()
	id := &corev1.RouteId{Value: "harbor"}
	_, err := c.Route.Delete(ctx, &daemonv1.RouteServiceDeleteRequest{Route: id, Repo: repo})
	if codeOf(err) != rpc.FailedPrecondition || !strings.Contains(err.Error(), "harbor-orchestrator") {
		t.Fatalf("got %v", err)
	}
	list, err := c.Route.Get(ctx, &daemonv1.RouteServiceGetRequest{Route: id, Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	folder := list.GetStart().GetFolder()
	if _, err := os.Stat(folder); err != nil {
		t.Fatal(err)
	}
	dry, err := c.Route.Delete(ctx, &daemonv1.RouteServiceDeleteRequest{Route: id, Repo: repo, Force: true, DryRun: true})
	if err != nil || dry.GetResult().GetState() != "planned" || !strings.Contains(dry.GetResult().GetPlan(), "archive") {
		t.Fatalf("%v %v", dry, err)
	}
	if _, err := os.Stat(folder); err != nil {
		t.Fatalf("a dry run touched the folder: %v", err)
	}
	res, err := c.Route.Delete(ctx, &daemonv1.RouteServiceDeleteRequest{Route: id, Repo: repo, Force: true})
	if err != nil || res.GetResult().GetState() != "done" || res.GetResult().GetControl() != "deleted" || res.GetResult().GetArchive() == "" {
		t.Fatalf("%v %v", res, err)
	}
	if _, err := os.Stat(folder); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := c.Route.Resume(ctx, &daemonv1.RouteServiceResumeRequest{Route: id, Repo: repo}); codeOf(err) != rpc.FailedPrecondition {
		t.Fatalf("resume of a deleted route: %v", err)
	}
}

func TestUpRefusesAPausedRouteAndStartsAfreshAfterDelete(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	repo, _ := upNoAdjutant(t, h, c)
	ctx := context.Background()
	id := &corev1.RouteId{Value: "harbor"}
	if _, err := c.Route.Pause(ctx, &daemonv1.RouteServicePauseRequest{Route: id, Repo: repo}); err != nil {
		t.Fatal(err)
	}
	q := upRequest(repo)
	q.DryRun, q.NoAdjutant, q.AdjutantCommand = false, true, ""
	q.Caller = &daemonv1.UpCaller{Pid: int64(os.Getpid())}
	if _, err := c.Route.Up(ctx, q); codeOf(err) != rpc.FailedPrecondition {
		t.Fatalf("up on a paused route: %v", err)
	}
	if _, err := c.Route.Delete(ctx, &daemonv1.RouteServiceDeleteRequest{Route: id, Repo: repo, Force: true}); err != nil {
		t.Fatal(err)
	}
	res, err := c.Route.Up(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.GetStart().GetState(), "running") && res.GetStart().GetState() != "done" {
		t.Fatalf("%+v", res.GetStart())
	}
	list, err := c.Route.List(ctx, &daemonv1.RouteServiceListRequest{})
	if err != nil || len(list.GetStarts()) != 2 {
		t.Fatalf("%d starts, %v", len(list.GetStarts()), err)
	}
}

func TestControlRefusesARouteWithNoStart(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	ctx := context.Background()
	_, err := c.Route.Stop(ctx, &daemonv1.RouteServiceStopRequest{Route: &corev1.RouteId{Value: "nope"}, Repo: "/x"})
	if codeOf(err) != rpc.NotFound {
		t.Fatalf("got %v", err)
	}
	if _, err := c.Route.Stop(ctx, &daemonv1.RouteServiceStopRequest{}); codeOf(err) != rpc.InvalidArgument {
		t.Fatalf("empty: %v", err)
	}
}
