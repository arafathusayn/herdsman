package web

import (
	"net/http"
	"net/url"
	"strings"
)

const (
	// HeaderSession tells the daemon which paired browser a call came from.
	HeaderSession = "Herdsman-Session"
	// sessionProtocol prefixes the device secret among a WebSocket's offered
	// subprotocols, the one place a browser can put a secret on an upgrade
	// without it landing in a URL. The server never selects this entry.
	sessionProtocol = "herdsman.session."
	sessionIDLength = 32
)

// sessionID names a browser to the daemon without revealing its secret.
func sessionID(device string) string {
	return digest("herdsman-session\x00" + device)[:sessionIDLength]
}

// sessionSecret is the device secret a request presents.
func sessionSecret(r *http.Request) string {
	if secret := bearer(r); secret != "" {
		return secret
	}
	for _, value := range r.Header.Values("Sec-Websocket-Protocol") {
		for entry := range strings.SplitSeq(value, ",") {
			if secret, ok := strings.CutPrefix(strings.TrimSpace(entry), sessionProtocol); ok {
				return secret
			}
		}
	}
	return ""
}

// session is the id of the paired browser behind a request.
func (g *gateway) session(r *http.Request) (string, bool) {
	secret := sessionSecret(r)
	if !g.devices.isPaired(secret) {
		return "", false
	}
	return sessionID(secret), true
}

// originAllowed admits a client that sends no Origin, this server's own page,
// and the hosted app named at startup.
func (g *gateway) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host) || g.origins[strings.ToLower(origin)]
}
