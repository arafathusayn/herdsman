package web

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// newForward carries calls to the daemon unchanged, apart from the headers
// that belong to this side of the gateway.
func newForward(dial func(ctx context.Context, network, addr string) (net.Conn, error)) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(p *httputil.ProxyRequest) {
			p.Out.URL.Scheme, p.Out.URL.Host, p.Out.Host = "http", daemonHost, daemonHost
			session := p.In.Header.Get(HeaderSession)
			for name := range p.Out.Header {
				if browserOnly(name) {
					p.Out.Header.Del(name)
				}
			}
			p.Out.Header.Set(HeaderSession, session)
		},
		Transport: &http.Transport{
			DialContext:        dial,
			MaxIdleConns:       2,
			IdleConnTimeout:    30 * time.Second,
			DisableCompression: true,
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			writeFailure(w, rpc.Unavailable, "the herdsman daemon is not reachable")
		},
	}
}

// browserOnly names what must not cross to the daemon: the browser's secrets,
// and any header the daemon reads as who an agent is, which a browser could
// otherwise forge.
func browserOnly(name string) bool {
	switch name = strings.ToLower(name); name {
	case "authorization", "cookie", "origin", "referer":
		return true
	default:
		return strings.HasPrefix(name, "herdsman-") || strings.HasPrefix(name, "x-herdsman-")
	}
}
