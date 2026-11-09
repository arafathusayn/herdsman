package events

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/internal/ws"
)

func TestTopicNames(t *testing.T) {
	for _, good := range []string{"registry", "route/r-1", "pane/w:1/tail.log", "a_b"} {
		if !ValidTopic(good) {
			t.Errorf("%q refused", good)
		}
	}
	for _, bad := range []string{"", "Route", "/x", "route/", "route//x", "../x", "a b", strings.Repeat("a", maxTopicLength+1)} {
		if ValidTopic(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	if RouteTopic("r") != "route/r" || !ValidTopic(RouteTopic("r")) {
		t.Fatal(RouteTopic("r"))
	}
}

func TestCursorRoundTripAndRefusals(t *testing.T) {
	epoch := strings.Repeat("ab", 16)
	got, seq, ok := ParseCursor(FormatCursor(epoch, 42))
	if !ok || got != epoch || seq != 42 {
		t.Fatalf("%q %d %v", got, seq, ok)
	}
	for _, bad := range []string{"", "x-1", epoch, epoch + "-", epoch + "-a", strings.ToUpper(epoch) + "-1", epoch + "-" + strings.Repeat("9", 19)} {
		if _, _, ok := ParseCursor(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The bytes of each encoding are a contract: a browser and a later daemon both
// read them.
func TestFrameEncodingsAreStable(t *testing.T) {
	frame := &daemonv1.StreamFrame{Frame: &daemonv1.StreamFrame_Lagged{Lagged: &daemonv1.Lagged{Topic: "t", Cursor: "c", Reason: "r"}}}
	op, binary, err := encodeFrame(frame, false)
	if err != nil || op != ws.OpBinary {
		t.Fatalf("%v %v", op, err)
	}
	if want := "\x22\x09\x0a\x01t\x12\x01c\x1a\x01r"; string(binary) != want {
		t.Fatalf("binary %q", binary)
	}
	op, text, err := encodeFrame(frame, true)
	if err != nil || op != ws.OpText {
		t.Fatalf("%v %v", op, err)
	}
	if got := strings.ReplaceAll(string(text), " ", ""); got != `{"lagged":{"topic":"t","cursor":"c","reason":"r"}}` {
		t.Fatalf("text %s", text)
	}
	for _, input := range []struct {
		op   ws.Opcode
		data []byte
	}{{ws.OpBinary, binary}, {ws.OpText, text}} {
		back, err := decodeFrame(input.op, input.data)
		if err != nil || back.GetLagged().GetCursor() != "c" {
			t.Fatalf("%v %v", back, err)
		}
	}
}

func TestDecodeRefusesGarbageAndIgnoresLaterFields(t *testing.T) {
	if _, err := decodeFrame(ws.OpBinary, []byte{0xff, 0xff}); err == nil {
		t.Fatal("binary garbage accepted")
	}
	if _, err := decodeFrame(ws.OpText, []byte("{")); err == nil {
		t.Fatal("text garbage accepted")
	}
	frame, err := decodeFrame(ws.OpText, []byte(`{"from_the_future":1}`))
	if err != nil || frame.GetFrame() != nil {
		t.Fatalf("%v %v", frame, err)
	}
}

func TestEncodeReportsAnInvalidFrame(t *testing.T) {
	bad := &daemonv1.StreamFrame{Frame: &daemonv1.StreamFrame_Ping{Ping: &daemonv1.Ping{Nonce: "\xff"}}}
	for _, json := range []bool{false, true} {
		if _, _, err := encodeFrame(bad, json); err == nil {
			t.Fatalf("json=%v encoded invalid UTF-8", json)
		}
	}
}

// echoServer accepts a stream and hands its server side to the test.
func echoServer(t *testing.T, opts AcceptOptions) (string, <-chan *Conn) {
	t.Helper()
	conns := make(chan *Conn, 1)
	finish := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Accept(w, r, opts)
		if err != nil {
			return
		}
		conns <- conn
		<-finish
	}))
	t.Cleanup(func() {
		close(finish)
		server.CloseClientConnections()
		server.Close()
	})
	return "ws" + strings.TrimPrefix(server.URL, "http") + Path, conns
}

func dialStream(t *testing.T, url string, json bool) *Stream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := Dial(ctx, DialOptions{URL: url, JSON: json})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stream.Close() })
	return stream
}

func TestStreamFramesTravelInBothEncodings(t *testing.T) {
	for _, json := range []bool{false, true} {
		url, conns := echoServer(t, AcceptOptions{})
		stream := dialStream(t, url, json)
		server := <-conns
		if server.json != json {
			t.Fatalf("server json=%v, want %v", server.json, json)
		}

		if err := stream.Subscribe("t", "c0"); err != nil {
			t.Fatal(err)
		}
		if got, err := server.Read(); err != nil || got.GetSubscribe().GetCursor() != "c0" {
			t.Fatalf("%v %v", got, err)
		}
		if err := stream.Unsubscribe("t"); err != nil {
			t.Fatal(err)
		}
		if got, err := server.Read(); err != nil || got.GetUnsubscribe().GetTopic() != "t" {
			t.Fatalf("%v %v", got, err)
		}
		if err := stream.Ping("n"); err != nil {
			t.Fatal(err)
		}
		if got, err := server.Read(); err != nil || got.GetPing().GetNonce() != "n" {
			t.Fatalf("%v %v", got, err)
		}

		send := func(frame *daemonv1.StreamFrame) *daemonv1.StreamFrame {
			t.Helper()
			if err := server.Write(frame); err != nil {
				t.Fatal(err)
			}
			got, err := stream.Recv()
			if err != nil {
				t.Fatal(err)
			}
			return got
		}
		send(&daemonv1.StreamFrame{Frame: &daemonv1.StreamFrame_Event{Event: &daemonv1.Event{Topic: "t", Cursor: "c1", Payload: []byte{0, 1, 2}}}})
		if stream.Cursor("t") != "c1" {
			t.Fatalf("cursor %q", stream.Cursor("t"))
		}
		send(&daemonv1.StreamFrame{Frame: &daemonv1.StreamFrame_Lagged{Lagged: &daemonv1.Lagged{Topic: "u", Cursor: "c2"}}})
		cursors := stream.Cursors()
		if cursors["t"] != "c1" || cursors["u"] != "c2" {
			t.Fatalf("cursors %v", cursors)
		}
		cursors["t"] = "changed"
		if stream.Cursor("t") != "c1" {
			t.Fatal("Cursors shares its map")
		}
		if err := server.Close(CloseNormal, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Recv(); err == nil {
			t.Fatal("recv after close")
		}
	}
}

func TestStreamReportsWhatItCannotDecode(t *testing.T) {
	url, conns := echoServer(t, AcceptOptions{})
	stream := dialStream(t, url, false)
	server := <-conns
	if err := server.conn.WriteMessage(ws.OpBinary, []byte{0xff, 0xff}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err == nil || !strings.Contains(err.Error(), "undecodable") {
		t.Fatalf("err %v", err)
	}
	if err := stream.Send(&daemonv1.StreamFrame{Frame: &daemonv1.StreamFrame_Ping{Ping: &daemonv1.Ping{Nonce: "\xff"}}}); err == nil {
		t.Fatal("sent an invalid frame")
	}
	if err := server.Write(&daemonv1.StreamFrame{Frame: &daemonv1.StreamFrame_Ping{Ping: &daemonv1.Ping{Nonce: "\xff"}}}); err == nil {
		t.Fatal("wrote an invalid frame")
	}
	stream.Send(&daemonv1.StreamFrame{Frame: &daemonv1.StreamFrame_Ping{Ping: &daemonv1.Ping{}}})
	if err := server.conn.Close(CloseNormal, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Read(); err == nil {
		t.Fatal("read after close")
	}
}

func TestAcceptEnforcesTheOriginCheck(t *testing.T) {
	url, _ := echoServer(t, AcceptOptions{CheckOrigin: func(*http.Request) bool { return false }})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := Dial(ctx, DialOptions{URL: url}); err == nil {
		t.Fatal("origin check ignored")
	}
}

func TestDialSendsHeaders(t *testing.T) {
	seen := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("X-Session")
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Dial(ctx, DialOptions{URL: "ws" + strings.TrimPrefix(server.URL, "http"), Header: http.Header{"X-Session": {"s"}}})
	if err == nil {
		t.Fatal("dial succeeded")
	}
	if got := <-seen; got != "s" {
		t.Fatalf("header %q", got)
	}
}

type fakeWait struct {
	requests []*daemonv1.EventServiceWaitRequest
	reply    *daemonv1.EventServiceWaitResponse
	err      error
}

func (f *fakeWait) Wait(_ context.Context, req *daemonv1.EventServiceWaitRequest) (*daemonv1.EventServiceWaitResponse, error) {
	f.requests = append(f.requests, req)
	return f.reply, f.err
}

func TestWaiterKeepsPositionsAcrossCalls(t *testing.T) {
	fake := &fakeWait{reply: &daemonv1.EventServiceWaitResponse{
		Events:  []*daemonv1.Event{{Topic: "a", Cursor: "a1"}},
		Lagged:  []*daemonv1.Lagged{{Topic: "b", Cursor: "b9", Reason: "cursor_expired"}},
		Cursors: []*daemonv1.TopicCursor{{Topic: "a", Cursor: "a1"}, {Topic: "b", Cursor: "b9"}},
	}}
	waiter := NewWaiter(fake, "a", "b")
	batch, err := waiter.Next(context.Background(), 1500*time.Millisecond)
	if err != nil || len(batch.Events) != 1 || len(batch.Lagged) != 1 {
		t.Fatalf("%v %v", batch, err)
	}
	if got := fake.requests[0]; got.GetWaitMs() != 1500 || got.GetTopics()[0].GetCursor() != "" || len(got.GetTopics()) != 2 {
		t.Fatalf("first request %v", got)
	}
	if waiter.Cursor("a") != "a1" || waiter.Cursor("b") != "b9" || waiter.Cursor("c") != "" {
		t.Fatalf("cursors %q %q", waiter.Cursor("a"), waiter.Cursor("b"))
	}
	if _, err := waiter.Next(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if got := fake.requests[1].GetTopics()[1].GetCursor(); got != "b9" {
		t.Fatalf("second request cursor %q", got)
	}
}

func TestWaiterKeepsItsPositionOnFailure(t *testing.T) {
	fake := &fakeWait{err: errors.New("down")}
	waiter := NewWaiter(fake, "a")
	if _, err := waiter.Next(context.Background(), 0); err == nil {
		t.Fatal("error lost")
	}
	if len(waiter.topics) != 1 {
		t.Fatalf("topics %v", waiter.topics)
	}
}
