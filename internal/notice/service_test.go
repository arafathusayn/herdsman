package notice

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/runtime"
)

// world is a set of executions and a clock the tests move by hand.
type world struct {
	mu     sync.Mutex
	execs  map[string]runtime.Execution
	now    time.Time
	events []Event
}

func newWorld() *world {
	return &world{execs: map[string]runtime.Execution{}, now: time.Unix(1794000000, 0)}
}

func (w *world) Execution(_ context.Context, id string) (runtime.Execution, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	e, ok := w.execs[id]
	return e, ok, nil
}

func (w *world) Running(context.Context) ([]runtime.Execution, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []runtime.Execution
	for _, e := range w.execs {
		if e.Desired == runtime.DesiredRunning {
			out = append(out, e)
		}
	}
	return out, nil
}

func (w *world) clock() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.now
}

func (w *world) advance(d time.Duration) {
	w.mu.Lock()
	w.now = w.now.Add(d)
	w.mu.Unlock()
}

func (w *world) emit(ev Event) {
	w.mu.Lock()
	w.events = append(w.events, ev)
	w.mu.Unlock()
}

func (w *world) kinds(kind string) []Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []Event
	for _, ev := range w.events {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

func (w *world) edit(id string, f func(*runtime.Execution)) {
	w.mu.Lock()
	e := w.execs[id]
	f(&e)
	w.execs[id] = e
	w.mu.Unlock()
}

// add records a ready, visible, registered execution of route harbor.
func (w *world) add(id, kind string) runtime.Execution {
	e := runtime.Execution{ID: id, Repo: "/r", Route: "harbor", Agent: "a-" + id, Generation: 1, Backend: runtime.BackendCore, EndpointID: "core-1",
		Evidence: runtime.ProcessEvidence{PID: 100, Start: "s"}, HarnessKind: kind, PolicyDigest: "pol", Desired: runtime.DesiredRunning,
		Observation: runtime.Observation{Process: runtime.ProcessLive, Readiness: runtime.ReadinessReady, Program: runtime.ProgramDone, Visibility: runtime.VisibilityVerified}}
	w.mu.Lock()
	w.execs[id] = e
	w.mu.Unlock()
	return e
}

func (w *world) get(id string) runtime.Execution {
	e, _, _ := w.Execution(context.Background(), id)
	return e
}

func newService(t *testing.T, w *world, store Store) *Service {
	t.Helper()
	s := New(store, w, Config{Poll: 40 * time.Millisecond, Now: w.clock, Emit: w.emit, Boot: "boot-1",
		Sleep:      func(_ context.Context, d time.Duration) error { w.advance(d); return nil },
		RecordPath: func(n Notice) string { return "/records/" + n.ID + ".md" },
		Attended:   func(k string) bool { return k == "codex" }})
	t.Cleanup(s.Close)
	return s
}

func approved(t *testing.T, s *Service, e runtime.Execution) {
	t.Helper()
	if err := s.Approve(e, ""); err != nil {
		t.Fatal(err)
	}
}

func issue(t *testing.T, s *Service, exec, key string) Notice {
	t.Helper()
	n, err := s.Issue(context.Background(), IssueRequest{Key: key, Kind: KindBrief, Execution: exec, Content: "do " + key, Issuer: "op-up"})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func wait(t *testing.T, s *Service, e runtime.Execution, req WaitRequest) WaitAnswer {
	t.Helper()
	a, err := s.Wait(context.Background(), e, req)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// Notice lifecycle: a notice is claimed once; a repeat read returns the same
// token marked as claimed before; a read after handling returns the result
// and no instruction; another execution, an older generation, a hold or a
// stopping route cannot claim it, and it stays issued.
func TestNoticeLifecycle(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	s := newService(t, w, NewMemStore())
	a, b := w.add("e-a", "claude"), w.add("e-b", "claude")
	approved(t, s, a)
	approved(t, s, b)
	n := issue(t, s, a.ID, "k1")
	if again := issue(t, s, a.ID, "k1"); again.ID != n.ID {
		t.Fatalf("a repeated key issued again: %s %s", again.ID, n.ID)
	}

	// Another execution cannot read it.
	if _, err := s.Read(ctx, b, n.ID); !errors.Is(err, ErrUnknown) {
		t.Fatalf("another execution read it: %v", err)
	}
	// An older generation: the same id, generation 0.
	old := a
	old.Generation = 0
	w.edit(a.ID, func(e *runtime.Execution) { e.Generation = 2 })
	got, err := s.Read(ctx, w.get(a.ID), n.ID)
	if err != nil || got.Refused != GateIdentity {
		t.Fatalf("a newer generation claimed the old generation's notice: %+v %v", got, err)
	}
	w.edit(a.ID, func(e *runtime.Execution) { e.Generation = 1 })
	// A hold and a stopping route refuse the claim.
	if err := s.Hold("/r", "harbor", true, "op-pause"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Read(ctx, a, n.ID); got.Refused != GateHeld || got.Token != "" {
		t.Fatalf("a held claim: %+v", got)
	}
	if err := s.Hold("/r", "harbor", false, ""); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List(a.ID)
	if list[0].State != StateIssued {
		t.Fatalf("a refused claim changed the notice: %s", list[0].State)
	}

	first, err := s.Read(ctx, a, "")
	if err != nil || first.Token == "" || first.ClaimedBefore || first.Content != "do k1" {
		t.Fatalf("claim: %+v %v", first, err)
	}
	repeat, err := s.Read(ctx, a, n.ID)
	if err != nil || repeat.Token != first.Token || !repeat.ClaimedBefore || repeat.Notice.RecordPath == "" {
		t.Fatalf("repeat read: %+v %v", repeat, err)
	}
	if _, err := s.Ack(ctx, a, n.ID, "wrong", OutcomeCompleted, "done"); !errors.Is(err, ErrToken) {
		t.Fatalf("a wrong token: %v", err)
	}
	if _, err := s.Ack(ctx, a, n.ID, first.Token, OutcomeCompleted, " "); !errors.Is(err, ErrRecord) {
		t.Fatalf("no record: %v", err)
	}
	ack, err := s.Ack(ctx, a, n.ID, first.Token, OutcomeCompleted, "shipped")
	if err != nil || ack.Repeated || ack.Notice.State != StateHandled {
		t.Fatalf("ack: %+v %v", ack, err)
	}
	if again, err := s.Ack(ctx, a, n.ID, first.Token, OutcomeCompleted, "shipped"); err != nil || !again.Repeated {
		t.Fatalf("a repeated ack: %+v %v", again, err)
	}
	if _, err := s.Ack(ctx, a, n.ID, first.Token, OutcomeAbandoned, "no"); !errors.Is(err, ErrOutcome) {
		t.Fatalf("another outcome: %v", err)
	}
	after, err := s.Read(ctx, a, n.ID)
	if err != nil || after.Content != "" || after.Token != "" || after.Notice.State != StateHandled {
		t.Fatalf("a read after handling: %+v %v", after, err)
	}

	// A stopping route refuses ordinary claims; its notice stays issued.
	n2 := issue(t, s, a.ID, "k2")
	if err := s.Stopping("/r", "harbor", "op-stop", time.Minute); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Read(ctx, a, n2.ID); got.Refused != GateStopping {
		t.Fatalf("a claim while stopping: %+v", got)
	}
	if _, err := s.Issue(ctx, IssueRequest{Key: "k3", Kind: KindContinue, Execution: a.ID, Content: "x", Issuer: "op"}); GateOf(err) != GateStopping {
		t.Fatalf("an ordinary issue while stopping: %v", err)
	}

	// An execution killed after its claim leaves its notice returned; the
	// replacement gets a new notice with the old record.
	if err := s.Restart("/r", "harbor"); err != nil {
		t.Fatal(err)
	}
	claimed, _ := s.Read(ctx, a, n2.ID)
	if claimed.Token == "" {
		t.Fatalf("claim n2: %+v", claimed)
	}
	if ret, err := s.ReturnExecution(a.ID, "execution lost"); err != nil || len(ret) != 1 {
		t.Fatalf("return: %v %v", ret, err)
	}
	if got, _ := s.Read(ctx, a, n2.ID); got.Refused != GateReturned {
		t.Fatalf("a returned notice was served again: %+v", got)
	}
	c := w.add("e-c", "claude")
	approved(t, s, c)
	re, err := s.Reissue(ctx, a.ID, c.ID, "op-resume")
	if err != nil || len(re) != 1 || re[0].Previous == "" {
		t.Fatalf("reissue: %+v %v", re, err)
	}
	if len(w.kinds(EventReturned)) != 1 {
		t.Fatalf("returned events: %v", w.events)
	}
}

// The first-dispatch gate: no notice is issued or claimed before
// registration, readiness, approval and visibility.
func TestFirstDispatchGate(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	s := newService(t, w, NewMemStore())
	e := w.add("e-a", "claude")
	if _, err := s.Issue(ctx, IssueRequest{Key: "k", Kind: KindBrief, Execution: e.ID, Content: "x", Issuer: "op"}); GateOf(err) != GateApproval {
		t.Fatalf("issue before approval: %v", err)
	}
	approved(t, s, e)
	for name, edit := range map[string]func(*runtime.Execution){
		"unregistered": func(e *runtime.Execution) { e.Evidence = runtime.ProcessEvidence{} },
		"not ready":    func(e *runtime.Execution) { e.Observation.Readiness = runtime.ReadinessWaiting },
		"unverified":   func(e *runtime.Execution) { e.Observation.Visibility = runtime.VisibilityUnverified },
		"missing":      func(e *runtime.Execution) { e.Observation.Visibility = runtime.VisibilityMissing },
		"lost":         func(e *runtime.Execution) { e.Observation.Process = runtime.ProcessLost },
		"policy":       func(e *runtime.Execution) { e.PolicyDigest = "other" },
	} {
		saved := w.get(e.ID)
		w.edit(e.ID, edit)
		if _, err := s.Issue(ctx, IssueRequest{Key: "k-" + name, Kind: KindBrief, Execution: e.ID, Content: "x", Issuer: "op"}); err == nil {
			t.Errorf("%s: issued", name)
		}
		w.edit(e.ID, func(x *runtime.Execution) { *x = saved })
	}
	n := issue(t, s, e.ID, "ok")
	w.edit(e.ID, func(x *runtime.Execution) { x.Observation.Readiness = runtime.ReadinessNeedsAttention })
	if got, _ := s.Read(ctx, w.get(e.ID), n.ID); got.Refused != GateIdentity {
		t.Fatalf("a claim by an execution that is not ready: %+v", got)
	}
}

// Wait ownership: the highest generation owns the wait; a replaced command is
// replaced every time it tries again, also after a lost answer, a repeat of
// its first call, late copies of its requests and a daemon restart.
func TestWaitOwnership(t *testing.T) {
	w := newWorld()
	store := NewMemStore()
	s := newService(t, w, store)
	e := w.add("e-a", "claude")
	a := wait(t, s, e, WaitRequest{WaitID: "A"})
	if a.Generation != 1 || a.Answer != AnswerNone {
		t.Fatalf("A's first call: %+v", a)
	}
	start := w.clock()
	b := wait(t, s, e, WaitRequest{WaitID: "B"})
	if b.Generation != 2 {
		t.Fatalf("B's first call: %+v", b)
	}
	if w.clock().Sub(start) < DefaultGenerationGap {
		t.Fatalf("generations less than a second apart: %s", w.clock().Sub(start))
	}
	check := func(s *Service) {
		t.Helper()
		// A lost B's replaced answer and tries again, as a renewal and as
		// its first call again: replaced every time.
		for range 3 {
			if got := wait(t, s, e, WaitRequest{WaitID: "A", Generation: 1}); got.Answer != AnswerReplaced {
				t.Fatalf("A renewing: %+v", got)
			}
			if got := wait(t, s, e, WaitRequest{WaitID: "A"}); got.Answer != AnswerReplaced {
				t.Fatalf("A repeating its first call: %+v", got)
			}
		}
		// A late copy of A's request with B's generation does not pass.
		if got := wait(t, s, e, WaitRequest{WaitID: "A", Generation: 2}); got.Answer != AnswerReplaced {
			t.Fatalf("A with B's generation: %+v", got)
		}
		// An unknown id may ask only as a first call.
		if got := wait(t, s, e, WaitRequest{WaitID: "Z", Generation: 9}); got.Answer != AnswerReplaced {
			t.Fatalf("an unknown renewal: %+v", got)
		}
		// B lost the answer to its first call and repeats it: answered
		// from its record, no new generation.
		if got := wait(t, s, e, WaitRequest{WaitID: "B"}); got.Generation != 2 || got.Answer == AnswerReplaced {
			t.Fatalf("B repeating its first call: %+v", got)
		}
		if got := wait(t, s, e, WaitRequest{WaitID: "B", Generation: 2}); got.Answer != AnswerNone {
			t.Fatalf("B renewing: %+v", got)
		}
	}
	check(s)
	// A daemon restart: a new service on the same store; the two connect
	// again in either order.
	s.Close()
	check(newService(t, w, store))
	s3 := newService(t, w, store)
	if got := wait(t, s3, e, WaitRequest{WaitID: "B", Generation: 2}); got.Answer != AnswerNone {
		t.Fatalf("B first after a restart: %+v", got)
	}
	if got := wait(t, s3, e, WaitRequest{WaitID: "A", Generation: 1}); got.Answer != AnswerReplaced {
		t.Fatalf("A second after a restart: %+v", got)
	}
	// Only a new command's first call changes the owner.
	c := wait(t, s3, e, WaitRequest{WaitID: "C"})
	if c.Generation != 3 {
		t.Fatalf("C: %+v", c)
	}
	if got := wait(t, s3, e, WaitRequest{WaitID: "B", Generation: 2}); got.Answer != AnswerReplaced {
		t.Fatalf("B after C: %+v", got)
	}
}

// Both commands lose their connections at once: the newer still owns the
// wait, and an attached older poll is ended as replaced when the newer one
// attaches.
func TestWaitOwnershipWithBothConnectionsLost(t *testing.T) {
	w := newWorld()
	s := newService(t, w, NewMemStore())
	e := w.add("e-a", "claude")
	_ = wait(t, s, e, WaitRequest{WaitID: "A"})
	ctxA, cancelA := context.WithCancel(context.Background())
	done := make(chan WaitAnswer, 1)
	go func() {
		a, _ := s.Wait(ctxA, e, WaitRequest{WaitID: "A", Generation: 1})
		done <- a
	}()
	for s.Waits() == 0 {
		time.Sleep(time.Millisecond)
	}
	b := wait(t, s, e, WaitRequest{WaitID: "B"})
	if got := <-done; got.Answer != AnswerReplaced {
		t.Fatalf("the attached older poll: %+v", got)
	}
	cancelA()
	// Both drop; both come back.
	if got := wait(t, s, e, WaitRequest{WaitID: "A", Generation: 1}); got.Answer != AnswerReplaced {
		t.Fatalf("A back: %+v", got)
	}
	if got := wait(t, s, e, WaitRequest{WaitID: "B", Generation: b.Generation}); got.Answer != AnswerNone {
		t.Fatalf("B back: %+v", got)
	}
}

// Held notices: under a hold no wait is answered and the read is refused
// once; lifting the hold announces the same notice once, claimed once. The
// order of two notices is kept, and an open claim under a hold is not
// offered.
func TestHeldNotices(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	s := newService(t, w, NewMemStore())
	e := w.add("e-a", "claude")
	approved(t, s, e)
	n1 := issue(t, s, e.ID, "k1")
	n2 := issue(t, s, e.ID, "k2")
	if err := s.Hold("/r", "harbor", true, "op-pause"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Read(ctx, e, n1.ID); got.Refused != GateHeld {
		t.Fatalf("read under a hold: %+v", got)
	}
	for range 5 {
		if got := wait(t, s, e, WaitRequest{WaitID: "A"}); got.Answer != AnswerNone {
			t.Fatalf("a wait under a hold was answered: %+v", got)
		}
	}
	// Lifting the hold wakes an attached wait with the first notice.
	got := make(chan WaitAnswer, 1)
	go func() {
		a, _ := s.Wait(ctx, e, WaitRequest{WaitID: "A", Generation: 1})
		got <- a
	}()
	for s.Waits() == 0 {
		time.Sleep(time.Millisecond)
	}
	if err := s.Hold("/r", "harbor", false, ""); err != nil {
		t.Fatal(err)
	}
	if a := <-got; a.Answer != AnswerNotice || a.Notice.ID != n1.ID {
		t.Fatalf("after the hold: %+v", a)
	}
	// The second notice waits behind the first, even by its id.
	if r, _ := s.Read(ctx, e, n2.ID); r.Refused != GateOrder {
		t.Fatalf("the second notice out of order: %+v", r)
	}
	r1, _ := s.Read(ctx, e, n1.ID)
	// An open claim under a hold is not offered.
	if err := s.Hold("/r", "harbor", true, "op-pause-2"); err != nil {
		t.Fatal(err)
	}
	if a := wait(t, s, e, WaitRequest{WaitID: "A", Generation: 1}); a.Answer != AnswerNone {
		t.Fatalf("a recovery answer under a hold: %+v", a)
	}
	if rr, _ := s.Read(ctx, e, n1.ID); rr.MayContinue || rr.Next != nextWaitAfter(n1.ID) {
		t.Fatalf("a repeat read under a hold may continue: %+v", rr)
	}
	if err := s.Hold("/r", "harbor", false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ack(ctx, e, n1.ID, r1.Token, OutcomeCompleted, "ok"); err != nil {
		t.Fatal(err)
	}
	if a := wait(t, s, e, WaitRequest{WaitID: "A", Generation: 1}); a.Notice.ID != n2.ID {
		t.Fatalf("after the first is handled: %+v", a)
	}
	// Visibility missing and a withdrawn approval hold the same way.
	w.edit(e.ID, func(x *runtime.Execution) { x.Observation.Visibility = runtime.VisibilityMissing })
	if a := wait(t, s, w.get(e.ID), WaitRequest{WaitID: "A", Generation: 1}); a.Answer != AnswerNone {
		t.Fatalf("a wait with visibility missing: %+v", a)
	}
	w.edit(e.ID, func(x *runtime.Execution) { x.Observation.Visibility = runtime.VisibilityVerified })
	if err := s.Withdraw(e.ID); err != nil {
		t.Fatal(err)
	}
	if a := wait(t, s, e, WaitRequest{WaitID: "A", Generation: 1}); a.Answer != AnswerNone {
		t.Fatalf("a wait with approval withdrawn: %+v", a)
	}
	// Past the grace period the held notice is reported once, with its gate.
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	w.advance(DefaultHeldGrace)
	_ = s.Tick(ctx)
	_ = s.Tick(ctx)
	held := w.kinds(EventHeld)
	if len(held) != 1 || held[0].Reason != GateApproval {
		t.Fatalf("notice_held: %+v", held)
	}
}

// Claim recovery: a wait that does not name the open claim gets it at once;
// a wait that names it is answered only when recovery is due; later answers
// come at the end of a poll, and three ignored ones make claim_stuck.
func TestClaimRecovery(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	store := NewMemStore()
	s := newService(t, w, store)
	e := w.add("e-a", "claude")
	approved(t, s, e)
	n := issue(t, s, e.ID, "k1")
	issue(t, s, e.ID, "k2")
	if _, err := s.Read(ctx, e, n.ID); err != nil {
		t.Fatal(err)
	}
	if a := wait(t, s, e, WaitRequest{WaitID: "A", After: n.ID}); a.Answer != AnswerNone {
		t.Fatalf("a wait naming its claim: %+v", a)
	}
	// A daemon restart with the context cleared: the first wait gets the
	// recovery answer, before the newer notice.
	s2 := newService(t, w, store)
	if a := wait(t, s2, e, WaitRequest{WaitID: "B"}); a.Answer != AnswerRecovery || a.Notice.ID != n.ID {
		t.Fatalf("a wait that does not name the claim: %+v", a)
	}
	// The pause interrupted it and the route was resumed.
	if ok, _ := s2.MarkRecoveryDue(e.ID, true); !ok {
		t.Fatal("no open claim")
	}
	started := time.Now()
	a := wait(t, s2, e, WaitRequest{WaitID: "B", Generation: 2, After: n.ID})
	if a.Answer != AnswerRecovery || !a.Notice.ResumedSince {
		t.Fatalf("recovery due: %+v", a)
	}
	if time.Since(started) < 30*time.Millisecond {
		t.Fatal("a second recovery answer came before the end of its poll")
	}
	_ = wait(t, s2, e, WaitRequest{WaitID: "B", Generation: 2, After: n.ID})
	if got := w.kinds(EventClaimStuck); len(got) != 1 {
		t.Fatalf("claim_stuck: %+v", got)
	}
	// The agent reads its record and acknowledges interrupted: the next
	// notice becomes claimable.
	rr, _ := s2.Read(ctx, e, n.ID)
	if !rr.ClaimedBefore {
		t.Fatalf("repeat read: %+v", rr)
	}
	if _, err := s2.Ack(ctx, e, n.ID, rr.Token, OutcomeInterrupted, "stopped at step 2"); err != nil {
		t.Fatal(err)
	}
	if got := w.kinds(EventOutcome); len(got) != 1 || got[0].Outcome != OutcomeInterrupted {
		t.Fatalf("outcome event: %+v", got)
	}
	if a := wait(t, s2, e, WaitRequest{WaitID: "B", Generation: 2}); a.Answer != AnswerNotice {
		t.Fatalf("the next notice: %+v", a)
	}
	// A turn a person ended: the program rests past the grace period.
	n2, _ := s2.Read(ctx, e, "")
	_ = s2.Tick(ctx)
	w.advance(DefaultRecoveryGrace)
	_ = s2.Tick(ctx)
	if a := wait(t, s2, e, WaitRequest{WaitID: "B", Generation: 2, After: n2.Notice.ID}); a.Answer != AnswerRecovery {
		t.Fatalf("recovery by status: %+v", a)
	}
}

// Stop reports at the domain level: while stopping, only the final-report
// notice is announced, ahead of an open claim, which stays open beside it;
// its deadline moves once after a daemon restart.
func TestFinalReport(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	store := NewMemStore()
	s := newService(t, w, store)
	e := w.add("e-a", "claude")
	approved(t, s, e)
	n := issue(t, s, e.ID, "k1")
	issue(t, s, e.ID, "k2")
	claim, _ := s.Read(ctx, e, n.ID)
	if _, err := s.IssueFinalReport(ctx, FinalRequest{Key: "f", Execution: e.ID, StopOp: "op-stop", Grace: time.Minute}); GateOf(err) != GateNotStopping {
		t.Fatalf("a final report outside the stopping phase: %v", err)
	}
	if err := s.Stopping("/r", "harbor", "op-stop", time.Minute); err != nil {
		t.Fatal(err)
	}
	// Not waiting: refused when the harness keeps a wait and holds none.
	if _, err := s.IssueFinalReport(ctx, FinalRequest{Key: "f0", Execution: e.ID, StopOp: "op-stop", Grace: time.Minute, HoldsWait: true}); GateOf(err) != EventNotWaiting {
		t.Fatalf("not waiting: %v", err)
	}
	f, err := s.IssueFinalReport(ctx, FinalRequest{Key: "f", Execution: e.ID, StopOp: "op-stop", Grace: time.Minute})
	if err != nil || f.Content != FinalReportText(n.ID) {
		t.Fatalf("final report: %+v %v", f, err)
	}
	if a := wait(t, s, e, WaitRequest{WaitID: "A", After: n.ID}); a.Notice.ID != f.ID {
		t.Fatalf("the wait while stopping: %+v", a)
	}
	r, _ := s.Read(ctx, e, "")
	if r.Notice.ID != n.ID || !r.ClaimedBefore || r.MayContinue {
		t.Fatalf("read without id returns the open claim first: %+v", r)
	}
	fr, _ := s.Read(ctx, e, f.ID)
	if fr.Token == "" {
		t.Fatalf("final report claim: %+v", fr)
	}
	// The daemon restarts: the deadline moves once.
	s2 := New(store, w, Config{Now: w.clock, Boot: "boot-2"})
	defer s2.Close()
	w.advance(50 * time.Second)
	got, _ := s2.ConfirmDeadlines("/r", "harbor")
	if len(got) != 1 || !got[0].DeadlineMoved || !got[0].Deadline.Equal(w.clock().Add(time.Minute)) {
		t.Fatalf("moved deadline: %+v", got)
	}
	s3 := New(store, w, Config{Now: w.clock, Boot: "boot-3"})
	defer s3.Close()
	again, _ := s3.ConfirmDeadlines("/r", "harbor")
	if !again[0].Deadline.Equal(got[0].Deadline) {
		t.Fatal("a second restart moved the deadline again")
	}
	if _, err := s2.Ack(ctx, e, f.ID, fr.Token, OutcomeCompleted, "report sent"); err != nil {
		t.Fatal(err)
	}
	_ = claim
}

// Wait capacity: many executions hold waits at once while reads and acks
// keep answering; a hundred reconnects of one execution leave one slot; the
// budget refuses a wait beyond it.
func TestWaitCapacity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := newWorld()
	s := New(NewMemStore(), w, Config{Poll: 5 * time.Second, Now: w.clock, MaxWaits: 40,
		Sleep: func(_ context.Context, d time.Duration) error { w.advance(d); return nil }})
	defer s.Close()
	var execs []runtime.Execution
	for i := range 40 {
		e := w.add(fmt.Sprintf("e-%02d", i), "claude")
		execs = append(execs, e)
		if err := s.Approve(e, ""); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for _, e := range execs[:39] {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.Wait(ctx, e, WaitRequest{WaitID: "w-" + e.ID})
		}()
	}
	for s.Waits() < 39 {
		time.Sleep(time.Millisecond)
	}
	// Reads and acks answer at once beside 39 waits.
	last := execs[39]
	n := issue(t, s, last.ID, "k")
	start := time.Now()
	r, err := s.Read(ctx, last, n.ID)
	if err != nil || r.Token == "" {
		t.Fatalf("read: %+v %v", r, err)
	}
	if _, err := s.Ack(ctx, last, n.ID, r.Token, OutcomeCompleted, "ok"); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("read and ack took %s beside the waits", time.Since(start))
	}
	// A hundred reconnects of one execution leave one slot.
	for range 100 {
		c, stop := context.WithCancel(ctx)
		go func() { _, _ = s.Wait(c, last, WaitRequest{WaitID: "again"}) }()
		time.Sleep(time.Millisecond)
		stop()
	}
	deadline := time.Now().Add(2 * time.Second)
	for s.Waits() > 40 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := s.Waits(); got > 40 {
		t.Fatalf("%d waits after the reconnects", got)
	}
	// The budget is full only once every slot is taken: one more execution
	// is refused.
	for s.Waits() < 40 {
		go func() { _, _ = s.Wait(ctx, last, WaitRequest{WaitID: "again"}) }()
		time.Sleep(5 * time.Millisecond)
	}
	extra := w.add("e-extra", "claude")
	if _, err := s.Wait(ctx, extra, WaitRequest{WaitID: "x"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("a wait beyond the budget: %v", err)
	}
	cancel()
	wg.Wait()
}

// Not waiting and attended executions: a wait-holding execution with no wait
// is reported after the grace period, once; a notice for an attended one
// raises needs_attention with the fixed line, once.
func TestTickReportsWaitsAndAttention(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	s := newService(t, w, NewMemStore())
	claude, codex := w.add("e-claude", "claude"), w.add("e-codex", "codex")
	approved(t, s, claude)
	approved(t, s, codex)
	_ = wait(t, s, claude, WaitRequest{WaitID: "A"})
	_ = s.Tick(ctx)
	if len(w.kinds(EventNotWaiting)) != 0 {
		t.Fatal("not_waiting inside the grace period")
	}
	w.advance(DefaultRenewal + DefaultWaitGrace)
	_ = s.Tick(ctx)
	_ = s.Tick(ctx)
	if got := w.kinds(EventNotWaiting); len(got) != 1 || got[0].Execution != claude.ID {
		t.Fatalf("not_waiting: %+v", got)
	}
	n := issue(t, s, codex.ID, "k")
	_ = s.Tick(ctx)
	_ = s.Tick(ctx)
	att := w.kinds(EventNeedsAttention)
	if len(att) != 1 || att[0].Reason != AttentionNoticeWaiting || att[0].Line != AttendedLine || att[0].Notice != n.ID {
		t.Fatalf("needs_attention: %+v", att)
	}
	// A person's Esc in a turn: the program reports idle with a claim open.
	if _, err := s.Read(ctx, codex, n.ID); err != nil {
		t.Fatal(err)
	}
	w.edit(codex.ID, func(e *runtime.Execution) { e.Observation.Program = runtime.ProgramIdle })
	_ = s.Tick(ctx)
	att = w.kinds(EventNeedsAttention)
	if len(att) != 2 || att[1].Reason != AttentionInterrupted {
		t.Fatalf("interrupted: %+v", att)
	}
}

// Trace: each notice's issue, claim and handling reach the trace once, in
// that order, with the claim timed from the issue and the handling from the
// claim; a repeated issue, read or acknowledgement is no step.
func TestTraceSeesEachStepOnce(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	type step struct {
		name       string
		id         string
		start, end time.Time
	}
	var steps []step
	s := New(NewMemStore(), w, Config{Poll: 40 * time.Millisecond, Now: w.clock, Emit: w.emit, Boot: "boot-1",
		Sleep:      func(_ context.Context, d time.Duration) error { w.advance(d); return nil },
		RecordPath: func(n Notice) string { return "/records/" + n.ID + ".md" },
		Trace: func(name string, n Notice, start, end time.Time) {
			steps = append(steps, step{name, n.ID, start, end})
		}})
	t.Cleanup(s.Close)
	a := w.add("e-a", "claude")
	approved(t, s, a)
	n := issue(t, s, a.ID, "k1")
	issue(t, s, a.ID, "k1")
	w.advance(time.Second)
	first, err := s.Read(ctx, a, "")
	if err != nil || first.Token == "" {
		t.Fatalf("%+v %v", first, err)
	}
	if _, err := s.Read(ctx, a, n.ID); err != nil {
		t.Fatal(err)
	}
	w.advance(2 * time.Second)
	for range 2 {
		if _, err := s.Ack(ctx, a, n.ID, first.Token, OutcomeCompleted, "shipped"); err != nil {
			t.Fatal(err)
		}
	}
	if len(steps) != 3 || steps[0].name != TraceIssue || steps[1].name != TraceClaim || steps[2].name != TraceHandle {
		t.Fatalf("steps %+v", steps)
	}
	for _, st := range steps {
		if st.id != n.ID {
			t.Errorf("step of another notice: %+v", st)
		}
	}
	if got := steps[1].end.Sub(steps[1].start); got != time.Second {
		t.Errorf("claim timed %s from issue", got)
	}
	if got := steps[2].end.Sub(steps[2].start); got != 2*time.Second {
		t.Errorf("handling timed %s from claim", got)
	}
}
