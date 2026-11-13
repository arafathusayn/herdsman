package core_test

import (
	"context"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	"github.com/arafathusayn/herdsman/internal/runtime/core/coretest"
)

// The section 17 gates of delivery: "Input race", "State guards" and "`if` /
// first status", written once against the gate interface (gates_test.go) and
// run against the fake core and a private real core.

// submitServices are a notice service and a typed-submit service over the
// gate's records, reaching core through the gate's counted connection.
func submitServices(g gate) (*notice.Service, *core.Submitter, *coretest.MemSubmits) {
	t := g.T()
	n := notice.New(notice.NewMemStore(), coretest.Executions{Records: g.gRecords()}, notice.Config{Poll: 100 * time.Millisecond})
	t.Cleanup(n.Close)
	store := &coretest.MemSubmits{}
	s := &core.Submitter{Store: store, Records: g.gRecords(), Notices: n, Procs: g.gProcs(),
		Dial: func(context.Context) (core.Sender, func(), error) { return g.gDial(), func() {}, nil }}
	return n, s, store
}

// racing is a Sender that lets the world change between the submit's own
// checks and core's: before runs just before the send goes to core.
type racing struct {
	core.Sender
	before func()
}

func (r racing) Send(ctx context.Context, terminal, text, ifStatus string) (hc.SendResult, error) {
	r.before()
	return r.Sender.Send(ctx, terminal, text, ifStatus)
}

// coreWord is the status word core shows for rec now, empty for none.
func coreWord(g gate, rec runtime.Execution) string {
	g.T().Helper()
	term, err := g.gDial().Get(testCtx(g.T()), rec.TerminalAddress)
	if err != nil {
		g.T().Fatal(err)
	}
	if term.Status == nil {
		return ""
	}
	return term.Status.State
}

// nothingTyped fails unless exactly sends terminal.send calls (a person's
// typed submits) and no terminal.keys reached core.
func nothingTyped(g gate, sends int) {
	g.T().Helper()
	if n := g.gCalls(hc.MethodSend); n != sends {
		g.T().Fatalf("terminal.send reached core %d times, want %d", n, sends)
	}
	if n := g.gCalls("terminal.keys"); n != 0 {
		g.T().Fatalf("terminal.keys reached core %d times", n)
	}
}

// readOnce waits for the program of rec to read text and fails unless it
// read it exactly once.
func readOnce(g gate, rec runtime.Execution, text string) {
	t := g.T()
	t.Helper()
	if _, ok := g.gReceived(rec); !ok {
		return
	}
	waitFor(t, func() bool { in, _ := g.gReceived(rec); return strings.Contains(in, text) })
	time.Sleep(time.Second) // the Enter follows the paste by about 200 ms
	if in, _ := g.gReceived(rec); strings.Count(in, text) != 1 {
		t.Fatalf("the program read %q", in)
	}
}

// State guards, `if` and first status: a typed submit needs a first status,
// the state the adapter permits, and is sent with `if` and force:false. No
// other state the program reports (working, blocked, error, a word such as
// starting that no adapter knows, and done, which `if` cannot express) gets
// past the submit's own check, and an execution with no status is handed no
// notice.
func gateTypedSubmitStateGuards(g gate) {
	t := g.T()
	ctx := testCtx(t)
	n, s, _ := submitServices(g)
	g.gOpenApp()
	l, e := g.gUp("sub", "sub-o", "orchestrator", "claude")
	if err := n.Approve(g.gRecord(l.Intent), ""); err != nil {
		t.Fatal(err)
	}

	// No status since registration (unknown): refused before anything is
	// recorded, and no notice is issued to it.
	if w := coreWord(g, e); w != "" {
		t.Fatalf("core shows %q before the program reported", w)
	}
	if _, err := s.Submit(ctx, core.SubmitRequest{Execution: e.ID, Text: "go on"}); core.CodeOf(err) != core.CodeSubmitNoStatus {
		t.Fatalf("before the first status: %v", err)
	}
	if _, err := n.Issue(ctx, notice.IssueRequest{Key: "brief:sub-o", Kind: notice.KindBrief, Execution: e.ID, Content: "work", Issuer: "up-1"}); notice.GateOf(err) != notice.GateIdentity {
		t.Fatalf("a notice to an execution with no status: %v", err)
	}
	e = g.gReady(l)
	if err := n.Approve(e, ""); err != nil {
		t.Fatal(err)
	}

	// A multi-line text is refused: a typed submit is one line.
	if _, err := s.Submit(ctx, core.SubmitRequest{Execution: e.ID, Text: "a\nb"}); core.CodeOf(err) != core.CodeSubmitGate {
		t.Fatalf("two lines: %v", err)
	}

	// Every state but the adapter's: nothing is typed.
	for _, state := range []string{"working", "blocked", "error", "starting", "done"} {
		word := g.gStatus(e, state)
		_, err := s.Submit(ctx, core.SubmitRequest{Execution: e.ID, Text: "go on"})
		t.Logf("the program reports %s; core shows %q; the submit: %v", state, word, err)
		if core.CodeOf(err) != core.CodeSubmitState {
			t.Fatalf("%s: %v", state, err)
		}
	}
	nothingTyped(g, 0)

	if w := g.gStatus(e, "idle"); w != "idle" {
		t.Fatalf("core shows %q after idle", w)
	}
	rec, err := s.Submit(ctx, core.SubmitRequest{Execution: e.ID, Text: "go on"})
	if err != nil || rec.State != core.SubmitTyped {
		t.Fatalf("idle: %+v %v", rec, err)
	}
	got := g.gSends()
	if len(got) != 1 || got[0]["if"] != core.SubmitIf || got[0]["force"] != false || got[0]["text"] != "go on" {
		t.Fatalf("send params: %+v", got)
	}
	if rec.TextDigest == "" || rec.TextDigest == "go on" {
		t.Fatalf("the text is stored: %+v", rec)
	}
	readOnce(g, e, "go on")

	// A held route refuses through the dispatch gates; so does a stopping
	// one, with its own code.
	if err := n.Hold(e.Repo, e.Route, true, "pause-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit(ctx, core.SubmitRequest{Execution: e.ID, Text: "go on"}); core.CodeOf(err) != core.CodeSubmitGate {
		t.Fatalf("held: %v", err)
	}
	if err := n.Hold(e.Repo, e.Route, false, ""); err != nil {
		t.Fatal(err)
	}
	if err := n.Stopping(e.Repo, e.Route, "stop-1", time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit(ctx, core.SubmitRequest{Execution: e.ID, Text: "go on"}); core.CodeOf(err) != core.CodeRouteStopping {
		t.Fatalf("stopping: %v", err)
	}
	nothingTyped(g, 1)

	// An execution outside an approved pair is refused.
	if err := n.Restart(e.Repo, e.Route); err != nil {
		t.Fatal(err)
	}
	if err := n.Withdraw(e.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit(ctx, core.SubmitRequest{Execution: e.ID, Text: "go on"}); core.CodeOf(err) != core.CodeSubmitGate {
		t.Fatalf("not approved: %v", err)
	}
	nothingTyped(g, 1)
}

// State guards for a first status: a program whose first report is a dialog
// or an error needs a person, and is neither handed a notice nor typed into;
// done (a harness at rest) may be handed a notice it pulls, never a typed
// line; and a word no adapter knows is not idle.
func gateFirstStatusGuards(g gate) {
	t := g.T()
	ctx := testCtx(t)
	n, s, _ := submitServices(g)
	g.gOpenApp()
	for _, state := range []string{"blocked", "error", "starting", "done"} {
		l, e := g.gUp("fs", "fs-"+state, "orchestrator", "claude")
		if _, err := g.gProvider().VisibilityStep(ctx, l, core.Timeouts{Visibility: 5 * time.Second, Poll: 20 * time.Millisecond}); err != nil {
			t.Fatalf("visibility: %v", err)
		}
		word := g.gStatus(e, state)
		e, err := g.gProvider().ReadyStep(ctx, l, core.Timeouts{Ready: 2 * time.Second, Poll: 20 * time.Millisecond})
		t.Logf("first status %s: core shows %q; readiness %s, program %s (%v)", state, word, e.Observation.Readiness, e.Observation.Program, err)
		if err := n.Approve(e, ""); err != nil {
			t.Fatal(err)
		}
		_, serr := s.Submit(ctx, core.SubmitRequest{Execution: e.ID, Text: "go on"})
		_, ierr := n.Issue(ctx, notice.IssueRequest{Key: "brief:" + e.ID, Kind: notice.KindBrief, Execution: e.ID, Content: "work", Issuer: "up-1"})
		if state == "blocked" || state == "error" || word == "" {
			// A dialog, an error, or a word core dropped (no status at
			// all): a person is needed.
			if core.CodeOf(err) != core.CodeNeedsAttention || e.Observation.Readiness != runtime.ReadinessNeedsAttention {
				t.Fatalf("%s: ready step %v, readiness %s", state, err, e.Observation.Readiness)
			}
			if core.CodeOf(serr) != core.CodeSubmitNoStatus {
				t.Fatalf("%s: submit %v", state, serr)
			}
			if notice.GateOf(ierr) != notice.GateIdentity {
				t.Fatalf("%s: issue %v", state, ierr)
			}
			continue
		}
		// done, or a word core keeps that no adapter knows (unknown): a
		// status was reported, so a notice may wait for the agent to pull
		// it; a typed line may not go out.
		if err != nil || e.Observation.Readiness != runtime.ReadinessReady {
			t.Fatalf("%s: ready step %v, readiness %s", state, err, e.Observation.Readiness)
		}
		want := runtime.ProgramOf(word)
		if state == "starting" && want != runtime.ProgramUnknown {
			t.Fatalf("starting maps to %s", want)
		}
		if e.Observation.Program != want {
			t.Fatalf("%s: program %s, want %s", state, e.Observation.Program, want)
		}
		if core.CodeOf(serr) != core.CodeSubmitState {
			t.Fatalf("%s: submit %v", state, serr)
		}
		if ierr != nil {
			t.Fatalf("%s: issue %v", state, ierr)
		}
	}
	nothingTyped(g, 0)
}

// Input race: the full notice path (issue, the agent's wake, its claim)
// types nothing into a program that turns into a dialog or is replaced. A
// person's typed submit whose program turns into a dialog between the
// submit's check and core's is refused by core with nothing typed; one to a
// replaced program is refused by identity before any send; and one whose
// program is replaced between the check and the send is refused by core's
// `if`, the shell core starts at the address having reported nothing.
func gateInputRace(g gate) {
	t := g.T()
	ctx := testCtx(t)
	n, s, _ := submitServices(g)
	g.gOpenApp()
	empty := func(e runtime.Execution) {
		t.Helper()
		if in, ok := g.gReceived(e); ok && in != "" {
			t.Fatalf("the program read %q", in)
		}
	}

	// The program that turns into a dialog: the notice path.
	l, _ := g.gUp("race", "race-o", "orchestrator", "claude")
	e := g.gReady(l)
	if err := n.Approve(e, ""); err != nil {
		t.Fatal(err)
	}
	issued, err := n.Issue(ctx, notice.IssueRequest{Key: "brief:race-o", Kind: notice.KindBrief, Execution: e.ID, Content: "race work", Issuer: "up-1"})
	if err != nil {
		t.Fatal(err)
	}
	if a, err := n.Wait(ctx, e, notice.WaitRequest{WaitID: "w-race"}); err != nil || a.Answer != notice.AnswerNotice || a.Notice.ID != issued.ID {
		t.Fatalf("wake: %+v %v", a, err)
	}
	g.gStatus(e, "blocked") // the dialog opens while the agent holds its wake
	if got, err := n.Read(ctx, e, issued.ID); err != nil || got.Content != "race work" {
		t.Fatalf("claim: %+v %v", got, err)
	}
	if err := g.gReconciler(&events{}).Pass(ctx); err != nil {
		t.Fatal(err)
	}
	nothingTyped(g, 0)
	empty(e)

	// A person's typed submit: the check sees idle; the dialog opens
	// before core checks `if`.
	g.gStatus(e, "idle")
	plain := s.Dial
	raced := func(before func()) func(context.Context) (core.Sender, func(), error) {
		return func(ctx context.Context) (core.Sender, func(), error) {
			c, done, err := plain(ctx)
			return racing{Sender: c, before: before}, done, err
		}
	}
	s.Dial = raced(func() { g.gStatus(e, "blocked") })
	rec, err := s.Submit(ctx, core.SubmitRequest{Execution: e.ID, Text: "hs-race-line"})
	t.Logf("a dialog between the check and the send: %s; core answered: %s", rec.State, rec.Detail)
	if err != nil || rec.State != core.SubmitRejectedBeforeWrite {
		t.Fatalf("race: %+v %v", rec, err)
	}
	if got := g.gSends(); len(got) != 1 || got[0]["if"] != core.SubmitIf || got[0]["force"] != false {
		t.Fatalf("the refused send: %+v", got)
	}
	time.Sleep(500 * time.Millisecond)
	empty(e)
	s.Dial = plain

	// The program lost and replaced by core's shell: the notice path, and
	// a submit to the replaced address.
	l2, _ := g.gUp("race", "race-i", "implementer", "claude")
	e2 := g.gReady(l2)
	if err := n.Approve(e2, ""); err != nil {
		t.Fatal(err)
	}
	second, err := n.Issue(ctx, notice.IssueRequest{Key: "brief:race-i", Kind: notice.KindBrief, Execution: e2.ID, Content: "lost work", Issuer: "up-1"})
	if err != nil {
		t.Fatal(err)
	}
	g.gLose(e2)
	if _, err := s.Submit(ctx, core.SubmitRequest{Execution: e2.ID, Text: "hs-race-line"}); core.CodeOf(err) != core.CodeIdentityNotProven {
		t.Fatalf("a submit to the replaced address: %v", err)
	}
	if err := g.gReconciler(&events{}).Pass(ctx); err != nil {
		t.Fatal(err)
	}
	lost := g.gRecord(l2.Intent)
	if a, err := n.Wait(ctx, lost, notice.WaitRequest{WaitID: "w-lost"}); err != nil || a.Answer == notice.AnswerNotice {
		t.Fatalf("a replaced program was woken: %+v %v", a, err)
	}
	if got, err := n.Read(ctx, lost, second.ID); err != nil || got.Refused != notice.GateIdentity || got.Token != "" {
		t.Fatalf("a replaced program claimed: %+v %v", got, err)
	}
	if _, err := s.Submit(ctx, core.SubmitRequest{Execution: e2.ID, Text: "hs-race-line"}); core.CodeOf(err) != core.CodeIdentityNotProven {
		t.Fatalf("a submit to the replaced address, once the reconciler saw it: %v", err)
	}
	nothingTyped(g, 1)

	// Replaced between the submit's check and the send.
	l3, _ := g.gUp("race", "race-r", "reviewer", "claude")
	e3 := g.gReady(l3)
	if err := n.Approve(e3, ""); err != nil {
		t.Fatal(err)
	}
	s.Dial = raced(func() { g.gLose(e3) })
	rec, err = s.Submit(ctx, core.SubmitRequest{Execution: e3.ID, Text: "hs-race-line"})
	t.Logf("replaced between the check and the send: %s; core answered: %s", rec.State, rec.Detail)
	if err != nil || rec.State != core.SubmitRejectedBeforeWrite {
		t.Fatalf("replaced under the send: %+v %v", rec, err)
	}
	s.Dial = plain
	nothingTyped(g, 2)
}
