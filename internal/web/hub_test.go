package web

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/route"
)

// An event from herdr makes the hub send a new state, and stopping the hub
// leaves no goroutine or subscription behind.
func TestHubFollowsEventsAndStopsClean(t *testing.T) {
	f := newFixture(t)
	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.hub.Run(ctx); close(done) }()

	ch, _, _ := f.hub.subscribe()
	defer f.hub.unsubscribe(ch)
	// One structural subscription plus one for the agent panes' status.
	waitUntil(t, func() bool { return f.herdr.Subscribers() == 2 })

	f.focused.Store("w1:t9")
	f.herdr.Emit("tab_focused", map[string]any{"tab_id": "w1:t9"})
	deadline := time.After(3 * time.Second)
	for got := false; !got; {
		select {
		case m := <-ch:
			got = m.event == "state" && f.hub.Current().Snapshot.FocusedTabID != nil && *f.hub.Current().Snapshot.FocusedTabID == "w1:t9"
		case <-deadline:
			t.Fatal("no new state after a herdr event")
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("hub did not stop")
	}
	waitUntil(t, func() bool { return f.herdr.Subscribers() == 0 })
	waitUntil(t, func() bool { return runtime.NumGoroutine() <= before })
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A status event names the status the agent moved to, so the activity list
// can say "im-auth is working" instead of only the event's name.
func TestRecordKeepsTheAgentStatus(t *testing.T) {
	f := newFixture(t)
	ch, _, _ := f.hub.subscribe()
	defer f.hub.unsubscribe(ch)
	f.hub.record(herdr.Event{Event: "pane.agent_status_changed", Data: json.RawMessage(`{"pane_id":"w1:p2","workspace_id":"w1","agent_status":"blocked"}`)})
	m := <-ch
	var e Entry
	if err := json.Unmarshal(m.data, &e); err != nil || m.event != "entry" {
		t.Fatalf("message %q: %v", m.event, err)
	}
	if e.PaneID != "w1:p2" || e.Status != "blocked" || e.Source != "herdr" {
		t.Fatalf("entry = %+v", e)
	}
}

// A browser too slow to drain its queue is dropped rather than left on an old
// state: its stream ends, and it reconnects to the current snapshot.
func TestHubDropsAStalledSubscriber(t *testing.T) {
	f := newFixture(t)
	ch, _, _ := f.hub.subscribe()
	defer f.hub.unsubscribe(ch)
	for range cap(ch) + 1 {
		f.hub.broadcast(message{event: "state", data: []byte("{}")})
	}
	for range cap(ch) {
		<-ch
	}
	if _, ok := <-ch; ok {
		t.Fatal("a stalled subscriber stayed subscribed")
	}
	f.hub.mu.Lock()
	_, kept := f.hub.subs[ch]
	f.hub.mu.Unlock()
	if kept {
		t.Fatal("a dropped subscriber is still listed")
	}
}

// Two projects may each have a route folder of one name: each gets its own
// ID, so links and requests reach the right one. A unique name is its ID.
func TestRouteIDsTellSameNamedRoutesApart(t *testing.T) {
	rs := []RouteView{
		{Route: &route.Route{Name: "review-2026-10-24", Dir: "/a/.herdsman/review-2026-10-24"}},
		{Route: &route.Route{Name: "review-2026-10-24", Dir: "/b/.herdsman/review-2026-10-24"}},
		{Route: &route.Route{Name: "auth-2026-10-24", Dir: "/a/.herdsman/auth-2026-10-24"}},
	}
	setRouteIDs(rs)
	if rs[0].ID == rs[1].ID || rs[0].ID[:len("review-2026-10-24~")] != "review-2026-10-24~" || rs[2].ID != "auth-2026-10-24" {
		t.Fatalf("ids: %q %q %q", rs[0].ID, rs[1].ID, rs[2].ID)
	}
	if got := findRoute(rs, rs[1].ID); got == nil || got.Dir != "/b/.herdsman/review-2026-10-24" {
		t.Fatalf("findRoute(%q) = %+v", rs[1].ID, got)
	}
	if findRoute(rs, "review-2026-10-24") != nil {
		t.Fatal("a shared name alone picked one of the routes")
	}
	again := []RouteView{{Route: &route.Route{Name: "review-2026-10-24", Dir: "/b/.herdsman/review-2026-10-24"}}, rs[0]}
	setRouteIDs(again)
	if again[0].ID != rs[1].ID {
		t.Fatalf("id changed with the list order: %q, was %q", again[0].ID, rs[1].ID)
	}
}
