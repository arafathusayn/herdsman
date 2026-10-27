package store

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

const (
	epochA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	epochB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// eventsRoute is a fresh route database with its first epoch.
func eventsRoute(t *testing.T) Conn {
	t.Helper()
	c := openKind(t, KindRoute)
	if err := InitEvents(c, epochA); err != nil {
		t.Fatal(err)
	}
	return c
}

// appendN commits n events, one transaction each, and returns their seqs.
func appendN(t *testing.T, c Conn, n int) []int64 {
	t.Helper()
	var seqs []int64
	for i := 0; i < n; i++ {
		err := c.Tx(func(tx Conn) error {
			e, err := AppendEvent(tx, Event{Time: int64(i), Type: "message", Recipient: "orch", Data: `{"i":` + strconv.Itoa(i) + `}`})
			seqs = append(seqs, e.Seq)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return seqs
}

// TestEventsPagedReplay: a replay below the barrier comes in pages of at
// most 64, in seq order, never past the barrier, and with the fields
// written (a NULL recipient reads back as "").
func TestEventsPagedReplay(t *testing.T) {
	c := eventsRoute(t)
	appendN(t, c, 150)
	if err := c.Tx(func(tx Conn) error {
		_, err := AppendEvent(tx, Event{Time: 9, Type: "health", Data: "{}"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	barrier := int64(140)
	var got []Event
	for cursor := int64(0); ; {
		page, err := ReadEvents(c, epochA, cursor, barrier, ReplayPage)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) > ReplayPage {
			t.Fatalf("page of %d", len(page))
		}
		if len(page) == 0 {
			break
		}
		got = append(got, page...)
		cursor = page[len(page)-1].Seq
	}
	if len(got) != 140 || got[0].Seq != 1 || got[139].Seq != 140 {
		t.Fatalf("replayed %d events", len(got))
	}
	for i, e := range got {
		if e.Seq != int64(i+1) || e.Epoch != epochA || e.Recipient != "orch" || e.Data != `{"i":`+strconv.Itoa(i)+`}` {
			t.Fatalf("event %d: %+v", i, e)
		}
	}
	last, err := ReadEvents(c, epochA, 150, 151, ReplayPage)
	if err != nil || len(last) != 1 || last[0].Recipient != "" || last[0].Type != "health" {
		t.Fatalf("last: %+v %v", last, err)
	}
	if v, _ := VisibleSeq(c); v != 151 {
		t.Fatalf("visible %d", v)
	}
}

// TestEventAppendRollsBack: an event appended in a transaction that rolls
// back takes no seq: the next committed event gets it.
func TestEventAppendRollsBack(t *testing.T) {
	c := eventsRoute(t)
	appendN(t, c, 2)
	boom := errors.New("boom")
	err := c.Tx(func(tx Conn) error {
		if _, err := AppendEvent(tx, Event{Type: "message", Data: "{}"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if seqs := appendN(t, c, 1); seqs[0] != 3 {
		t.Fatalf("seq after rollback %d, want 3", seqs[0])
	}
}

// TestEventRetention: pruning runs in batches of at most 1000, keeps the
// newest window, raises oldest_seq, and a cursor below it is expired
// while one at its edge still replays.
func TestEventRetention(t *testing.T) {
	c := eventsRoute(t)
	err := c.Tx(func(tx Conn) error {
		for i := 0; i < 2600; i++ {
			if _, err := AppendEvent(tx, Event{Type: "message", Data: "{}"}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var batches []int64
	for {
		n, err := PruneEvents(c, 100)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
		batches = append(batches, n)
	}
	if len(batches) != 3 || batches[0] != 1000 || batches[1] != 1000 || batches[2] != 500 {
		t.Fatalf("batches %v, want [1000 1000 500]", batches)
	}
	oldest, _ := metaInt(c, "oldest_seq")
	if oldest != 2501 {
		t.Fatalf("oldest_seq %d, want 2501", oldest)
	}
	if _, err := ReadEvents(c, epochA, 2499, 2600, ReplayPage); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("cursor below the window: %v", err)
	}
	if page, err := ReadEvents(c, epochA, 2500, 2600, ReplayPage); err != nil || len(page) != 64 || page[0].Seq != 2501 {
		t.Fatalf("cursor at the edge: %d %v", len(page), err)
	}
}

// TestSeqNeverReused: neither retention nor reset-events gives a seq
// twice; a cursor of the old epoch gets epoch_changed after a reset.
func TestSeqNeverReused(t *testing.T) {
	c := eventsRoute(t)
	seen := map[int64]bool{}
	note := func(seqs []int64) {
		for _, s := range seqs {
			if seen[s] {
				t.Fatalf("seq %d issued twice", s)
			}
			seen[s] = true
		}
	}
	note(appendN(t, c, 30))
	if _, err := PruneEvents(c, 5); err != nil {
		t.Fatal(err)
	}
	note(appendN(t, c, 10))
	if err := ResetEvents(c, epochB); err != nil {
		t.Fatal(err)
	}
	left := int64(-1)
	if err := c.Query("SELECT count(*) FROM events", nil, func(r Row) error { left = r.Int64(0); return nil }); err != nil || left != 0 {
		t.Fatalf("%d events kept by the reset (%v)", left, err)
	}
	if _, err := ReadEvents(c, epochA, 40, 40, ReplayPage); !errors.Is(err, ErrEpochChanged) {
		t.Fatalf("old epoch cursor: %v", err)
	}
	if page, err := ReadEvents(c, epochB, 0, 100, ReplayPage); !errors.Is(err, ErrCursorExpired) || page != nil {
		t.Fatalf("cursor 0 after reset: %v", err)
	}
	seqs := appendN(t, c, 3)
	note(seqs)
	if seqs[0] != 41 {
		t.Fatalf("first seq after reset %d, want 41", seqs[0])
	}
	page, err := ReadEvents(c, epochB, 40, 100, ReplayPage)
	if err != nil || len(page) != 3 || page[0].Epoch != epochB {
		t.Fatalf("new epoch replay: %+v %v", page, err)
	}
}

// TestEventDataBound: an event's data over 2 KiB is refused, and the
// failed append leaves next_seq unchanged.
func TestEventDataBound(t *testing.T) {
	c := eventsRoute(t)
	err := c.Tx(func(tx Conn) error {
		_, err := AppendEvent(tx, Event{Type: "message", Data: strings.Repeat("x", 2049)})
		return err
	})
	if err == nil {
		t.Fatal("2049-byte event data accepted")
	}
	if v, _ := VisibleSeq(c); v != 0 {
		t.Fatalf("visible %d after a refused append", v)
	}
}
