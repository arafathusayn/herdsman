package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/ledger"
	"github.com/arafathusayn/herdsman/internal/wtree"
)

func dropTable(t *testing.T, common, table string) {
	t.Helper()
	state, err := wtree.OpenState(common)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err := state.DB().Exec("DROP TABLE " + table); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerCallsReportAnUnreadableTable(t *testing.T) {
	w := setupLedger(t, "a")
	dropTable(t, w.common, "resources")
	ctx := context.Background()
	if _, err := w.service.Plan(ctx, w.request("a")); err == nil {
		t.Error("plan")
	}
	if _, err := w.service.Resources(ctx, w.request("a")); err == nil {
		t.Error("resources")
	}
	if _, err := w.service.Teardown(ctx, TeardownRequest{LedgerRequest: w.request("a")}); err == nil {
		t.Error("teardown")
	}
	if _, err := w.service.AddResource(ctx, w.request("a"), ledger.Record{Kind: ledger.KindVolume, ID: "v"}, ""); err == nil {
		t.Error("add")
	}
}

func TestAddResourceReportsAMissingIdentity(t *testing.T) {
	w := setupLedger(t, "a")
	w.service.Created = func(context.Context, string, string) (time.Time, error) {
		return time.Time{}, errors.New("no engine")
	}
	if _, err := w.service.AddResource(context.Background(), w.request("a"), ledger.Record{Execution: "zz", Kind: ledger.KindNetwork, ID: "n"}, ""); err == nil {
		t.Fatal("recorded with no identity")
	}
}

func TestTeardownReportsALogItCannotOpen(t *testing.T) {
	w := setupLedger(t, "a")
	if err := os.MkdirAll(filepath.Join(w.common, "herdsman"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.common, "herdsman", "logs"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.service.Teardown(context.Background(), TeardownRequest{LedgerRequest: w.request("a")}); err == nil {
		t.Fatal("no log")
	}
}

func TestVerbsReportAnUnreadableTaskTable(t *testing.T) {
	s, repo, _, common := verbRepo(t)
	useLedger(t, setupLedger(t).service)
	dropTable(t, common, "tasks")
	if _, err := s.TeardownPlan(context.Background(), WorktreeTeardownPlanRequest{Dir: repo, Route: "r"}); err == nil {
		t.Fatal("route over a missing table")
	}
}

func TestLedgerHealthReportsAFailedPlan(t *testing.T) {
	s, _, worktree, _ := verbRepo(t)
	w := setupLedger(t)
	useLedger(t, w.service)
	w.fail = errors.New("no read")
	if _, err := s.LedgerHealth(context.Background(), WorktreeLedgerHealthRequest{Dir: worktree}); err == nil {
		t.Fatal("a failed read")
	}
}

func TestAHookThatCannotBePlannedStopsThePlan(t *testing.T) {
	s, repo, _ := hookRepo(t, `{"hooks":{"pre-teardown":"echo {{ nope"}}`)
	useLedger(t, setupLedger(t).service)
	if _, err := s.TeardownPlan(context.Background(), WorktreeTeardownPlanRequest{Dir: repo, Selector: "task"}); err == nil {
		t.Fatal("an unrenderable hook")
	}
}

func TestATableLostDuringTeardownEndsIt(t *testing.T) {
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
	dropTable(t, w.common, "worktrees")
	if _, err := w.service.Teardown(ctx, TeardownRequest{LedgerRequest: w.request("a"), Approved: plan}); err == nil {
		t.Fatal("went on with no record of the worktrees")
	}
}
