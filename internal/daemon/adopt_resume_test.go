package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/routedb"
)

// An adoption resumes only from the record the daemon keeps in the
// private home (<private home>/daemon/adopting.json), written before the
// inbox's marker: the marker, which any writer of the inbox can make, is
// only a hint that must match that record (inbox, route, confirmed pair
// and epoch), and the epoch is 32 lowercase hex.

const hexEpoch = "0123456789abcdef0123456789abcdef"

// forge writes an adoption marker into the inbox, as an agent can.
func forge(t *testing.T, dir string, m adoptMarker) {
	t.Helper()
	if err := writeMarker(filepath.Join(dir, "state", "adopting.json"), m); err != nil {
		t.Fatal(err)
	}
}

// adoptErr adopts dir as route through the API and returns its error.
func adoptErr(h *harness, route, dir, confirm string) error {
	_, err := h.client().Do(context.Background(), "POST", "/v1/routes/"+route+"/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: confirm}, nil)
	return err
}

// notAdopted checks a refused forged resume: no owner record, no route
// database, no route served.
func notAdopted(t *testing.T, h *harness, dir, route string) {
	t.Helper()
	if _, ok := ReadOwner(dir); ok {
		t.Error("the forged resume published owner.json")
	}
	if exists(routedb.Path(h.ph, route)) {
		t.Error("the forged resume published a route database")
	}
	if _, e := h.d.route(route); e == nil {
		t.Error("the forged resume serves the route")
	}
}

// TestForgedMarkerTraversalEpoch: a marker whose epoch is a path (32
// characters, not hex) never moves another route's database into the
// route's place, and never skips the pair confirmation or the census.
func TestForgedMarkerTraversalEpoch(t *testing.T) {
	h := newHarness(t)
	dir, sum := fileInbox(t, h)
	victim := routedb.Path(h.ph, "victim")
	if err := os.MkdirAll(filepath.Dir(victim), 0o700); err != nil {
		t.Fatal(err)
	}
	c, err := store.Open(victim, store.Writer, store.KindRoute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InitRoute(c, strings.Repeat("b", 32)); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	before := readDB(t, victim)
	epoch := strings.Repeat("a", 16) + "/../../../victim"
	if len(epoch) != 32 || filepath.Clean(h.d.stagingPath("old", epoch)) != victim {
		t.Fatalf("bad fixture: epoch length %d, path %s", len(epoch), h.d.stagingPath("old", epoch))
	}
	forge(t, dir, adoptMarker{Phase: "drain", Started: h.clock.Now().Unix(), Epoch: epoch, Route: "old", Confirm: sum})
	if err := os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("the census refuses this"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := adoptErr(h, "old", dir, strings.Repeat("0", 64)); err == nil {
		t.Fatal("a forged marker with a path epoch adopted")
	}
	after := readDB(t, victim)
	for s, b := range before {
		if after[s] != b {
			t.Errorf("route victim's database%s changed", s)
		}
	}
	notAdopted(t, h, dir, "old")
	for _, e := range []*APIError{h.d.importStaging("old", dir, epoch), h.d.publishStaging("old", epoch)} {
		if e == nil || !strings.Contains(e.Message, "epoch") {
			t.Errorf("a path epoch reached the staging file: %v", e)
		}
	}
}

// TestForgedMarkerNoPrivateRecord: a well-formed marker the daemon's
// record does not list is refused, even with the right pair; once it is
// removed, the adoption runs every check (here the census refuses).
func TestForgedMarkerNoPrivateRecord(t *testing.T) {
	h := newHarness(t)
	dir, sum := fileInbox(t, h)
	forge(t, dir, adoptMarker{Phase: "publish", Started: h.clock.Now().Unix(), Epoch: hexEpoch, Route: "old", Confirm: sum})
	if err := os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("the census refuses this"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := adoptErr(h, "old", dir, sum)
	if err == nil || !strings.Contains(err.Error(), "adopting.json") {
		t.Fatalf("a marker with no private record: %v", err)
	}
	notAdopted(t, h, dir, "old")
	for _, m := range []string{`{"phase":"drain"`, "not json", "", strings.Repeat(" ", 5000)} {
		if err := os.WriteFile(filepath.Join(dir, "state", "adopting.json"), []byte(m), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := adoptErr(h, "old", dir, sum); err == nil {
			t.Errorf("a malformed marker %q adopted", m)
		}
	}
	_ = os.Remove(filepath.Join(dir, "state", "adopting.json"))
	if err := adoptErr(h, "old", dir, sum); err == nil || !strings.Contains(err.Error(), "stray.txt") {
		t.Fatalf("after the marker went the census did not run: %v", err)
	}
	notAdopted(t, h, dir, "old")
}

// TestForgedMarkerMismatch: a marker that differs from the daemon's
// record of the adoption (another confirmed pair, epoch or route), or a
// request whose pair or route differs from it, is refused.
func TestForgedMarkerMismatch(t *testing.T) {
	other := strings.Repeat("e", 64)
	for _, c := range []struct {
		name, route, request string
		entry                pendingEntry
		marker               adoptMarker
	}{
		{"marker confirm", "old", "", pendingEntry{Route: "old", Epoch: hexEpoch}, adoptMarker{Route: "old", Epoch: hexEpoch, Confirm: other}},
		{"request confirm", "old", other, pendingEntry{Route: "old", Epoch: hexEpoch}, adoptMarker{Route: "old", Epoch: hexEpoch}},
		{"marker epoch", "old", "", pendingEntry{Route: "old", Epoch: hexEpoch}, adoptMarker{Route: "old", Epoch: strings.Repeat("f", 32)}},
		{"marker route", "old", "", pendingEntry{Route: "old", Epoch: hexEpoch}, adoptMarker{Route: "new", Epoch: hexEpoch}},
		{"request route", "new", "", pendingEntry{Route: "old", Epoch: hexEpoch}, adoptMarker{Route: "new", Epoch: hexEpoch}},
		{"record epoch", "old", "", pendingEntry{Route: "old", Epoch: strings.Repeat("a", 16) + "/../../../victim"}, adoptMarker{Route: "old", Epoch: strings.Repeat("a", 16) + "/../../../victim"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			dir, sum := fileInbox(t, h)
			e := c.entry
			e.Inbox, e.Started = dir, h.clock.Now().Unix()
			if e.Confirm == "" {
				e.Confirm = sum
			}
			m := c.marker
			m.Phase, m.Started = "publish", e.Started
			if m.Confirm == "" {
				m.Confirm = sum
			}
			if err := h.d.writePending(pendingList{Version: 1, Inboxes: []pendingEntry{e}}); err != nil {
				t.Fatal(err)
			}
			forge(t, dir, m)
			req := c.request
			if req == "" {
				req = sum
			}
			if err := adoptErr(h, c.route, dir, req); err == nil {
				t.Fatal("a mismatched resume adopted")
			}
			notAdopted(t, h, dir, c.route)
		})
	}
}

// TestPendingRecordKeepsEpoch: the daemon's record of an adoption, written
// before the marker, names the marker's epoch and confirmed pair, so an
// interrupted adoption resumes from it.
func TestPendingRecordKeepsEpoch(t *testing.T) {
	h := newHarness(t)
	h.stop()
	dir, sum := fileInbox(t, h)
	h.cfg.Crash = func(p string) {
		if p == "adopt.marker" {
			panic(crashSignal{p})
		}
	}
	h.cfg.Log = func(string) {}
	h.start()
	_ = adoptErr(h, "old", dir, sum)
	h.stop()
	b, err := os.ReadFile(filepath.Join(dir, "state", "adopting.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m adoptMarker
	if err := strictDecode(b, &m); err != nil {
		t.Fatal(err)
	}
	pl := h.d.readPending()
	if len(pl.Inboxes) != 1 || pl.Inboxes[0].Epoch != m.Epoch || !validEpoch(m.Epoch) || pl.Inboxes[0].Confirm != sum || pl.Inboxes[0].Route != "old" {
		t.Fatalf("record %+v, marker %+v", pl.Inboxes, m)
	}
	h.cfg.Crash = nil
	h.start()
	if _, e := h.d.route("old"); e != nil {
		t.Fatalf("the interrupted adoption did not resume: %v", e)
	}
}
