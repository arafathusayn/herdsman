package core_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

// events collects what a reconciler raises.
type events struct {
	mu   sync.Mutex
	list []core.Event
}

func (e *events) add(ev core.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, ev)
}

func (e *events) of(kind string) []core.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []core.Event
	for _, ev := range e.list {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

func (w *world) reconciler(ev *events) *core.Reconciler {
	return &core.Reconciler{Socket: w.core.Socket, ExpectCore: w.core.ID, Records: w.records, Procs: w.procs, App: w.p.App, Bindings: w.bindings,
		HarnessNames: []string{"claude", "codex"}, Emit: ev.add, Safety: time.Hour, Settle: 10 * time.Millisecond}
}

// ready brings an execution through visibility and readiness.
func (w *world) ready(l core.Launch) runtime.Execution {
	w.t.Helper()
	ctx := testCtx(w.t)
	if _, err := w.p.VisibilityStep(ctx, l, core.Timeouts{Visibility: 2 * time.Second, Poll: 20 * time.Millisecond}); err != nil {
		w.t.Fatalf("visibility: %v", err)
	}
	w.core.SetStatus(w.record(l.Intent).TerminalAddress, "idle")
	rec, err := w.p.ReadyStep(ctx, l, core.Timeouts{Ready: 2 * time.Second, Poll: 20 * time.Millisecond})
	if err != nil {
		w.t.Fatalf("ready: %v", err)
	}
	return rec
}

// Identity replacement, against the fake core (gates_test.go).
func TestRecoveryShellInheritsNothing(t *testing.T) { gateRecoveryShellInheritsNothing(newWorld(t)) }

// Recovery shells after a core restart, against the fake core.
func TestCoreRestartLosesEveryExecutionOnePlan(t *testing.T) {
	gateCoreRestartLosesEveryExecutionOnePlan(newWorld(t))
}

// Caller identity by process, against the fake core.
func TestCallerIdentityByProcess(t *testing.T) { gateCallerIdentityByProcess(newWorld(t)) }

// Pause: one interrupt at most, none at rest, none to a Codex turn unless a
// person asked. Stop: only the proven process, its recorded work too.
func TestPauseAndStopDiscipline(t *testing.T) {
	w := newWorld(t)
	ctx := testCtx(t)
	c := w.dial()
	ledgerDir := core.FilePauseLedger{Dir: t.TempDir()}
	_, claude := w.up("life", "life-o", "orchestrator", "claude")
	w.core.SetStatus(claude.TerminalAddress, "working")

	rest, err := core.Pause(ctx, c, core.PauseInput{Pause: "p1", Execution: claude, InTurn: false, Ledger: ledgerDir, Procs: w.procs})
	if err != nil || rest.Outcome != core.PauseLeftWaiting {
		t.Fatalf("at rest: %+v %v", rest, err)
	}
	for range 3 {
		if _, err := core.Pause(ctx, c, core.PauseInput{Pause: "p2", Execution: claude, InTurn: true, Ledger: ledgerDir, Procs: w.procs}); err != nil {
			t.Fatal(err)
		}
	}
	if n := w.core.Calls(hc.MethodInterrupt); n != 1 {
		t.Fatalf("interrupts = %d, want 1", n)
	}

	_, codex := w.up("life", "life-i", "implementer", "codex")
	got, err := core.Pause(ctx, c, core.PauseInput{Pause: "p3", Execution: codex, InTurn: true, Ledger: ledgerDir, Procs: w.procs})
	if err != nil || got.Outcome != core.PausePending {
		t.Fatalf("codex pause = %+v %v", got, err)
	}
	if n := w.core.Calls(hc.MethodInterrupt); n != 1 {
		t.Fatalf("a Codex turn was interrupted: %d", n)
	}

	// Codex left a background command that outlives its terminal.
	background := w.procs.Add(codex.Evidence.PID, "node", "server.js")
	stubborn := w.procs.Add(background, "sleep", "600")
	w.procs.IgnoreTerm(stubborn)
	res, err := core.Stop(ctx, c, core.StopInput{Execution: codex, Procs: w.procs, Grace: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Killed || len(res.Ended) != 2 || len(res.Left) != 0 {
		t.Fatalf("stop = %+v", res)
	}
	w.procs.Kill(codex.Evidence.PID) // core's kill ended the harness
	if alive, _ := core.Alive(w.procs, codex.Evidence); alive {
		t.Fatal("the harness lives")
	}
	if term := w.core.Terminal(codex.TerminalAddress); term["state"] != "finished" {
		t.Fatalf("terminal = %v", term["state"])
	}
}

// Unattended launch: with the binding verified earlier and the app closed, a
// launch goes on as pending; with no binding ever, it waits for visibility.
func TestUnattendedLaunch(t *testing.T) {
	ctx := testCtx(t)
	t.Run("verified earlier", func(t *testing.T) {
		w := newWorld(t)
		w.openApp()
		l1, _ := w.up("night", "night-o", "orchestrator", "claude")
		w.ready(l1)
		w.app.Quit()
		l2, _ := w.up("night", "night-i", "implementer", "claude")
		rec, err := w.p.VisibilityStep(ctx, l2, core.Timeouts{Visibility: time.Second, Poll: 20 * time.Millisecond})
		if err != nil {
			t.Fatalf("visibility with the app closed: %v", err)
		}
		if rec.Observation.Visibility != runtime.VisibilityPending || rec.Observation.App != runtime.AppClosed {
			t.Fatalf("visibility = %+v", rec.Observation)
		}
		w.core.SetStatus(rec.TerminalAddress, "idle")
		if _, err := w.p.ReadyStep(ctx, l2, core.Timeouts{Ready: time.Second, Poll: 20 * time.Millisecond}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.p.Enrollable(ctx, []string{l2.Intent}); err != nil {
			t.Fatalf("a pending execution may not be enrolled: %v", err)
		}
	})
	t.Run("never verified", func(t *testing.T) {
		w := newWorld(t)
		l, _ := w.up("fresh", "fresh-o", "orchestrator", "claude")
		_, err := w.p.VisibilityStep(ctx, l, core.Timeouts{Visibility: time.Second, Poll: 20 * time.Millisecond})
		if core.CodeOf(err) != core.CodeWaitingVisibility || !strings.Contains(err.Error(), w.core.ID) {
			t.Fatalf("visibility: %v", err)
		}
		if rec := w.record(l.Intent); rec.Observation.Visibility != runtime.VisibilityUnverified {
			t.Fatalf("visibility = %s", rec.Observation.Visibility)
		}
		w.openApp()
		if _, err := w.p.VisibilityStep(ctx, l, core.Timeouts{Visibility: 2 * time.Second, Poll: 20 * time.Millisecond}); err != nil {
			t.Fatalf("visibility once the app is open: %v", err)
		}
	})
}

// Endpoint mismatch, against the fake core.
func TestEndpointMismatchEndToEnd(t *testing.T) { gateEndpointMismatch(newWorld(t)) }

// User actions in the app: quit keeps the execution, a park holds it, a move
// is kept, a close ends it and nothing restarts it.
func TestUserUIActions(t *testing.T) {
	w := newWorld(t)
	w.openApp()
	ctx := testCtx(t)
	l, _ := w.up("ui", "ui-o", "orchestrator", "claude")
	rec := w.ready(l)
	ev := &events{}
	r := w.reconciler(ev)

	w.app.Quit()
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	got := w.record(l.Intent)
	if got.Observation.Process != runtime.ProcessLive || got.Observation.App != runtime.AppClosed || got.Observation.Visibility != runtime.VisibilityPending {
		t.Fatalf("after quit: %+v", got.Observation)
	}
	w.openApp()
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if v := w.record(l.Intent).Observation.Visibility; v != runtime.VisibilityVerified {
		t.Fatalf("after reopening: %s", v)
	}

	// A move to another tab keeps the execution and its identity.
	c := w.dial()
	if _, err := c.Label(ctx, rec.TerminalAddress, map[string]string{herdrapp.LabelTab: "hs-moved-by-a-person"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	got = w.record(l.Intent)
	if got.Observation.Visibility != runtime.VisibilityVerified || got.App.TabID != "hs-moved-by-a-person" || got.Evidence != rec.Evidence {
		t.Fatalf("after a move: %+v %+v", got.Observation, got.App)
	}

	// A park removes the placement: missing, and dispatch is held.
	if _, err := c.Label(ctx, rec.TerminalAddress, map[string]string{herdrapp.LabelSpace: "", herdrapp.LabelTab: ""}); err != nil {
		t.Fatal(err)
	}
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if v := w.record(l.Intent).Observation.Visibility; v != runtime.VisibilityMissing {
		t.Fatalf("after a park: %s", v)
	}
	if _, err := w.p.Enrollable(ctx, []string{l.Intent}); core.CodeOf(err) != core.CodeWaitingVisibility {
		t.Fatalf("a parked execution passed the gate: %v", err)
	}
	if err := w.p.Show(ctx, runtime.Target{ExecutionID: rec.ID}); err != nil {
		t.Fatalf("show: %v", err)
	}
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if v := w.record(l.Intent).Observation.Visibility; v != runtime.VisibilityVerified {
		t.Fatalf("after show: %s", v)
	}

	// A close ends the terminal: externally stopped, never restarted.
	runs := w.core.Calls(hc.MethodRun)
	if _, err := c.Kill(ctx, rec.TerminalAddress); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := r.Pass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	got = w.record(l.Intent)
	if got.Observation.Process != runtime.ProcessFinished || got.Desired != runtime.DesiredStopped {
		t.Fatalf("after a close: %+v %s", got.Observation, got.Desired)
	}
	if w.core.Calls(hc.MethodRun) != runs || len(ev.of(core.EventExecutionFinished)) != 1 {
		t.Fatalf("resurrection: runs %d->%d, events %+v", runs, w.core.Calls(hc.MethodRun), ev.list)
	}
}

// Reconnect and a changed core identity, against the fake core.
func TestReconnectAndIdentityChange(t *testing.T) { gateReconnectAndIdentityChange(newWorld(t)) }

// A core that stops ends its terminals one by one: a pass that runs in the
// middle sees one execution lost while the next is still dying. The plan
// waits for the incident to settle and names both, once.
func TestRecoveryPlanSettlesOneIncident(t *testing.T) {
	w := newWorld(t)
	ctx := testCtx(t)
	_, a := w.up("split", "split-a", "orchestrator", "claude")
	_, b := w.up("split", "split-b", "implementer", "claude")
	// The pass checks a first and finds it live; b is lost, and a's holder
	// dies while the pass is under way.
	w.gLose(b)
	ev := &events{}
	r := w.reconciler(ev)
	r.Emit = func(e core.Event) {
		ev.add(e)
		if e.Kind == core.EventExecutionLost && e.Execution == b.ID {
			w.gLose(a)
		}
	}
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	plans := ev.of(core.EventRecoveryPlan)
	if len(plans) != 1 || len(plans[0].Executions) != 2 {
		t.Fatalf("plans = %+v", plans)
	}
	if got := w.record(a.LaunchIntent); got.Observation.Process != runtime.ProcessLost {
		t.Fatalf("a is %s", got.Observation.Process)
	}
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if len(ev.of(core.EventRecoveryPlan)) != 1 || len(ev.of(core.EventExecutionLost)) != 2 {
		t.Fatalf("events after a second pass = %+v", ev.list)
	}
}

// A loss after a plan was offered for the route gets a plan of its own; the
// executions the first plan named are not named again.
func TestRecoveryPlanForALaterLoss(t *testing.T) {
	w := newWorld(t)
	ctx := testCtx(t)
	_, a := w.up("later", "later-a", "orchestrator", "claude")
	_, b := w.up("later", "later-b", "implementer", "claude")
	ev := &events{}
	r := w.reconciler(ev)
	w.gLose(a)
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	w.gLose(b)
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	plans := ev.of(core.EventRecoveryPlan)
	if len(plans) != 2 || len(plans[0].Executions) != 1 || plans[0].Executions[0] != a.ID || len(plans[1].Executions) != 1 || plans[1].Executions[0] != b.ID {
		t.Fatalf("plans = %+v", plans)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
