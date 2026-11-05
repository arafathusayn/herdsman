package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// timeline is what a lost-timeline reset may change: the epoch, the kept
// window, the never-issued ranges and the events.
func timeline(t *testing.T, c Conn) string {
	t.Helper()
	var b strings.Builder
	for _, k := range []string{"epoch", "next_seq", "oldest_seq", "seq_gaps"} {
		v, err := metaGet(c, k)
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString(k + "=" + v + " ")
	}
	err := c.Query("SELECT seq, epoch, data FROM events ORDER BY seq", nil, func(r Row) error {
		b.WriteString(r.Text(1) + ":" + r.Text(2) + " ")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestLostTimelineResetAtomic: a lost-timeline refusal whose new-epoch
// transaction fails after its first writes answers ErrLostTimeline with
// the reason and leaves the epoch, the kept window, the gaps and the
// events as they were. The failure is a TEMP trigger on the writer that
// refuses the reset's last write (oldest_seq), after DELETE FROM events
// and the seq_gaps delete have run, so only the transaction undoes them.
func TestLostTimelineResetAtomic(t *testing.T) {
	w := open(t, filepath.Join(dir(t), "r.db"), Writer)
	if err := InitEvents(w, epochA); err != nil {
		t.Fatal(err)
	}
	appendN(t, w, 3)
	setGaps(t, w, "2-2")
	before := timeline(t, w)
	for _, op := range []string{"INSERT", "UPDATE"} {
		q := "CREATE TEMP TRIGGER fail_oldest_" + op + " BEFORE " + op + " ON meta WHEN NEW.k = 'oldest_seq' BEGIN SELECT RAISE(ABORT, 'injected oldest_seq failure'); END"
		if _, err := w.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_, err := ReadEvents(w, epochA, 2, 3, ReplayPage)
	if !errors.Is(err, ErrLostTimeline) || !strings.Contains(err.Error(), "no new epoch") || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("lost cursor with a failing reset: %v, want lost_timeline (no new epoch: injected ...)", err)
	}
	for _, op := range []string{"INSERT", "UPDATE"} {
		if _, err := w.Exec("DROP TRIGGER fail_oldest_" + op); err != nil {
			t.Fatal(err)
		}
	}
	if got := timeline(t, w); got != before {
		t.Errorf("a failed reset changed the timeline:\n got %s\nwant %s", got, before)
	}
}

// TestLostTimelineEpochAlreadyMoved: when another refusal started a new
// epoch first, the reset answers ErrLostTimeline and starts no other.
func TestLostTimelineEpochAlreadyMoved(t *testing.T) {
	c := eventsRoute(t)
	appendN(t, c, 2)
	before := timeline(t, c)
	if err := resetLostTimeline(c, epochB); !errors.Is(err, ErrLostTimeline) || strings.Contains(err.Error(), "no new epoch") {
		t.Fatalf("reset after the epoch moved: %v, want lost_timeline", err)
	}
	if got := timeline(t, c); got != before {
		t.Errorf("a reset of an old epoch changed the timeline:\n got %s\nwant %s", got, before)
	}
}
