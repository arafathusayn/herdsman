package daemon

// Review a-8 reproductions, kept as fail-first tests.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestA8AckCacheIndividualPaths(t *testing.T) {
	for _, part := range []string{"cur", "ack", "id", "ids"} {
		t.Run(part, func(t *testing.T) {
			x := newDirect(t)
			m, e := x.send("one", key())
			if e != nil {
				t.Fatal(e)
			}
			cl, e := x.r.claim("orch", ClaimRequest{ReadKey: key()})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = x.r.ack("orch", cl.Claim, AckRequest{ID: m.ID, Record: "note"}); e != nil {
				t.Fatal(e)
			}
			box := x.path("orch")
			if !x.r.acks.valid(x.r.root(), box, m.ID) {
				t.Fatal("invalid fixture")
			}
			link := x.path("orch", part)
			if part == "id" {
				link = x.path("ids", m.ID)
			}
			if part == "ids" {
				link = x.path("ids")
			}
			moved := filepath.Join(filepath.Dir(x.dir), "moved-"+part)
			if e := os.Rename(link, moved); e != nil {
				t.Fatal(e)
			}
			if e := os.Symlink(moved, link); e != nil {
				t.Fatal(e)
			}
			if x.r.root().AckValid(box, m.ID) {
				t.Fatal("canonical guard accepted fixture link")
			}
			if x.r.acks.valid(x.r.root(), box, m.ID) {
				t.Fatal("ack cache bypassed " + part + " path guard")
			}
		})
	}
}

func TestA8IntakeLaterDispositions(t *testing.T) {
	for _, state := range []string{"ready", "cur", "claim", "ack", "quarantine", "terminal"} {
		t.Run(state, func(t *testing.T) {
			x := newDirect(t)
			id := "m000000000001"
			legacyEntry(t, x.dir, id, "adj", "orch", "original", "duplicate")
			if e := x.r.boxInit("orch"); e != nil {
				t.Fatal(e)
			}
			p := x.path("orch", state, id+".md")
			if state == "claim" {
				p = x.path("ids", id, "claim")
			}
			if state == "ack" {
				p = x.path("orch", "ack", id+".md.json")
			}
			if state == "terminal" {
				p = x.path("ids", id, "terminal.json")
			}
			if e := os.WriteFile(p, []byte("later disposition"), 0600); e != nil {
				t.Fatal(e)
			}
			if e := x.r.redoIntake(&Record{Op: "intake", ID: id, To: "orch", From: "adj", State: "verified"}); e != nil {
				t.Fatal(e)
			}
			if !exists(x.path("orch", "new", id+".md")) {
				t.Fatal("replay promoted duplicate past " + state)
			}
			b, e := os.ReadFile(p)
			if e != nil || string(b) != "later disposition" {
				t.Fatalf("replay changed %s: %q %v", state, b, e)
			}
		})
	}
}

// Exercise disposal, rather than just the verdict returned by verifyEntry.
func TestA8LinkedAllocationDisposition(t *testing.T) {
	for _, parent := range []bool{false, true} {
		t.Run(map[bool]string{false: "allocation", true: "ids-parent"}[parent], func(t *testing.T) {
			h := newHarness(t)
			adj, orch, dir := h.route("r")
			h.stop()
			rt := newRoute(h.d, "r", dir)
			if e := rt.open(); e != nil {
				t.Fatal(e)
			}
			defer rt.closeFiles()
			id := "m000000000001"
			legacyEntry(t, dir, id, adj.name, orch.name, "body", "body")
			outside := filepath.Join(h.dir, "outside-allocation")
			linked := rt.path("ids", id)
			if parent {
				linked = rt.path("ids")
			}
			if e := os.Rename(linked, outside); e != nil {
				t.Fatal(e)
			}
			if e := os.Symlink(outside, linked); e != nil {
				t.Fatal(e)
			}
			state := rt.verifyEntry(orch.name, id)
			t.Logf("linked evidence verification=%s", state)
			if state == "wait" || state == "held" {
				t.Fatalf("fixture not eligible for disposition: %s", state)
			}
			rt.intakeDue, rt.intakeAt = true, time.Time{}
			e := rt.runOp(func() *APIError { return nil })
			t.Logf("intake answer=%v", e)
			target := filepath.Join(outside, "terminal.json")
			if parent {
				target = filepath.Join(outside, id, "terminal.json")
			}
			if exists(target) {
				t.Fatal("quarantine wrote terminal.json through linked allocation outside inbox")
			}
		})
	}
}

// A failed intake journal recovery degrades the route during this turn.
func TestA8IntakeUnknownTailRefusesCaller(t *testing.T) {
	x := newDirect(t)
	legacyEntry(t, x.dir, "m000000000001", "adj", "orch", "one", "tampered")
	x.r.intakeDue, x.r.intakeAt = true, time.Time{}
	restore := failWhere(journalCurrent)
	defer restore()
	ran := false
	e := x.op(func() *APIError { ran = true; return nil })
	if !x.r.degraded.Load() {
		t.Fatal("fixture did not degrade journal")
	}
	if ran || e == nil {
		t.Fatalf("caller admitted after intake lost journal: ran=%v error=%v journalNil=%v", ran, e, x.r.j == nil)
	}
}

// Each retry must make the move durable even if the first move happened.
func TestA8IntakeRetryResync(t *testing.T) {
	x := newDirect(t)
	id := "m000000000001"
	legacyEntry(t, x.dir, id, "adj", "orch", "one", "one")
	old := fsync
	defer func() { fsync = old }()
	failed, synced := false, false
	fsync = func(f *os.File) error {
		if f.Name() == x.path("orch", "ready") {
			if !failed {
				failed = true
				return errInjected
			}
			synced = true
		}
		return old(f)
	}
	if e := x.r.dispose("orch", id, "verified"); e == nil {
		t.Fatal("fixture did not fault")
	}
	if e := x.op(func() *APIError { return nil }); e != nil {
		t.Fatal(e)
	}
	if e := x.r.writeCheckpoint(true); e != nil {
		t.Fatal(e)
	}
	if !synced {
		t.Fatal("intake retry checkpointed an unsynced ready move")
	}
}

func TestA8RotationErrorResyncsNames(t *testing.T) {
	x := newDirect(t)
	if _, e := x.send("before", key()); e != nil {
		t.Fatal(e)
	}
	old := fsync
	defer func() { fsync = old }()
	failed, synced := false, false
	fsync = func(f *os.File) error {
		if f.Name() == x.path("state", "events") {
			if !failed {
				failed = true
				return errInjected
			}
			synced = true
		}
		return old(f)
	}
	x.r.j.size = journalRotate
	if e := x.op(func() *APIError { return nil }); e == nil || !failed {
		t.Fatalf("rotation fault did not fire: %v", e)
	}
	if e := x.op(func() *APIError { _, e := x.send("after", key()); return e }); e != nil {
		t.Logf("safe refusal: %v", e)
		return
	}
	if !synced {
		t.Fatal("successful send after failed rotation without fsync of events/ names")
	}
}

func TestA8MailboxMkdirRetryResyncsParent(t *testing.T) {
	x := newDirect(t)
	old := fsync
	defer func() { fsync = old }()
	failed, synced := false, false
	fsync = func(f *os.File) error {
		if f.Name() == x.dir {
			if !failed {
				failed = true
				return errInjected
			}
			synced = true
		}
		return old(f)
	}
	if _, e := x.send("first", key()); e == nil || !failed {
		t.Fatalf("mkdir parent fault did not fire: %v", e)
	}
	if e := x.op(func() *APIError { _, e := x.send("second", key()); return e }); e != nil {
		t.Logf("safe refusal: %v", e)
		return
	}
	if e := x.r.writeCheckpoint(true); e != nil {
		t.Fatal(e)
	}
	if e := x.r.writeCheckpoint(true); e != nil {
		t.Fatal(e)
	}
	if !synced {
		t.Fatal("successful send uses mailbox whose parent fsync failed and was never retried")
	}
}

func TestA8ProtectedRequestRetriesItsCommit(t *testing.T) {
	x := newDirect(t)
	unlock, e := x.r.inputLock()
	if e != nil {
		t.Fatal(e)
	}
	reason := "pause"
	restore := failWhere("/state/hold-orch")
	_, e = x.r.hold("adj", "orch", &reason)
	restore()
	unlock()
	if e == nil || len(x.r.unfinished) != 1 {
		t.Fatalf("fixture did not leave committed hold: %v", e)
	}
	// The HTTP hold handler always takes this lock before submitting to the actor.
	unlock, e = x.r.inputLock()
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	e = x.op(func() *APIError { _, e := x.r.hold("adj", "orch", &reason); return e })
	if e != nil {
		t.Fatalf("same protected request cannot retry its committed effect after fault cleared: %v", e)
	}
	if !exists(x.path("state", "hold-orch")) {
		t.Fatal("retry lost committed hold")
	}
}
