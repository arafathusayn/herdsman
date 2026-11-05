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
// transaction fails answers ErrLostTimeline with the reason and leaves
// the epoch, the kept window, the gaps and the events as they were. The
// refusal is read on a reader, whose writes SQLite refuses (query_only).
func TestLostTimelineResetAtomic(t *testing.T) {
	p := filepath.Join(dir(t), "r.db")
	w := open(t, p, Writer)
	if err := InitEvents(w, epochA); err != nil {
		t.Fatal(err)
	}
	appendN(t, w, 3)
	setGaps(t, w, "2-2")
	r := open(t, p, Reader)
	before := timeline(t, w)
	_, err := ReadEvents(r, epochA, 2, 3, ReplayPage)
	if !errors.Is(err, ErrLostTimeline) || !strings.Contains(err.Error(), "no new epoch") {
		t.Fatalf("lost cursor on a reader: %v, want lost_timeline (no new epoch: ...)", err)
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
