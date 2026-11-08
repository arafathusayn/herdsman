package ws

import (
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const acceptGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// HandshakeError is a refused opening handshake. Status is the HTTP status a
// server answered with, or the one a client received; zero when none applies.
type HandshakeError struct {
	Status int
	Reason string
}

func (e *HandshakeError) Error() string { return "ws: handshake: " + e.Reason }

// ServerOptions configure Upgrade.
type ServerOptions struct {
	Options
	// CheckOrigin decides whether the request's origin may connect. Nil
	// accepts a request with no Origin or one whose host equals the request's,
	// which stops another site's page from opening a connection with the
	// user's credentials.
	CheckOrigin func(*http.Request) bool
}

func acceptKey(key string) string {
	sum := sha1.Sum([]byte(key + acceptGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// headerTokens returns the comma-separated values of a header.
func headerTokens(h http.Header, name string) []string {
	var tokens []string
	for _, value := range h.Values(name) {
		for token := range strings.SplitSeq(value, ",") {
			tokens = append(tokens, strings.TrimSpace(token))
		}
	}
	return tokens
}

func headerHasToken(h http.Header, name, token string) bool {
	return slices.ContainsFunc(headerTokens(h, name), func(t string) bool {
		return strings.EqualFold(t, token)
	})
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

func refuse(w http.ResponseWriter, status int, reason string) error {
	http.Error(w, reason, status)
	return &HandshakeError{Status: status, Reason: reason}
}

// Upgrade validates an opening handshake and takes over the connection. On
// failure it has already answered the request.
func Upgrade(w http.ResponseWriter, r *http.Request, opts ServerOptions) (*Conn, error) {
	if r.Method != http.MethodGet {
		return nil, refuse(w, http.StatusMethodNotAllowed, "method must be GET")
	}
	if !r.ProtoAtLeast(1, 1) {
		return nil, refuse(w, http.StatusBadRequest, "HTTP/1.1 or later required")
	}
	if !headerHasToken(r.Header, "Upgrade", "websocket") {
		return nil, refuse(w, http.StatusBadRequest, "missing Upgrade: websocket")
	}
	if !headerHasToken(r.Header, "Connection", "upgrade") {
		return nil, refuse(w, http.StatusBadRequest, "missing Connection: Upgrade")
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		w.Header().Set("Sec-WebSocket-Version", "13")
		return nil, refuse(w, http.StatusUpgradeRequired, "unsupported WebSocket version")
	}
	keys := r.Header.Values("Sec-WebSocket-Key")
	if len(keys) != 1 {
		return nil, refuse(w, http.StatusBadRequest, "exactly one Sec-WebSocket-Key required")
	}
	if nonce, err := base64.StdEncoding.DecodeString(keys[0]); err != nil || len(nonce) != 16 {
		return nil, refuse(w, http.StatusBadRequest, "invalid Sec-WebSocket-Key")
	}
	checkOrigin := opts.CheckOrigin
	if checkOrigin == nil {
		checkOrigin = sameOrigin
	}
	if !checkOrigin(r) {
		return nil, refuse(w, http.StatusForbidden, "origin not allowed")
	}

	o := opts.Options.withDefaults()
	offered := headerTokens(r.Header, "Sec-WebSocket-Protocol")
	var subprotocol string
	if i := slices.IndexFunc(o.Subprotocols, func(p string) bool { return slices.Contains(offered, p) }); i >= 0 {
		subprotocol = o.Subprotocols[i]
	}

	netConn, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return nil, refuse(w, http.StatusInternalServerError, "connection cannot be taken over")
	}

	var response strings.Builder
	fmt.Fprintf(&response, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n", acceptKey(keys[0]))
	if subprotocol != "" {
		fmt.Fprintf(&response, "Sec-WebSocket-Protocol: %s\r\n", subprotocol)
	}
	response.WriteString("\r\n")

	// The HTTP server's deadlines must not carry over into the connection.
	netConn.SetDeadline(time.Time{})
	netConn.SetWriteDeadline(time.Now().Add(o.HandshakeTimeout))
	if _, err := netConn.Write([]byte(response.String())); err != nil {
		netConn.Close()
		return nil, fmt.Errorf("ws: handshake: %w", err)
	}
	netConn.SetWriteDeadline(time.Time{})
	return newConn(netConn, brw.Reader, false, o, subprotocol), nil
}
