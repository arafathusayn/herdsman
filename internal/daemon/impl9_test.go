package daemon

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"strings"
	"testing"
)

// Item 9, review a-2: guard tests that fail when their guard is removed.

// TestPeerOtherUserRefused: a connection whose peer runs as another user
// is closed before any request is read (3.3).
func TestPeerOtherUserRefused(t *testing.T) {
	h := newHarness(t)
	old := peerOf.Load()
	other := func(*net.UnixConn) (int, error) { return os.Getuid() + 1, nil }
	peerOf.Store(&other)
	t.Cleanup(func() { peerOf.Store(old) })
	c := NewClient(SocketPath(h.ph), testVersion)
	defer c.Close()
	if _, err := c.Hello(context.Background()); !IsDown(err) {
		t.Fatalf("another user's connection was answered: %v", err)
	}
	peerOf.Store(old)
	if _, err := c.Hello(context.Background()); err != nil {
		t.Fatalf("this user's connection: %v", err)
	}
}

// TestStreamQueueCutsAt64: a stream takes exactly streamQueue frames
// without a reader; the next closes it, and the route forgets it.
func TestStreamQueueCutsAt64(t *testing.T) {
	if streamQueue != 64 {
		t.Fatalf("stream queue %d, the design says 64 (9.3)", streamQueue)
	}
	x := newDirect(t)
	s := newSubscriber("r", "orch")
	if _, e := x.r.subscribe(s, ""); e != nil {
		t.Fatal(e)
	}
	for i := range 70 {
		if _, e := x.send("m", key()); e != nil {
			t.Fatalf("send %d: %v", i, e)
		}
	}
	n := 0
	for range s.q {
		n++
	}
	if n != streamQueue {
		t.Fatalf("a stream that fell behind kept %d frames, want %d then closed", n, streamQueue)
	}
	if _, attached := x.r.subs[s]; attached {
		t.Fatal("the route keeps a closed stream")
	}
}

// TestOversizedValidBodyRefused: a body over 160 KiB is refused for its
// size alone: the same request padded with JSON whitespace is otherwise
// valid.
func TestOversizedValidBodyRefused(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	ctx := context.Background()
	if err := c.Check(ctx); err != nil {
		t.Fatal(err)
	}
	post := func(body []byte) (int, string) {
		resp := postRPC(t, c, "CreateRoute", string(body), nil)
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	pad := func(route string, n int) []byte {
		return []byte(`{"repo":"repo",` + strings.Repeat(" ", n) + `"route":{"value":"` + route + `"}}`)
	}
	if code, b := post(pad("small", 1024)); code != 200 {
		t.Fatalf("control: %d %s", code, b)
	}
	big := pad("big", maxBody)
	if code, b := post(big); code != 429 || !strings.Contains(b, "resource_exhausted") {
		t.Fatalf("a %d byte body: %d %s", len(big), code, b)
	}
	if bytes.Contains([]byte(mustRoutes(t, h.ph)), []byte(`"big"`)) {
		t.Fatal("the oversized request created its route")
	}
}

func mustRoutes(t *testing.T, ph string) string {
	t.Helper()
	b, err := os.ReadFile(ph + "/daemon/routes.json")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
