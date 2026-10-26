package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Item 0a (review a-7 P2): a full hash cache evicts one entry for each
// new one, and setup bounds the proof's files below the cache, so a
// proof's own files never evict each other.

func TestHashCacheEvictsOne(t *testing.T) {
	dir := t.TempDir()
	var c hashCache
	for i := range hashCacheMax {
		if _, err := c.sum(writeFile(t, dir, fmt.Sprintf("f%d", i), "x")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.sum(writeFile(t, dir, "one-more", "y")); err != nil {
		t.Fatal(err)
	}
	if len(c.m) != hashCacheMax {
		t.Fatalf("a full cache keeps %d entries after one miss, want %d (one evicted)", len(c.m), hashCacheMax)
	}
	if _, ok := c.m[filepath.Join(dir, "one-more")]; !ok {
		t.Fatal("the new entry is not cached")
	}
}

func TestSetupBoundsRules(t *testing.T) {
	x, _, q := proofRoute(t)
	dir := t.TempDir()
	q.Rules = nil
	for i := range maxSetupRules + 1 {
		q.Rules = append(q.Rules, writeFile(t, dir, fmt.Sprintf("rule-%d.sb", i), "(allow default)\n"))
	}
	if _, e := x.r.setup(q); e == nil || e.Code != "bad_request" {
		t.Fatalf("setup with %d rules: %v, want bad_request", len(q.Rules), e)
	}
	q.Rules = q.Rules[:maxSetupRules]
	if _, e := x.r.setup(q); e != nil {
		t.Fatal(e)
	}
	for range 3 {
		if !x.r.proofValid() {
			t.Fatal("proof not valid")
		}
	}
	if len(x.r.hashes.m) != maxSetupRules+1 {
		t.Fatalf("the cache holds %d of the proof's %d files", len(x.r.hashes.m), maxSetupRules+1)
	}
}

// Item 1 (review a-2 P1s on server.go and stream.go).

// frames reads a stream's frames into a channel until it ends.
func frames(ev *EventReader) <-chan SSEFrame {
	ch := make(chan SSEFrame, 256)
	go func() {
		defer close(ch)
		for {
			f, err := ev.Next()
			if err != nil {
				return
			}
			ch <- f
		}
	}()
	return ch
}

// nextFrame is the next frame of type typ, or a failure after 5 s.
func nextFrame(t testing.TB, ch <-chan SSEFrame, typ string) Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case f, ok := <-ch:
			if !ok {
				t.Fatalf("stream ended before %s", typ)
			}
			if f.Event == typ {
				var e Event
				if err := json.Unmarshal(f.Data, &e); err != nil {
					t.Fatal(err)
				}
				return e
			}
		case <-deadline:
			t.Fatalf("no %s frame within 5 s", typ)
		}
	}
}

// subCount is the number of streams attached to a route, read on its
// actor without the operation rules (a degraded route still answers).
func subCount(rt *Route) int {
	n := make(chan int, 1)
	rt.ops <- func() { n <- len(rt.subs) }
	return <-n
}

// TestViewListedRouteRemovedBeforeAttach: a route removed after the view
// listed it but before the view attached to it still reaches the view as
// route_removed.
func TestViewListedRouteRemovedBeforeAttach(t *testing.T) {
	h := newHarness(t)
	_, _, _ = h.route("a")
	_, _, _ = h.route("b")
	var once sync.Once
	h.d.cfg.Crash = func(point string) {
		if point == "view.listed" {
			once.Do(func() { h.do("DELETE", "/v1/routes/a", nil, nil) })
		}
	}
	ev, err := h.client().Events(context.Background(), "/v1/events", "")
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()
	ch := frames(ev)
	if reg := nextFrame(t, ch, "registry"); len(reg.Routes) != 2 {
		t.Fatalf("registry %+v", reg.Routes)
	}
	if rm := nextFrame(t, ch, "route_removed"); rm.Route != "a" {
		t.Fatalf("route_removed for %q", rm.Route)
	}
}

// TestViewCloseDetachesDegradedRoute: a closed view (or party) stream
// leaves no subscriber behind, even on a route that refuses operations.
func TestViewCloseDetachesDegradedRoute(t *testing.T) {
	h := newHarness(t)
	_, orch, _ := h.route("a")
	rt, _ := h.d.route("a")
	view, err := h.client().Events(context.Background(), "/v1/events", "")
	if err != nil {
		t.Fatal(err)
	}
	vch := frames(view)
	nextFrame(t, vch, "snapshot")
	party, err := orch.c.Events(context.Background(), "/v1/routes/a/events", "")
	if err != nil {
		t.Fatal(err)
	}
	pch := frames(party)
	nextFrame(t, pch, "snapshot")
	if n := subCount(rt); n != 2 {
		t.Fatalf("%d streams attached, want 2", n)
	}
	for range 3 {
		_ = rt.do(context.Background(), func() *APIError { panic("boom") })
	}
	view.Close()
	party.Close()
	for i := 0; subCount(rt) != 0; i++ {
		if i == 500 {
			t.Fatalf("%d closed streams stay attached to the degraded route", subCount(rt))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestReplayReadsFilesInSteps: a cursor older than the ring replays from
// the journal files one record at a time, not as one materialized range,
// and two rotations during the replay lose nothing of the range.
func TestReplayReadsFilesInSteps(t *testing.T) {
	x := newDirect(t)
	start := x.r.visible
	for i := range 300 {
		if _, e := x.send(fmt.Sprintf("m%d", i), key()); e != nil {
			t.Fatal(e)
		}
	}
	barrier := x.r.visible
	s := newSubscriber("r", "orch")
	first, e := x.r.subscribe(s, x.r.epoch+"-"+itoa(start))
	if e != nil {
		t.Fatal(e)
	}
	if len(first) != 0 || s.replay == nil {
		t.Fatalf("subscribe materialized %d frames (replay %v)", len(first), s.replay != nil)
	}
	defer s.replay.close()
	var got []int64
	for {
		rec, err := s.replay.next()
		if err != nil {
			t.Fatal(err)
		}
		if rec == nil {
			break
		}
		if s.wants(rec) {
			got = append(got, rec.Seq)
		}
		if len(got) == 10 {
			for range 2 {
				if err := x.r.writeCheckpoint(true); err != nil {
					t.Fatal(err)
				}
				if _, e := x.send("after", key()); e != nil {
					t.Fatal(e)
				}
			}
		}
	}
	if len(got) != 300 {
		t.Fatalf("replayed %d messages, want 300", len(got))
	}
	for i, seq := range got {
		if seq <= start || seq > barrier || (i > 0 && seq <= got[i-1]) {
			t.Fatalf("record %d has seq %d (range %d..%d)", i, seq, start, barrier)
		}
	}
}
