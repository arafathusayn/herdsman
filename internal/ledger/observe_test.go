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
