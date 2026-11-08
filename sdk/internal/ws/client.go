package ws

import (
	"bufio"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// maxHandshakeResponse bounds the server's handshake response, because
// http.ReadResponse puts no limit on the size of the headers it reads.
const maxHandshakeResponse = 64 << 10

// ClientOptions configure Dial.
type ClientOptions struct {
	Options
	// Header adds request headers. The handshake's own headers always win.
	Header http.Header
	// TLSConfig applies to wss URLs. Nil verifies against the system roots.
	TLSConfig *tls.Config
	// DialContext replaces the default TCP dialer.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Dial connects to a ws or wss URL and runs the opening handshake.
func Dial(ctx context.Context, rawURL string, opts ClientOptions) (*Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("ws: dial: %w", err)
	}
	port := map[string]string{"ws": "80", "wss": "443"}[u.Scheme]
	if port == "" || u.Hostname() == "" {
		return nil, fmt.Errorf("ws: dial: %q is not a ws or wss URL", rawURL)
	}
	addr := net.JoinHostPort(u.Hostname(), cmp.Or(u.Port(), port))

	o := opts.Options.withDefaults()
	ctx, cancel := context.WithTimeout(ctx, o.HandshakeTimeout)
	defer cancel()

	dial := opts.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	raw, err := dial(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("ws: dial: %w", err)
	}

	stop := context.AfterFunc(ctx, func() { raw.SetDeadline(time.Unix(1, 0)) })
	c, err := handshake(ctx, raw, u, opts, o)
	stop()
	// A context that ended here may already have poisoned the deadline.
	if err = cmp.Or(err, ctx.Err()); err != nil {
		raw.Close()
		return nil, fmt.Errorf("ws: dial: %w", err)
	}
	raw.SetDeadline(time.Time{})
	return c, nil
}

func handshake(ctx context.Context, raw net.Conn, u *url.URL, opts ClientOptions, o Options) (*Conn, error) {
	conn := raw
	if u.Scheme == "wss" {
		cfg := &tls.Config{}
		if opts.TLSConfig != nil {
			cfg = opts.TLSConfig.Clone()
		}
		cfg.ServerName = cmp.Or(cfg.ServerName, u.Hostname())
		cfg.NextProtos = []string{"http/1.1"}
		tc := tls.Client(raw, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		conn = tc
	}

	var nonce [16]byte
	rand.Read(nonce[:])
	key := base64.StdEncoding.EncodeToString(nonce[:])

	h := opts.Header.Clone()
	if h == nil {
		h = http.Header{}
	}
	h.Set("Upgrade", "websocket")
	h.Set("Connection", "Upgrade")
	h.Set("Sec-WebSocket-Version", "13")
	h.Set("Sec-WebSocket-Key", key)
	if len(o.Subprotocols) > 0 {
		h.Set("Sec-WebSocket-Protocol", strings.Join(o.Subprotocols, ", "))
	}
	req := &http.Request{Method: http.MethodGet, URL: u, Host: u.Host, Header: h, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1}
	if err := req.Write(conn); err != nil {
		return nil, err
	}

	limited := &io.LimitedReader{R: conn, N: maxHandshakeResponse}
	br := bufio.NewReader(limited)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return nil, err
	}
	if err := checkResponse(resp, key, o.Subprotocols); err != nil {
		return nil, err
	}
	// The limit was for the handshake only; frames have their own.
	limited.N = math.MaxInt64
	return newConn(conn, br, true, o, resp.Header.Get("Sec-WebSocket-Protocol")), nil
}

func checkResponse(resp *http.Response, key string, offered []string) error {
	fail := func(reason string) error {
		return &HandshakeError{Status: resp.StatusCode, Reason: reason}
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return fail("unexpected status " + resp.Status)
	}
	if !headerHasToken(resp.Header, "Upgrade", "websocket") {
		return fail("missing Upgrade: websocket")
	}
	if !headerHasToken(resp.Header, "Connection", "upgrade") {
		return fail("missing Connection: Upgrade")
	}
	if resp.Header.Get("Sec-WebSocket-Accept") != acceptKey(key) {
		return fail("wrong Sec-WebSocket-Accept")
	}
	if resp.Header.Get("Sec-WebSocket-Extensions") != "" {
		return fail("server selected an extension that was not offered")
	}
	if p := resp.Header.Get("Sec-WebSocket-Protocol"); p != "" && !slices.Contains(offered, p) {
		return fail("server selected a subprotocol that was not offered")
	}
	return nil
}
