package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/ledger"
	"github.com/arafathusayn/herdsman/internal/ops"
)

// journalBench is a service with an engine on a real account database.
type journalBench struct {
	db   store.Conn
	stop string
}

func journalFor(t *testing.T, service *WorktreeService) *journalBench {
	t.Helper()
	dir := shortDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "daemon"), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "daemon", "account.db"), store.Writer, store.KindAccount)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bench := &journalBench{db: db}
	bench.restart(service)
	return bench
}

// restart is a new process over the same journal: nothing in memory survives.
func (b *journalBench) restart(service *WorktreeService) *ops.Engine {
	journal := newTeardownJournal(service)
	engine := ops.NewEngine(b.db, ops.Options{Effects: journal.effects(), Fault: func(point string) {
		if b.stop != "" && point == b.stop {
			panic("crash at " + point)
		}
	}})
	service.useTeardownOperations(journal, engine)
	return engine
}

// crash runs a teardown until the fault point, as a process dying there.
func (b *journalBench) crash(t *testing.T, service *WorktreeService, point string, request WorktreeTeardownRequest) {
	t.Helper()
	b.stop = point
	defer func() {
		b.stop = ""
		if recover() == nil {
			t.Fatalf("the run went past %s", point)
		}
	}()
	_, _ = service.Teardown(context.Background(), request)
}

func oneOperation(t *testing.T, engine *ops.Engine) *ops.Operation {
	t.Helper()
	list, err := engine.List()
	if err != nil || len(list) == 0 {
		t.Fatalf("operations %v %v", list, err)
	}
	return list[len(list)-1]
}

func TestATeardownIsJournaledWithItsSteps(t *testing.T) {
	const before, after = "touch before", "touch after"
	s, repo, worktree := hookRepo(t, `{"hooks":{"pre-teardown":"`+before+`","post-teardown":"`+after+`"}}`)
	approveHooks(t, s, repo, before, after)
	useLedger(t, setupLedger(t).service)
	ctx := context.Background()
	planned, err := s.TeardownPlan(ctx, WorktreeTeardownPlanRequest{Dir: repo, Selector: "task"})
	if err != nil {
		t.Fatal(err)
	}
	done, err := s.Teardown(ctx, WorktreeTeardownRequest{Dir: repo, Plans: planned.Plans})
	if err != nil || done.Reports[0].Error != "" {
		t.Fatalf("%+v %v", done, err)
	}
	op := oneOperation(t, s.engine)
	if op.State != ops.OpDone || len(op.Steps) != 3 || op.Kind != "teardown" {
		t.Fatalf("%+v", op)
	}
	if _, err := os.Stat(filepath.Join(worktree, "before")); err != nil {
		t.Fatal(err)
	}
	held, err := s.engine.HeldLocks()
	if err != nil || len(held) != 0 {
		t.Fatalf("locks %v %v", held, err)
	}
}

func TestAHookCutOffByACrashIsNotReplayed(t *testing.T) {
	const command = "echo ran >> count"
	s, repo, worktree := hookRepo(t, `{"hooks":{"pre-teardown":"`+command+`"}}`)
	approveHooks(t, s, repo, command)
	useLedger(t, setupLedger(t).service)
	ctx := context.Background()
	planned, err := s.TeardownPlan(ctx, WorktreeTeardownPlanRequest{Dir: repo, Selector: "task"})
	if err != nil {
		t.Fatal(err)
	}
	bench := journalFor(t, s)
	request := WorktreeTeardownRequest{Dir: repo, Plans: planned.Plans}
	bench.crash(t, s, "step:0:intended", request)
	engine := bench.restart(s)
	if err := engine.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	op := oneOperation(t, engine)
	if op.State != ops.OpStopped || !strings.Contains(op.Reason, "may not be repeated") {
		t.Fatalf("%+v", op)
	}
	if _, err := os.Stat(filepath.Join(worktree, "count")); !os.IsNotExist(err) {
		t.Fatalf("the hook ran: %v", err)
	}
	refused, err := s.Teardown(ctx, request)
	if err != nil || !strings.Contains(refused.Reports[0].Error, "busy") {
		t.Fatalf("%+v %v", refused, err)
	}
}

func TestAStopCutOffByACrashFinishesOnRecovery(t *testing.T) {
	w := setupLedger(t)
	s, repo, worktree, common := verbRepo(t)
	useLedger(t, w.service)
	w.common = common
	ctx := context.Background()
	w.pids = []ledger.Process{{Pid: 5, Group: 5, Parent: 1, Start: ledgerStart, Command: "server"}}
	if _, err := s.ResourceAdd(ctx, WorktreeResourceAddRequest{Dir: worktree, Execution: "zz", Kind: ledger.KindProcess, ID: "5"}); err != nil {
		t.Fatal(err)
	}
	planned, err := s.TeardownPlan(ctx, WorktreeTeardownPlanRequest{Dir: repo, Selector: "task"})
	if err != nil {
		t.Fatal(err)
	}
	bench := journalFor(t, s)
	bench.crash(t, s, "step:0:intended", WorktreeTeardownRequest{Dir: repo, Plans: planned.Plans, GraceSeconds: 1})
	if len(w.calls) != 0 {
		t.Fatalf("calls %v", w.calls)
	}
	engine := bench.restart(s)
	if err := engine.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if op := oneOperation(t, engine); op.State != ops.OpDone {
		t.Fatalf("%+v", op)
	}
	if len(w.calls) == 0 {
		t.Fatal("recovery did not stop the process")
	}
}

func TestAStopFinishedBeforeACrashIsNotRepeated(t *testing.T) {
	w := setupLedger(t)
	s, repo, worktree, _ := verbRepo(t)
	useLedger(t, w.service)
	ctx := context.Background()
	w.pids = []ledger.Process{{Pid: 5, Group: 5, Parent: 1, Start: ledgerStart, Command: "server"}}
	if _, err := s.ResourceAdd(ctx, WorktreeResourceAddRequest{Dir: worktree, Execution: "zz", Kind: ledger.KindProcess, ID: "5"}); err != nil {
		t.Fatal(err)
	}
	planned, err := s.TeardownPlan(ctx, WorktreeTeardownPlanRequest{Dir: repo, Selector: "task"})
	if err != nil {
		t.Fatal(err)
	}
	bench := journalFor(t, s)
	bench.crash(t, s, "step:0:applied", WorktreeTeardownRequest{Dir: repo, Plans: planned.Plans, GraceSeconds: 1})
	w.pids = nil
	calls := len(w.calls)
	engine := bench.restart(s)
	if err := engine.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if op := oneOperation(t, engine); op.State != ops.OpDone {
		t.Fatalf("%+v", op)
	}
	if len(w.calls) != calls {
		t.Fatalf("signaled again: %v", w.calls)
	}
}

func TestAFailingHookEndsTheOperationAndFreesTheTask(t *testing.T) {
	const command = "false"
	s, repo, _ := hookRepo(t, `{"hooks":{"pre-teardown":"`+command+`"}}`)
	approveHooks(t, s, repo, command)
	useLedger(t, setupLedger(t).service)
	ctx := context.Background()
	planned, err := s.TeardownPlan(ctx, WorktreeTeardownPlanRequest{Dir: repo, Selector: "task"})
	if err != nil {
		t.Fatal(err)
	}
	done, err := s.Teardown(ctx, WorktreeTeardownRequest{Dir: repo, Plans: planned.Plans})
	if err != nil || done.Reports[0].Error == "" {
		t.Fatalf("%+v %v", done, err)
	}
	if op := oneOperation(t, s.engine); op.State != ops.OpFailed {
		t.Fatalf("%+v", op)
	}
	if held, err := s.engine.HeldLocks(); err != nil || len(held) != 0 {
		t.Fatalf("locks %v %v", held, err)
	}
}

func TestARefusedStopFailsTheOperationAndSaysWhy(t *testing.T) {
	w := setupLedger(t)
	s, repo, worktree, _ := verbRepo(t)
	useLedger(t, w.service)
	ctx := context.Background()
	w.pids = []ledger.Process{{Pid: 5, Group: 5, Parent: 1, Start: ledgerStart, Command: "server"}}
	if _, err := s.ResourceAdd(ctx, WorktreeResourceAddRequest{Dir: worktree, Execution: "zz", Kind: ledger.KindProcess, ID: "5"}); err != nil {
		t.Fatal(err)
	}
	planned, err := s.TeardownPlan(ctx, WorktreeTeardownPlanRequest{Dir: repo, Selector: "task"})
	if err != nil {
		t.Fatal(err)
	}
	w.service.Kill = func(int, syscall.Signal) error { return syscall.EPERM }
	done, err := s.Teardown(ctx, WorktreeTeardownRequest{Dir: repo, Plans: planned.Plans, GraceSeconds: 1})
	if err != nil || !strings.Contains(done.Reports[0].Error, "signal") {
		t.Fatalf("%+v %v", done, err)
	}
	if op := oneOperation(t, s.engine); op.State != ops.OpFailed {
		t.Fatalf("%+v", op)
	}
}

func TestProbeAnswersFromTheWorld(t *testing.T) {
	s, _, _, _ := verbRepo(t)
	fx := teardownEffect{journal: s.journal}
	if v, _, err := fx.Probe(context.Background(), ops.Step{StepSpec: ops.StepSpec{Effect: effectTeardownStop, Spec: "not json"}}); v != ops.Unknown || err == nil {
		t.Fatalf("%v %v", v, err)
	}
	if err := fx.Apply(context.Background(), ops.Step{StepSpec: ops.StepSpec{Effect: effectTeardownStop, Spec: "not json"}}); err == nil {
		t.Fatal("a bad spec applied")
	}
	if v, _, err := fx.Probe(context.Background(), ops.Step{StepSpec: ops.StepSpec{Effect: effectTeardownStop, Spec: `{"dir":"relative"}`}}); v != ops.Unknown || err == nil {
		t.Fatalf("%v %v", v, err)
	}
}
