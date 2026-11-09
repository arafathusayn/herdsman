package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/ledger"
	"github.com/arafathusayn/herdsman/internal/wtree"
)

var ledgerStart = time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

type ledgerWorld struct {
	common  string
	service *LedgerService
	calls   []int
	pids    []ledger.Process
	fail    error
}

func setupLedger(t *testing.T, tasks ...string) *ledgerWorld {
	t.Helper()
	common := t.TempDir()
	state, err := wtree.OpenState(common)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	for _, task := range tasks {
		if err := state.PutWorktree(context.Background(), wtree.Worktree{Task: task, Branch: task, Path: "/w/" + task}); err != nil {
			t.Fatal(err)
		}
	}
	w := &ledgerWorld{common: common}
	w.service = &LedgerService{
		Read: func(_ context.Context, base ledger.Snapshot) (ledger.Snapshot, error) {
			if w.fail != nil {
				return ledger.Snapshot{}, w.fail
			}
			base.Processes = append([]ledger.Process(nil), w.pids...)
			base.Commands = map[int]string{}
			for _, process := range w.pids {
				base.Commands[process.Pid] = process.Command
			}
			return base, nil
		},
		Kill: func(pid int, signal syscall.Signal) error {
			if signal == 0 {
				return syscall.ESRCH
			}
			w.calls = append(w.calls, pid)
			return nil
		},
		Run:    func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
		Engine: func(context.Context) string { return "docker" },
		Facts: func(_ context.Context, pid int) (time.Time, string, error) {
			if pid == 404 {
				return time.Time{}, "", errors.New("not running")
			}
			return ledgerStart, "server", nil
		},
	}
	return w
}

func (w *ledgerWorld) request(task string) LedgerRequest {
	return LedgerRequest{CommonDir: w.common, Task: task}
}

func TestPlanAndResourcesUseTheRecords(t *testing.T) {
	w := setupLedger(t, "a", "b")
	ctx := context.Background()
	w.pids = []ledger.Process{{Pid: 5, Group: 5, Parent: 1, Start: ledgerStart, Command: "server"}}
	if _, err := w.service.AddResource(ctx, w.request("a"), ledger.Record{Execution: "old", Kind: ledger.KindVolume, ID: "v"}, "hook"); err != nil {
		t.Fatal(err)
	}
	record, err := w.service.AddResource(ctx, w.request("a"), ledger.Record{Execution: "zz", Kind: ledger.KindProcess, ID: "5"}, "hook")
	if err != nil {
		t.Fatal(err)
	}
	if record.Command != "server" || !record.Start.Equal(ledgerStart) {
		t.Fatalf("record = %+v", record)
	}
	if _, err := w.service.AddResource(ctx, w.request("a"), ledger.Record{Execution: "zz", Kind: ledger.KindCompose, ID: "proj"}, "hook"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.service.AddResource(ctx, w.request("b"), ledger.Record{Execution: "q", Kind: ledger.KindVolume, ID: "x"}, ""); err != nil {
		t.Fatal(err)
	}
	plan, err := w.service.Plan(ctx, w.request("a"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Execution != "zz" || len(plan.Lines) == 0 || plan.Lines[0].Action != ledger.ActionStop {
		t.Fatalf("plan = %+v", plan)
	}
	resources, err := w.service.Resources(ctx, w.request("a"))
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Records) != 3 || resources.Plan.Task != "a" {
		t.Fatalf("resources = %+v", resources)
	}
	base, state, err := w.service.snapshotOf(ctx, w.request("a"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if base.Compose != "proj" || len(base.Others) != 1 || base.Others[0].Task != "b" || base.Earlier[0] != "old" {
		t.Fatalf("base = %+v", base)
	}
}

func TestTeardownStopsTheApprovedPlan(t *testing.T) {
	w := setupLedger(t, "a")
	ctx := context.Background()
	w.pids = []ledger.Process{{Pid: 5, Group: 5, Parent: 1, Start: ledgerStart, Command: "server"}}
	if _, err := w.service.AddResource(ctx, w.request("a"), ledger.Record{Execution: "zz", Kind: ledger.KindProcess, ID: "5"}, ""); err != nil {
		t.Fatal(err)
	}
	plan, err := w.service.Plan(ctx, w.request("a"))
	if err != nil {
		t.Fatal(err)
	}
	outcomes, err := w.service.Teardown(ctx, TeardownRequest{LedgerRequest: w.request("a"), Approved: plan, Grace: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if len(w.calls) == 0 || len(outcomes) != 1 {
		t.Fatalf("calls = %v, outcomes = %v", w.calls, outcomes)
	}
	if _, err := os.Stat(filepath.Join(w.common, "herdsman", "logs", "a", "teardown.log")); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerServiceErrors(t *testing.T) {
	w := setupLedger(t, "a")
	ctx := context.Background()
	if _, err := w.service.Plan(ctx, w.request("missing")); err == nil || !strings.Contains(err.Error(), "no worktree") {
		t.Fatalf("err = %v", err)
	}
	if _, err := w.service.Resources(ctx, w.request("missing")); err == nil {
		t.Fatal("resources of a missing task")
	}
	if _, err := w.service.Teardown(ctx, TeardownRequest{LedgerRequest: w.request("missing")}); err == nil {
		t.Fatal("teardown of a missing task")
	}
	w.fail = errors.New("no read")
	if _, err := w.service.Plan(ctx, w.request("a")); err == nil {
		t.Fatal("plan with a failed read")
	}
	if _, err := w.service.Resources(ctx, w.request("a")); err == nil {
		t.Fatal("resources with a failed read")
	}
	if _, err := w.service.AddResource(ctx, w.request("a"), ledger.Record{Kind: ledger.KindVolume, ID: "v"}, ""); err == nil || !strings.Contains(err.Error(), "no execution") {
		t.Fatalf("err = %v", err)
	}
	for _, id := range []string{"abc", "404"} {
		if _, err := w.service.AddResource(ctx, w.request("a"), ledger.Record{Execution: "e", Kind: ledger.KindProcess, ID: id}, ""); err == nil {
			t.Fatalf("process id %s accepted", id)
		}
	}
	if _, err := w.service.AddResource(ctx, w.request("a"), ledger.Record{Execution: "e", Kind: "", ID: "v"}, ""); err == nil {
		t.Fatal("a record with no kind")
	}
	bad := LedgerRequest{CommonDir: filepath.Join(w.common, "nowhere"), Task: "a"}
	if _, err := w.service.AddResource(ctx, bad, ledger.Record{}, ""); err == nil {
		t.Fatal("a missing repository")
	}
	if _, err := w.service.Plan(ctx, bad); err == nil {
		t.Fatal("plan of a missing repository")
	}
}

func TestPortBlockIsTen(t *testing.T) {
	block := portBlock("a")
	if block.Last != block.First+9 {
		t.Fatalf("block = %+v", block)
	}
}

func TestNewLedgerServiceUsesTheMachine(t *testing.T) {
	if s := NewLedgerService(); s.Read == nil || s.Kill == nil || s.Run == nil || s.Engine == nil || s.Facts == nil {
		t.Fatal("a field is missing")
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=a", "-c", "user.email=a@b.c"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// verbRepo is a real repository with one task worktree recorded in the state.
func verbRepo(t *testing.T) (*WorktreeService, string, string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo, worktree := filepath.Join(root, "repo"), filepath.Join(root, "wt")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "first")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "task", worktree)
	common := filepath.Join(repo, ".git")
	state, err := wtree.OpenState(common)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.PutWorktree(context.Background(), wtree.Worktree{Task: "task", Branch: "task", Path: worktree}); err != nil {
		t.Fatal(err)
	}
	state.Close()
	return &WorktreeService{}, repo, worktree, common
}

func useLedger(t *testing.T, service *LedgerService) {
	t.Helper()
	old := ledgerService
	ledgerService = service
	t.Cleanup(func() { ledgerService = old })
}

func TestVerbMethods(t *testing.T) {
	s, repo, worktree, _ := verbRepo(t)
	ctx := context.Background()
	w := setupLedger(t)
	useLedger(t, w.service)
	w.pids = []ledger.Process{{Pid: 5, Group: 5, Parent: 1, Start: ledgerStart, Command: "server"}}
	record, err := s.ResourceAdd(ctx, WorktreeResourceAddRequest{Dir: worktree, Execution: "zz", Kind: ledger.KindProcess, ID: "5"})
	if err != nil {
		t.Fatal(err)
	}
	if record.Task != "task" || record.Command != "server" {
		t.Fatalf("record = %+v", record)
	}
	if _, err := s.ResourceAdd(ctx, WorktreeResourceAddRequest{Dir: repo, Task: "task", Kind: ledger.KindVolume, ID: "v", Execution: "zz"}); err != nil {
		t.Fatal(err)
	}
	plans, err := s.TeardownPlan(ctx, WorktreeTeardownPlanRequest{Dir: worktree})
	if err != nil || len(plans.Plans) != 1 || plans.Plans[0].Task != "task" {
		t.Fatalf("plans = %+v, err = %v", plans, err)
	}
	named, err := s.TeardownPlan(ctx, WorktreeTeardownPlanRequest{Dir: repo, Selector: "task"})
	if err != nil || len(named.Plans) != 1 {
		t.Fatalf("named = %+v, err = %v", named, err)
	}
	all, err := s.TeardownPlan(ctx, WorktreeTeardownPlanRequest{Dir: repo, All: true})
	if err != nil || len(all.Plans) != 1 {
		t.Fatalf("all = %+v, err = %v", all, err)
	}
	route, err := s.TeardownPlan(ctx, WorktreeTeardownPlanRequest{Dir: repo, Route: "no-route"})
	if err != nil || len(route.Plans) != 0 {
		t.Fatalf("route = %+v, err = %v", route, err)
	}
	health, err := s.LedgerHealth(ctx, WorktreeLedgerHealthRequest{Dir: worktree})
	if err != nil || len(health.Running) != 1 || health.Running[0].ID != "5" {
		t.Fatalf("health = %+v, err = %v", health, err)
	}
	done, err := s.Teardown(ctx, WorktreeTeardownRequest{Dir: repo, Plans: plans.Plans, GraceSeconds: 1})
	if err != nil || done.Reports[0].Error != "" {
		t.Fatalf("done = %+v, err = %v", done, err)
	}
	if len(w.calls) == 0 {
		t.Fatal("nothing was signaled")
	}
	failed, err := s.Teardown(ctx, WorktreeTeardownRequest{Dir: repo, Plans: []ledger.Plan{{Task: "absent"}}})
	if err != nil || failed.Reports[0].Error == "" {
		t.Fatalf("failed = %+v, err = %v", failed, err)
	}
}

func TestVerbMethodsReportErrors(t *testing.T) {
	s, repo, _, _ := verbRepo(t)
	ctx := context.Background()
	w := setupLedger(t)
	useLedger(t, w.service)
	nowhere := filepath.Join(repo, "absent")
	if _, err := s.TeardownPlan(ctx, WorktreeTeardownPlanRequest{Dir: nowhere}); err == nil {
		t.Fatal("plan from a missing folder")
	}
	if _, err := s.Teardown(ctx, WorktreeTeardownRequest{Dir: nowhere}); err == nil {
		t.Fatal("teardown from a missing folder")
	}
	if _, err := s.ResourceAdd(ctx, WorktreeResourceAddRequest{Dir: nowhere}); err == nil {
		t.Fatal("add from a missing folder")
	}
	if _, err := s.LedgerHealth(ctx, WorktreeLedgerHealthRequest{Dir: nowhere}); err == nil {
		t.Fatal("health from a missing folder")
	}
	if _, err := s.TeardownPlan(ctx, WorktreeTeardownPlanRequest{Dir: repo, Selector: "pr:notanumber"}); err == nil {
		t.Fatal("a bad selector")
	}
	if _, err := s.TeardownPlan(ctx, WorktreeTeardownPlanRequest{Dir: repo, Selector: "unknown-task"}); err == nil {
		t.Fatal("an unknown task")
	}
	w.fail = errors.New("no read")
	if _, err := s.TeardownPlan(ctx, WorktreeTeardownPlanRequest{Dir: repo, All: true}); err == nil {
		t.Fatal("a failed read")
	}
}

func TestARecordWrittenDuringTeardownIsSeenBeforeTheNextSignal(t *testing.T) {
	w := setupLedger(t, "a", "b")
	ctx := context.Background()
	w.pids = []ledger.Process{
		{Pid: 5, Group: 5, Parent: 1, Start: ledgerStart, Command: "server"},
		{Pid: 6, Group: 6, Parent: 1, Start: ledgerStart, Command: "server"},
	}
	for _, id := range []string{"5", "6"} {
		if _, err := w.service.AddResource(ctx, w.request("a"), ledger.Record{Execution: "zz", Kind: ledger.KindProcess, ID: id}, ""); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := w.service.Plan(ctx, w.request("a"))
	if err != nil {
		t.Fatal(err)
	}
	kill := w.service.Kill
	w.service.Kill = func(pid int, signal syscall.Signal) error {
		if signal != 0 && len(w.calls) == 0 {
			if _, err := w.service.AddResource(ctx, w.request("b"), ledger.Record{Execution: "q", Kind: ledger.KindProcess, ID: "6"}, ""); err != nil {
				t.Error(err)
			}
		}
		return kill(pid, signal)
	}
	_, err = w.service.Teardown(ctx, TeardownRequest{LedgerRequest: w.request("a"), Approved: plan, Grace: time.Millisecond})
	var stop *ledger.StopError
	if !errors.As(err, &stop) {
		t.Fatalf("err = %v", err)
	}
	if len(w.calls) != 1 {
		t.Fatalf("calls = %v, want only the first pid", w.calls)
	}
}

func TestAuthorityRevokedDuringTeardownIsSeenBeforeTheNextSignal(t *testing.T) {
	w := setupLedger(t, "a")
	ctx := context.Background()
	w.pids = []ledger.Process{
		{Pid: 5, Group: 5, Parent: 1, Start: ledgerStart, Command: "server"},
		{Pid: 6, Group: 6, Parent: 1, Start: ledgerStart, Command: "server"},
	}
	for _, id := range []string{"5", "6"} {
		if _, err := w.service.AddResource(ctx, w.request("a"), ledger.Record{Execution: "zz", Kind: ledger.KindProcess, ID: id}, ""); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := w.service.Plan(ctx, w.request("a"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := wtree.OpenState(w.request("a").CommonDir)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	kill := w.service.Kill
	w.service.Kill = func(pid int, signal syscall.Signal) error {
		if signal != 0 && len(w.calls) == 0 {
			if _, err := state.DB().ExecContext(ctx, `DELETE FROM resources WHERE id = '6'`); err != nil {
				t.Error(err)
			}
		}
		return kill(pid, signal)
	}
	_, err = w.service.Teardown(ctx, TeardownRequest{LedgerRequest: w.request("a"), Approved: plan, Grace: time.Millisecond})
	var stop *ledger.StopError
	if !errors.As(err, &stop) {
		t.Fatalf("err = %v", err)
	}
	if len(w.calls) != 1 {
		t.Fatalf("calls = %v, want only the first pid", w.calls)
	}
}
