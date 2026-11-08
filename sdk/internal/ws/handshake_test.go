package ws

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const sampleKey = "dGhlIHNhbXBsZSBub25jZQ=="

func TestAcceptKeyMatchesRFCExample(t *testing.T) {
	if got := acceptKey(sampleKey); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("acceptKey = %q", got)
	}
}

func upgradeRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://example.test/ws", nil)
	r.Header.Set("Upgrade", "WebSocket")
	r.Header.Set("Connection", "keep-alive, Upgrade")
	r.Header.Set("Sec-WebSocket-Version", "13")
	r.Header.Set("Sec-WebSocket-Key", sampleKey)
	return r
}

func TestUpgradeRejectsBadRequests(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*http.Request)
		status int
	}{
		{"method", func(r *http.Request) { r.Method = http.MethodPost }, http.StatusMethodNotAllowed},
		{"old protocol", func(r *http.Request) { r.ProtoMajor, r.ProtoMinor = 1, 0 }, http.StatusBadRequest},
		{"no upgrade", func(r *http.Request) { r.Header.Del("Upgrade") }, http.StatusBadRequest},
		{"no connection token", func(r *http.Request) { r.Header.Set("Connection", "close") }, http.StatusBadRequest},
		{"wrong version", func(r *http.Request) { r.Header.Set("Sec-WebSocket-Version", "8") }, http.StatusUpgradeRequired},
		{"no key", func(r *http.Request) { r.Header.Del("Sec-WebSocket-Key") }, http.StatusBadRequest},
		{"two keys", func(r *http.Request) { r.Header.Add("Sec-WebSocket-Key", sampleKey) }, http.StatusBadRequest},
		{"key not base64", func(r *http.Request) { r.Header.Set("Sec-WebSocket-Key", "!!!") }, http.StatusBadRequest},
		{"short key", func(r *http.Request) { r.Header.Set("Sec-WebSocket-Key", "c2hvcnQ=") }, http.StatusBadRequest},
		{"cross origin", func(r *http.Request) { r.Header.Set("Origin", "https://evil.test") }, http.StatusForbidden},
		{"null origin", func(r *http.Request) { r.Header.Set("Origin", "null") }, http.StatusForbidden},
		{"malformed origin", func(r *http.Request) { r.Header.Set("Origin", "http://%zz") }, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := upgradeRequest()
			tt.mutate(r)
			w := httptest.NewRecorder()
			var he *HandshakeError
			if _, err := Upgrade(w, r, ServerOptions{}); !errors.As(err, &he) {
				t.Fatalf("Upgrade = %v, want a handshake error", err)
			}
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d", w.Code, tt.status)
			}
			if tt.status == http.StatusUpgradeRequired && w.Header().Get("Sec-WebSocket-Version") != "13" {
				t.Fatal("426 must advertise version 13")
			}
		})
	}
}

func TestUpgradeWithoutHijackSupport(t *testing.T) {
	w := httptest.NewRecorder()
	if _, err := Upgrade(w, upgradeRequest(), ServerOptions{}); err == nil {
		t.Fatal("want an error when the writer cannot hijack")
	}
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", w.Code)
	}
}

// echoServer upgrades and echoes until the connection ends.
func echoServer(t *testing.T, opts ServerOptions) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(echoHandler(opts))
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func echoHandler(opts ServerOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Upgrade(w, r, opts)
		if err != nil {
			return
		}
		for {
			op, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			if err := c.WriteMessage(op, data); err != nil {
				return
			}
		}
	})
}

func wsURL(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func TestDialEchoRoundTrip(t *testing.T) {
	srv := echoServer(t, ServerOptions{})
	c, err := Dial(context.Background(), wsURL(srv)+"/echo?x=1", ClientOptions{
		Header: http.Header{"X-Test": {"1"}, "Sec-Websocket-Key": {"attacker"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{"hello", strings.Repeat("x", 70000)} {
		if err := c.WriteMessage(OpText, []byte(msg)); err != nil {
			t.Fatal(err)
		}
		op, got, err := c.ReadMessage()
		if err != nil || op != OpText || string(got) != msg {
			t.Fatalf("echo = %v %d bytes %v", op, len(got), err)
		}
	}
	if err := c.Close(CloseNormal, ""); err != nil {
		t.Fatalf("Close = %v", err)
	}
}

func TestDialOverTLS(t *testing.T) {
	srv := httptest.NewUnstartedServer(echoHandler(ServerOptions{}))
	srv.StartTLS()
	t.Cleanup(srv.Close)

	c, err := Dial(context.Background(), "wss"+strings.TrimPrefix(srv.URL, "https"), ClientOptions{
		TLSConfig: srv.Client().Transport.(*http.Transport).TLSClientConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.WriteMessage(OpBinary, []byte("tls")); err != nil {
		t.Fatal(err)
	}
	if _, got, err := c.ReadMessage(); err != nil || string(got) != "tls" {
		t.Fatalf("echo = %q %v", got, err)
	}
	c.Close(CloseNormal, "")
}

func TestDialTLSVerifiesTheServerByDefault(t *testing.T) {
	srv := httptest.NewUnstartedServer(echoHandler(ServerOptions{}))
	srv.StartTLS()
	t.Cleanup(srv.Close)
	if _, err := Dial(context.Background(), "wss"+strings.TrimPrefix(srv.URL, "https"), ClientOptions{}); err == nil {
		t.Fatal("an untrusted certificate must fail the dial")
	}
}

func TestSubprotocolNegotiation(t *testing.T) {
	srv := echoServer(t, ServerOptions{Options: Options{Subprotocols: []string{"b", "a"}}})
	c, err := Dial(context.Background(), wsURL(srv), ClientOptions{Options: Options{Subprotocols: []string{"a", "b"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(CloseNormal, "")
	if c.Subprotocol() != "b" {
		t.Fatalf("Subprotocol = %q, want the server's first match", c.Subprotocol())
	}

	c2, err := Dial(context.Background(), wsURL(srv), ClientOptions{Options: Options{Subprotocols: []string{"z"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close(CloseNormal, "")
	if c2.Subprotocol() != "" {
		t.Fatalf("Subprotocol = %q, want none", c2.Subprotocol())
	}
}

func TestCheckOriginOverrideAndSameOrigin(t *testing.T) {
	srv := echoServer(t, ServerOptions{CheckOrigin: func(*http.Request) bool { return true }})
	c, err := Dial(context.Background(), wsURL(srv), ClientOptions{Header: http.Header{"Origin": {"https://other.test"}}})
	if err != nil {
		t.Fatal(err)
	}
	c.Close(CloseNormal, "")

	same := echoServer(t, ServerOptions{})
	c2, err := Dial(context.Background(), wsURL(same), ClientOptions{Header: http.Header{"Origin": {same.URL}}})
	if err != nil {
		t.Fatalf("same origin refused: %v", err)
	}
	c2.Close(CloseNormal, "")
}

func TestDialBadURLs(t *testing.T) {
	for _, u := range []string{"http://x.test", "::bad", "ws://"} {
		if _, err := Dial(context.Background(), u, ClientOptions{}); err == nil {
			t.Fatalf("Dial(%q) must fail", u)
		}
	}
}

func TestDialUsesDefaultPorts(t *testing.T) {
	var seen []string
	dial := func(_ context.Context, _, addr string) (net.Conn, error) {
		seen = append(seen, addr)
		return nil, errors.New("stop")
	}
	for _, u := range []string{"ws://a.test/x", "wss://b.test/x", "ws://c.test:8080/x"} {
		if _, err := Dial(context.Background(), u, ClientOptions{DialContext: dial}); err == nil {
			t.Fatal("want the dial failure")
		}
	}
	want := "a.test:80 b.test:443 c.test:8080"
	if got := strings.Join(seen, " "); got != want {
		t.Fatalf("addresses = %q, want %q", got, want)
	}
}

// fakeServer answers a client handshake with a canned response.
func fakeServer(t *testing.T, respond func(key string) string) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				req, err := http.ReadRequest(bufio.NewReader(conn))
				if err != nil {
					return
				}
				fmt.Fprint(conn, respond(req.Header.Get("Sec-WebSocket-Key")))
				time.Sleep(50 * time.Millisecond)
			}()
		}
	}()
	return "ws://" + l.Addr().String() + "/"
}

func response(accept string, extra ...string) string {
	lines := append([]string{
		"HTTP/1.1 101 Switching Protocols",
		"Upgrade: websocket",
		"Connection: Upgrade",
		"Sec-WebSocket-Accept: " + accept,
	}, extra...)
	return strings.Join(lines, "\r\n") + "\r\n\r\n"
}

func TestDialRejectsBadServerResponses(t *testing.T) {
	tests := []struct {
		name    string
		respond func(string) string
	}{
		{"status", func(string) string { return "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n" }},
		{"no upgrade", func(k string) string {
			return "HTTP/1.1 101 X\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + acceptKey(k) + "\r\n\r\n"
		}},
		{"no connection", func(k string) string {
			return "HTTP/1.1 101 X\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: " + acceptKey(k) + "\r\n\r\n"
		}},
		{"wrong accept", func(string) string { return response("AAAA") }},
		{"extension never offered", func(k string) string {
			return response(acceptKey(k), "Sec-WebSocket-Extensions: permessage-deflate")
		}},
		{"subprotocol never offered", func(k string) string {
			return response(acceptKey(k), "Sec-WebSocket-Protocol: other")
		}},
		{"not http", func(string) string { return "garbage\r\n\r\n" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := fakeServer(t, tt.respond)
			if _, err := Dial(context.Background(), url, ClientOptions{}); err == nil {
				t.Fatal("Dial must refuse this response")
			}
		})
	}
}

func TestDialBoundsTheHandshakeResponse(t *testing.T) {
	url := fakeServer(t, func(string) string {
		return "HTTP/1.1 101 X\r\nX-Pad: " + strings.Repeat("a", 2*maxHandshakeResponse) + "\r\n\r\n"
	})
	if _, err := Dial(context.Background(), url, ClientOptions{}); err == nil {
		t.Fatal("Dial must refuse an oversized response")
	}
}

func TestDialKeepsBytesAfterTheHandshake(t *testing.T) {
	url := fakeServer(t, func(k string) string {
		return response(acceptKey(k)) + string(plain(true, OpText, []byte("early")))
	})
	c, err := Dial(context.Background(), url, ClientOptions{Options: Options{CloseTimeout: time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	if _, got, err := c.ReadMessage(); err != nil || string(got) != "early" {
		t.Fatalf("first message = %q %v", got, err)
	}
}

func TestDialHonoursContext(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := Dial(ctx, "ws://"+l.Addr().String(), ClientOptions{}); err == nil {
		t.Fatal("a silent server must hit the deadline")
	}
}

type failWriteDialConn struct{ net.Conn }

func (failWriteDialConn) Write([]byte) (int, error) { return 0, errors.New("no write") }

func TestDialRequestWriteFailure(t *testing.T) {
	a, _ := tcpPair(t)
	_, err := Dial(context.Background(), "ws://x.test/", ClientOptions{
		DialContext: func(context.Context, string, string) (net.Conn, error) { return failWriteDialConn{a}, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "no write") {
		t.Fatalf("Dial = %v", err)
	}
}

func TestUpgradeResponseWriteFailure(t *testing.T) {
	a, _ := tcpPair(t)
	w := &hijackRecorder{ResponseRecorder: httptest.NewRecorder(), conn: failWriteDialConn{a}}
	if _, err := Upgrade(w, upgradeRequest(), ServerOptions{}); err == nil || !strings.Contains(err.Error(), "no write") {
		t.Fatalf("Upgrade = %v", err)
	}
}

type hijackRecorder struct {
	*httptest.ResponseRecorder
	conn net.Conn
}

func (h *hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return h.conn, bufio.NewReadWriter(bufio.NewReader(h.conn), bufio.NewWriter(h.conn)), nil
}

func TestTLSConfigIsNotMutated(t *testing.T) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	Dial(context.Background(), "wss://x.test/", ClientOptions{
		TLSConfig:   cfg,
		DialContext: func(context.Context, string, string) (net.Conn, error) { a, _ := net.Pipe(); a.Close(); return a, nil },
	})
	if cfg.ServerName != "" {
		t.Fatalf("Dial wrote ServerName %q into the caller's config", cfg.ServerName)
	}
}

func TestHeaderTokens(t *testing.T) {
	h := http.Header{"Connection": {"keep-alive", " foo , UPGRADE"}}
	if !headerHasToken(h, "Connection", "upgrade") {
		t.Fatal("token across values and case not found")
	}
	if headerHasToken(h, "Connection", "close") {
		t.Fatal("found an absent token")
	}
}
