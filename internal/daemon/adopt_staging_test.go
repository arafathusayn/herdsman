package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/routedb"
)

// readDB reads a route database file and its -wal and -shm.
func readDB(t *testing.T, p string) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, s := range []string{"", "-wal", "-shm"} {
		if b, err := os.ReadFile(p + s); err == nil {
			got[s] = string(b)
		}
	}
	if _, ok := got[""]; !ok {
		t.Fatalf("no database at %s", p)
	}
	return got
}

// TestStagingKeepsDottedNeighbour: a route name may hold dots and
// hyphens, so the adoption of route old never touches the database of
// route old.staging-keep (or a staging file shaped like one), and the
// staging file of route old.staging-<epoch> is never old's.
func TestStagingKeepsDottedNeighbour(t *testing.T) {
	h := newHarness(t)
	first, sum := fileInbox(t, h)
	moved := filepath.Join(h.ph, "repo-first")
	if err := os.Rename(first, moved); err != nil {
		t.Fatal(err)
	}
	const neighbour = "old.staging-keep"
	h.do("POST", "/v1/routes/"+neighbour+"/adopt", AdoptRequest{Inbox: moved, ConfirmPairSHA256: sum}, nil)
	db := routedb.Path(h.ph, neighbour)
	before := readDB(t, db)
	second, sum := fileInbox(t, h)
	h.do("POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: second, ConfirmPairSHA256: sum}, nil)
	after := readDB(t, db)
	for s, b := range before {
		if after[s] != b {
			t.Errorf("adopting old changed %s%s", neighbour, s)
		}
	}
	const epoch = "0123456789abcdef0123456789abcdef"
	if p := h.d.stagingPath("old", epoch); filepath.Dir(p) == filepath.Dir(routedb.Path(h.ph, "old")) {
		t.Errorf("staging file %s is in the routes folder, where a route database can be named so", p)
	}
	if h.d.stagingPath("old", epoch) == h.d.stagingPath("old.staging-"+epoch, epoch) {
		t.Error("two routes share a staging path")
	}
}

// TestStagingCleanupExact: the leftover cleanup removes only the route's
// own staging files of the exact form (32 lowercase hex, .db and its
// -wal and -shm), never another file.
func TestStagingCleanupExact(t *testing.T) {
	h := newHarness(t)
	leftover := h.d.stagingPath("r", "ffffffffffffffffffffffffffffffff")
	dir := filepath.Dir(leftover)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := []string{"notes.txt", "gggggggggggggggggggggggggggggggg.db", "Ab" + "0123456789abcdef0123456789abcd.db", "fff.db", "ffffffffffffffffffffffffffffffff.db-journal"}
	for _, n := range append([]string{filepath.Base(leftover), filepath.Base(leftover) + "-wal", filepath.Base(leftover) + "-shm"}, keep...) {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	other := h.d.stagingPath("r.x", "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	if err := os.MkdirAll(filepath.Dir(other), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = h.d.importStaging("r", h.ph, "0123456789abcdef0123456789abcdef")
	for _, s := range []string{"", "-wal", "-shm"} {
		if exists(leftover + s) {
			t.Errorf("leftover %s%s kept", filepath.Base(leftover), s)
		}
	}
	for _, n := range keep {
		if !exists(filepath.Join(dir, n)) {
			t.Errorf("cleanup removed %s", n)
		}
	}
	if !exists(other) {
		t.Error("cleanup removed another route's staging file")
	}
}
