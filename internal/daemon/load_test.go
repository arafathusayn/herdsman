package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// heapAfterGC is the live heap after two collections.
func heapAfterGC() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// settle waits for goroutines to drop to at most n.
func settle(n int) int {
	g := runtime.NumGoroutine()
	for i := 0; i < 200 && g > n; i++ {
		time.Sleep(10 * time.Millisecond)
		g = runtime.NumGoroutine()
	}
	return g
}

// TestSlowClientBounded: a subscriber that never reads while many events
// are published costs at most its fixed queue; the stream is closed and a
// reconnect with its cursor receives every event (9.3, 9.4).
func TestSlowClientBounded(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	cursor := mustCursor(t, h, orch)
	// A raw connection that requests the stream and never reads.
	conn, err := net.Dial("unix", SocketPath(h.ph))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET /v1/routes/r/events HTTP/1.1\r\nHost: h\r\n%s: %s\r\n%s: 1\r\n%s: %s\r\n%s: %s\r\nAuthorization: Bearer %s\r\n\r\n",
		HeaderVersion, testVersion, HeaderProtocol, HeaderPane, orch.pane, HeaderName, orch.name, orch.c.Credential)
	time.Sleep(100 * time.Millisecond)
	base := heapAfterGC()
	const n = 300
	for i := range n {
		adj.send(t, "r", orch.name, "m"+strconv.Itoa(i))
	}
	grown := int64(heapAfterGC()) - int64(base)
	if grown > 2<<20 {
		t.Fatalf("heap grew %d bytes with a stalled stream", grown)
	}
	ev, err := orch.c.Events(context.Background(), "/v1/routes/r/events", cursor)
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()
	for range n {
		waitFrame(t, ev, "message")
	}
}

// openRetry opens a stream, retrying while the daemon is busy: a stream
// the client just closed frees its slot when the daemon sees the close.
func openRetry(t *testing.T, c *Client, path string) *EventReader {
	t.Helper()
	for i := 0; ; i++ {
		ev, err := c.Events(context.Background(), path, "")
		if err == nil {
			return ev
		}
		if code(err) != "busy" || i == 200 {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestLoopsStayFlat runs the daemon's loops 1000 times (A24.3): actor
// operations (send, read, ack, release), stream connects and the view
// stream; the live heap grows by under 1 MiB and the goroutines return.
func TestLoopsStayFlat(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	// Warm up once so lazily made structures exist.
	m := adj.send(t, "r", orch.name, "warm")
	cl := orch.read(t, "r")
	orch.ack(t, "r", cl.Claim, m.ID)
	_, _ = orch.c.Do(context.Background(), "DELETE", "/v1/routes/r/claims/"+cl.Claim, nil, nil)
	// The loops measure memory and goroutines, not durability.
	oldSync := fsync
	fsync = func(*os.File) error { return nil }
	t.Cleanup(func() { fsync = oldSync })
	g0 := runtime.NumGoroutine()
	h.srv.ForgetRequests()
	base := heapAfterGC()
	// The streams run first: a snapshot reads every mailbox, so its cost
	// grows with the acknowledged history the actor loop leaves.
	t.Run("party stream", func(t *testing.T) {
		for range 1000 {
			ev := openRetry(t, orch.c, "/v1/routes/r/events")
			waitFrame(t, ev, "snapshot")
			ev.Close()
		}
	})
	t.Run("view stream", func(t *testing.T) {
		v := h.client()
		for range 1000 {
			ev := openRetry(t, v, "/v1/events")
			waitFrame(t, ev, "snapshot")
			ev.Close()
		}
	})
	h.srv.ForgetRequests()
	if grown := int64(heapAfterGC()) - int64(base); grown > 1<<20 {
		t.Errorf("stream loops: live heap grew %d bytes", grown)
	}
	cycle := func(t *testing.T) {
		// Two seconds per send: at most 300 keys inside the window.
		h.clock.Add(2 * time.Second)
		m := adj.send(t, "r", orch.name, "x")
		cl := orch.read(t, "r")
		orch.ack(t, "r", cl.Claim, m.ID)
		if _, err := orch.c.Do(context.Background(), "DELETE", "/v1/routes/r/claims/"+cl.Claim, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	// The actor's bounded memory fills first: 512 send keys (5.4), the
	// read keys kept until the next checkpoint, the ack cache (at most
	// 4096 entries). The loop then measures growth past that.
	for range 600 {
		cycle(t)
	}
	h.srv.ForgetRequests()
	actorBase := heapAfterGC()
	t.Run("actor", func(t *testing.T) {
		for range 1000 {
			cycle(t)
		}
	})
	h.srv.ForgetRequests()
	if grown := int64(heapAfterGC()) - int64(actorBase); grown > 1<<20 {
		t.Errorf("actor loop: live heap grew %d bytes", grown)
	}
	adj.c.Close()
	orch.c.Close()
	if g := settle(g0 + 2); g > g0+2 {
		t.Fatalf("goroutines %d, started with %d", g, g0)
	}
	// Over the whole test the heap also holds the bounded state the first
	// 600 cycles filled; it is reported, the steady loops are asserted.
	t.Logf("live heap over the whole test: %+d bytes", int64(heapAfterGC())-int64(base))
}

// TestLoadManyRoutes: 20 routes, 50 streams, messages sent, read and
// acked on every route; every message is delivered once per cursor and
// the heap after GC returns near the idle heap (9.4).
func TestLoadManyRoutes(t *testing.T) {
	if testing.Short() {
		t.Skip("load test")
	}
	h := newHarness(t)
	type rp struct {
		name      string
		adj, orch *party
	}
	var routes []rp
	for i := range 20 {
		n := "l" + strconv.Itoa(i)
		a, o, _ := h.route(n)
		routes = append(routes, rp{n, a, o})
	}
	h.srv.ForgetRequests()
	base := heapAfterGC()
	var streams []*EventReader
	for i := range 50 {
		r := routes[i%20]
		ev, err := r.orch.c.Events(context.Background(), "/v1/routes/"+r.name+"/events", "")
		if err != nil {
			t.Fatal(err)
		}
		streams = append(streams, ev)
	}
	per := 25
	for _, r := range routes {
		for range per {
			r.adj.send(t, r.name, r.orch.name, "x")
		}
		cl := r.orch.read(t, r.name)
		if len(cl.Manifest) != per {
			t.Fatalf("%s: %d messages", r.name, len(cl.Manifest))
		}
		for _, m := range cl.Manifest {
			r.orch.ack(t, r.name, cl.Claim, m.ID)
		}
	}
	for i, ev := range streams {
		seen := map[string]bool{}
		for len(seen) < per {
			f := waitFrame(t, ev, "message")
			if seen[f.ID] {
				t.Fatalf("stream %d: %s twice", i, f.ID)
			}
			seen[f.ID] = true
		}
		ev.Close()
	}
	h.closeClients()
	for _, r := range routes {
		r.adj.c.Close()
		r.orch.c.Close()
	}
	h.srv.ForgetRequests()
	if grown := int64(heapAfterGC()) - int64(base); grown > 2<<20 {
		t.Fatalf("heap grew %d bytes", grown)
	}
}

// TestRequestSemaphore: at most 32 requests in flight; streams give their
// slot back once attached.
func TestRequestSemaphore(t *testing.T) {
	h := newHarness(t)
	_, orch, _ := h.route("r")
	var evs []*EventReader
	for range 4 {
		ev, err := orch.c.Events(context.Background(), "/v1/routes/r/events", "")
		if err != nil {
			t.Fatal(err)
		}
		evs = append(evs, ev)
	}
	if len(h.d.sem) != 0 {
		t.Fatalf("streams hold %d request slots", len(h.d.sem))
	}
	// The fifth stream of one party is busy (4 per party).
	_, err := orch.c.Events(context.Background(), "/v1/routes/r/events", "")
	if code(err) != "busy" {
		t.Fatalf("fifth stream: %v", err)
	}
	for _, ev := range evs {
		ev.Close()
	}
}

// settleAt waits for the goroutine count to equal n, returning the last
// count seen.
func settleAt(n int) int {
	g := runtime.NumGoroutine()
	for i := 0; i < 300 && g != n; i++ {
		time.Sleep(10 * time.Millisecond)
		g = runtime.NumGoroutine()
	}
	return g
}

// TestGoroutineAccounting asserts the goroutine formula of 9.1 for the
// Phase A daemon: a base of 3 (Serve, the scheduler and the listener),
// plus 1 per route (its actor), plus 1 per open connection and 1 more per
// open stream; none per message. The card watcher, goal resume and alert
// sender are Phase B.
func TestGoroutineAccounting(t *testing.T) {
	h := newHarness(t)
	h.stop()
	h.cfg.Log = func(string) {}
	g0 := settle(0)
	h.start()
	if g := settleAt(g0 + 3); g != g0+3 {
		t.Fatalf("base: %d goroutines over %d, want 3", g-g0, g0)
	}
	var parties []*party
	for _, n := range []string{"a", "b", "c"} {
		_, orch, _ := h.route(n)
		parties = append(parties, orch)
	}
	// Admin requests leave idle keep-alive connections: close them so only
	// routes count.
	h.closeClients()
	for _, p := range parties {
		p.c.Close()
	}
	base := g0 + 3 + 3
	if g := settleAt(base); g != base {
		t.Fatalf("3 routes: %d goroutines over the base, want 3", g-(g0+3))
	}
	// Each open stream holds its connection's goroutine, its writer and
	// the client side's reader and writer of the connection.
	var evs []*EventReader
	for i, p := range parties {
		ev, err := p.c.Events(context.Background(), "/v1/routes/"+[]string{"a", "b", "c"}[i]+"/events", "")
		if err != nil {
			t.Fatal(err)
		}
		waitFrame(t, ev, "snapshot")
		evs = append(evs, ev)
	}
	// Per stream: the server's connection goroutine (which writes the
	// stream) and its background reader, and the client's reader and
	// writer of the connection.
	if g := settleAt(base + 4*len(evs)); g != base+4*len(evs) {
		t.Fatalf("%d streams: %d goroutines over the routes, want %d", len(evs), g-base, 4*len(evs))
	}
	for _, ev := range evs {
		ev.Close()
	}
	for _, p := range parties {
		p.c.Close()
	}
	if g := settleAt(base); g != base {
		t.Fatalf("after closing the streams: %d goroutines, want %d", g, base)
	}
	h.stop()
	if g := settleAt(g0); g != g0 {
		t.Fatalf("after stop: %d goroutines, want %d", g, g0)
	}
}
