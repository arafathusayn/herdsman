package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/automations"
	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/runtime"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// recordingPause stands in for the route pause built-in and records each
// event it is given.
type recordingPause struct {
	mu  sync.Mutex
	got []automations.Event
}

func (r *recordingPause) builtin(_ context.Context, ev automations.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, ev)
	return nil
}

func (r *recordingPause) events() []automations.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]automations.Event(nil), r.got...)
}

// runtimeSinkHarness is a daemon whose automation sink has one approved rule
// for event, acting through a recorded stand-in for the route pause.
func runtimeSinkHarness(t *testing.T, event, project string) (*harness, *AutomationSink, *recordingPause) {
	t.Helper()
	rule := automations.Rule{Event: event, Kind: automations.KindBuiltin, Body: automations.BuiltinRoutePause}
	dir := t.TempDir()
	approvals := automations.OpenApprovals(filepath.Join(dir, "approvals.toml"))
	if err := approvals.Approve(project, rule); err != nil {
		t.Fatal(err)
	}
	rec := &recordingPause{}
	sink := &AutomationSink{Log: func(string) {}, Engine: &automations.Engine{
		Config: automations.Config{Enabled: true, Rules: []automations.Rule{rule}}, Approvals: approvals,
		StateDir: dir, Dir: dir, Stderr: &strings.Builder{}, Timeout: 5 * time.Second,
		Builtins: map[string]automations.Builtin{automations.BuiltinRoutePause: rec.builtin},
	}}
	h := newAccountHarnessWith(t, func(_ string, cfg *Config) { cfg.Sink = sink })
	return h, sink, rec
}

// Every runtime event reaches automations as runtime.<kind>, with the
// route, execution, reason and notice as values, and publishing never waits
// for the automation.
func TestRuntimeEventsReachAutomations(t *testing.T) {
	h, _, rec := runtimeSinkHarness(t, "runtime.needs_attention", "/srv/proj")
	h.d.publishRuntime(&daemonv1.RuntimeEvent{Kind: "needs_attention", Repo: "/srv/proj", Route: "harbor", ExecutionId: "e1",
		Reason: "interrupted", NoticeId: "n1", Detail: "type the line"})
	h.d.publishRuntime(&daemonv1.RuntimeEvent{Kind: "submit_unresolved", Repo: "/srv/proj", Route: "harbor", ExecutionId: "e1"})
	deadline := time.Now().Add(5 * time.Second)
	for len(rec.events()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	got := rec.events()
	if len(got) != 1 {
		t.Fatalf("fired %+v", got)
	}
	ev := got[0]
	if ev.Kind != "runtime.needs_attention" || ev.Project != "/srv/proj" || ev.Values["route"] != "harbor" || ev.Values["execution"] != "e1" ||
		ev.Values["reason"] != "interrupted" || ev.Values["notice"] != "n1" || ev.Values["detail"] != "type the line" {
		t.Fatalf("event %+v", ev)
	}
	if _, ok := ev.Values["outcome"]; ok {
		t.Errorf("an empty field became a value: %+v", ev.Values)
	}
}

// A full queue drops the runtime event rather than wait for automations.
func TestARuntimeEventIsDroppedRatherThanWait(t *testing.T) {
	d := New(Config{Log: func(string) {}})
	block := make(chan struct{})
	defer close(block)
	d.cfg.Sink = blockingFirer{block}
	done := make(chan struct{})
	go func() {
		for i := 0; i < runtimeAutomationQueue+5; i++ {
			d.fireRuntime(&daemonv1.RuntimeEvent{Kind: "needs_attention"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publishing a runtime event waited for an automation")
	}
	close(d.done)
}

type blockingFirer struct{ block chan struct{} }

func (b blockingFirer) Deliver(context.Context, ops.OutboxEvent) error { return nil }
func (b blockingFirer) FireRuntime(context.Context, automations.Event) {
	<-b.block
}

// The route pause built-in on a route on herdr-core runs the route's pause
// verb through the daemon, which acts through the provider and the notices:
// the Herdr TUI's interrupt key is never sent.
func TestRoutePauseOnCoreGoesThroughTheDaemon(t *testing.T) {
	h, sink, rec := runtimeSinkHarness(t, "review.p0", "/srv/proj")
	if err := h.d.ops.Read(func(c store.Conn) error {
		_, err := store.PutExecution(c, store.Execution{ID: "11111111-1111-4111-8111-111111111111", Repo: "/srv/proj", Route: "harbor",
			Agent: "proj-harbor-orchestrator", Role: "orchestrator", LaunchIntent: "op-1:agent:a", Backend: runtime.BackendCore, EndpointID: "core-1",
			HarnessKind: "codex", DesiredState: "running", Process: "live", Readiness: "waiting", Program: "idle", Visibility: "verified",
			Orchestration: "active", ObservedAt: 10}, h.d.now().UnixMilli())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	pause := sink.Engine.Builtins[automations.BuiltinRoutePause]
	err := pause(context.Background(), automations.Event{Kind: "review.p0", Project: "/srv/proj", Values: map[string]string{"route": "harbor"}})
	// No such route in the registry: the daemon's verb refuses, which shows
	// the pause went to the daemon and not to a pane.
	if err == nil || !strings.Contains(err.Error(), "on herdr-core") {
		t.Fatalf("pause on core: %v", err)
	}
	if got := rec.events(); len(got) != 0 {
		t.Fatalf("the TUI's interrupt ran for a core route: %+v", got)
	}
	if calls := h.srv.Requests(); len(calls) != 0 {
		t.Fatalf("a pause on core reached herdr: %v", calls)
	}
	// A route on the TUI keeps the built-in.
	if err := pause(context.Background(), automations.Event{Kind: "review.p0", Project: "/srv/proj", Values: map[string]string{"route": "other"}}); err != nil {
		t.Fatal(err)
	}
	if got := rec.events(); len(got) != 1 || got[0].Values["route"] != "other" {
		t.Fatalf("the TUI route: %+v", got)
	}
}

func TestRoutePauseOnCoreSaysWhichRoute(t *testing.T) {
	want := errors.New("refused")
	pause := routePauseOnCore(func(context.Context, automations.Event) error { return nil },
		func(string, string) bool { return true },
		func(context.Context, string, string) error { return want })
	err := pause(context.Background(), automations.Event{Values: map[string]string{"route": "r1"}})
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "r1 on herdr-core") {
		t.Fatal(err)
	}
}
