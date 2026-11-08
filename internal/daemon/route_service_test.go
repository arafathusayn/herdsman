package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
