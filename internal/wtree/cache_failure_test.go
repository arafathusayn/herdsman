package wtree

import (
	"context"
	"testing"
)

func TestCachedReportsARowItCannotRead(t *testing.T) {
	st, _ := openState(t)
	ctx := context.Background()
	for _, statement := range []string{
		`DROP TABLE caches`,
		`CREATE TABLE caches (kind TEXT, key TEXT, value TEXT, updated_at INTEGER)`,
		`INSERT INTO caches (kind, key, value, updated_at) VALUES ('commit', 'k', NULL, 0)`,
	} {
		if _, err := st.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Cached(ctx, CacheCommit, []string{"k"}); err == nil {
		t.Fatal("a NULL value was read as a cached entry")
	}
}

func TestPutCachedStoresNothingWhenAnEntryFails(t *testing.T) {
	st, _ := openState(t)
	ctx := context.Background()
	if _, err := st.db.ExecContext(ctx, `CREATE TRIGGER refuse BEFORE INSERT ON caches WHEN NEW.key = 'bad'
		BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatal(err)
	}
	if err := st.PutCached(ctx, CacheCommit, map[string]string{"good": "1", "bad": "2"}); err == nil {
		t.Fatal("the failing entry was not reported")
	}
	found, err := st.Cached(ctx, CacheCommit, []string{"good", "bad"})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatalf("a failed write left %v", found)
	}
}
