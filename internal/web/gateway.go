package web

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/arafathusayn/herdsman/sdk/rpc"
)

const (
	// daemonHost is the URL host of a call on the socket; the socket decides
	// where it goes.
	daemonHost = "herdsman"
	eventsPath = rpc.BasePath + "herdsman.daemon.v1.EventService/"
	streamPath = eventsPath + "Stream"
	waitPath   = eventsPath + "Wait"
)

// streamProxy serves the event stream. It is nil in a build without the stream
// contract, and such a build answers the stream's path as unimplemented.
var streamProxy func(*gateway, http.ResponseWriter, *http.Request)

// gateway forwards the daemon's RPC services to paired browsers on this
// machine. It holds no state of its own: the daemon stays the only authority,
// and the gateway only says which browser is asking.
type gateway struct {
	devices *devices
	origins map[string]bool
	dial    func(ctx context.Context, network, addr string) (net.Conn, error)
	forward http.Handler
	// activity records a call as the user's own.
	activity func()
}

func newGateway(socket string, origins []string, d *devices, activity func()) *gateway {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socket)
	}
	allowed := make(map[string]bool, len(origins))
	for _, origin := range origins {
		allowed[strings.ToLower(origin)] = true
	}
	return &gateway{devices: d, origins: allowed, dial: dial, forward: newForward(dial), activity: activity}
}

func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !localRequest(r) {
		writeFailure(w, rpc.PermissionDenied, "the RPC gateway answers only on this machine")
		return
	}
	// The stream's own handshake applies the session and origin rules below,
	// since a refusal there must also end the upgrade.
	if r.URL.Path == streamPath && streamProxy != nil {
		streamProxy(g, w, r)
		return
	}
	session, ok := g.session(r)
	if !ok {
		writeFailure(w, rpc.Unauthenticated, "this browser is not paired: open the URL that herdsman web printed")
		return
	}
	if !g.originAllowed(r) {
		writeFailure(w, rpc.PermissionDenied, "this origin may not call the gateway")
		return
	}
	// A reverse proxy would tunnel an upgrade straight past the stream's rules.
	if r.Header.Get("Upgrade") != "" {
		writeFailure(w, rpc.Unimplemented, "this build has no event stream")
		return
	}
	if r.URL.Path != waitPath {
		g.activity()
	}
	r.Header.Set(HeaderSession, session)
	g.forward.ServeHTTP(w, r)
}

// localRequest reports whether a request reached a loopback listener, under a
// loopback name, with no proxy in between. Any one alone can be defeated: a
// tunnel connects over loopback, and a proxy can rewrite the name.
func localRequest(r *http.Request) bool {
	addr, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr)
	if !ok || !addr.IP.IsLoopback() {
		return false
	}
	for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Real-Ip"} {
		if r.Header.Get(name) != "" {
			return false
		}
	}
	return loopbackName(r.Host)
}

func loopbackName(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// writeFailure answers in the binding's failure form, for a refusal the daemon
// never sees.
func writeFailure(w http.ResponseWriter, code rpc.Code, message string) {
	w.Header().Set("Content-Type", rpc.ContentTypeJSON)
	w.WriteHeader(code.HTTPStatus())
	_ = json.NewEncoder(w).Encode(struct {
		Code    rpc.Code `json:"code"`
		Message string   `json:"message"`
	}{code, message})
}
