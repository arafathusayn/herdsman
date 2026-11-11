package core_test

import (
	"context"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	"github.com/arafathusayn/herdsman/internal/runtime/core/coretest"
)

// submitWorld is a world with a notice service and a typed-submit service.
func submitWorld(t *testing.T) (*world, *notice.Service, *core.Submitter, *coretest.MemSubmits) {
	t.Helper()
	w := newWorld(t)
	n := notice.New(notice.NewMemStore(), coretest.Executions{Records: w.records}, notice.Config{Poll: 100 * time.Millisecond})
	t.Cleanup(n.Close)
	store := &coretest.MemSubmits{}
	s := &core.Submitter{Store: store, Records: w.records, Notices: n, Procs: w.procs,
		Dial: func(ctx context.Context) (core.Sender, func(), error) {
			c, err := hc.Dial(ctx, w.core.Socket, hc.Options{})
			if err != nil {
				return nil, nil, err
			}
			return c, func() { _ = c.Close() }, nil
		}}
	return w, n, s, store
}

func sends(w *world) []sendCall {
	var out []sendCall
	for _, r := range w.core.Requests() {
		if r.Method == hc.MethodSend {
			out = append(out, sendCall{r.Params})
		}
	}
	return out
}

type sendCall struct{ params map[string]any }

// State guards, `if` and first status: a typed submit needs a first status,
// the state the adapter permits, and is sent with `if` and force:false.
func TestTypedSubmitStateGuards(t *testing.T) {
	w, n, s, _ := submitWorld(t)
	ctx := testCtx(t)
	w.openApp()
	l, e := w.up("sub", "sub-o", "orchestrator", "claude")
	if err := n.Approve(w.record(l.Intent), ""); err != nil {
		t.Fatal(err)
	}

	// No status since registration: refused before anything is recorded.
	if _, err := s.Submit(ctx, core.SubmitRequest{Execution: e.ID, Text: "go on"}); core.CodeOf(err) != core.CodeSubmitNoStatus {
		t.Fatalf("before the first status: %v", err)
	}
	e = w.ready(l)
	if err := n.Approve(e, ""); err != nil {
		t.Fatal(err)
	}

	// A multi-line text is refused: a typed submit is one line.
	if _, err := s.Submit(ctx, core.SubmitRequest{Execution: e.ID, Text: "a\nb"}); core.CodeOf(err) != core.CodeSubmitGate {
		t.Fatalf("two lines: %v", err)
	}

	// Working: the state is not the adapter's; nothing is typed.
	w.core.SetStatus(e.TerminalAddress, "working")
	if _, err := s.Submit(ctx, core.SubmitRequest{Execution: e.ID, Text: "go on"}); core.CodeOf(err) != core.CodeSubmitState {
		t.Fatalf("working: %v", err)
	}
	if len(sends(w)) != 0 {
		t.Fatal("a refused submit was sent")
	}

	w.core.SetStatus(e.TerminalAddress, "idle")
	rec, err := s.Submit(ctx, core.SubmitRequest{Execution: e.ID, Text: "go on"})
	if err != nil || rec.State != core.SubmitTyped {
		t.Fatalf("idle: %+v %v", rec, err)
	}
	got := sends(w)
	if len(got) != 1 || got[0].params["if"] != core.SubmitIf || got[0].params["force"] == true {
		t.Fatalf("send params: %+v", got)
	}
	if rec.TextDigest == "" || rec.TextDigest == "go on" {
		t.Fatalf("the text is stored: %+v", rec)
	}

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
	if len(sends(w)) != 1 {
		t.Fatalf("sends = %d", len(sends(w)))
	}

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
}

// Input race, send uncertainty and outstanding send: core's own refusal of
// `if` proves nothing was typed; an answer that never comes leaves the
// submit unresolved, which blocks the next one until the person sends again
// knowing both may be typed. A daemon restart settles what core held.
func TestTypedSubmitRaceAndUncertainty(t *testing.T) {
	w, n, s, store := submitWorld(t)
	ctx := testCtx(t)
	w.openApp()
	l, _ := w.up("race", "race-o", "orchestrator", "claude")
	e := w.ready(l)
	if err := n.Approve(e, ""); err != nil {
		t.Fatal(err)
	}

	// Input race: the person started typing between the check and the send;
	// core's `if` refuses.
	w.core.Fail(hc.MethodSend, hc.KindStatusMismatch)
	rec, err := s.Submit(ctx, core.SubmitRequest{Execution: e.ID, Text: "one"})
	if err != nil || rec.State != core.SubmitRejectedBeforeWrite {
		t.Fatalf("race: %+v %v", rec, err)
	}

	// Send uncertainty: an error that does not prove the line was not
	// written leaves it unresolved.
	w.core.Fail(hc.MethodSend, "internal")
	rec, err = s.Submit(ctx, core.SubmitRequest{Execution: e.ID, Text: "two"})
	if err != nil || rec.State != core.SubmitUnresolved {
		t.Fatalf("uncertain: %+v %v", rec, err)
	}
	if state, _ := s.Draining(e.ID); state != core.SubmitUnresolved {
		t.Fatalf("draining = %q", state)
	}

	// Outstanding send: the unresolved line blocks the next submit.
	before := len(sends(w))
	if _, err := s.Submit(ctx, core.SubmitRequest{Execution: e.ID, Text: "three"}); core.CodeOf(err) != core.CodeSubmitOutstanding {
		t.Fatalf("outstanding: %v", err)
	}
	if len(sends(w)) != before {
		t.Fatal("an outstanding submit was sent again")
	}
	rec, err = s.Submit(ctx, core.SubmitRequest{Execution: e.ID, Text: "three", Again: true})
	if err != nil || rec.State != core.SubmitTyped {
		t.Fatalf("again: %+v %v", rec, err)
	}

	// A submit left pending in core by a daemon that stopped is unresolved
	// after the restart, never retired by time.
	pending := core.SubmitRecord{ID: "s-pending", Execution: e.ID, State: core.SubmitPendingInCore, TextDigest: "x", Created: time.Now(), Updated: time.Now()}
	if err := store.PutSubmit(pending); err != nil {
		t.Fatal(err)
	}
	if state, _ := s.Draining(e.ID); state != "dispatch_draining" {
		t.Fatalf("draining while pending = %q", state)
	}
	settled, err := s.Settle()
	if err != nil || len(settled) != 1 || settled[0].State != core.SubmitUnresolved {
		t.Fatalf("settle: %+v %v", settled, err)
	}
	all, _ := store.Submits(e.ID)
	for _, r := range all {
		if r.State == core.SubmitPendingInCore || r.State == core.SubmitQueued {
			t.Fatalf("left pending: %+v", r)
		}
	}
}

// A submit to an execution the reconciler found replaced is refused by
// identity, as before the reconciler saw it, never as a missing status, and
// nothing is sent to the program at its address.
func TestTypedSubmitToAReplacedExecutionIsAnIdentityRefusal(t *testing.T) {
	w, n, s, _ := submitWorld(t)
	ctx := testCtx(t)
	w.openApp()
	l, _ := w.up("lost", "lost-o", "orchestrator", "claude")
	e := w.ready(l)
	if err := n.Approve(e, ""); err != nil {
		t.Fatal(err)
	}
	w.gLose(e)
	if _, err := s.Submit(ctx, core.SubmitRequest{Execution: e.ID, Text: "go on"}); core.CodeOf(err) != core.CodeIdentityNotProven {
		t.Fatalf("before the reconciler saw it: %v", err)
	}
	if err := w.reconciler(&events{}).Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if r := w.record(l.Intent).Observation.Readiness; r != runtime.ReadinessIdentityUnknown {
		t.Fatalf("readiness = %s", r)
	}
	if _, err := s.Submit(ctx, core.SubmitRequest{Execution: e.ID, Text: "go on"}); core.CodeOf(err) != core.CodeIdentityNotProven {
		t.Fatalf("after the reconciler saw it: %v", err)
	}
	if c := w.core.Calls(hc.MethodSend); c != 0 {
		t.Fatalf("sent %d times", c)
	}
}

// A submit names a running execution on core; any other is refused.
func TestTypedSubmitNeedsARunningExecution(t *testing.T) {
	_, _, s, _ := submitWorld(t)
	if _, err := s.Submit(testCtx(t), core.SubmitRequest{Execution: runtime.NewExecutionID(), Text: "go"}); core.CodeOf(err) != core.CodeIdentityNotProven {
		t.Fatalf("unknown execution: %v", err)
	}
}

// Unattended launch: with the binding verified earlier and the app closed,
// the new execution's visibility is pending, and its brief is still issued,
// woken through its wait and claimable, with nothing typed.
func TestUnattendedLaunchBriefIsClaimable(t *testing.T) {
	w, n, _, _ := submitWorld(t)
	ctx := testCtx(t)
	w.openApp()
	l1, _ := w.up("night", "night-o", "orchestrator", "claude")
	w.ready(l1)
	w.app.Quit()
	l2, _ := w.up("night", "night-i", "implementer", "claude")
	if _, err := w.p.VisibilityStep(ctx, l2, core.Timeouts{Visibility: time.Second, Poll: 20 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	w.core.SetStatus(w.record(l2.Intent).TerminalAddress, "idle")
	e, err := w.p.ReadyStep(ctx, l2, core.Timeouts{Ready: time.Second, Poll: 20 * time.Millisecond})
	if err != nil || e.Observation.Visibility != runtime.VisibilityPending {
		t.Fatalf("ready: %+v %v", e.Observation, err)
	}
	if err := n.Approve(e, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	issued, err := n.Issue(ctx, notice.IssueRequest{Key: "brief:night-i", Kind: notice.KindBrief, Execution: e.ID, Content: "night work", Issuer: "up-1"})
	if err != nil {
		t.Fatalf("issue with the app closed: %v", err)
	}
	a, err := n.Wait(ctx, e, notice.WaitRequest{WaitID: "w-1"})
	if err != nil || a.Answer != notice.AnswerNotice || a.Notice.ID != issued.ID {
		t.Fatalf("wake: %+v %v", a, err)
	}
	got, err := n.Read(ctx, e, issued.ID)
	if err != nil || got.Token == "" || got.Content != "night work" {
		t.Fatalf("claim: %+v %v", got, err)
	}
	if c := w.core.Calls(hc.MethodSend); c != 0 {
		t.Fatalf("typed %d times", c)
	}
}
