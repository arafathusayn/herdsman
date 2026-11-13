package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	"github.com/arafathusayn/herdsman/internal/runtime/core/coretest"
	"github.com/arafathusayn/herdsman/sdk/client"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// newCoreHarness is a new account whose config file opts into herdr-core, the
// experiment, and whose core is a fake core with a fake app and process table;
// every launch terminal runs the wrapper in process.

func newCoreHarness(t *testing.T) (*harness, *coretest.World) {
	t.Helper()
	w := coretest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w.AutoWrap(ctx)
	h := newAccountHarnessWith(t, func(dir string, cfg *Config) {
		cfg.Core = CoreConfig{Socket: w.Core.Socket, App: coretest.AppRunner{W: w}, AppHome: w.AppHome, Procs: w.Procs, Peer: w.Peer,
			Environ:  []string{"PATH=/usr/bin:/bin", "HOME=" + filepath.Join(dir, "h"), "HERDR_PANE_ID=p9", "LANG=C"},
			Timeouts: core.Timeouts{Register: 5 * time.Second, Ready: 5 * time.Second, Visibility: 300 * time.Millisecond, Poll: 10 * time.Millisecond}, NoReconcile: true}
	})
	optInCore(t, h)
	// The inbox's setup proof needs a sandbox policy file to prove.
	if err := os.MkdirAll(filepath.Join(h.home, ".claude"), 0o700); err != nil {

		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.home, ".claude", "settings.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return h, w
}

func coreUp(repo string) *daemonv1.UpRequest {
	return &daemonv1.UpRequest{
		Route: &corev1.RouteId{Value: "harbor"}, Repo: repo, Prompt: "ship it", AdjutantCommand: "claude", OrchestratorCommand: "codex --no-daemon",
		Date: "2026-11-11", Caller: &daemonv1.UpCaller{Pid: int64(os.Getpid())},
	}
}

// waitHalt waits until the start is no longer running.
func waitHalt(t *testing.T, c *client.Client, repo string) *daemonv1.RouteStart {
	t.Helper()
	return waitStart(t, c, &corev1.RouteId{Value: "harbor"}, repo)
}

// Core path, through the daemon: a new account starts on herdr-core with no
// herdr anywhere, records its executions in the account store, stops at each
// wait for a person, and settles them with --continue.
func TestDaemonStartsARouteOnCore(t *testing.T) {
	h, w := newCoreHarness(t)
	c := routeClient(t, h)
	ctx := context.Background()
	repo := filepath.Join(h.ph, "proj")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Route.Up(ctx, coreUp(repo)); err != nil {
		t.Fatal(err)
	}
	start := waitHalt(t, c, repo)
	if start.GetBackend() != runtime.BackendCore || start.GetState() != "stopped" || !strings.Contains(start.GetReason(), core.CodeWaitingVisibility) {
		t.Fatalf("first wait: %+v", start)
	}
	w.OpenApp()
	again := func() *daemonv1.RouteStart {
		t.Helper()
		req := coreUp(repo)
		req.Continue = true
		if _, err := c.Route.Up(ctx, req); err != nil {
			t.Fatal(err)
		}
		return waitHalt(t, c, repo)
	}
	start = again()
	if !strings.Contains(start.GetReason(), core.CodeNeedsAttention) || !strings.Contains(start.GetReason(), core.ClaudeStartLine) {
		t.Fatalf("start line: %+v", start)
	}
	start = again() // the orchestrator's start line: Codex is attended
	if !strings.Contains(start.GetReason(), core.CodexStartLine) || !strings.Contains(start.GetPlan(), "attended") {
		t.Fatalf("orchestrator's start line: %+v", start)
	}
	var rows []store.Execution
	if err := h.d.ops.Read(func(c store.Conn) error {
		var err error
		rows, err = store.Executions(c, repo, "harbor")
		return err
	}); err != nil || len(rows) != 2 {
		t.Fatalf("records in the account store: %v %d", err, len(rows))
	}
	for _, r := range rows {
		if r.Backend != runtime.BackendCore || r.PID == 0 || r.EndpointID != w.Core.ID {
			t.Errorf("record %+v", r)
		}
	}
	// Environment: the harness got the login values and none of the
	// daemon's agent markers.
	for _, r := range rows {
		if got := w.Execed(r.LaunchIntent); len(got) == 0 {
			t.Errorf("%s never ran its harness", r.Agent)
		}
	}
	if n := w.Core.Calls("terminal.send"); n != 0 {
		t.Errorf("the daemon typed into core %d times", n)
	}
	if calls := h.srv.Requests(); len(calls) != 0 {
		t.Errorf("a core start reached herdr: %v", calls)
	}

	// Caller identity: a caller below an execution's process is that agent,
	// and the pair is never enrolled for it.
	w.Procs.Put(os.Getpid(), int(rows[0].PID), "herdsman", "up")
	start = again()
	if start.GetState() == "done" || !strings.Contains(start.GetReason(), "an agent") {
		t.Fatalf("an agent caller enrolled the pair: %+v", start)
	}
}

// The doctor names the inventory rule in force and the terminal at fault.
func TestDoctorNamesTheInventoryRuleAndTheTerminalAtFault(t *testing.T) {
	h, w := newCoreHarness(t)
	c := routeClient(t, h)
	ctx := context.Background()
	repo := filepath.Join(h.ph, "proj")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	w.OpenApp()
	if _, err := c.Route.Up(ctx, coreUp(repo)); err != nil {
		t.Fatal(err)
	}
	waitHalt(t, c, repo)
	var rows []store.Execution
	_ = h.d.ops.Read(func(c store.Conn) error {
		var err error
		rows, err = store.Executions(c, repo, "harbor")
		return err
	})
	if len(rows) == 0 {
		t.Fatal("no execution")
	}
	// A terminal with the route's labels that no execution registered.
	labels := map[string]string{}
	for k, v := range w.Core.Terminal(rows[0].TerminalAddress)["labels"].(map[string]string) {
		labels[k] = v
	}
	labels[herdrapp.LabelExecution] = "e-copied"
	impostor := w.Core.Add(labels)
	doc, err := (&runtimeService{d: h.d}).Doctor(ctx, &daemonv1.RuntimeServiceDoctorRequest{Repo: repo, Route: &corev1.RouteId{Value: "harbor"}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ch := range doc.GetChecks() {
		if ch.GetName() == "inventory" {
			found = true
			if ch.GetState() != "fail" || !strings.Contains(ch.GetDetail(), "inventory rule route") || !strings.Contains(ch.GetDetail(), impostor) {
				t.Errorf("inventory check: %+v", ch)
			}
		}
	}
	if !found {
		t.Fatalf("no inventory check: %v", doc.GetChecks())
	}
}
