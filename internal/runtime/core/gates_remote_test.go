package core_test

import (
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/herdrapp/apptest"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

// remoteGate is a gate whose core has another machine added.
type remoteGate interface {
	gate
	// gRemote starts a live terminal carrying labels on the core's other
	// machine and returns its address as the selected core shows it. On
	// one computer its pid is a live process of this machine's table.
	gRemote(labels map[string]string) string
}

// Remote records (spec sections 6, 7 and 11). A terminal on another machine
// of the selected core can carry a launch's placement and ownership labels,
// and, where the machine is this computer, a pid that is a live local
// process. It is never a local member: no launch adopts it, no identity is
// proven by it, no visibility is established for it, and the inventory
// classifies it as an impostor when it carries the route's labels.
func gateRemoteRecordNeverLocal(g remoteGate) {
	t := g.T()
	ctx := testCtx(t)
	g.gOpenApp()
	// A ready member: the binding is verified, so a closed window would
	// make any terminal whose labels read back pending.
	ml, _ := g.gUp("faraway", "faraway-o", "orchestrator", "claude")
	member := g.gReady(ml)
	runs := g.gCalls(hc.MethodRun)

	// Spawn: a remote record with every label of a reserved launch is not
	// that launch's terminal, and nothing is started over it.
	l := g.gLaunch("faraway", "faraway-i", "implementer", "claude")
	labels, err := l.Labels().Map()
	if err != nil {
		t.Fatal(err)
	}
	remote := g.gRemote(labels)
	t.Logf("a remote record with the launch's labels: %s", remote)
	if v, d, _ := g.gProvider().ProbeSpawn(ctx, l); v != core.ProbeDifferent {
		t.Fatalf("probe spawn = %v %s", v, d)
	}
	if rec, err := g.gProvider().SpawnStep(ctx, l); core.CodeOf(err) != core.CodeConflict || rec.TerminalAddress != "" {
		t.Fatalf("spawn over a remote record: %+v %v", rec.TerminalAddress, err)
	}
	if n := g.gCalls(hc.MethodRun); n != runs {
		t.Fatalf("runs = %d, want %d", n, runs)
	}

	// Identity: a record naming the remote address with that record's own
	// live process is not proven.
	term, err := g.gDial().Get(ctx, remote)
	if err != nil || term.PID == nil {
		t.Fatalf("get %s = %+v %v", remote, term, err)
	}
	ev, err := core.Evidence(g.gProcs(), int(*term.PID))
	if err != nil {
		t.Fatalf("the remote record's pid %d is no process here: %v", *term.PID, err)
	}
	forged := member
	forged.ID, forged.LaunchIntent, forged.TerminalAddress, forged.Evidence = runtime.NewExecutionID(), "forged", remote, ev
	if _, ok, err := core.Proven(ctx, g.gDial(), g.gProcs(), forged); ok || err != nil {
		t.Fatalf("a remote record proved execution identity (%v)", err)
	}

	// Visibility: labels that read back from a remote record establish no
	// state that allows dispatch, with the window open, with it quit while
	// the binding stays verified (which would be pending), and with no app.
	for _, step := range []string{"open", "quit", "no app"} {
		app := g.gProvider().App
		switch step {
		case "quit":
			g.gApp().Quit()
		case "no app":
			app = nil
		}
		res, err := core.EstablishVisibility(ctx, g.gDial(), core.VisibilityInput{
			App: app, Bindings: g.gBindings(), Core: herdrapp.Core{ID: g.gCoreID(), Endpoint: g.gProvider().Socket}, Launch: l, Address: remote,
			Timeout: 300 * time.Millisecond, Poll: 20 * time.Millisecond,
		})
		if err == nil || res.State.DispatchAllowed() || res.Owned != nil {
			t.Fatalf("visibility of a remote record (%s): %+v %v", step, res, err)
		}
	}
	g.gOpenApp()

	// Inventory: remote records carrying the route's labels, one a copy of
	// the member's own and one that names the member's execution with
	// another route, are impostors; none is a member, and the member stays.
	mt, err := g.gDial().Get(ctx, member.TerminalAddress)
	if err != nil {
		t.Fatal(err)
	}
	copied := g.gRemote(mt.Labels)
	hijack := g.gRemote(map[string]string{herdrapp.LabelExecution: member.ID, herdrapp.LabelRoute: "someone-else"})
	plain := g.gRemote(nil)
	live, err := g.gDial().List(ctx, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	execs, _ := g.gRecords().List(ctx, "", "")
	procs, _ := g.gProcs().List(ctx)
	want := map[string]core.Class{member.TerminalAddress: core.ClassMember, remote: core.ClassImpostor, copied: core.ClassImpostor, plain: core.ClassPlain}
	check := func(what string, inv core.Inventory) {
		t.Helper()
		got := map[string]core.Entry{}
		for _, e := range inv.Entries {
			got[e.Address] = e
		}
		for addr, class := range want {
			if got[addr].Class != class || got[addr].Fault != (class == core.ClassImpostor) {
				t.Fatalf("%s: %s is %+v, want %s\n%s", what, addr, got[addr], class, inv.FaultText())
			}
		}
		if e := got[hijack]; e.Class == core.ClassMember || e.Class == core.ClassOtherRouteMember || e.Class == core.ClassRecoveryShell {
			t.Fatalf("%s: %s is %s", what, hijack, e.Class)
		}
	}
	in := core.InventoryInput{Terminals: live.Terminals, Executions: execs, Repo: "/repo", Route: "faraway", RouteID: ml.RouteID,
		Procs: procs, HarnessNames: []string{"claude", "codex"}}
	check("classify", core.Classify(in))
	// A record that names the remote address makes it no member either.
	in.Executions = append(execs, forged)
	check("classify with a record of the remote address", core.Classify(in))

	// The reconciler finds the route's id on the member's own terminal, not
	// on a remote copy, and keeps the member live.
	evs := &events{}
	r := g.gReconciler(evs)
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	inv, ok := r.RouteInventory("/repo", "faraway")
	if !ok {
		t.Fatal("the reconciler holds no inventory of the route")
	}
	check("the reconciler's inventory", inv)
	if got := g.gRecord(ml.Intent); got.Observation.Process != runtime.ProcessLive || got.TerminalAddress != member.TerminalAddress {
		t.Fatalf("the member after the pass: %s at %s", got.Observation.Process, got.TerminalAddress)
	}
	if faults := evs.of(core.EventInventoryFault); len(faults) != 1 {
		t.Fatalf("inventory faults = %+v", faults)
	}
	for _, m := range []string{hc.MethodKill, hc.MethodSend, hc.MethodInterrupt, "terminal.keys"} {
		if n := g.gCalls(m); n != 0 {
			t.Fatalf("%s was called %d times", m, n)
		}
	}
}

// Remote records against the fake core.
func TestRemoteRecordNeverLocal(t *testing.T) { gateRemoteRecordNeverLocal(newWorld(t)) }

// gRemote adds a terminal on the fake core's machine "remote", whose pid is
// a live process of the fake table, as on one computer; the window shows
// that machine's terminals under it.
func (w *world) gRemote(labels map[string]string) string {
	if w.appRemote == nil {
		w.appRemote = apptest.NewCore("remote-core")
		w.app.AddMachine("remote", w.appRemote)
	}
	return w.core.AddRemote("remote", labels, uint32(w.procs.Add(1, "/bin/sleep", "300")))
}

var _ remoteGate = (*world)(nil)
