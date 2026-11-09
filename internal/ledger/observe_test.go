package ledger

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestSameResourceUsesIdentityAndNeverAReusedID(t *testing.T) {
	line := Line{Kind: KindProcess, ID: "5", Identity: ProcessIdentity(5, testStart).String()}
	if !sameResource(Record{Kind: KindProcess, ID: "5", Start: testStart}, line) {
		t.Fatal("the same process")
	}
	if sameResource(Record{Kind: KindProcess, ID: "5", Start: testStart.Add(time.Hour)}, line) {
		t.Fatal("a reused pid")
	}
	if sameResource(Record{Kind: KindProcess, ID: "5"}, line) || sameResource(Record{Kind: KindVolume, ID: "5"}, Line{Kind: KindVolume, ID: "5"}) {
		t.Fatal("an unproved record")
	}
	if !sameResource(Record{Kind: KindContainer, ID: "c"}, Line{Kind: KindContainer, ID: "c"}) {
		t.Fatal("a container has no creation identity in its record")
	}
	if sameResource(Record{Kind: KindContainer, ID: "c"}, Line{Kind: KindContainer, ID: "d"}) {
		t.Fatal("another container")
	}
}

func TestObserveReportsAClosedDatabase(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	table := NewTable(db)
	if err := table.Observe(context.Background(), Plan{}); err == nil {
		t.Fatal("observe on a closed database")
	}
	if _, err := table.Observations(context.Background(), "a"); err == nil {
		t.Fatal("observations on a closed database")
	}
}

func observedTable(t *testing.T) *Table {
	t.Helper()
	table := openTable(t)
	for _, column := range []string{"evidence TEXT NOT NULL DEFAULT ''", "detail TEXT NOT NULL DEFAULT ''", "seen_at INTEGER NOT NULL DEFAULT 0"} {
		if _, err := table.db.Exec("ALTER TABLE resources ADD COLUMN " + column); err != nil {
			t.Fatal(err)
		}
	}
	return table
}

func TestObserveStoresOnlyTheLinesThatMatchAReader(t *testing.T) {
	table := observedTable(t)
	ctx := context.Background()
	for _, record := range []Record{
		{Task: "a", Execution: "e", Kind: KindContainer, ID: "c"},
		{Task: "a", Execution: "e", Kind: KindVolume, ID: "v", Start: testStart},
		{Task: "b", Execution: "e", Kind: KindContainer, ID: "c"},
	} {
		if err := table.Add(ctx, record, ""); err != nil {
			t.Fatal(err)
		}
	}
	plan := Plan{Task: "a", Lines: []Line{
		{Kind: KindContainer, ID: "c", Level: LevelExclusive, Class: "compensatable", State: "running"},
		{Kind: KindVolume, ID: "v", Identity: Identity{Kind: KindVolume, ID: "v", Start: testStart}.String(), Level: LevelExclusive},
	}}
	if err := table.Observe(ctx, plan); err != nil {
		t.Fatal(err)
	}
	seen, err := table.Observations(ctx, "a")
	if err != nil || len(seen) != 2 {
		t.Fatalf("seen = %+v, err = %v", seen, err)
	}
	if seen[0].Kind != KindContainer || seen[0].Detail != `{"class":"compensatable","state":"running"}` || seen[0].Evidence != LevelExclusive {
		t.Fatalf("seen = %+v", seen[0])
	}
	if other, err := table.Observations(ctx, "b"); err != nil || len(other) != 0 {
		t.Fatalf("other = %+v, err = %v", other, err)
	}
}

func TestObserveReportsAFailedWrite(t *testing.T) {
	table := openTable(t)
	ctx := context.Background()
	if err := table.Add(ctx, Record{Task: "a", Execution: "e", Kind: KindContainer, ID: "c"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := table.Observe(ctx, Plan{Task: "a", Lines: []Line{{Kind: KindContainer, ID: "c"}}}); err == nil {
		t.Fatal("a table without the columns")
	}
	if _, err := table.Observations(ctx, "a"); err == nil {
		t.Fatal("a table without the columns")
	}
}
