package wtree

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestSchemaTwoKeepsExistingRecords(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := migrate(ctx, db, migrations[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resources (task, execution, kind, id, registered_at) VALUES ('a', 'e', 'volume', 'v', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, migrations); err != nil {
		t.Fatal(err)
	}
	var id, evidence, detail string
	var seen int64
	if err := db.QueryRow(`SELECT id, evidence, detail, seen_at FROM resources`).Scan(&id, &evidence, &detail, &seen); err != nil {
		t.Fatal(err)
	}
	if id != "v" || evidence != "" || detail != "" || seen != 0 {
		t.Fatalf("row = %q %q %q %d", id, evidence, detail, seen)
	}
}
