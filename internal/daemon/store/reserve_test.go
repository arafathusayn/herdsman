package store

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// accountWith is an account database whose route r has open_gen gen and
// no reservation yet.
func accountWith(t *testing.T, gen string) Conn {
	t.Helper()
	acct := openKind(t, KindAccount)
	if _, err := acct.Exec("INSERT INTO routes(route, inbox, repo, herdr_socket, db_path, created, state, open_gen, reserved_message_id, reserved_seq) "+
		"VALUES ('r', '/i', '/g', '/s', '/r.db', 1, 'active', ?, 0, 0)", gen); err != nil {
		t.Fatal(err)
	}
	return acct
}

// routeFile is a route database at p with its meta rows and store_gen gen.
func routeFile(t *testing.T, p, gen string) Conn {
	t.Helper()
	c, err := Open(p, Writer, KindRoute)
	if err != nil {
		t.Fatal(err)
	}
	if err := InitRoute(c, epochA); err != nil {
		t.Fatal(err)
	}
	if err := metaSet(c, "store_gen", gen); err != nil {
		t.Fatal(err)
	}
	return c
}

// issue sends n messages the way the send path must: reserve first, then
// one route transaction per id with its event. It returns the ids.
func issue(t *testing.T, acct, rc Conn, n int) []int64 {
	t.Helper()
	var ids []int64
	for i := 0; i < n; i++ {
		last, _ := metaInt(rc, "last_message_id")
		next, _ := metaInt(rc, "next_seq")
		if _, err := Reserve(acct, "r", last+1, next); err != nil {
			t.Fatal(err)
		}
		err := rc.Tx(func(tx Conn) error {
			if err := metaSet(tx, "last_message_id", strconv.FormatInt(last+1, 10)); err != nil {
				return err
			}
			_, err := AppendEvent(tx, Event{Type: "message", Data: "{}"})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, last+1)
	}
	return ids
}

// snapshot copies the route database to a new mode 600 file next to it.
func snapshot(t *testing.T, c Conn, p string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := c.Exec("VACUUM INTO ?", p); err != nil {
		t.Fatal(err)
	}
}

// restore puts the snapshot back as the route database, its old side
// files gone, and opens it as the writer.
func restore(t *testing.T, rc Conn, p, snap string) Conn {
	t.Helper()
	_ = rc.Close()
	for _, side := range []string{"-wal", "-shm"} {
		if err := os.Remove(p + side); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	if err := os.Rename(snap, p); err != nil {
		t.Fatal(err)
	}
	c, err := Open(p, Writer, KindRoute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestReserveBlocks: a reservation rises by whole blocks only when an id
// or seq above it is needed, in one account commit.
func TestReserveBlocks(t *testing.T) {
	acct := accountWith(t, "g")
	for _, s := range []struct{ id, seq, wantID, wantSeq int64 }{
		{1, 1, 1000, 1000}, {500, 999, 1000, 1000}, {1001, 1000, 2000, 1000}, {2500, 1001, 3000, 2000}, {3000, 2000, 3000, 2000},
	} {
		r, err := Reserve(acct, "r", s.id, s.seq)
		if err != nil || r.MessageID != s.wantID || r.Seq != s.wantSeq {
			t.Fatalf("reserve %d/%d: %+v %v, want %d/%d", s.id, s.seq, r, err, s.wantID, s.wantSeq)
		}
	}
	if _, err := Reserve(acct, "nope", 1, 1); err != ErrUnknownRoute {
		t.Fatalf("unknown route: %v", err)
	}
}

// TestCrashNoSpuriousReset: a plain reopen (also after a crash) keeps the
// epoch and its events; only the ids and seqs jump to the reservation.
func TestCrashNoSpuriousReset(t *testing.T) {
	acct := accountWith(t, "g0")
	rc := routeFile(t, filepath.Join(dir(t), "r.db"), "g0")
	if res, err := OpenGeneration(acct, rc, "r", "g1", epochB); err != nil || res.Restored || res.Epoch != epochA {
		t.Fatalf("first open %+v %v", res, err)
	}
	issue(t, acct, rc, 3)
	if res, err := OpenGeneration(acct, rc, "r", "g2", epochB); err != nil || res.Restored || res.Epoch != epochA {
		t.Fatalf("reopen %+v %v", res, err)
	}
	if page, err := ReadEvents(rc, epochA, 0, 10, ReplayPage); err != nil || len(page) != 3 {
		t.Fatalf("events after a reopen: %d %v", len(page), err)
	}
}

// TestRestoreBackupNewEpochNoIdReuse: a backup taken before an open (an
// older store_gen) restored after ids were issued gets a new epoch with
// no events, and its next id and seq are above everything issued.
func TestRestoreBackupNewEpochNoIdReuse(t *testing.T) {
	acct := accountWith(t, "g0")
	p := filepath.Join(dir(t), "r.db")
	rc := routeFile(t, p, "g0")
	issue(t, acct, rc, 2)
	snapshot(t, rc, p+".bak")
	if _, err := OpenGeneration(acct, rc, "r", "g1", epochB); err != nil {
		t.Fatal(err)
	}
	seen := issue(t, acct, rc, 5)
	rc = restore(t, rc, p, p+".bak")
	res, err := OpenGeneration(acct, rc, "r", "g2", epochB)
	if err != nil || !res.Restored || res.Epoch != epochB {
		t.Fatalf("open of the restored backup %+v %v", res, err)
	}
	if n := count(t, rc, "SELECT count(*) FROM events"); n != 0 {
		t.Fatalf("%d events of the old epoch survived the restore", n)
	}
	next := issue(t, acct, rc, 1)[0]
	if next <= seen[len(seen)-1] {
		t.Fatalf("id %d issued again after a restore (last seen %d)", next, seen[len(seen)-1])
	}
}

// TestRestoreSameGenCopyNoIdReuse: a copy taken while the route runs
// carries the current store_gen, so its restore looks like a plain
// reopen; the max rule still puts the next id and seq above everything
// issued since the copy.
func TestRestoreSameGenCopyNoIdReuse(t *testing.T) {
	acct := accountWith(t, "g0")
	p := filepath.Join(dir(t), "r.db")
	rc := routeFile(t, p, "g0")
	if _, err := OpenGeneration(acct, rc, "r", "g1", epochB); err != nil {
		t.Fatal(err)
	}
	issue(t, acct, rc, 2)
	snapshot(t, rc, p+".copy")
	seen := issue(t, acct, rc, 5)
	lastSeq, _ := VisibleSeq(rc)
	rc = restore(t, rc, p, p+".copy")
	res, err := OpenGeneration(acct, rc, "r", "g2", epochB)
	if err != nil || res.Restored {
		t.Fatalf("open of the same-generation copy %+v %v", res, err)
	}
	if next := issue(t, acct, rc, 1)[0]; next <= seen[len(seen)-1] {
		t.Fatalf("id %d issued again (last seen %d)", next, seen[len(seen)-1])
	}
	if seq, _ := VisibleSeq(rc); seq <= lastSeq {
		t.Fatalf("seq %d issued again (last seen %d)", seq, lastSeq)
	}
}

// TestAdoptFirstOpenKeepsIds: a route published from its staging file
// opens as a plain start: the imported epoch and events stay, and the
// next id follows the highest imported one.
func TestAdoptFirstOpenKeepsIds(t *testing.T) {
	acct := openKind(t, KindAccount)
	p := filepath.Join(dir(t), "r.db")
	sc := routeFile(t, p, "staged")
	if err := metaSet(sc, "last_message_id", "4242"); err != nil {
		t.Fatal(err)
	}
	if err := sc.Tx(func(tx Conn) error { _, err := AppendEvent(tx, Event{Type: "notice", Data: "{}"}); return err }); err != nil {
		t.Fatal(err)
	}
	if err := PublishRoute(acct, sc, "r", "/i", "/g", "/s", p, 1); err != nil {
		t.Fatal(err)
	}
	res, err := OpenGeneration(acct, sc, "r", "g1", epochB)
	if err != nil || res.Restored || res.Epoch != epochA {
		t.Fatalf("first open after adoption %+v %v", res, err)
	}
	if page, err := ReadEvents(sc, epochA, 0, 10, ReplayPage); err != nil || len(page) != 1 {
		t.Fatalf("imported events: %d %v", len(page), err)
	}
	if next := issue(t, acct, sc, 1)[0]; next != 4243 {
		t.Fatalf("first id after adoption %d, want 4243", next)
	}
}

// TestAdoptEarlierStagingCopyNoIdReuse: the account row published from a
// staging file reserves every imported id, so an earlier copy of that
// file (same store_gen) put back before the first open still issues
// above the imported ids.
func TestAdoptEarlierStagingCopyNoIdReuse(t *testing.T) {
	acct := openKind(t, KindAccount)
	p := filepath.Join(dir(t), "r.db")
	sc := routeFile(t, p, "staged")
	if err := metaSet(sc, "last_message_id", "100"); err != nil {
		t.Fatal(err)
	}
	snapshot(t, sc, p+".early")
	if err := metaSet(sc, "last_message_id", "4242"); err != nil {
		t.Fatal(err)
	}
	if err := PublishRoute(acct, sc, "r", "/i", "/g", "/s", p, 1); err != nil {
		t.Fatal(err)
	}
	sc = restore(t, sc, p, p+".early")
	if _, err := OpenGeneration(acct, sc, "r", "g1", epochB); err != nil {
		t.Fatal(err)
	}
	if next := issue(t, acct, sc, 1)[0]; next <= 4242 {
		t.Fatalf("id %d issued again after an earlier staging copy came back", next)
	}
}
