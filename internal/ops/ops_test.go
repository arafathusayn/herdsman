package ops_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/ops"
)

// scripted is an effect whose verdict and Apply result the test sets.
type scripted struct {
	applied int
	verdict ops.Verdict
	applyEr error
	seen    []ops.Step
}

func (s *scripted) Apply(_ context.Context, st ops.Step) error {
	s.applied++
	s.seen = append(s.seen, st)
	return s.applyEr
}

func (s *scripted) Probe(context.Context, ops.Step) (ops.Verdict, string, error) {
	return s.verdict, "probe detail", nil
}

func openEngine(t *testing.T, o ops.Options) (*ops.Engine, store.Conn) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "s")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "a.db"), store.Writer, store.KindAccount)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return ops.NewEngine(db, o), db
}

func oneStep(retry ops.Retry) ops.Spec {
	return ops.Spec{Kind: "k", Plan: "p", Locks: []string{ops.TaskLock("t1")},
		Steps: []ops.StepSpec{{Name: "s", Effect: "fx", Key: "agent-a", Spec: "{}", Retry: retry}}}
}

// crashBefore stops the engine right after the step is intended, the way a kill would.
func intendedOp(t *testing.T, e *ops.Engine, s ops.Spec) *ops.Operation {
	t.Helper()
	op, err := e.Submit(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.MarkIntendedForTest(op.ID, 0); err != nil {
		t.Fatal(err)
	}
	return op
}

func TestReconcileVerdicts(t *testing.T) {
	cases := []struct {
		name     string
		verdict  ops.Verdict
		retry    ops.Retry
		wantOp   ops.OpState
		wantStep ops.StepState
		applied  int
	}{
		{"found and matching is done, no second effect", ops.Matches, ops.RetryNever, ops.OpDone, ops.StepDone, 0},
		{"absent runs again", ops.Absent, ops.RetryNever, ops.OpDone, ops.StepDone, 1},
		{"different is a conflict", ops.Different, ops.RetryIdempotent, ops.OpConflict, ops.StepConflict, 0},
		{"unknown and never stops", ops.Unknown, ops.RetryNever, ops.OpStopped, ops.StepIntended, 0},
		{"unknown and idempotent runs again", ops.Unknown, ops.RetryIdempotent, ops.OpDone, ops.StepDone, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := &scripted{verdict: c.verdict}
			e, _ := openEngine(t, ops.Options{Effects: map[string]ops.Effect{"fx": fx}})
			op := intendedOp(t, e, oneStep(c.retry))
			if err := e.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			got, err := e.Get(op.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != c.wantOp || got.Steps[0].State != c.wantStep || fx.applied != c.applied {
				t.Fatalf("got op %s step %s applied %d: %+v", got.State, got.Steps[0].State, fx.applied, got)
			}
			if c.wantOp == ops.OpStopped && !strings.Contains(got.Reason, "resolve") {
				t.Fatalf("no resume line in %q", got.Reason)
			}
		})
	}
}

func TestResolveAfterStop(t *testing.T) {
	for _, c := range []struct {
		d       ops.Decision
		applied int
	}{{ops.ResolveDone, 0}, {ops.ResolveRerun, 1}} {
		fx := &scripted{verdict: ops.Unknown}
		e, _ := openEngine(t, ops.Options{Effects: map[string]ops.Effect{"fx": fx}})
		ctx := context.Background()
		op := intendedOp(t, e, oneStep(ops.RetryNever))
		if err := e.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		if err := e.Resolve(op.ID, 0, c.d); err != nil {
			t.Fatal(err)
		}
		got, err := e.Drive(ctx, op.ID)
		if err != nil || got.State != ops.OpDone || fx.applied != c.applied {
			t.Fatalf("%v: %+v applied %d err %v", c.d, got, fx.applied, err)
		}
	}
}

func TestResolveRefusesARunningOperation(t *testing.T) {
	e, _ := openEngine(t, ops.Options{Effects: map[string]ops.Effect{"fx": &scripted{}}})
	op, err := e.Submit(context.Background(), oneStep(ops.RetryNever))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Resolve(op.ID, 0, ops.ResolveDone); err == nil {
		t.Fatal("a running operation was resolved")
	}
}

func TestLocksSerializeAndReleaseOnFinish(t *testing.T) {
	e, _ := openEngine(t, ops.Options{Effects: map[string]ops.Effect{"fx": &scripted{}}})
	ctx := context.Background()
	a, err := e.Submit(ctx, oneStep(ops.RetryNever))
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Submit(ctx, oneStep(ops.RetryNever))
	var le *ops.LockedError
	if !errors.As(err, &le) || le.Holder != a.ID || le.Lock != ops.TaskLock("t1") {
		t.Fatalf("second submit: %v", err)
	}
	other := oneStep(ops.RetryNever)
	other.Locks = []string{ops.RefLock("repo", "refs/heads/main")}
	if _, err := e.Submit(ctx, other); err != nil {
		t.Fatalf("a different lock must not wait: %v", err)
	}
	if _, err := e.Drive(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Submit(ctx, oneStep(ops.RetryNever)); err != nil {
		t.Fatalf("after finish: %v", err)
	}
}

func TestSubmitWaitingGetsTheLockOnceFree(t *testing.T) {
	e, _ := openEngine(t, ops.Options{Effects: map[string]ops.Effect{"fx": &scripted{}}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a, err := e.Submit(ctx, oneStep(ops.RetryNever))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := e.SubmitWaiting(ctx, oneStep(ops.RetryNever)); done <- err }()
	time.Sleep(30 * time.Millisecond)
	if _, err := e.Drive(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestIdempotencyKey(t *testing.T) {
	e, _ := openEngine(t, ops.Options{Effects: map[string]ops.Effect{"fx": &scripted{}}})
	ctx := context.Background()
	s := oneStep(ops.RetryNever)
	s.Key = "same"
	a, err := e.Submit(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.Submit(ctx, s)
	if err != nil || a.ID != b.ID {
		t.Fatalf("repeat: %v %v", b, err)
	}
	s.Plan = "another plan"
	if _, err := e.Submit(ctx, s); !errors.Is(err, ops.ErrKeyReused) {
		t.Fatalf("different work under one key: %v", err)
	}
}

func TestPreconditionChangedForcesANewPlan(t *testing.T) {
	head := "abc"
	check := func(context.Context, string) (string, error) { return head, nil }
	fx := &scripted{}
	e, _ := openEngine(t, ops.Options{Effects: map[string]ops.Effect{"fx": fx}, Checks: map[string]ops.Check{"head": check}})
	ctx := context.Background()
	s := oneStep(ops.RetryNever)
	s.Preconditions = []ops.Precondition{{Name: "head:main", Want: "abc"}}
	op, err := e.Submit(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	head = "def"
	got, err := e.Drive(ctx, op.ID)
	if !errors.Is(err, ops.ErrPrecondition) || fx.applied != 0 {
		t.Fatalf("drive: %v, applied %d", err, fx.applied)
	}
	if got.State != ops.OpStale {
		t.Fatalf("state %s", got.State)
	}
	if held, _ := e.HeldLocks(); len(held) != 0 {
		t.Fatalf("a stale plan keeps locks: %v", held)
	}
	if _, err := e.Submit(ctx, s); !errors.Is(err, ops.ErrPrecondition) {
		t.Fatalf("submit on a changed head: %v", err)
	}
}

func TestApplyFailureUsesTheProbe(t *testing.T) {
	boom := errors.New("boom")
	for _, c := range []struct {
		verdict ops.Verdict
		want    ops.OpState
	}{{ops.Matches, ops.OpDone}, {ops.Absent, ops.OpFailed}, {ops.Different, ops.OpConflict}} {
		fx := &scripted{verdict: c.verdict, applyEr: boom}
		e, _ := openEngine(t, ops.Options{Effects: map[string]ops.Effect{"fx": fx}})
		ctx := context.Background()
		op, err := e.Submit(ctx, oneStep(ops.RetryNever))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := e.Drive(ctx, op.ID)
		if got == nil || got.State != c.want {
			t.Fatalf("verdict %v: %+v", c.verdict, got)
		}
		if held, _ := e.HeldLocks(); (c.want == ops.OpConflict) != (len(held) == 1) {
			t.Fatalf("verdict %v locks %v", c.verdict, held)
		}
	}
}

func TestStepWithoutARegisteredEffectStops(t *testing.T) {
	e, _ := openEngine(t, ops.Options{Effects: map[string]ops.Effect{"fx": &scripted{}}})
	ctx := context.Background()
	op := intendedOp(t, e, oneStep(ops.RetryIdempotent))
	e2 := ops.NewEngine(ops.EngineConnForTest(e), ops.Options{})
	if err := e2.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := e2.Get(op.ID)
	if got.State != ops.OpStopped {
		t.Fatalf("state %s", got.State)
	}
}

func TestOutboxOncePerStepAndKeptOnSinkFailure(t *testing.T) {
	e, _ := openEngine(t, ops.Options{Effects: map[string]ops.Effect{"fx": &scripted{}}})
	ctx := context.Background()
	s := oneStep(ops.RetryNever)
	s.Steps[0].Events = []ops.Event{{Topic: "a", Payload: "1"}, {Topic: "b", Payload: "2"}}
	op, err := e.Submit(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := e.Undelivered(); n != 0 {
		t.Fatalf("events before the effect: %d", n)
	}
	if _, err := e.Drive(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	sink := &memSink{fail: true}
	if err := e.DeliverOutbox(ctx, sink); err == nil {
		t.Fatal("sink failure hidden")
	}
	if n, _ := e.Undelivered(); n != 2 {
		t.Fatalf("undelivered %d", n)
	}
	sink.fail = false
	if err := e.DeliverOutbox(ctx, sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.keys) != 2 || sink.keys[0] == sink.keys[1] {
		t.Fatalf("keys %v", sink.keys)
	}
	if err := e.DeliverOutbox(ctx, sink); err != nil || len(sink.keys) != 2 {
		t.Fatalf("redelivered: %v %v", sink.keys, err)
	}
}

type memSink struct {
	fail bool
	keys []string
}

func (m *memSink) Deliver(_ context.Context, ev ops.OutboxEvent) error {
	if m.fail {
		return errors.New("sink down")
	}
	m.keys = append(m.keys, ev.Key)
	return nil
}

func TestSubmitValidates(t *testing.T) {
	e, _ := openEngine(t, ops.Options{Effects: map[string]ops.Effect{"fx": &scripted{}}})
	ctx := context.Background()
	bad := []func(*ops.Spec){
		func(s *ops.Spec) { s.Kind = "" },
		func(s *ops.Spec) { s.Steps = nil },
		func(s *ops.Spec) { s.Steps[0].Key = "" },
		func(s *ops.Spec) { s.Steps[0].Effect = "nope" },
		func(s *ops.Spec) { s.Steps[0].Retry = "sometimes" },
		func(s *ops.Spec) { s.Steps = append(s.Steps, s.Steps[0]) },
		func(s *ops.Spec) { s.Preconditions = []ops.Precondition{{Name: "unknown:x", Want: "y"}} },
	}
	for i, f := range bad {
		s := oneStep(ops.RetryNever)
		f(&s)
		if _, err := e.Submit(ctx, s); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
	s := oneStep("")
	op, err := e.Submit(ctx, s)
	if err != nil || op.Steps[0].Retry != ops.RetryNever {
		t.Fatalf("default retry: %+v %v", op, err)
	}
}

func TestDriveOfAFinishedOperationDoesNothing(t *testing.T) {
	fx := &scripted{}
	e, _ := openEngine(t, ops.Options{Effects: map[string]ops.Effect{"fx": fx}})
	ctx := context.Background()
	op, _ := e.Submit(ctx, oneStep(ops.RetryNever))
	if _, err := e.Drive(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Drive(ctx, op.ID); err != nil || fx.applied != 1 {
		t.Fatalf("applied %d err %v", fx.applied, err)
	}
	if _, err := e.Get("missing"); !errors.Is(err, ops.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestDriveLeavesAnIntendedStepForReconcile(t *testing.T) {
	fx := &scripted{}
	e, _ := openEngine(t, ops.Options{Effects: map[string]ops.Effect{"fx": fx}})
	op := intendedOp(t, e, oneStep(ops.RetryNever))
	if _, err := e.Drive(context.Background(), op.ID); !errors.Is(err, ops.ErrNeedsReconcile) || fx.applied != 0 {
		t.Fatalf("drive on an intended step: %v applied %d", err, fx.applied)
	}
}

func TestCancelledContextStopsBetweenSteps(t *testing.T) {
	fx := &scripted{}
	e, _ := openEngine(t, ops.Options{Effects: map[string]ops.Effect{"fx": fx}})
	ctx, cancel := context.WithCancel(context.Background())
	op, _ := e.Submit(ctx, oneStep(ops.RetryNever))
	cancel()
	if _, err := e.Drive(ctx, op.ID); !errors.Is(err, context.Canceled) || fx.applied != 0 {
		t.Fatalf("%v applied %d", err, fx.applied)
	}
}

func TestSchemaFileMatchesTheShippedMigration(t *testing.T) {
	a, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../daemon/store/schema/account/0002_operations.sql")
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("internal/ops/schema.sql and the account migration differ")
	}
}
