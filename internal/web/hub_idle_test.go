package web

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/url"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func countSnapshots(f *fixture) int {
	n := 0
	for _, r := range f.herdr.Requests() {
		if r.Method == "session.snapshot" {
			n++
		}
	}
	return n
}

func liveHeap() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// With no browser on the live stream, the hub's timer reads neither herdr
// nor the route folders; the first browser gets a fresh state at once, and
// while it watches the timer refreshes again.
func TestHubIdleWithoutBrowsers(t *testing.T) {
	f := newFixture(t)
	f.hub.every = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.hub.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitUntil(t, func() bool { return f.herdr.Subscribers() == 2 })
	start := countSnapshots(f)
	time.Sleep(100 * time.Millisecond)
	if n := countSnapshots(f) - start; n != 0 {
		t.Fatalf("%d snapshots with no browser", n)
	}

	// A stale state is read again before a request uses it.
	f.focused.Store("w1:t7")
	if got := f.hub.Current().Snapshot.FocusedTabID; got == nil || *got != "w1:t7" {
		t.Fatalf("Current kept a stale state: %v", got)
	}

	f.focused.Store("w1:t8")
	time.Sleep(10 * time.Millisecond)
	ch, state, _ := f.hub.subscribe()
	defer f.hub.unsubscribe(ch)
	if !strings.Contains(string(state), `"focused_tab_id":"w1:t8"`) {
		t.Fatalf("first browser got a stale state: %s", state)
	}
	before := countSnapshots(f)
	waitUntil(t, func() bool { return countSnapshots(f) > before+3 })
}

// waitFast is waitUntil with a short poll, for loops of many waits.
func waitFast(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(50 * time.Microsecond)
	}
}

// A24: the refresh loop runs 1000 times with a browser watching; the live
// heap grows by under 1 MiB and every goroutine ends with the hub.
func TestHubRefreshLoopKeepsMemoryFlat(t *testing.T) {
	f := newFixture(t)
	f.hub.every = time.Millisecond
	var reads atomic.Int64
	ch, _, _ := f.hub.subscribe()
	go func() {
		for range ch {
			reads.Add(1)
		}
	}()
	goroutines := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.hub.Run(ctx); close(done) }()
	start := countSnapshots(f)
	waitUntil(t, func() bool { return countSnapshots(f) >= start+100 })
	heap := liveHeap()
	deadline := time.Now().Add(60 * time.Second)
	for countSnapshots(f) < start+1100 && time.Now().Before(deadline) {
		f.focused.Store([]string{"w1:t1", "w1:t2"}[countSnapshots(f)%2])
		time.Sleep(time.Millisecond)
	}
	if n := countSnapshots(f) - start; n < 1100 {
		t.Fatalf("only %d refreshes", n)
	}
	cancel()
	<-done
	// The fake herdr server records every request; take its share out.
	grown := int64(liveHeap()) - int64(heap)
	if grown > 1<<20 {
		t.Errorf("live heap grew %d bytes over 1000 refreshes", grown)
	}
	waitUntil(t, func() bool { return f.herdr.Subscribers() == 0 })
	waitUntil(t, func() bool { return runtime.NumGoroutine() <= goroutines })
	if reads.Load() == 0 {
		t.Error("the browser got no state")
	}
	f.hub.unsubscribe(ch)
	close(ch)
}

// A24: the status loop wakes only when the agent panes change, and 1000
// changes leave no subscription, goroutine or heap growth behind.
func TestHubStatusLoopFollowsPaneChanges(t *testing.T) {
	f := newFixture(t)
	var alt atomic.Bool
	f.herdr.Handle("session.snapshot", func(map[string]any) (any, string) {
		pane := "w1:p1"
		if alt.Load() {
			pane = "w1:p2"
		}
		return map[string]any{"snapshot": map[string]any{"version": "0.9.4", "protocol": 22,
			"agents": []map[string]any{{"pane_id": pane, "tab_id": "w1:t1", "workspace_id": "w1", "agent_status": "idle"}}}}, ""
	})
	f.hub.refresh(context.Background())
	goroutines := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.hub.statusLoop(ctx); close(done) }()
	waitUntil(t, func() bool { return f.herdr.Subscribers() == 1 })
	// No change: no new subscription.
	subs := func() int {
		n := 0
		for _, r := range f.herdr.Requests() {
			if r.Method == "events.subscribe" {
				n++
			}
		}
		return n
	}
	first := subs()
	time.Sleep(50 * time.Millisecond)
	f.hub.refresh(context.Background())
	time.Sleep(20 * time.Millisecond)
	if subs() != first {
		t.Fatal("resubscribed without a change")
	}
	heap := liveHeap()
	for i := range 1000 {
		alt.Store(i%2 == 0)
		f.hub.refresh(context.Background())
		want := first + i + 1
		waitFast(t, func() bool { return subs() >= want })
	}
	waitUntil(t, func() bool { return f.herdr.Subscribers() == 1 })
	if grown := int64(liveHeap()) - int64(heap); grown > 1<<20 {
		t.Errorf("live heap grew %d bytes over 1000 pane changes", grown)
	}
	cancel()
	<-done
	waitUntil(t, func() bool { return f.herdr.Subscribers() == 0 })
	waitUntil(t, func() bool { return runtime.NumGoroutine() <= goroutines })
}

// A browser that stops reading cannot grow the server's memory: its queue is
// bounded, it is dropped once full, and later states are not kept for it.
func TestStalledBrowserCannotGrowMemory(t *testing.T) {
	f := newFixture(t)
	ch, _, _ := f.hub.subscribe()
	defer f.hub.unsubscribe(ch)
	big := make([]byte, 64<<10)
	heap := liveHeap()
	for range 10000 {
		f.hub.broadcast(message{event: "state", data: big})
	}
	if grown := int64(liveHeap()) - int64(heap); grown > 1<<20 {
		t.Errorf("live heap grew %d bytes for a stalled browser", grown)
	}
	if f.hub.watched() {
		t.Error("the stalled browser is still subscribed")
	}
}

// A browser that keeps its connection open but stops reading ends its
// stream once one write waits past streamWriteTimeout, so its handler does
// not block for good holding the states it was sent.
func TestStalledStreamEndsAfterWriteTimeout(t *testing.T) {
	f := newFixture(t)
	streamWriteTimeout = 200 * time.Millisecond
	t.Cleanup(func() { streamWriteTimeout = 30 * time.Second })
	u, err := url.Parse(f.http.URL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.(*net.TCPConn).SetReadBuffer(4096)
	fmt.Fprintf(conn, "GET /api/stream HTTP/1.1\r\nHost: %s\r\nAuthorization: %s\r\n\r\n", u.Host, f.authed["Authorization"])
	waitUntil(t, f.hub.watched)
	big := bytes.Repeat([]byte("x"), 1<<20)
	// Fewer states than the queue holds, so only the write deadline can end
	// the stream; together far more than the socket buffers hold.
	for range 16 {
		f.hub.broadcast(message{event: "state", data: big})
	}
	waitUntil(t, func() bool { return !f.hub.watched() })
}
