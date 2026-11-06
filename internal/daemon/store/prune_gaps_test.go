package store

import (
	"errors"
	"testing"
)

// TestPruneTrimsGaps: once retention has dropped every event, VisibleSeq
// (oldest_seq-1) is never inside a recorded gap, so a cursor at it reads
// nothing instead of resetting the epoch; ranges below the kept window go.
func TestPruneTrimsGaps(t *testing.T) {
	for _, tc := range []struct {
		name, gaps, want string
	}{
		{"window inside a gap", "4-30", "10-30"},
		{"gap ends at the oldest kept seq", "4-10", "10-10"},
		{"gap ends just below the window", "4-9", ""},
		{"below and above", "2-3,12-14", "12-14"},
		{"apart", "15-16", "15-16"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := eventsRoute(t)
			appendN(t, c, 3)
			// The max rule skipped seqs, so next_seq is far ahead.
			if err := metaSet(c, "next_seq", "31"); err != nil {
				t.Fatal(err)
			}
			setGaps(t, c, tc.gaps)
			if _, err := PruneEvents(c, 21); err != nil {
				t.Fatal(err)
			}
			if got, _ := metaGet(c, "seq_gaps"); got != tc.want {
				t.Errorf("seq_gaps %q, want %q", got, tc.want)
			}
			head, err := VisibleSeq(c)
			if err != nil || head != 9 {
				t.Fatalf("VisibleSeq %d %v, want 9", head, err)
			}
			epoch, _ := metaGet(c, "epoch")
			if evs, err := ReadEvents(c, epoch, head, head, 10); err != nil || len(evs) != 0 {
				t.Fatalf("cursor at VisibleSeq: %v %v", evs, err)
			}
			if _, err := ReadEvents(c, epoch, 2, head, 10); !errors.Is(err, ErrCursorExpired) {
				t.Errorf("cursor below the window: %v, want ErrCursorExpired", err)
			}
		})
	}
}

// TestPruneCorruptGaps: a malformed seq_gaps fails the prune, which then
// leaves oldest_seq alone.
func TestPruneCorruptGaps(t *testing.T) {
	c := eventsRoute(t)
	appendN(t, c, 3)
	setGaps(t, c, "x")
	if _, err := PruneEvents(c, 1); !errors.Is(err, ErrSchemaCorrupt) {
		t.Fatalf("prune with seq_gaps x: %v, want ErrSchemaCorrupt", err)
	}
	if got, _ := metaGet(c, "oldest_seq"); got != "1" {
		t.Errorf("oldest_seq %q after the failed prune, want 1", got)
	}
}
