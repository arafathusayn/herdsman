package core_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/herdrapp/apptest"
	"github.com/arafathusayn/herdsman/internal/ledger"
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
		HarnessNames: []string{"claude", "codex"}, Emit: ev.add, Safety: time.Hour}
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

// Identity replacement: the harness of a kept terminal dies, core starts a
// shell at the same address with the same labels. The shell inherits
// nothing: no readiness, no caller identity, no stop, no interrupt.
func TestRecoveryShellInheritsNothing(t *testing.T) {
	w := newWorld(t)
	w.openApp()
	ctx := testCtx(t)
	l, rec := w.up("omega", "omega-o", "orchestrator", "claude")
	rec = w.ready(l)
	harness := rec.Evidence.PID
	w.procs.Kill(harness)
	shell := w.procs.Add(w.holder, "-zsh")
	w.setProcess(rec.TerminalAddress, shell)

	ev := &events{}
	r := w.reconciler(ev)
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	got := w.record(l.Intent)
	if got.Observation.Process != runtime.ProcessLost || got.Observation.Readiness != runtime.ReadinessIdentityUnknown {
		t.Fatalf("after the loss: %+v", got.Observation)
	}
	if len(ev.of(core.EventExecutionLost)) != 1 || len(ev.of(core.EventRecoveryPlan)) != 1 {
		t.Fatalf("events = %+v", ev.list)
	}
	child := w.procs.Add(shell, "herdsman", "message", "send")
	if _, ok := core.CallerExecution(w.procs, child, []runtime.Execution{got}); ok {
		t.Fatal("a command from the recovery shell passed as the execution")
	}
	if _, err := w.p.Enrollable(ctx, []string{l.Intent}); err == nil {
		t.Fatal("the shell passed the dispatch gate")
	}
	if _, err := w.p.Interrupt(ctx, runtime.Target{ExecutionID: got.ID}); core.CodeOf(err) != core.CodeIdentityNotProven {
		t.Fatalf("interrupt: %v", err)
	}
	stopped, err := w.p.Stop(ctx, runtime.Target{ExecutionID: got.ID})
	if err != nil || !stopped.Already {
		t.Fatalf("stop = %+v %v", stopped, err)
	}
	for _, m := range []string{hc.MethodKill, hc.MethodInterrupt, hc.MethodSend, "terminal.keys"} {
		if n := w.core.Calls(m); n != 0 {
			t.Fatalf("%s reached the shell %d times", m, n)
		}
	}
	// A second pass offers no second plan.
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if len(ev.of(core.EventRecoveryPlan)) != 1 || len(ev.of(core.EventExecutionLost)) != 1 {
		t.Fatalf("events after a second pass = %+v", ev.list)
	}
}

// After a core stop and start every kept execution is a shell: all are lost,
// one plan names them all, nothing is typed and no shell is killed.
func TestCoreRestartLosesEveryExecutionOnePlan(t *testing.T) {
	w := newWorld(t)
	ctx := testCtx(t)
	var launches []core.Launch
	for _, role := range []string{"adjutant", "orchestrator", "implementer"} {
		l, _ := w.up("multi", "multi-"+role, role, "claude")
		launches = append(launches, l)
	}
	w.core.Restart()
	newHolder := w.procs.Add(1, "hc", "hold")
	for _, l := range launches {
		rec := w.record(l.Intent)
		w.procs.Kill(rec.Evidence.PID)
		w.setProcess(rec.TerminalAddress, w.procs.Add(newHolder, "-zsh"))
	}
	ev := &events{}
	if err := w.reconciler(ev).Pass(ctx); err != nil {
		t.Fatal(err)
	}
	for _, l := range launches {
		if p := w.record(l.Intent).Observation.Process; p != runtime.ProcessLost {
			t.Fatalf("%s is %s", l.AgentName, p)
		}
	}
	plans := ev.of(core.EventRecoveryPlan)
	if len(plans) != 1 || len(plans[0].Executions) != 3 {
		t.Fatalf("plans = %+v", plans)
	}
	for _, m := range []string{hc.MethodKill, hc.MethodSend, "terminal.keys", hc.MethodInterrupt} {
		if n := w.core.Calls(m); n != 0 {
			t.Fatalf("%s was called %d times", m, n)
		}
	}
	// The ledger holds each shell as associated; a stop leaves them.
	execs, _ := w.records.List(ctx, "", "")
	live, err := w.dial().List(ctx, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	inv := core.Classify(core.InventoryInput{Terminals: live.Terminals, Executions: execs, Repo: "/repo", Route: "multi", RouteID: launches[0].RouteID})
	resources := core.Resources(inv, execs, live.Terminals, w.procs)
	act, leave := core.StopSet(resources, nil)
	if len(act) != 0 || len(leave) != 3 {
		t.Fatalf("stop set: act %v leave %v", act, leave)
	}
	for _, r := range leave {
		if r.Kind != core.ResourceShell || r.Level() != ledger.LevelAssociated || !strings.Contains(r.String(), "lost execution") {
			t.Fatalf("shell resource = %s", r)
		}
	}
	if act, _ := core.StopSet(resources, map[string]bool{leave[0].Address: true}); len(act) != 1 {
		t.Fatalf("a shell a person named is not acted on: %v", act)
	}
}

// A command run by the harness is its execution's caller; the same command
// from outside the tree is not.
func TestCallerIdentityByProcess(t *testing.T) {
	w := newWorld(t)
	_, rec := w.up("callers", "callers-o", "orchestrator", "claude")
	execs, _ := w.records.List(context.Background(), "", "")
	tool := w.procs.Add(rec.Evidence.PID, "/bin/zsh", "-c", "herdsman message send")
	cmd := w.procs.Add(tool, "herdsman", "message", "send")
	got, ok := core.CallerExecution(w.procs, cmd, execs)
	if !ok || got.ID != rec.ID {
		t.Fatalf("caller = %+v %v", got, ok)
	}
	outside := w.procs.Add(1, "herdsman", "message", "send")
	if _, ok := core.CallerExecution(w.procs, outside, execs); ok {
		t.Fatal("a process outside the tree passed")
	}
	// A reused pid is another process.
	reused := rec
	reused.Evidence.Start = "1999-01-01T00:00:00Z"
	if _, ok := core.CallerExecution(w.procs, cmd, []runtime.Execution{reused}); ok {
		t.Fatal("a reused pid passed")
	}
}

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

// An app on another core cannot verify a terminal with the same address.
func TestEndpointMismatchEndToEnd(t *testing.T) {
	w := newWorld(t)
	ctx := testCtx(t)
	l, rec := w.up("mm", "mm-o", "orchestrator", "claude")
	other := apptest.NewCore("other-core")
	labels, _ := l.Labels().Map()
	labels[herdrapp.LabelSpace], labels[herdrapp.LabelTab] = "hs-elsewhere-space", "hs-elsewhere-tab"
	other.Run(rec.TerminalAddress, "same-name", labels)
	w.app.Open(other)
	w.p.App = herdrapp.New(w.app, w.appHome) // no mirroring: the window follows the other core
	_, err := w.p.VisibilityStep(ctx, l, core.Timeouts{Visibility: 300 * time.Millisecond, Poll: 20 * time.Millisecond})
	if core.CodeOf(err) != core.CodeWaitingVisibility {
		t.Fatalf("visibility: %v", err)
	}
	if _, ok, _ := w.bindings.Load(w.appHome, w.core.ID); ok {
		t.Fatal("a binding was verified against another core")
	}
	// A route pinned to its core refuses a socket that leads elsewhere.
	if _, err := core.Preflight(ctx, w.dial(), core.PreflightInput{Kind: "claude", Argv: []string{"claude"}, ExpectCore: "core-the-route-ran-on"}); core.CodeOf(err) != core.CodeEndpointMismatch {
		t.Fatalf("preflight: %v", err)
	}
}

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

// Reconnect and events: a core restart opens a new epoch and a full pass; a
// changed core identity freezes mutations.
func TestReconnectAndIdentityChange(t *testing.T) {
	w := newWorld(t)
	_, rec := w.up("rc", "rc-o", "orchestrator", "claude")
	ev := &events{}
	r := w.reconciler(ev)
	r.Options = hc.Options{IdleTimeout: 2 * time.Second}
	w.p.Reconciler = r
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	waitFor(t, func() bool { return len(ev.of(core.EventResync)) >= 1 })
	w.core.Restart()
	waitFor(t, func() bool { return len(ev.of(core.EventResync)) >= 2 })
	if got := w.record(rec.LaunchIntent); got.Observation.Process != runtime.ProcessLive {
		t.Fatalf("the surviving execution is %s", got.Observation.Process)
	}
	if n := w.core.Calls(hc.MethodRun); n != 1 {
		t.Fatalf("a reconnect relaunched: %d runs", n)
	}
	w.core.Wipe("a-wiped-core-0000000000000000001")
	w.core.Restart()
	select {
	case err := <-done:
		if core.CodeOf(err) != core.CodeFrozen {
			t.Fatalf("run ended with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the reconciler did not stop on a changed core")
	}
	if frozen, _ := r.Frozen(); !frozen || len(ev.of(core.EventEndpointMismatch)) != 1 {
		t.Fatalf("frozen %v, events %+v", frozen, ev.list)
	}
	if _, err := w.p.Stop(context.Background(), runtime.Target{ExecutionID: rec.ID}); core.CodeOf(err) != core.CodeFrozen {
		t.Fatalf("stop on a frozen endpoint: %v", err)
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
