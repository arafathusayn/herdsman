package context

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// An event's recorded time may run ahead of its id; the next one by the same
// actor must still keep the history valid.
func TestAppendPreservesCheckedActorClock(t *testing.T) {
	dir := newRepo(t)
	old := newULID(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	put(t, dir, old+eventExt, event(old, "someone", "2030-01-01T00:00:00Z", ""))
	if report, err := Check(dir); err != nil || len(report.Problems) != 0 {
		t.Fatalf("fixture: %+v, %v", report, err)
	}

	added, err := Append(dir, Input{Kind: KindNote, Actor: "someone", Body: "next", Now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if added.At.Before(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("at %v is before the actor's last time", added.At)
	}
	report, err := Check(dir)
	if err != nil || len(report.Problems) != 0 {
		t.Fatalf("after append: %v, %v", codes(report), err)
	}
	if _, err := Log(dir, Query{}); err != nil {
		t.Fatal(err)
	}
}

// Two clones behind the same shared event must not choose the same id, or
// their histories would not merge.
func TestClonesDoNotReuseULID(t *testing.T) {
	const actor = "herdsman/1.0.0"
	ahead := newULID(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	shared := event(ahead, actor, "2030-01-01T00:00:00Z", "")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	var clones [2]string
	var added [2]Event
	for i := range clones {
		clones[i] = newRepo(t)
		put(t, clones[i], ahead+eventExt, shared)
		var err error
		if added[i], err = Append(clones[i], Input{Kind: KindNote, Actor: actor, Body: "from a clone", Now: now}); err != nil {
			t.Fatal(err)
		}
		if added[i].ID <= ahead {
			t.Fatalf("id %s does not sort after %s", added[i].ID, ahead)
		}
	}
	if added[0].ID == added[1].ID {
		t.Fatalf("both clones chose %s", added[0].ID)
	}

	name := added[1].ID + eventExt
	data, err := os.ReadFile(filepath.Join(clones[1], EventsDir, name))
	if err != nil {
		t.Fatal(err)
	}
	put(t, clones[0], name, string(data))
	if report, err := Check(clones[0]); err != nil || len(report.Problems) != 0 {
		t.Fatalf("merged: %v, %v", codes(report), err)
	}
}
