package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Review a-5 reproductions, kept as fail-first tests.

func TestCheckpointFsyncFailureNoSeqReuse(t *testing.T) {
	x := newDirect(t)
	if _, e := x.send("before", key()); e != nil {
		t.Fatal(e)
	}
	before := x.r.seq
	old := fsync
	fsync = func(f *os.File) error {
		if filepath.Base(f.Name()) == journalCurrent {
			return errInjected
		}
		return old(f)
	}
	x.r.j.size = journalRotate // force the actor's normal rotation branch
	e := x.op(func() *APIError { return nil })
	fsync = old
	if e == nil {
		t.Fatal("fault did not fire")
	}
	var after SendResult
	if e := x.op(func() *APIError { var e *APIError; after, e = x.send("after", key()); return e }); e != nil {
		t.Logf("safely refused next send: %v", e)
		return
	}
	t.Logf("checkpoint append failed at next seq=%d; subsequent send succeeded at seq=%d", before+1, after.Seq)
	x.kill()
	r := newRoute(x.d, "r", x.dir)
	defer r.closeFiles()
	if e := r.open(); e != nil {
		t.Fatalf("successful send left unreadable journal after checkpoint fsync failure: %v", e)
	}
}

func TestClaimRetryResyncsMove(t *testing.T) {
	x := newDirect(t)
	if _, e := x.send("one", key()); e != nil {
		t.Fatal(e)
	}
	old := fsync
	defer func() { fsync = old }()
	failed := false
	synced := false
	fsync = func(f *os.File) error {
		if f.Name() == x.path("orch", "cur") {
			if !failed {
				failed = true
				return errInjected
			}
			synced = true
		}
		return old(f)
	}
	_, e := x.r.claim("orch", ClaimRequest{ReadKey: key()})
	if e == nil || e.Code != "busy" || !failed {
		t.Fatalf("expected failed destination-directory fsync: %v", e)
	}
	if e := x.op(func() *APIError { return nil }); e != nil {
		t.Fatal(e)
	}
	if e := x.r.writeCheckpoint(true); e != nil {
		t.Fatal(e)
	}
	if !synced {
		t.Fatal("claim retry and checkpoint succeeded without ever fsyncing the renamed cur entry")
	}
}

func TestUnfinishedHoldWaitsForInputLock(t *testing.T) {
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
	if e == nil || e.Code != "busy" || len(x.r.unfinished) != 1 {
		t.Fatalf("expected committed unfinished hold: %v", e)
	}
	if exists(x.path("state", "hold-orch")) {
		t.Fatal("fault left a visible hold")
	}
	// The external executor/gate takes this exact lock while consuming
	// approval. A status turn must not mutate the protected files under it.
	external, e := x.r.inputLock()
	if e != nil {
		t.Fatal(e)
	}
	defer external()
	e = x.op(func() *APIError { return nil })
	if exists(x.path("state", "hold-orch")) {
		t.Fatalf("unrelated actor turn wrote a hold while another input-lock owner held the boundary; answer=%v", e)
	}
}

func TestLostEpochChangeResumes(t *testing.T) {
	x := newDirect(t)
	if _, e := x.send("one", key()); e != nil {
		t.Fatal(e)
	}
	x.kill()
	_ = os.Remove(x.path("state", "events", journalCurrent))
	_ = os.Remove(x.path("state", "events", journalPrev))
	old := rename
	rename = func(a, b string) error {
		if strings.HasSuffix(b, filepath.Join("courier.lock", "owner.json")) {
			return errors.New("injected lock epoch write failure")
		}
		return old(a, b)
	}
	r := newRoute(x.d, "r", x.dir)
	e := r.open()
	r.closeFiles()
	rename = old
	if e == nil {
		t.Fatal("epoch fault did not fire")
	}
	r2 := newRoute(x.d, "r", x.dir)
	defer r2.closeFiles()
	if e := r2.open(); e != nil {
		t.Fatalf("recovery of lost-journal epoch transaction cannot reopen its own retained locks: %v", e)
	}
}
