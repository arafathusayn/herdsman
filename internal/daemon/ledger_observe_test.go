package daemon

import (
	"context"
	"testing"

	"github.com/arafathusayn/herdsman/internal/ledger"
	"github.com/arafathusayn/herdsman/internal/wtree"
)

func TestAPlanRefreshesWhatWasLastSeen(t *testing.T) {
	w := setupLedger(t, "a", "b")
	ctx := context.Background()
	w.pids = []ledger.Process{{Pid: 5, Group: 5, Parent: 1, Start: ledgerStart, Command: "server"}}
	if _, err := w.service.AddResource(ctx, w.request("a"), ledger.Record{Execution: "zz", Kind: ledger.KindProcess, ID: "5"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.service.AddResource(ctx, w.request("b"), ledger.Record{Execution: "q", Kind: ledger.KindVolume, ID: "elsewhere"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.service.Plan(ctx, w.request("a")); err != nil {
		t.Fatal(err)
	}
	state, err := wtree.OpenState(w.common)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	table := ledger.NewTable(state.DB())
	seen, err := table.Observations(ctx, "a")
	if err != nil || len(seen) != 1 {
		t.Fatalf("seen = %+v, err = %v", seen, err)
	}
	if seen[0].ID != "5" || seen[0].Evidence == "" || seen[0].Detail == "" || seen[0].SeenAt.IsZero() {
		t.Fatalf("seen = %+v", seen[0])
	}
	if other, err := table.Observations(ctx, "b"); err != nil || len(other) != 0 {
		t.Fatalf("other = %+v, err = %v", other, err)
	}
	records, err := table.Records(ctx)
	if err != nil || len(records) != 2 {
		t.Fatalf("records = %+v, err = %v", records, err)
	}
}

func TestResourcesRefreshesWhatWasLastSeen(t *testing.T) {
	w := setupLedger(t, "a")
	ctx := context.Background()
	w.pids = []ledger.Process{{Pid: 5, Group: 5, Parent: 1, Start: ledgerStart, Command: "server"}}
	if _, err := w.service.AddResource(ctx, w.request("a"), ledger.Record{Execution: "zz", Kind: ledger.KindProcess, ID: "5"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.service.Resources(ctx, w.request("a")); err != nil {
		t.Fatal(err)
	}
	state, err := wtree.OpenState(w.common)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if seen, err := ledger.NewTable(state.DB()).Observations(ctx, "a"); err != nil || len(seen) != 1 {
		t.Fatalf("seen = %+v, err = %v", seen, err)
	}
}
