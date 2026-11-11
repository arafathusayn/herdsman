package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/runtime"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// runtimeEvents are the events the daemon published on the runtime topic.
func runtimeEvents(t *testing.T, h *harness) []*daemonv1.RuntimeEvent {
	t.Helper()
	h.d.hub.mu.Lock()
	defer h.d.hub.mu.Unlock()
	var out []*daemonv1.RuntimeEvent
	if topic := h.d.hub.topics["runtime"]; topic != nil {
		for _, ev := range topic.ring {
			var re daemonv1.RuntimeEvent
			if err := protojson.Unmarshal(ev.GetPayload(), &re); err != nil {
				t.Fatal(err)
			}
			out = append(out, &re)
		}
	}
	return out
}

// Services: each supporting service of a core route has one owner, the
// daemon. It is recorded, survives a restart of the owner, acts on the typed
// execution reference and types nothing; a stop forgets it.
func TestCoreServicesHaveOneOwner(t *testing.T) {
	h, w := newCoreHarness(t)
	ctx := context.Background()
	c := h.d.coreRT()
	c.services.poll = 10 * time.Millisecond
	repo := filepath.Join(h.ph, "proj")
	addr := w.Core.Add(map[string]string{})
	w.Core.Mutate(addr, func(t map[string]any) { t["screen"] = "Edit file?\n❯ 1. Yes\n  2. No\n Do you want to proceed?" })
	if err := h.d.recordExecution(ctx, runtime.Execution{ID: execOrch, Repo: repo, Route: "harbor", Agent: "orch", Role: "orchestrator", Backend: runtime.BackendCore,
		LaunchIntent: "i-orch", Generation: 1, Desired: runtime.DesiredRunning, TerminalAddress: addr, Evidence: runtime.ProcessEvidence{PID: 4242, Start: "s"}, Observation: liveObservation()}); err != nil {
		t.Fatal(err)
	}
	watch := route.ServiceSpec{Repo: repo, Route: "harbor", Name: route.ServiceCardWatch}
	if err := c.services.Start(ctx, watch); err != nil {
		t.Fatal(err)
	}
	if err := c.services.Start(ctx, watch); err != nil { // a repeat is a no-op
		t.Fatal(err)
	}
	if !c.services.Running(repo, "harbor", route.ServiceCardWatch) {
		t.Fatal("not running")
	}
	dialogs := func() int {
		n := 0
		for _, ev := range runtimeEvents(t, h) {
			if ev.GetKind() == "needs_attention" && ev.GetReason() == "dialog" && ev.GetExecutionId() == execOrch {
				n++
			}
		}
		return n
	}
	eventually(t, func() bool { return dialogs() == 1 }) // a dialog raises needs_attention
	time.Sleep(50 * time.Millisecond)
	if n := dialogs(); n != 1 {
		t.Errorf("one dialog raised %d events", n)
	}
	if n := w.Core.Calls("terminal.send") + w.Core.Calls("terminal.keys"); n != 0 {
		t.Errorf("the card watch typed %d times", n)
	}
	// A courier is the route's daemon channel: with none it is refused.
	if err := c.services.Start(ctx, route.ServiceSpec{Repo: repo, Route: "harbor", Name: route.ServiceCourier, Inbox: "/nowhere"}); err == nil {
		t.Error("a courier without a daemon channel started")
	}
	// The owner restarts: the recorded service is its again, once.
	c.services.mu.Lock()
	for _, run := range c.services.running {
		run.cancel()
		<-run.done
	}
	c.services.running = map[string]*coreService{}
	c.services.mu.Unlock()
	c.services.resume()
	if !c.services.Running(repo, "harbor", route.ServiceCardWatch) {
		t.Fatal("not taken up again after a restart")
	}
	if err := c.services.Stop(ctx, repo, "harbor", route.ServiceCardWatch); err != nil {
		t.Fatal(err)
	}
	if c.services.Running(repo, "harbor", route.ServiceCardWatch) {
		t.Fatal("running after its stop")
	}
	all, err := c.services.load()
	if err != nil || len(all) != 0 {
		t.Fatalf("records after the stop: %v %v", all, err)
	}
}

func liveObservation() runtime.Observation {
	return runtime.Observation{Process: runtime.ProcessLive, Readiness: runtime.ReadinessReady, Program: runtime.ProgramDone, RawProgram: "done",
		Visibility: runtime.VisibilityVerified, Orchestration: runtime.OrchestrationActive}
}

// execOrch is an execution id of the store's shape.
const execOrch = "0c1f2e3d-4b5a-4c6d-8e7f-a1b2c3d4e5f6"
