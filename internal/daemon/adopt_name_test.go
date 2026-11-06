package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// A route name equal, ignoring case and exactly, to a registered, served
// or pending route of another inbox is refused by adopt and by create;
// only the same inbox's own pending adoption (adopt's resume) passes.

func createErr(h *harness, repo, route string) error {
	_, err := h.client().Do(context.Background(), "POST", "/v1/routes", CreateRequest{Repo: repo, Route: route}, nil)
	return err
}

// pendingOld leaves route Old's adoption of inbox repo-a pending on a busy
// drain; restart restarts the daemon in between, so the start's resume
// finds it busy too. It returns inbox a, its confirm and the busy switch.
func pendingOld(t *testing.T, h *harness, restart bool) (a, sum string, busy *atomic.Bool) {
	t.Helper()
	h.stop()
	a, sum = pairInbox(t, h, "repo-a", "p1", "p2")
	busy = &atomic.Bool{}
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
	if restart {
		h.stop()
		h.start()
		h.d.cfg.DrainWait = time.Millisecond
	}
	return a, sum, busy
}

// finishOld ends the busy drain and checks that Old completes on inbox a:
// registered to a and served from a.
func finishOld(t *testing.T, h *harness, a, sum string, busy *atomic.Bool) {
	t.Helper()
	busy.Store(false)
	if err := adoptErr(h, "Old", a, sum); err != nil {
		t.Fatalf("Old's own resume: %v", err)
	}
	rf, err := ReadRoutes(h.ph)
	if err != nil || rf.Routes["Old"].Inbox != a {
		t.Fatalf("Old registered to %q (%v), want inbox a", rf.Routes["Old"].Inbox, err)
	}
	rt, e := h.d.route("Old")
	if e != nil || rt.inbox != a {
		t.Fatalf("Old served from %v %v, want inbox a", rt, e)
	}
}

// TestNameOfPendingRefused: while Old's adoption of inbox a is pending,
// before and after a restart, adopting Old or old from inbox b and
// creating Old or old are refused, a's staged import stays, and Old then
// completes on inbox a.
func TestNameOfPendingRefused(t *testing.T) {
	for _, restart := range []bool{false, true} {
		name := "pending"
		if restart {
			name = "pending after a restart"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			a, sum, busy := pendingOld(t, h, restart)
			pl := h.d.readPending()
			if len(pl.Inboxes) != 1 {
				t.Fatalf("pending %+v", pl)
			}
			staged := h.d.stagingPath("Old", pl.Inboxes[0].Epoch)
			b, bsum := pairInbox(t, h, "repo-b", "p3", "p4")
			for _, n := range []string{"Old", "old"} {
				if err := createErr(h, "zz", n); code(err) != "route_exists" {
					t.Errorf("creating %s beside Old's pending adoption: %v", n, err)
				}
			}
			for _, n := range []string{"Old", "old"} {
				if err := adoptErr(h, n, b, bsum); code(err) != "route_exists" {
					t.Errorf("adopting %s from inbox b beside Old's pending adoption: %v", n, err)
				}
			}
			if !exists(staged) {
				t.Error("inbox a's staged import of Old went")
			}
			if rf, _ := ReadRoutes(h.ph); len(rf.Routes) != 0 {
				t.Errorf("routes registered beside the pending adoption: %+v", rf.Routes)
			}
			finishOld(t, h, a, sum, busy)
			// Inbox b, refused, is untouched: it adopts under its own name.
			if err := adoptErr(h, "Bee", b, bsum); err != nil {
				t.Errorf("re-adopt of inbox b after its refusal: %v", err)
			}
		})
	}
}

// TestNameOfRouteRefused: once Old is registered and served (adopted),
// registered by create, or served only (its routes.json entry gone), Old
// and old are refused for adopt from another inbox and for create.
func TestNameOfRouteRefused(t *testing.T) {
	for _, how := range []string{"adopted", "created", "served only"} {
		t.Run(how, func(t *testing.T) {
			h := newHarness(t)
			if how == "created" {
				if err := createErr(h, "repo", "Old"); err != nil {
					t.Fatal(err)
				}
			} else {
				a, sum := pairInbox(t, h, "repo-a", "p1", "p2")
				if err := adoptErr(h, "Old", a, sum); err != nil {
					t.Fatal(err)
				}
			}
			if how == "served only" {
				if err := h.d.writeRoutes(RoutesFile{Version: 1, Routes: map[string]RouteEntry{}}); err != nil {
					t.Fatal(err)
				}
			}
			b, bsum := pairInbox(t, h, "repo-b", "p3", "p4")
			for _, n := range []string{"Old", "old"} {
				if err := adoptErr(h, n, b, bsum); code(err) != "route_exists" {
					t.Errorf("adopting %s beside route Old: %v", n, err)
				}
				if err := createErr(h, "zz", n); code(err) != "route_exists" {
					t.Errorf("creating %s beside route Old: %v", n, err)
				}
			}
		})
	}
}

// TestCreateAfterOwnRefusal: a create refused for its own inbox (the
// folder exists) creates nothing, so the same create passes once the
// cause is gone.
func TestCreateAfterOwnRefusal(t *testing.T) {
	h := newHarness(t)
	dir := filepath.Join(h.ph, "repo-New")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := createErr(h, "repo", "New"); code(err) != "route_exists" {
		t.Fatalf("create over an existing folder: %v", err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := createErr(h, "repo", "New"); err != nil {
		t.Fatalf("re-create after its own refusal: %v", err)
	}
}
