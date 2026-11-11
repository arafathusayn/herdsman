package core_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/herdrapp/apptest"
	"github.com/arafathusayn/herdsman/internal/ledger"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

// gate is one world the section 17 gates run in: the fake core of world, or
// a private real core of liveWorld. A gate body is written once and runs
// against both, so the live run checks the same assertions as the fake one.
type gate interface {
	T() *testing.T
	gProvider() *core.Provider
	gRecords() *core.MemRecords
	gProcs() core.ProcessTable
	gCoreID() string
	gOpenApp()
	gApp() *apptest.Fake
	gAppHome() string
	gBindings() *core.FileBindings
	// gLaunch reserves a launch; gUp starts it through registration;
	// gRegister runs the register step of a terminal already started.
	gLaunch(route, agent, role, kind string) core.Launch
	gUp(route, agent, role, kind string) (core.Launch, runtime.Execution)
	gRegister(l core.Launch) (runtime.Execution, error)
	// gReady brings an execution through visibility and readiness.
	gReady(l core.Launch) runtime.Execution
	gReconciler(ev *events) *core.Reconciler
	// gDial is a connection the test acts through; its calls are counted.
	gDial() *hc.Client
	// gCalls is how many requests of method reached core.
	gCalls(method string) int
	gRecord(intent string) runtime.Execution
	// gLose ends the harness of a kept terminal; core starts a shell at the
	// same address. It returns the shell's pid.
	gLose(rec runtime.Execution) int
	// gRestartCore stops and starts the core: every kept execution comes
	// back as a shell.
	gRestartCore()
	// gDescendant is a process below pid, or pid when it has none.
	gDescendant(pid int) int
	// gOutsider is a process outside every execution's tree.
	gOutsider() int
	// gDrop drops every client connection; the processes survive.
	gDrop()
	// gChangeCore makes the socket lead to a core with another identity.
	gChangeCore()
	// gOtherApp is an app core of another core id that holds a terminal at
	// address with labels.
	gOtherApp(address string, labels map[string]string) *apptest.Core
	// gStatus makes the program of rec report state and returns the word
	// core then shows (empty for none). The live fixture prints it as its
	// OSC 7501 status; "blocked" also opens its dialog.
	gStatus(rec runtime.Execution, state string) string
	// gReceived is what the program of rec read from its terminal; ok is
	// false where the world has no program to ask (the fake core).
	gReceived(rec runtime.Execution) (got string, ok bool)
	// gSends are the params of every terminal.send that reached core.
	gSends() []map[string]any
}

// Identity replacement: the harness of a kept terminal dies, core starts a
// shell at the same address with the same labels. The shell inherits
// nothing: no readiness, no caller identity, no stop, no interrupt.
func gateRecoveryShellInheritsNothing(g gate) {
	t := g.T()
	g.gOpenApp()
	ctx := testCtx(t)
	l, _ := g.gUp("omega", "omega-o", "orchestrator", "claude")
	rec := g.gReady(l)
	shell := g.gLose(rec)

	ev := &events{}
	r := g.gReconciler(ev)
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	got := g.gRecord(l.Intent)
	if got.Observation.Process != runtime.ProcessLost || got.Observation.Readiness != runtime.ReadinessIdentityUnknown {
		t.Fatalf("after the loss: %+v", got.Observation)
	}
	if len(ev.of(core.EventExecutionLost)) != 1 || len(ev.of(core.EventRecoveryPlan)) != 1 {
		t.Fatalf("events = %+v", ev.list)
	}
	if _, ok := core.CallerExecution(g.gProcs(), g.gDescendant(shell), []runtime.Execution{got}); ok {
		t.Fatal("a command from the recovery shell passed as the execution")
	}
	if _, err := g.gProvider().Enrollable(ctx, []string{l.Intent}); err == nil {
		t.Fatal("the shell passed the dispatch gate")
	}
	if _, err := g.gProvider().Interrupt(ctx, runtime.Target{ExecutionID: got.ID}); core.CodeOf(err) != core.CodeIdentityNotProven {
		t.Fatalf("interrupt: %v", err)
	}
	stopped, err := g.gProvider().Stop(ctx, runtime.Target{ExecutionID: got.ID})
	if err != nil || !stopped.Already {
		t.Fatalf("stop = %+v %v", stopped, err)
	}
	for _, m := range []string{hc.MethodKill, hc.MethodInterrupt, hc.MethodSend, "terminal.keys"} {
		if n := g.gCalls(m); n != 0 {
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

// Recovery shells: after a core stop and start every kept execution is a
// shell: all are lost, one plan names them all, nothing is typed and no
// shell is killed, by the stop set or by a stop of each execution.
func gateCoreRestartLosesEveryExecutionOnePlan(g gate) []core.Launch {
	t := g.T()
	ctx := testCtx(t)
	var launches []core.Launch
	for _, role := range []string{"adjutant", "orchestrator", "implementer"} {
		l, _ := g.gUp("multi", "multi-"+role, role, "claude")
		launches = append(launches, l)
	}
	g.gRestartCore()
	ev := &events{}
	if err := g.gReconciler(ev).Pass(ctx); err != nil {
		t.Fatal(err)
	}
	for _, l := range launches {
		if p := g.gRecord(l.Intent).Observation.Process; p != runtime.ProcessLost {
			t.Fatalf("%s is %s", l.AgentName, p)
		}
	}
	plans := ev.of(core.EventRecoveryPlan)
	if len(plans) != 1 || len(plans[0].Executions) != 3 {
		t.Fatalf("plans = %+v", plans)
	}
	// The ledger holds each shell as associated; a stop leaves them.
	execs, _ := g.gRecords().List(ctx, "", "")
	live, err := g.gDial().List(ctx, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	inv := core.Classify(core.InventoryInput{Terminals: live.Terminals, Executions: execs, Repo: "/repo", Route: "multi", RouteID: launches[0].RouteID})
	resources := core.Resources(inv, execs, live.Terminals, g.gProcs())
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
	for _, l := range launches {
		stopped, err := g.gProvider().Stop(ctx, runtime.Target{ExecutionID: g.gRecord(l.Intent).ID})
		if err != nil || !stopped.Already {
			t.Fatalf("stop of %s = %+v %v", l.AgentName, stopped, err)
		}
	}
	for _, m := range []string{hc.MethodKill, hc.MethodSend, "terminal.keys", hc.MethodInterrupt} {
		if n := g.gCalls(m); n != 0 {
			t.Fatalf("%s was called %d times", m, n)
		}
	}
	after, err := g.gDial().List(ctx, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Terminals) != len(live.Terminals) {
		t.Fatalf("a shell ended: %d live terminals, then %d", len(live.Terminals), len(after.Terminals))
	}
	return launches
}

// Caller identity: a command run by the harness is its execution's caller;
// the same command from outside the tree is not, nor a reused pid.
func gateCallerIdentityByProcess(g gate) {
	t := g.T()
	_, rec := g.gUp("callers", "callers-o", "orchestrator", "claude")
	execs, _ := g.gRecords().List(context.Background(), "", "")
	cmd := g.gDescendant(rec.Evidence.PID)
	if cmd == rec.Evidence.PID {
		t.Fatalf("no process runs below the harness %d", cmd)
	}
	got, ok := core.CallerExecution(g.gProcs(), cmd, execs)
	if !ok || got.ID != rec.ID {
		t.Fatalf("caller = %+v %v", got, ok)
	}
	if _, ok := core.CallerExecution(g.gProcs(), g.gOutsider(), execs); ok {
		t.Fatal("a process outside the tree passed")
	}
	// A reused pid is another process.
	reused := rec
	reused.Evidence.Start = "1999-01-01T00:00:00Z"
	if _, ok := core.CallerExecution(g.gProcs(), cmd, []runtime.Execution{reused}); ok {
		t.Fatal("a reused pid passed")
	}
}

// Endpoint mismatch: an app on another core cannot verify a terminal with
// the same address, and a route pinned to its core refuses another.
func gateEndpointMismatch(g gate) {
	t := g.T()
	ctx := testCtx(t)
	l, rec := g.gUp("mm", "mm-o", "orchestrator", "claude")
	labels, _ := l.Labels().Map()
	labels[herdrapp.LabelSpace], labels[herdrapp.LabelTab] = "hs-elsewhere-space", "hs-elsewhere-tab"
	g.gApp().Open(g.gOtherApp(rec.TerminalAddress, labels))
	g.gProvider().App = herdrapp.New(g.gApp(), g.gAppHome()) // no mirroring: the window follows the other core
	_, err := g.gProvider().VisibilityStep(ctx, l, core.Timeouts{Visibility: 300 * time.Millisecond, Poll: 20 * time.Millisecond})
	if core.CodeOf(err) != core.CodeWaitingVisibility {
		t.Fatalf("visibility: %v", err)
	}
	if _, ok, _ := g.gBindings().Load(g.gAppHome(), g.gCoreID()); ok {
		t.Fatal("a binding was verified against another core")
	}
	if _, err := core.Preflight(ctx, g.gDial(), core.PreflightInput{Kind: "claude", Argv: []string{"claude"}, ExpectCore: "core-the-route-ran-on"}); core.CodeOf(err) != core.CodeEndpointMismatch {
		t.Fatalf("preflight: %v", err)
	}
}

// Reconnect and events: a dropped connection opens a new epoch and a full
// pass with nothing relaunched; a changed core identity freezes mutations.
func gateReconnectAndIdentityChange(g gate) {
	t := g.T()
	_, rec := g.gUp("rc", "rc-o", "orchestrator", "claude")
	ev := &events{}
	r := g.gReconciler(ev)
	r.Options = hc.Options{IdleTimeout: 2 * time.Second}
	g.gProvider().Reconciler = r
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	waitFor(t, func() bool { return len(ev.of(core.EventResync)) >= 1 })
	dropped := time.Now()
	g.gDrop()
	waitFor(t, func() bool { return len(ev.of(core.EventResync)) >= 2 })
	// The full pass after the reconnect has read the execution again.
	waitFor(t, func() bool { return g.gRecord(rec.LaunchIntent).Observation.ObservedAt.After(dropped) })
	if got := g.gRecord(rec.LaunchIntent); got.Observation.Process != runtime.ProcessLive || got.Evidence != rec.Evidence {
		t.Fatalf("the surviving execution is %s (%+v)", got.Observation.Process, got.Evidence)
	}
	if n := g.gCalls(hc.MethodRun); n != 1 {
		t.Fatalf("a reconnect relaunched: %d runs", n)
	}
	g.gChangeCore()
	select {
	case err := <-done:
		if core.CodeOf(err) != core.CodeFrozen {
			t.Fatalf("run ended with %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the reconciler did not stop on a changed core")
	}
	if frozen, _ := r.Frozen(); !frozen || len(ev.of(core.EventEndpointMismatch)) != 1 {
		t.Fatalf("frozen %v, events %+v", frozen, ev.list)
	}
	if _, err := g.gProvider().Stop(context.Background(), runtime.Target{ExecutionID: rec.ID}); core.CodeOf(err) != core.CodeFrozen {
		t.Fatalf("stop on a frozen endpoint: %v", err)
	}
}

// Launch crash points: whatever was or was not done before the daemon died,
// a retry adopts the one verified terminal, refuses an ambiguous one, and
// never starts a second.
func gateUncertainSpawn(t *testing.T, newGate func(*testing.T) gate) {
	t.Run("crash after the run, reply lost", func(t *testing.T) {
		g := newGate(t)
		ctx := testCtx(t)
		l := g.gLaunch("r2", "r2-o", "orchestrator", "claude")
		if _, err := core.Spawn(ctx, g.gDial(), l); err != nil { // the run whose answer was lost
			t.Fatal(err)
		}
		if v, _, _ := g.gProvider().ProbeSpawn(ctx, l); v != core.ProbeMatches {
			t.Fatalf("probe = %v", v)
		}
		rec, err := g.gProvider().SpawnStep(ctx, l)
		if err != nil || rec.TerminalAddress == "" {
			t.Fatalf("spawn: %+v %v", rec, err)
		}
		if n := g.gCalls(hc.MethodRun); n != 1 {
			t.Fatalf("a second terminal was started: %d runs", n)
		}
		// The adopted terminal's wrapper, which waited since before its
		// authentication, registers as that terminal's program.
		rec, err = g.gRegister(l)
		if err != nil {
			t.Fatalf("register the adopted launch: %v", err)
		}
		term, err := g.gDial().Get(ctx, rec.TerminalAddress)
		if err != nil || term.PID == nil || int(*term.PID) != rec.Evidence.PID {
			t.Fatalf("registered %d, terminal %+v (%v)", rec.Evidence.PID, term.PID, err)
		}
		if n := g.gCalls(hc.MethodRun); n != 1 {
			t.Fatalf("runs = %d", n)
		}
	})
	t.Run("two matches", func(t *testing.T) {
		g := newGate(t)
		ctx := testCtx(t)
		l := g.gLaunch("r3", "r3-o", "orchestrator", "claude")
		c := g.gDial()
		labels, _ := l.Labels().Map()
		for range 2 {
			if _, err := c.Run(ctx, hc.RunRequest{Argv: []string{"sh"}, Labels: labels, Keep: true}); err != nil {
				t.Fatal(err)
			}
		}
		if v, d, _ := g.gProvider().ProbeSpawn(ctx, l); v != core.ProbeDifferent {
			t.Fatalf("probe = %v %s", v, d)
		}
		if _, err := g.gProvider().SpawnStep(ctx, l); core.CodeOf(err) != core.CodeConflict {
			t.Fatalf("spawn: %v", err)
		}
		if n := g.gCalls(hc.MethodRun); n != 2 {
			t.Fatalf("runs = %d", n)
		}
	})
	t.Run("copied labels", func(t *testing.T) {
		g := newGate(t)
		ctx := testCtx(t)
		l := g.gLaunch("r4", "r4-o", "orchestrator", "claude")
		if _, err := g.gDial().Run(ctx, hc.RunRequest{Argv: []string{"sh"}, Labels: map[string]string{herdrapp.LabelExecution: l.Execution, herdrapp.LabelRoute: "someone-else"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := g.gProvider().SpawnStep(ctx, l); core.CodeOf(err) != core.CodeConflict {
			t.Fatalf("spawn: %v", err)
		}
		if n := g.gCalls(hc.MethodRun); n != 1 {
			t.Fatalf("runs = %d", n)
		}
	})
	t.Run("registered, then another process at the address", func(t *testing.T) {
		g := newGate(t)
		ctx := testCtx(t)
		l, rec := g.gUp("r6", "r6-o", "orchestrator", "claude")
		g.gLose(rec)
		if v, d, _ := g.gProvider().ProbeSpawn(ctx, l); v != core.ProbeDifferent || !strings.Contains(d, "recovery shell") {
			t.Fatalf("probe = %v %s", v, d)
		}
		if v, _, _ := g.gProvider().ProbeRegister(ctx, l); v != core.ProbeDifferent {
			t.Fatalf("probe register = %v", v)
		}
		if _, err := g.gProvider().SpawnStep(ctx, l); core.CodeOf(err) != core.CodeConflict {
			t.Fatalf("spawn over the shell: %v", err)
		}
		if n := g.gCalls(hc.MethodRun); n != 1 {
			t.Fatalf("runs = %d", n)
		}
	})
}

// The fake world is a gate.

func (w *world) T() *testing.T                           { return w.t }
func (w *world) gProvider() *core.Provider               { return w.p }
func (w *world) gRecords() *core.MemRecords              { return w.records }
func (w *world) gProcs() core.ProcessTable               { return w.procs }
func (w *world) gCoreID() string                         { return w.core.ID }
func (w *world) gOpenApp()                               { w.openApp() }
func (w *world) gApp() *apptest.Fake                     { return w.app }
func (w *world) gAppHome() string                        { return w.appHome }
func (w *world) gBindings() *core.FileBindings           { return w.bindings }
func (w *world) gDial() *hc.Client                       { return w.dial() }
func (w *world) gCalls(method string) int                { return w.core.Calls(method) }
func (w *world) gRecord(intent string) runtime.Execution { return w.record(intent) }
func (w *world) gReady(l core.Launch) runtime.Execution  { return w.ready(l) }
func (w *world) gReconciler(ev *events) *core.Reconciler { return w.reconciler(ev) }
func (w *world) gOutsider() int                          { return w.procs.Add(1, "herdsman", "message", "send") }
func (w *world) gDrop()                                  { w.core.Restart() }

func (w *world) gLaunch(route, agent, role, kind string) core.Launch {
	return w.launchOf(route, agent, role, kind)
}

func (w *world) gUp(route, agent, role, kind string) (core.Launch, runtime.Execution) {
	return w.up(route, agent, role, kind)
}

func (w *world) gRegister(l core.Launch) (runtime.Execution, error) {
	address := w.record(l.Intent).TerminalAddress
	if _, err := w.procs.Identify(w.pidOf(address)); err != nil { // the fake core's own pid
		w.setProcess(address, w.procs.Add(w.holder, "herdsman", "launch"))
	}
	rec, run, err := w.register(l, address, nil, nil)
	if err == nil && run.code != 0 {
		w.t.Fatalf("wrapper exited %d", run.code)
	}
	return rec, err
}

func (w *world) gLose(rec runtime.Execution) int {
	w.procs.Kill(rec.Evidence.PID)
	shell := w.procs.Add(w.holder, "-zsh")
	w.setProcess(rec.TerminalAddress, shell)
	// A real core's shell starts silent: the harness's status is not
	// carried to it (TestLiveInputRace records core's status_mismatch).
	w.core.Mutate(rec.TerminalAddress, func(t map[string]any) { t["status"] = nil })
	return shell
}

func (w *world) gRestartCore() {
	w.core.Restart()
	holder := w.procs.Add(1, "hc", "hold")
	execs, _ := w.records.List(context.Background(), "", "")
	for _, e := range execs {
		if e.Evidence.PID != 0 {
			w.procs.Kill(e.Evidence.PID)
			w.setProcess(e.TerminalAddress, w.procs.Add(holder, "-zsh"))
		}
	}
}

func (w *world) gDescendant(pid int) int {
	tool := w.procs.Add(pid, "/bin/zsh", "-c", "herdsman message send")
	return w.procs.Add(tool, "herdsman", "message", "send")
}

func (w *world) gChangeCore() {
	w.core.Wipe("a-wiped-core-0000000000000000001")
	w.core.Restart()
}

func (w *world) gOtherApp(address string, labels map[string]string) *apptest.Core {
	other := apptest.NewCore("other-core")
	other.Run(address, "same-name", labels)
	return other
}

func (w *world) gStatus(rec runtime.Execution, state string) string {
	w.core.SetStatus(rec.TerminalAddress, state)
	return state
}

func (w *world) gReceived(runtime.Execution) (string, bool) { return "", false }

func (w *world) gSends() []map[string]any {
	var out []map[string]any
	for _, r := range w.core.Requests() {
		if r.Method == hc.MethodSend {
			out = append(out, r.Params)
		}
	}
	return out
}

var _ gate = (*world)(nil)
