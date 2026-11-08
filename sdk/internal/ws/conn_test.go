package ws

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// scriptConn replays fixed input and records what the connection writes.
type scriptConn struct {
	mu       sync.Mutex
	in       io.Reader
	out      bytes.Buffer
	writeErr error
	closed   bool
}

func newScript(in []byte) *scriptConn { return &scriptConn{in: bytes.NewReader(in)} }

func (s *scriptConn) Read(b []byte) (int, error) { return s.in.Read(b) }
func (s *scriptConn) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return s.out.Write(b)
}
func (s *scriptConn) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}
func (s *scriptConn) written() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Clone(s.out.Bytes())
}
func (s *scriptConn) LocalAddr() net.Addr              { return nil }
func (s *scriptConn) RemoteAddr() net.Addr             { return nil }
func (s *scriptConn) SetDeadline(time.Time) error      { return nil }
func (s *scriptConn) SetReadDeadline(time.Time) error  { return nil }
func (s *scriptConn) SetWriteDeadline(time.Time) error { return nil }

func testConn(s net.Conn, client bool, opts Options) *Conn {
	return newConn(s, bufio.NewReader(s), client, opts, "")
}

var testKey = [4]byte{0x11, 0x22, 0x33, 0x44}

// frame builds one frame as the peer of a server would send it: masked.
func frame(fin bool, op Opcode, payload []byte) []byte {
	out := appendHeader(nil, fin, op, true, testKey, uint64(len(payload)))
	body := bytes.Clone(payload)
	mask(body, testKey, 0)
	return append(out, body...)
}

// plain builds one frame as the peer of a client would send it: unmasked.
func plain(fin bool, op Opcode, payload []byte) []byte {
	return append(appendHeader(nil, fin, op, false, [4]byte{}, uint64(len(payload))), payload...)
}

func closePayload(code uint16, reason string) []byte {
	return append([]byte{byte(code >> 8), byte(code)}, reason...)
}

func concat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func wantProtocol(t *testing.T, err error, code uint16) {
	t.Helper()
	var pe *ProtocolError
	if !errors.As(err, &pe) || pe.Code != code {
		t.Fatalf("error = %v, want protocol error %d", err, code)
	}
}

func TestReadSingleAndFragmentedMessages(t *testing.T) {
	in := concat(
		frame(true, OpText, []byte("hello")),
		frame(false, OpBinary, []byte{1, 2}),
		frame(false, OpContinuation, []byte{3}),
		frame(true, OpPing, []byte("mid")),
		frame(true, OpContinuation, []byte{4}),
		frame(true, OpBinary, nil),
	)
	s := newScript(in)
	c := testConn(s, false, Options{})

	op, data, err := c.ReadMessage()
	if err != nil || op != OpText || string(data) != "hello" {
		t.Fatalf("first = %v %q %v", op, data, err)
	}
	op, data, err = c.ReadMessage()
	if err != nil || op != OpBinary || !bytes.Equal(data, []byte{1, 2, 3, 4}) {
		t.Fatalf("second = %v %v %v", op, data, err)
	}
	op, data, err = c.ReadMessage()
	if err != nil || op != OpBinary || len(data) != 0 {
		t.Fatalf("empty = %v %v %v", op, data, err)
	}
	if got, want := s.written(), plain(true, OpPong, []byte("mid")); !bytes.Equal(got, want) {
		t.Fatalf("pong = %x, want %x", got, want)
	}
}

func TestMaskingEnforcedOnRead(t *testing.T) {
	t.Run("server rejects an unmasked frame", func(t *testing.T) {
		c := testConn(newScript(plain(true, OpText, []byte("x"))), false, Options{CloseTimeout: time.Millisecond})
		_, _, err := c.ReadMessage()
		wantProtocol(t, err, CloseProtocolError)
	})
	t.Run("client rejects a masked frame", func(t *testing.T) {
		c := testConn(newScript(frame(true, OpText, []byte("x"))), true, Options{CloseTimeout: time.Millisecond})
		_, _, err := c.ReadMessage()
		wantProtocol(t, err, CloseProtocolError)
	})
	t.Run("server rejects an unmasked control frame", func(t *testing.T) {
		c := testConn(newScript(plain(true, OpPing, nil)), false, Options{CloseTimeout: time.Millisecond})
		_, _, err := c.ReadMessage()
		wantProtocol(t, err, CloseProtocolError)
	})
}

func TestMaskingOnWrite(t *testing.T) {
	payload := bytes.Repeat([]byte("abcdefg"), 2000)
	original := bytes.Clone(payload)

	t.Run("client masks and leaves the caller's bytes alone", func(t *testing.T) {
		s := newScript(nil)
		c := testConn(s, true, Options{})
		if err := c.WriteMessage(OpBinary, payload); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(payload, original) {
			t.Fatal("WriteMessage changed the caller's slice")
		}
		out := s.written()
		h, n, err := parseHeader(out)
		if err != nil || !h.masked {
			t.Fatalf("header = %+v %v", h, err)
		}
		body := bytes.Clone(out[n:])
		if bytes.Equal(body, original) {
			t.Fatal("payload went out unmasked")
		}
		mask(body, h.key, 0)
		if !bytes.Equal(body, original) {
			t.Fatal("unmasked payload differs")
		}
	})
	t.Run("client picks a new key per frame", func(t *testing.T) {
		s := newScript(nil)
		c := testConn(s, true, Options{})
		for range 2 {
			if err := c.WriteMessage(OpBinary, []byte("a")); err != nil {
				t.Fatal(err)
			}
		}
		out := s.written()
		first, n, _ := parseHeader(out)
		second, _, _ := parseHeader(out[n+1:])
		if first.key == second.key {
			t.Fatal("two frames shared a masking key")
		}
	})
	t.Run("server never masks", func(t *testing.T) {
		s := newScript(nil)
		c := testConn(s, false, Options{WriteTimeout: time.Second})
		if err := c.WriteMessage(OpText, []byte("ok")); err != nil {
			t.Fatal(err)
		}
		if got, want := s.written(), plain(true, OpText, []byte("ok")); !bytes.Equal(got, want) {
			t.Fatalf("wire = %x, want %x", got, want)
		}
	})
}

func TestReadProtocolViolations(t *testing.T) {
	long := bytes.Repeat([]byte("x"), 126)
	tests := []struct {
		name string
		in   []byte
		code uint16
	}{
		{"bad header", []byte{0xC1, 0x80, 0, 0, 0, 0}, CloseProtocolError},
		{"continuation with nothing open", frame(true, OpContinuation, []byte("x")), CloseProtocolError},
		{"new message inside a fragmented one", concat(frame(false, OpText, []byte("a")), frame(true, OpText, []byte("b"))), CloseProtocolError},
		{"invalid text", frame(true, OpText, []byte{0xFF}), CloseInvalidPayload},
		{"invalid text in a fragment", frame(false, OpText, []byte{0xC0}), CloseInvalidPayload},
		{"truncated text at the end", frame(true, OpText, []byte{0xE2, 0x82}), CloseInvalidPayload},
		{"close with one byte", frame(true, OpClose, []byte{3}), CloseProtocolError},
		{"close with a bad code", frame(true, OpClose, closePayload(1005, "")), CloseProtocolError},
		{"close with bad reason", frame(true, OpClose, append(closePayload(1000, ""), 0xFF)), CloseInvalidPayload},
		{"message over the limit", frame(true, OpBinary, long), CloseMessageTooBig},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newScript(tt.in)
			c := testConn(s, false, Options{MaxMessageSize: 100, CloseTimeout: time.Millisecond})
			_, _, err := c.ReadMessage()
			wantProtocol(t, err, tt.code)

			out := s.written()
			h, n, perr := parseHeader(out)
			if perr != nil || h.op != OpClose {
				t.Fatalf("wire = %x (%v), want a close frame", out, perr)
			}
			if got := uint16(out[n])<<8 | uint16(out[n+1]); got != tt.code {
				t.Fatalf("close code on the wire = %d, want %d", got, tt.code)
			}
			if !s.closed {
				t.Fatal("connection left open after a protocol failure")
			}
			if _, _, again := c.ReadMessage(); !errors.Is(again, err) {
				t.Fatalf("second read = %v, want the sticky %v", again, err)
			}
			if err := c.WriteMessage(OpText, []byte("x")); !errors.Is(err, ErrClosed) {
				t.Fatalf("write after failure = %v, want ErrClosed", err)
			}
		})
	}
}

func TestMessageLimitCountsAllFragments(t *testing.T) {
	in := concat(frame(false, OpBinary, make([]byte, 60)), frame(true, OpContinuation, make([]byte, 60)))
	c := testConn(newScript(in), false, Options{MaxMessageSize: 100, CloseTimeout: time.Millisecond})
	_, _, err := c.ReadMessage()
	wantProtocol(t, err, CloseMessageTooBig)
}

func TestFragmentCountIsBounded(t *testing.T) {
	var in []byte
	in = append(in, frame(false, OpBinary, nil)...)
	for range 10 {
		in = append(in, frame(false, OpContinuation, nil)...)
	}
	c := testConn(newScript(in), false, Options{MaxFragments: 5, CloseTimeout: time.Millisecond})
	_, _, err := c.ReadMessage()
	wantProtocol(t, err, CloseMessageTooBig)
}

// A header may claim far more than the peer ever sends. Memory must follow the
// bytes that arrive, not the claim.
func TestClaimedLengthDoesNotAllocate(t *testing.T) {
	const limit = 1 << 30
	in := append(appendHeader(nil, true, OpBinary, true, testKey, limit), "short"...)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	c := testConn(newScript(in), false, Options{MaxMessageSize: limit})
	_, _, err := c.ReadMessage()
	runtime.ReadMemStats(&after)

	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v, want unexpected EOF", err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
		t.Fatalf("allocated %d bytes for a 5-byte body", grew)
	}
}

func TestHugeLengthRejectedBeforeReading(t *testing.T) {
	in := appendHeader(nil, true, OpBinary, true, testKey, 1<<62)
	c := testConn(newScript(in), false, Options{CloseTimeout: time.Millisecond})
	_, _, err := c.ReadMessage()
	wantProtocol(t, err, CloseMessageTooBig)
}

func TestLimitsDefaultWhenUnset(t *testing.T) {
	for _, v := range []int{0, -1} {
		o := Options{MaxMessageSize: v, MaxFragments: v}.withDefaults()
		if o.MaxMessageSize != DefaultMaxMessageSize || o.MaxFragments != DefaultMaxFragments {
			t.Fatalf("withDefaults(%d) = %+v", v, o)
		}
		if o.CloseTimeout != defaultCloseTimeout || o.HandshakeTimeout != defaultHandshakeTimeout || o.WriteTimeout != defaultWriteTimeout {
			t.Fatalf("timeouts = %+v", o)
		}
	}
}

func TestPongCallbackAndPeerClose(t *testing.T) {
	in := concat(
		frame(true, OpPong, []byte("beat")),
		frame(true, OpClose, closePayload(CloseGoingAway, "bye")),
	)
	s := newScript(in)
	var got string
	c := testConn(s, false, Options{CloseTimeout: time.Millisecond, OnPong: func(b []byte) { got = string(b) }})
	_, _, err := c.ReadMessage()

	var ce *CloseError
	if !errors.As(err, &ce) || ce.Code != CloseGoingAway || ce.Reason != "bye" {
		t.Fatalf("error = %v, want the peer's close", err)
	}
	if got != "beat" {
		t.Fatalf("OnPong saw %q", got)
	}
	if out, want := s.written(), plain(true, OpClose, closePayload(CloseGoingAway, "")); !bytes.Equal(out, want) {
		t.Fatalf("echo = %x, want %x", out, want)
	}
	if !s.closed {
		t.Fatal("connection left open")
	}
}

func TestPongWithoutCallbackAndEmptyClose(t *testing.T) {
	s := newScript(concat(frame(true, OpPong, nil), frame(true, OpClose, nil)))
	c := testConn(s, false, Options{CloseTimeout: time.Millisecond})
	_, _, err := c.ReadMessage()
	var ce *CloseError
	if !errors.As(err, &ce) || ce.Code != CloseNoStatus {
		t.Fatalf("error = %v, want no-status close", err)
	}
	if out, want := s.written(), plain(true, OpClose, nil); !bytes.Equal(out, want) {
		t.Fatalf("echo = %x, want %x", out, want)
	}
}

func TestPingWriteFailureEndsTheRead(t *testing.T) {
	s := newScript(frame(true, OpPing, nil))
	s.writeErr = errors.New("boom")
	c := testConn(s, false, Options{CloseTimeout: time.Millisecond})
	if _, _, err := c.ReadMessage(); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error = %v, want the write failure", err)
	}
	if !s.closed {
		t.Fatal("connection left open")
	}
}

// RFC 6455 section 5.5.2: a Ping is answered unless a Close was already
// received, and sending a Close does not count as receiving one.
func TestPingBeforeThePeersCloseIsAnsweredAfterLocalClose(t *testing.T) {
	s := newScript(concat(frame(true, OpPing, []byte("p")), frame(true, OpClose, closePayload(1000, ""))))
	c := testConn(s, false, Options{CloseTimeout: time.Millisecond})
	if err := c.Close(CloseNormal, "done"); err != nil {
		t.Fatalf("Close = %v", err)
	}
	want := concat(plain(true, OpClose, closePayload(CloseNormal, "done")), plain(true, OpPong, []byte("p")))
	if out := s.written(); !bytes.Equal(out, want) {
		t.Fatalf("wire = %x, want the close frame then the pong %x", out, want)
	}
}

func TestOnlyPongsFollowALocalClose(t *testing.T) {
	s := newScript(frame(true, OpClose, nil))
	c := testConn(s, false, Options{CloseTimeout: time.Millisecond})
	if err := c.Close(CloseNormal, ""); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if err := c.WriteMessage(OpText, []byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatalf("WriteMessage after Close = %v, want ErrClosed", err)
	}
	if err := c.Ping(nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("Ping after Close = %v, want ErrClosed", err)
	}
}

// deadlineConn holds the first SetWriteDeadline call open, the moment a writer
// is between deciding on a deadline and applying it.
type deadlineConn struct {
	*scriptConn
	entered, release chan struct{}
	calls            atomic.Int32
	mu               sync.Mutex
	applied          []time.Time
}

func (d *deadlineConn) SetWriteDeadline(t time.Time) error {
	// Only the first call waits; a later one must be free to overtake it.
	if d.calls.Add(1) == 1 {
		close(d.entered)
		<-d.release
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.applied = append(d.applied, t)
	return nil
}

func TestWriterCannotExtendTheCloseDeadline(t *testing.T) {
	d := &deadlineConn{scriptConn: newScript(nil), entered: make(chan struct{}), release: make(chan struct{})}
	c := testConn(d, false, Options{WriteTimeout: time.Hour, CloseTimeout: time.Millisecond})

	var wg sync.WaitGroup
	wg.Go(func() { c.WriteMessage(OpBinary, []byte("x")) })
	<-d.entered
	wg.Go(func() { c.Close(CloseNormal, "") })
	time.Sleep(20 * time.Millisecond)
	close(d.release)
	wg.Wait()

	last := d.applied[len(d.applied)-1]
	if time.Until(last) > time.Minute {
		t.Fatalf("the last write deadline is the writer's, %v away", time.Until(last))
	}
}

func TestCloseInterruptsABlockedWrite(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	c := testConn(a, false, Options{WriteTimeout: time.Hour, CloseTimeout: 20 * time.Millisecond})

	written := make(chan error, 1)
	go func() { written <- c.WriteMessage(OpBinary, []byte("nobody reads this")) }()
	time.Sleep(20 * time.Millisecond)

	closed := make(chan struct{})
	go func() {
		c.Close(CloseNormal, "")
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close stayed blocked behind a stalled writer")
	}
	if err := <-written; err == nil {
		t.Fatal("the stalled write should have failed")
	}
}

func TestCloseDeadlineIsSetOnce(t *testing.T) {
	d := &deadlineConn{scriptConn: newScript(nil), entered: make(chan struct{}), release: make(chan struct{})}
	close(d.release)
	c := testConn(d, false, Options{CloseTimeout: time.Millisecond})
	c.beginClosing()
	c.beginClosing()
	if len(d.applied) != 1 {
		t.Fatalf("deadline set %d times, want once", len(d.applied))
	}
}

func TestReadTruncatedInput(t *testing.T) {
	for name, in := range map[string][]byte{
		"empty":             {},
		"partial header":    {0x82},
		"partial length":    {0x82, 126},
		"control truncated": appendHeader(nil, true, OpPing, true, testKey, 10),
	} {
		t.Run(name, func(t *testing.T) {
			s := newScript(in)
			c := testConn(s, false, Options{CloseTimeout: time.Millisecond})
			_, _, err := c.ReadMessage()
			if err == nil {
				t.Fatal("want an error")
			}
			var pe *ProtocolError
			if errors.As(err, &pe) {
				t.Fatalf("truncation reported as a protocol error: %v", err)
			}
			if !s.closed {
				t.Fatal("connection left open")
			}
		})
	}
}

type failingReader struct{ err error }

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }

func TestReadBodyError(t *testing.T) {
	boom := errors.New("read failed")
	in := io.MultiReader(bytes.NewReader(appendHeader(nil, true, OpBinary, true, testKey, 10)), failingReader{boom})
	c := testConn(&scriptConn{in: in}, false, Options{})
	if _, _, err := c.ReadMessage(); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want %v", err, boom)
	}
}

func TestWriteMessageRejectsBadInput(t *testing.T) {
	c := testConn(newScript(nil), false, Options{})
	if err := c.WriteMessage(OpPing, nil); err == nil {
		t.Fatal("WriteMessage must refuse a control opcode")
	}
	if err := c.WriteMessage(OpContinuation, nil); err == nil {
		t.Fatal("WriteMessage must refuse a continuation opcode")
	}
	if err := c.WriteMessage(OpText, []byte{0xFF}); err == nil {
		t.Fatal("WriteMessage must refuse invalid text")
	}
}

func TestWriteErrorIsSticky(t *testing.T) {
	boom := errors.New("write failed")
	s := newScript(nil)
	s.writeErr = boom
	c := testConn(s, false, Options{})
	if err := c.WriteMessage(OpBinary, []byte("x")); !errors.Is(err, boom) {
		t.Fatalf("first = %v", err)
	}
	s.writeErr = nil
	if err := c.WriteMessage(OpBinary, []byte("x")); !errors.Is(err, boom) {
		t.Fatalf("second = %v, want the sticky failure", err)
	}
}

func TestPing(t *testing.T) {
	s := newScript(nil)
	c := testConn(s, true, Options{})
	if err := c.Ping(make([]byte, 126)); err == nil {
		t.Fatal("Ping must refuse more than 125 bytes")
	}
	if err := c.Ping([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	h, _, err := parseHeader(s.written())
	if err != nil || h.op != OpPing || !h.masked {
		t.Fatalf("header = %+v %v", h, err)
	}
}

func TestCloseRejectsBadArguments(t *testing.T) {
	c := testConn(newScript(nil), false, Options{})
	if err := c.Close(CloseNoStatus, ""); err == nil {
		t.Fatal("Close must refuse a code that cannot be sent")
	}
	if err := c.Close(CloseNormal, strings.Repeat("x", 124)); err == nil {
		t.Fatal("Close must refuse a reason over 123 bytes")
	}
	if err := c.Close(CloseNormal, "\xff"); err == nil {
		t.Fatal("Close must refuse an invalid reason")
	}
}

func TestCloseDrainsToThePeersClose(t *testing.T) {
	s := newScript(concat(frame(true, OpBinary, []byte("late")), frame(true, OpClose, closePayload(1000, ""))))
	c := testConn(s, false, Options{CloseTimeout: time.Millisecond})
	if err := c.Close(CloseNormal, ""); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if !s.closed {
		t.Fatal("connection left open")
	}
	if err := c.Close(CloseNormal, ""); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	var ce *CloseError
	if _, _, err := c.ReadMessage(); !errors.As(err, &ce) {
		t.Fatalf("read after Close = %v", err)
	}
}

func TestCloseReportsAWriteFailure(t *testing.T) {
	boom := errors.New("write failed")
	s := newScript(nil)
	s.writeErr = boom
	c := testConn(s, false, Options{CloseTimeout: time.Millisecond})
	if err := c.Close(CloseNormal, ""); !errors.Is(err, boom) {
		t.Fatalf("Close = %v, want %v", err, boom)
	}
}

func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	type result struct {
		c   net.Conn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := l.Accept()
		ch <- result{c, err}
	}()
	a, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	r := <-ch
	if r.err != nil {
		t.Fatal(r.err)
	}
	t.Cleanup(func() { a.Close(); r.c.Close() })
	return a, r.c
}

func TestCloseHandshakeOverTCP(t *testing.T) {
	a, b := tcpPair(t)
	client := testConn(a, true, Options{CloseTimeout: 5 * time.Second})
	server := testConn(b, false, Options{CloseTimeout: 5 * time.Millisecond})

	done := make(chan error, 1)
	go func() {
		_, _, err := server.ReadMessage()
		done <- err
	}()
	if err := client.Close(CloseNormal, "bye"); err != nil {
		t.Fatalf("client Close = %v", err)
	}
	var ce *CloseError
	if err := <-done; !errors.As(err, &ce) || ce.Code != CloseNormal || ce.Reason != "bye" {
		t.Fatalf("server read = %v", err)
	}
}

func TestSetReadDeadlineTimesOutARead(t *testing.T) {
	a, _ := tcpPair(t)
	c := testConn(a, false, Options{})
	if err := c.SetReadDeadline(time.Now().Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var ne net.Error
	if _, _, err := c.ReadMessage(); !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("error = %v, want a timeout", err)
	}
}

// A peer that keeps sending after a failure cannot hold the reader past the
// linger limit.
func TestLingerIsBounded(t *testing.T) {
	endless := io.MultiReader(bytes.NewReader([]byte{0xC1, 0x80, 0, 0, 0, 0}), zeroReader{})
	s := &scriptConn{in: endless}
	c := testConn(s, false, Options{CloseTimeout: time.Millisecond})
	done := make(chan struct{})
	go func() {
		c.ReadMessage()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("linger did not stop at the byte limit")
	}
}

type zeroReader struct{}

func (zeroReader) Read(b []byte) (int, error) {
	clear(b)
	return len(b), nil
}

func TestSubprotocolAccessor(t *testing.T) {
	s := newScript(nil)
	c := newConn(s, bufio.NewReader(s), false, Options{}, "herdsman.v1")
	if c.Subprotocol() != "herdsman.v1" {
		t.Fatalf("Subprotocol = %q", c.Subprotocol())
	}
}
