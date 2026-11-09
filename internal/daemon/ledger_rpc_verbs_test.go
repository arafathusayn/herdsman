package daemon

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/ledger"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/wtree"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// verbClient reaches a service through a daemon's real RPC handler.
func verbClient(t *testing.T, service *WorktreeService) daemonv1.LedgerService {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.WriteFile(bin, []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	d := New(Config{PrivateHome: root, Home: root, Version: testVersion, Executable: bin, Log: func(string) {}})
	d.opsCtx, d.worktrees = context.Background(), service
	server := httptest.NewServer(d.rpcHandler())
	t.Cleanup(server.Close)
	return daemonv1.NewLedgerServiceClient(server.Client(), server.URL)
}

func TestTheVerbsRunOverTheGeneratedClient(t *testing.T) {
	s, repo, worktree, _ := verbRepo(t)
	w := setupLedger(t)
	useLedger(t, w.service)
	w.pids = []ledger.Process{{Pid: 5, Group: 5, Parent: 1, Start: ledgerStart, Command: "server"}}
	client := verbClient(t, s)
	ctx := context.Background()
	added, err := client.RegisterResource(ctx, &daemonv1.RegisterResourceRequest{Dir: worktree, Execution: "zz", Kind: ledger.KindProcess, Id: "5"})
	if err != nil || added.GetRecord().GetTask() != "task" {
		t.Fatalf("%v %v", added, err)
	}
	health, err := client.LedgerHealth(ctx, &daemonv1.LedgerHealthRequest{Dir: worktree})
	if err != nil || len(health.GetRunning()) != 1 || health.GetRunning()[0].GetId() != "5" {
		t.Fatalf("%v %v", health, err)
	}
	planned, err := client.TeardownPlans(ctx, &daemonv1.TeardownPlansRequest{Dir: repo, Selector: "task"})
	if err != nil || len(planned.GetPlans()) != 1 {
		t.Fatalf("%v %v", planned, err)
	}
	done, err := client.Teardown(ctx, &daemonv1.TeardownRequest{Dir: repo, Plans: planned.GetPlans(), GraceSeconds: 1})
	if err != nil || done.GetReports()[0].GetError() != "" || len(done.GetReports()[0].GetOutcomes()) != 1 {
		t.Fatalf("%v %v", done, err)
	}
	if op := oneOperation(t, s.engine); op.State != ops.OpDone || op.Kind != "teardown" {
		t.Fatalf("%+v", op)
	}
}

func TestTheVerbsRefuseWhatTheyCannotServe(t *testing.T) {
	s, _, _, _ := verbRepo(t)
	client := verbClient(t, s)
	ctx := context.Background()
	if _, err := client.TeardownPlans(ctx, &daemonv1.TeardownPlansRequest{Dir: "relative"}); err == nil {
		t.Error("plans")
	}
	if _, err := client.Teardown(ctx, &daemonv1.TeardownRequest{Dir: "relative"}); err == nil {
		t.Error("teardown")
	}
	if _, err := client.Teardown(ctx, &daemonv1.TeardownRequest{Dir: "relative", Plans: [][]byte{[]byte("not a document")}}); err == nil {
		t.Error("a bad document")
	}
	if _, err := client.RegisterResource(ctx, &daemonv1.RegisterResourceRequest{Dir: "relative"}); err == nil {
		t.Error("register")
	}
	if _, err := client.LedgerHealth(ctx, &daemonv1.LedgerHealthRequest{Dir: "relative"}); err == nil {
		t.Error("health")
	}
}

func TestAVerbIsRefusedWhenTheDaemonCannotRunIt(t *testing.T) {
	ctx := context.Background()
	inspect := ledgerRPC{inspect: true}
	if _, err := inspect.TeardownPlans(ctx, &daemonv1.TeardownPlansRequest{}); err == nil {
		t.Error("plans in inspect mode")
	}
	if _, err := inspect.Teardown(ctx, &daemonv1.TeardownRequest{}); err == nil {
		t.Error("teardown in inspect mode")
	}
	if _, err := inspect.RegisterResource(ctx, &daemonv1.RegisterResourceRequest{}); err == nil {
		t.Error("register in inspect mode")
	}
	if _, err := inspect.LedgerHealth(ctx, &daemonv1.LedgerHealthRequest{}); err == nil {
		t.Error("health in inspect mode")
	}
	root := t.TempDir()
	d := New(Config{PrivateHome: root, Home: root, Version: testVersion, Executable: filepath.Join(root, "bin"), Log: func(string) {}})
	if _, _, err := (ledgerRPC{d: d}).admit(); err == nil {
		t.Error("before the daemon started")
	}
	d.opsCtx = ctx
	if _, _, err := (ledgerRPC{d: d}).admit(); err == nil {
		t.Error("with no worktree service")
	}
}

func raiseOn(t *testing.T, common, statement string) {
	t.Helper()
	state, err := wtree.OpenState(common)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err := state.DB().Exec(`CREATE TRIGGER fail_it BEFORE ` + statement + ` ON resources BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatal(err)
	}
}

func TestAPlanReportsAnObservationItCannotStore(t *testing.T) {
	w := setupLedger(t, "a")
	ctx := context.Background()
	w.pids = []ledger.Process{{Pid: 5, Group: 5, Parent: 1, Start: ledgerStart, Command: "server"}}
	if _, err := w.service.AddResource(ctx, w.request("a"), ledger.Record{Execution: "zz", Kind: ledger.KindProcess, ID: "5"}, ""); err != nil {
		t.Fatal(err)
	}
	raiseOn(t, w.common, "UPDATE")
	if _, err := w.service.Plan(ctx, w.request("a")); err == nil {
		t.Error("plan")
	}
	if _, err := w.service.Resources(ctx, w.request("a")); err == nil {
		t.Error("resources")
	}
}

func TestARecordTheTableRefusesIsReported(t *testing.T) {
	w := setupLedger(t, "a")
	raiseOn(t, w.common, "INSERT")
	if _, err := w.service.AddResource(context.Background(), w.request("a"), ledger.Record{Execution: "zz", Kind: ledger.KindVolume, ID: "v"}, ""); err == nil {
		t.Fatal("recorded")
	}
}

func TestAnUnreadableWorktreeTableIsReported(t *testing.T) {
	w := setupLedger(t, "a")
	dropTable(t, w.common, "worktrees")
	if _, err := w.service.Plan(context.Background(), w.request("a")); err == nil {
		t.Fatal("plan")
	}
}

func TestARouteListsItsOwnTasks(t *testing.T) {
	s, repo, _, common := verbRepo(t)
	useLedger(t, setupLedger(t).service)
	state, err := wtree.OpenState(common)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.DB().Exec(`INSERT INTO tasks (task, route, created_at) VALUES ('task', 'r', 1)`); err != nil {
		state.Close()
		t.Fatal(err)
	}
	state.Close()
	planned, err := s.TeardownPlan(context.Background(), WorktreeTeardownPlanRequest{Dir: repo, Route: "r"})
	if err != nil || len(planned.Plans) != 1 {
		t.Fatalf("%+v %v", planned, err)
	}
}

func TestEveryStepReportsWhatStopsIt(t *testing.T) {
	const command = "true"
	s, repo, _ := hookRepo(t, `{"hooks":{"pre-teardown":"`+command+`","post-teardown":"`+command+`"}}`)
	useLedger(t, setupLedger(t).service)
	ctx := context.Background()
	for _, run := range []teardownApply{runBeforeHooks, runStop, runAfterHooks} {
		if err := run(ctx, s.journal, "op", teardownStep{Dir: "relative", Plan: ledger.Plan{Task: "task"}}); err == nil {
			t.Error("a bad folder")
		}
	}
	for _, run := range []teardownApply{runBeforeHooks, runAfterHooks} {
		if err := run(ctx, s.journal, "op", teardownStep{Dir: repo, Plan: ledger.Plan{Task: "absent"}}); err == nil {
			t.Error("an unknown task")
		}
		if err := run(ctx, s.journal, "op", teardownStep{Dir: repo, Plan: ledger.Plan{Task: "task"}}); err == nil {
			t.Error("a hook nobody approved")
		}
	}
}

func TestAnOperationThatCannotBeJournaledIsReported(t *testing.T) {
	s, repo, _, _ := verbRepo(t)
	useLedger(t, setupLedger(t).service)
	r, err := s.open(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.teardownOne(ctx, r, WorktreeTeardownRequest{Dir: repo}, ledger.Plan{Task: "task"}); err == nil {
		t.Fatal("journaled under a cancelled context")
	}
}
