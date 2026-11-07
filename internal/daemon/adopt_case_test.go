package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/routedb"
)

// On a case-insensitive file system (the macOS default) the routes Old
// and old would share one route database and one staging folder, so a
// route name equal, ignoring case, to a registered, served or pending
// route is refused.

// pairInbox is a file-mode inbox at <ph>/<base> with an approved pair on
// panes pa and pb.
func pairInbox(t *testing.T, h *harness, base, pa, pb string) (dir, sum string) {
	t.Helper()
	dir = filepath.Join(h.ph, base)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = mkdirs(dir, "state", "ids", "enrolled", "alerts")
	h.fx.SetAgent(pa, herdrtest.Agent(pa, "s"+pa))
	h.fx.SetAgent(pb, herdrtest.Agent(pb, "s"+pb))
	adjID := inbox.Identity{Pane: pa, Session: inbox.Session{Agent: "claude", Kind: "id", Source: "hook", Value: "s" + pa}}
	orchID := inbox.Identity{Pane: pb, Session: inbox.Session{Agent: "claude", Kind: "id", Source: "hook", Value: "s" + pb}}
	reg := prettyJSON(obj("adj-1", obj("role", "adjutant", "identity", identityObj(adjID)), "orch-1", obj("role", "orchestrator", "identity", identityObj(orchID))))
	_ = os.WriteFile(filepath.Join(dir, "peers"), reg, 0o600)
	_ = os.WriteFile(filepath.Join(dir, "peers.approved"), reg, 0o600)
	_ = os.WriteFile(filepath.Join(dir, "setup.json"), []byte(`{"inbox":"`+dir+`","policy_verified":true,"rules":[],"binary":{"path":"/x","checksum":"1-1"}}`+"\n"), 0o600)
	for _, b := range []string{"adj-1", "orch-1"} {
		_ = mkdirs(dir, b+"/tmp", b+"/new", b+"/ready", b+"/cur", b+"/quarantine", b+"/ack", b+"/notices")
	}
	_ = os.WriteFile(filepath.Join(dir, "state", "sequence"), []byte("0\n"), 0o600)
	return dir, sha256Hex(reg)
}

// TestCaseTwinOfPendingRefused: while Old's adoption is pending, adopting
// old is refused and leaves Old's staged database; Old then completes on
// its own database.
func TestCaseTwinOfPendingRefused(t *testing.T) {
	h := newHarness(t)
	h.stop()
	a, sum := pairInbox(t, h, "repo-a", "p1", "p2")
	var busy atomic.Bool
	busy.Store(true)
	h.cfg.Procs = func() ([]Proc, error) {
		if busy.Load() {
			return []Proc{{PID: 999999, Command: "herdsman message hold orch-1 x", Env: []string{"HERDSMAN_INBOX=" + a}}}, nil
		}
		return nil, nil
	}
	h.cfg.Log = func(string) {}
	h.start()
	h.d.cfg.DrainWait = time.Millisecond
	if err := adoptErr(h, "Old", a, sum); code(err) != "adopt_busy" {
		t.Fatalf("want Old's adoption busy: %v", err)
	}
	pl := h.d.readPending()
	if len(pl.Inboxes) != 1 {
		t.Fatalf("pending %+v", pl)
	}
	staged := h.d.stagingPath("Old", pl.Inboxes[0].Epoch)
	before := readDB(t, staged)
	b, bsum := pairInbox(t, h, "repo-b", "p3", "p4")
	for _, n := range []string{"old", "OLD"} {
		if err := adoptErr(h, n, b, bsum); code(err) != "route_exists" {
			t.Errorf("adopting %s beside Old's pending adoption: %v", n, err)
		}
		if _, err := h.client().Do(context.Background(), "POST", "/v1/routes", CreateRequest{Repo: "repo", Route: n}, nil); code(err) != "route_exists" {
			t.Errorf("creating %s beside Old's pending adoption: %v", n, err)
		}
	}
	after := readDB(t, staged)
	for s, v := range before {
		if after[s] != v {
			t.Errorf("Old's staged database%s changed", s)
		}
	}
	if exists(routedb.Path(h.ph, "old")) {
		t.Error("a route database of old was published")
	}
	busy.Store(false)
	if err := adoptErr(h, "Old", a, sum); err != nil {
		t.Fatalf("Old's resume: %v", err)
	}
	if _, e := h.d.route("Old"); e != nil {
		t.Fatal(e)
	}
}

// TestCaseTwinOfRegisteredRefused: once Old is adopted (registered and
// served), neither old nor OLD can be adopted or created.
func TestCaseTwinOfRegisteredRefused(t *testing.T) {
	h := newHarness(t)
	a, asum := pairInbox(t, h, "repo-a", "p1", "p2")
	if err := adoptErr(h, "Old", a, asum); err != nil {
		t.Fatal(err)
	}
	b, bsum := pairInbox(t, h, "repo-b", "p3", "p4")
	for _, n := range []string{"old", "OLD"} {
		if err := adoptErr(h, n, b, bsum); code(err) != "route_exists" {
			t.Errorf("adopting %s beside route Old: %v", n, err)
		}
		if _, err := h.client().Do(context.Background(), "POST", "/v1/routes", CreateRequest{Repo: "repo", Route: n}, nil); code(err) != "route_exists" {
			t.Errorf("creating %s beside route Old: %v", n, err)
		}
	}
	if err := adoptErr(h, "Old", a, asum); code(err) != "route_exists" {
		t.Errorf("adopting Old again: %v", err)
	}
}
