package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/arafathusayn/herdsman/internal/ledger"
)

// hookRepo is a repository with approved teardown hooks.
func hookRepo(t *testing.T, config string) (*WorktreeService, string, string) {
	t.Helper()
	s, repo, worktree, _ := verbRepo(t)
	if err := os.WriteFile(filepath.Join(filepath.Dir(repo), "herdsman.config.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Approvals = filepath.Join(t.TempDir(), "approvals.json")
	return s, repo, worktree
}

func approveHooks(t *testing.T, s *WorktreeService, repo string, templates ...string) {
	t.Helper()
	store, err := s.approvals()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Approve(repo, templates); err != nil {
		t.Fatal(err)
	}
}

func TestAnEmptyPlanStillRunsTheStopHooks(t *testing.T) {
	const command = "touch hook-ran"
	s, repo, worktree := hookRepo(t, `{"hooks":{"pre-teardown":"`+command+`"}}`)
	approveHooks(t, s, repo, command)
	useLedger(t, setupLedger(t).service)
	ctx := context.Background()
	planned, err := s.TeardownPlan(ctx, WorktreeTeardownPlanRequest{Dir: repo, Selector: "task"})
	if err != nil || !planned.Hooks {
		t.Fatalf("planned = %+v, err = %v", planned, err)
	}
	done, err := s.Teardown(ctx, WorktreeTeardownRequest{Dir: repo, Plans: planned.Plans})
	if err != nil || done.Reports[0].Error != "" {
		t.Fatalf("done = %+v, err = %v", done, err)
	}
	if _, err := os.Stat(filepath.Join(worktree, "hook-ran")); err != nil {
		t.Fatal(err)
	}
}

func TestPlansWithoutHooksSayNone(t *testing.T) {
	s, repo, _, _ := verbRepo(t)
	useLedger(t, setupLedger(t).service)
	planned, err := s.TeardownPlan(context.Background(), WorktreeTeardownPlanRequest{Dir: repo, Selector: "task"})
	if err != nil || planned.Hooks {
		t.Fatalf("planned = %+v, err = %v", planned, err)
	}
}

func TestAFailingStopHookSignalsNothing(t *testing.T) {
	const command = "false"
	s, repo, worktree := hookRepo(t, `{"hooks":{"pre-teardown":"`+command+`"}}`)
	approveHooks(t, s, repo, command)
	world := setupLedger(t).service
	useLedger(t, world)
	ctx := context.Background()
	fake := ledger.Process{Pid: 5, Group: 5, Parent: 1, Start: ledgerStart, Command: "server"}
	world.Read = func(_ context.Context, base ledger.Snapshot) (ledger.Snapshot, error) {
		base.Processes, base.Commands = []ledger.Process{fake}, map[int]string{5: "server"}
		return base, nil
	}
	if _, err := s.ResourceAdd(ctx, WorktreeResourceAddRequest{Dir: worktree, Execution: "zz", Kind: ledger.KindProcess, ID: "5"}); err != nil {
		t.Fatal(err)
	}
	planned, err := s.TeardownPlan(ctx, WorktreeTeardownPlanRequest{Dir: repo, Selector: "task"})
	if err != nil {
		t.Fatal(err)
	}
	signals := 0
	world.Kill = func(int, syscall.Signal) error { signals++; return nil }
	done, err := s.Teardown(ctx, WorktreeTeardownRequest{Dir: repo, Plans: planned.Plans})
	if err != nil || done.Reports[0].Error == "" || signals != 0 {
		t.Fatalf("done = %+v, signals = %d, err = %v", done, signals, err)
	}
}

func TestAnUnapprovedHookIsReported(t *testing.T) {
	for _, hook := range []string{"pre-teardown", "post-teardown"} {
		s, repo, _ := hookRepo(t, `{"hooks":{"`+hook+`":"true"}}`)
		useLedger(t, setupLedger(t).service)
		ctx := context.Background()
		planned, err := s.TeardownPlan(ctx, WorktreeTeardownPlanRequest{Dir: repo, Selector: "task"})
		if err != nil {
			t.Fatal(err)
		}
		done, err := s.Teardown(ctx, WorktreeTeardownRequest{Dir: repo, Plans: planned.Plans})
		if err != nil || !strings.Contains(done.Reports[0].Error, "not approved") {
			t.Fatalf("%s: done = %+v, err = %v", hook, done, err)
		}
	}
}

func TestASecondTeardownOfATaskIsRefusedWhileOneRuns(t *testing.T) {
	s, repo, _, common := verbRepo(t)
	useLedger(t, setupLedger(t).service)
	release, err := lockTeardown(common, "task")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	refused, err := s.Teardown(ctx, WorktreeTeardownRequest{Dir: repo, Plans: []ledger.Plan{{Task: "task"}}})
	if err != nil || !strings.Contains(refused.Reports[0].Error, "already running") {
		t.Fatalf("refused = %+v, err = %v", refused, err)
	}
	release()
	again, err := lockTeardown(common, "task")
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	again()
}

func TestLockTeardownRefusesWhatItCannotLock(t *testing.T) {
	if _, err := lockTeardown(t.TempDir(), "a/b"); err == nil {
		t.Fatal("a bad name")
	}
	folder := t.TempDir()
	if err := os.MkdirAll(filepath.Join(folder, "herdsman", "logs", "a", "teardown.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := lockTeardown(folder, "a"); err == nil {
		t.Fatal("a folder as the lock")
	}
}

func TestARunThatNeverEndedIsReportedOnTheNext(t *testing.T) {
	s, repo, _, common := verbRepo(t)
	useLedger(t, setupLedger(t).service)
	useLedgerCommon(t, common)
	ctx := context.Background()
	planned, err := s.TeardownPlan(ctx, WorktreeTeardownPlanRequest{Dir: repo, Selector: "task"})
	if err != nil {
		t.Fatal(err)
	}
	done, err := s.Teardown(ctx, WorktreeTeardownRequest{Dir: repo, Plans: planned.Plans})
	if err != nil || !strings.Contains(done.Reports[0].Warning, "did not finish") {
		t.Fatalf("done = %+v, err = %v", done, err)
	}
	second, err := s.Teardown(ctx, WorktreeTeardownRequest{Dir: repo, Plans: planned.Plans})
	if err != nil || second.Reports[0].Warning != "" {
		t.Fatalf("second = %+v, err = %v", second, err)
	}
}

// useLedgerCommon leaves a log whose run wrote its plan and stopped there.
func useLedgerCommon(t *testing.T, common string) {
	t.Helper()
	folder, err := ledger.LogFolder(common, "task")
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(`{"event":"plan"}` + "\n")
	if err := os.WriteFile(filepath.Join(folder, "teardown.log"), line, 0o600); err != nil {
		t.Fatal(err)
	}
}
