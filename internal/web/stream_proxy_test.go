package web

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/events"
)

const (
	streamWait  = 10 * time.Second
	streamTopic = "route/r"
	streamEpoch = "0123456789abcdef0123456789abcdef"
)

// streamDaemon is the daemon's side of the stream: a retained log for one
// topic, replayed after a subscriber's cursor.
type streamDaemon struct {
	socket string
	mu     sync.Mutex
	log    []*daemonv1.Event
	conns  map[*events.Conn]bool
	header http.Header
	wake   chan struct{}
}

func startStreamDaemon(t *testing.T) *streamDaemon {
	t.Helper()
	d := &streamDaemon{socket: tempSocket(t), conns: map[*events.Conn]bool{}, wake: make(chan struct{}, 1)}
	ln, err := net.Listen("unix", d.socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(d.serve)}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		d.drop()
		_ = srv.Close()
	})
	return d
}

func (d *streamDaemon) serve(w http.ResponseWriter, r *http.Request) {
	conn, err := events.Accept(w, r, events.AcceptOptions{})
	if err != nil {
		return
	}
	d.mu.Lock()
	d.conns[conn] = true
	d.header = r.Header.Clone()
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.conns, conn)
		d.mu.Unlock()
	}()
	for {
		frame, err := conn.Read()
		if err != nil {
			return
		}
		if sub := frame.GetSubscribe(); sub != nil {
			if err := d.replay(conn, sub.GetCursor()); err != nil {
				return
			}
		}
		if ping := frame.GetPing(); ping != nil {
			_ = conn.Write(&daemonv1.StreamFrame{Frame: &daemonv1.StreamFrame_Ping{Ping: ping}})
		}
	}
}

func (d *streamDaemon) replay(conn *events.Conn, cursor string) error {
	var after int64
	if cursor != "" {
		_, seq, ok := events.ParseCursor(cursor)
		if !ok {
			return fmt.Errorf("bad cursor %q", cursor)
		}
		after = seq
	}
	d.mu.Lock()
	pending := append([]*daemonv1.Event(nil), d.log[min(int(after), len(d.log)):]...)
	d.mu.Unlock()
	for _, event := range pending {
		if err := conn.Write(&daemonv1.StreamFrame{Frame: &daemonv1.StreamFrame_Event{Event: event}}); err != nil {
			return err
		}
	}
	return nil
}

func (d *streamDaemon) publish(n int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for range n {
		seq := int64(len(d.log) + 1)
		d.log = append(d.log, &daemonv1.Event{Topic: streamTopic, Cursor: events.FormatCursor(streamEpoch, seq), Type: "note"})
	}
}

// drop ends every connection, as a restarting daemon would.
func (d *streamDaemon) drop() {
	d.mu.Lock()
	conns := make([]*events.Conn, 0, len(d.conns))
	for conn := range d.conns {
		conns = append(conns, conn)
	}
	d.mu.Unlock()
	for _, conn := range conns {
		go func() { _ = conn.Close(events.CloseGoingAway, "") }()
	}
}

func (d *streamDaemon) sawHeader() http.Header {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.header
}

func wsURL(g *gatewayFixture) string {
	return "ws://" + strings.TrimPrefix(g.url, "http://") + events.Path
}

func openStream(t *testing.T, g *gatewayFixture, header http.Header) (*events.Stream, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), streamWait)
	defer cancel()
	stream, err := events.Dial(ctx, events.DialOptions{URL: wsURL(g), Header: header})
	if err == nil {
		t.Cleanup(func() { _ = stream.Close() })
	}
	return stream, err
}

func authHeader(device string) http.Header {
	return http.Header{"Authorization": {"Bearer " + device}}
}

// recvEvents reads n events, failing rather than hanging when they stop.
func recvEvents(t *testing.T, stream *events.Stream, n int) []string {
	t.Helper()
	type result struct {
		cursors []string
		err     error
	}
	done := make(chan result, 1)
	go func() {
		var out result
		for len(out.cursors) < n {
			frame, err := stream.Recv()
			if err != nil {
				out.err = err
				break
			}
			if event := frame.GetEvent(); event != nil {
				out.cursors = append(out.cursors, event.GetCursor())
			}
		}
		done <- out
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("after %d events: %v", len(got.cursors), got.err)
		}
		return got.cursors
	case <-time.After(streamWait):
		t.Fatalf("timed out waiting for %d events", n)
		return nil
	}
}

func seqs(t *testing.T, cursors []string) []int64 {
	t.Helper()
	out := make([]int64, 0, len(cursors))
	for _, cursor := range cursors {
		_, seq, ok := events.ParseCursor(cursor)
		if !ok {
			t.Fatalf("bad cursor %q", cursor)
		}
		out = append(out, seq)
	}
	return out
}

func wantSeqs(t *testing.T, got []int64, want ...int64) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("sequence %v, want %v", got, want)
	}
}

func TestStreamResumesFromACursorAfterTheBrowserReconnects(t *testing.T) {
	daemon := startStreamDaemon(t)
	g := startGateway(t, daemon.socket, nil)
	daemon.publish(2)

	first, err := openStream(t, g, authHeader(g.device))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Subscribe(streamTopic, ""); err != nil {
		t.Fatal(err)
	}
	wantSeqs(t, seqs(t, recvEvents(t, first, 2)), 1, 2)
	cursor := first.Cursor(streamTopic)
	_ = first.Close()

	daemon.publish(2)
	second, err := openStream(t, g, authHeader(g.device))
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Subscribe(streamTopic, cursor); err != nil {
		t.Fatal(err)
	}
	wantSeqs(t, seqs(t, recvEvents(t, second, 2)), 3, 4)
}

func TestStreamResumesAfterTheDaemonDrops(t *testing.T) {
	daemon := startStreamDaemon(t)
	g := startGateway(t, daemon.socket, nil)
	daemon.publish(1)
	stream, err := openStream(t, g, authHeader(g.device))
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Subscribe(streamTopic, ""); err != nil {
		t.Fatal(err)
	}
	recvEvents(t, stream, 1)
	cursor := stream.Cursor(streamTopic)

	daemon.drop()
	ended := make(chan error, 1)
	go func() {
		for {
			if _, err := stream.Recv(); err != nil {
				ended <- err
				return
			}
		}
	}()
	select {
	case <-ended:
	case <-time.After(streamWait):
		t.Fatal("the browser's stream outlived the daemon's")
	}

	daemon.publish(1)
	again, err := openStream(t, g, authHeader(g.device))
	if err != nil {
		t.Fatal(err)
	}
	if err := again.Subscribe(streamTopic, cursor); err != nil {
		t.Fatal(err)
	}
	wantSeqs(t, seqs(t, recvEvents(t, again, 1)), 2)
}

func TestStreamCarriesFramesBothWays(t *testing.T) {
	daemon := startStreamDaemon(t)
	g := startGateway(t, daemon.socket, nil)
	stream, err := openStream(t, g, authHeader(g.device))
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Ping("n1"); err != nil {
		t.Fatal(err)
	}
	frame, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if frame.GetPing().GetNonce() != "n1" {
		t.Fatalf("frame %v", frame)
	}
}

func TestStreamAttachesTheSessionAndHidesTheSecret(t *testing.T) {
	daemon := startStreamDaemon(t)
	g := startGateway(t, daemon.socket, nil)
	stream, err := openStream(t, g, http.Header{"Sec-WebSocket-Protocol": {sessionProtocol + g.device}})
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Ping("n"); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	seen := daemon.sawHeader()
	if got := seen.Get(HeaderSession); got != sessionID(g.device) {
		t.Fatalf("session %q", got)
	}
	for name, values := range seen {
		for _, value := range values {
			if strings.Contains(value, g.device) {
				t.Fatalf("%s carries the device secret", name)
			}
		}
	}
}

func TestStreamRefusals(t *testing.T) {
	daemon := startStreamDaemon(t)
	g := startGateway(t, daemon.socket, func(c *Config) { c.Hosts = []string{"box.tailnet.ts.net"} })
	cases := []struct {
		name   string
		header http.Header
	}{
		{"no session", nil},
		{"wrong session", authHeader("nope")},
		{"another site", http.Header{"Authorization": {"Bearer " + g.device}, "Origin": {"https://evil.example"}}},
		{"through a proxy", http.Header{"Authorization": {"Bearer " + g.device}, "X-Forwarded-For": {"203.0.113.9"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := openStream(t, g, c.header); err == nil {
				t.Fatal("the stream opened")
			}
		})
	}
	daemon.mu.Lock()
	defer daemon.mu.Unlock()
	if daemon.header != nil {
		t.Fatal("a refused browser reached the daemon")
	}
}

func TestStreamClosesWhenTheDaemonIsDown(t *testing.T) {
	g := startGateway(t, tempSocket(t), nil)
	stream, err := openStream(t, g, authHeader(g.device))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err == nil {
		t.Fatal("no end of stream")
	}
}
