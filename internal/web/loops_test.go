package web

import (
	"context"
	"net/http"
	"runtime"
	"testing"
	"time"
)

// settle waits until the goroutine count is back to at most n.
func settle(n int) int {
	for range 200 {
		if g := runtime.NumGoroutine(); g <= n {
			return g
		}
		time.Sleep(10 * time.Millisecond)
	}
	return runtime.NumGoroutine()
}

// A24: 1000 actions through the queue (as the HTTP handler sends them) leave neither goroutines nor heap behind.
func TestQueueLoopKeepsResourcesFlat(t *testing.T) {
	f := newFixture(t)
	// Warm up past the activity log's bound; the fake's own record of
	// requests is dropped before each reading.
	act := func() {
		if r := f.queue.Do(context.Background(), Action{Type: "focus", Pane: "w1:p2"}); !r.OK {
			t.Fatalf("%+v", r)
		}
	}
	for range eventLog + 10 {
		act()
	}
	f.herdr.Forget()
	heap, base := liveHeap(), settle(1<<20)
	for range 1000 {
		act()
	}
	f.herdr.Forget()
	if g := settle(base + 2); g > base+2 {
		t.Errorf("goroutines %d -> %d after 1000 actions", base, g)
	}
	if grown := int64(liveHeap()) - int64(heap); grown > 1<<20 {
		t.Errorf("live heap grew %d bytes over 1000 actions", grown)
	}
}

// A24: 1000 browsers that open the live stream and leave end their
// handlers and subscriptions: goroutines and heap return to where they were.
func TestStreamLoopKeepsResourcesFlat(t *testing.T) {
	f := newFixture(t)
	open := func() {
		ctx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.http.URL+"/api/stream", nil)
		for k, v := range f.authed {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		buf := make([]byte, 512)
		_, _ = res.Body.Read(buf) // the first state
		cancel()
		_ = res.Body.Close()
	}
	open()
	http.DefaultClient.CloseIdleConnections()
	heap, base := liveHeap(), settle(1<<20)
	for range 1000 {
		open()
	}
	http.DefaultClient.CloseIdleConnections()
	if g := settle(base + 2); g > base+2 {
		t.Errorf("goroutines %d -> %d after 1000 streams", base, g)
	}
	waitFast(t, func() bool { return !f.hub.watched() })
	if grown := int64(liveHeap()) - int64(heap); grown > 1<<20 {
		t.Errorf("live heap grew %d bytes over 1000 streams", grown)
	}
}
