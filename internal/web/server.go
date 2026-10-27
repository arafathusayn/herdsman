// Package web serves the herdsman web app: a pairing-guarded HTTP server that
// streams herdr state to the browser and runs its actions. The app it serves
// can also be hosted elsewhere and reach this server across origins.
package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/arafathusayn/herdsman/internal/afk"
	"github.com/arafathusayn/herdsman/internal/catalog"
	"github.com/arafathusayn/herdsman/internal/choices"
	"github.com/arafathusayn/herdsman/internal/doctor"
	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/route"
)

//go:embed all:dist
var dist embed.FS

type Server struct {
	hub     *Hub
	queue   *Queue
	client  *herdr.Client
	cfg     Config
	devices *devices
	assets  fs.FS
	// diffSlots bounds how many code diff requests run git at once.
	diffSlots chan struct{}
	artifacts *artifacts
	// catalog lists the harnesses a new route can start.
	catalog *catalogSource
	now     func() time.Time
	// lastActivity is the unix time of the last recorded user action.
	lastActivity atomic.Int64
}

// Config is what one run of the server is set up with.
type Config struct {
	// Token lets a browser ask to pair; it never grants access by itself.
	Token string
	Port  int
	// Hosts names the non-loopback address bound with --host and the names
	// given with --allow-host (a proxy's domain); requests for any other
	// Host are refused.
	Hosts []string
	// Inbox is the private herdsman message folder, or "" for none.
	Inbox string
	// Executable is this herdsman binary (links resolved), which a new
	// route's checkpointer pane runs as `<Executable> checkpointer`; ""
	// turns the checkpointer of a new route off.
	Executable string
	// OnPair shows a pairing code to the person at the terminal.
	OnPair func(PairNotice)
	// OnRotate shows the person at the terminal the links for a new token,
	// after too many wrong codes ended the old one.
	OnRotate func(token string)
	// Choices keeps the picks a started route launched with (A30); nil
	// keeps none.
	Choices *choices.Store
	// GitHub probes the GitHub accounts that can read the repository of a
	// folder (A32), for the form to confirm; nil offers none.
	GitHub func(ctx context.Context, dir string) (doctor.GitHubAccess, error)
	// ActivityFile is where a paired browser's last action (a request that
	// changes something, not the app's own polling) is recorded for
	// `herdsman afk` (A33); "" records nothing.
	ActivityFile string
}

// NewToken returns a random token for one run of the server.
func NewToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func NewServer(c *herdr.Client, hub *Hub, q *Queue, cfg Config) *Server {
	sub, _ := fs.Sub(dist, "dist")
	if cfg.OnPair == nil {
		cfg.OnPair = func(PairNotice) {}
	}
	if cfg.OnRotate == nil {
		cfg.OnRotate = func(string) {}
	}
	return &Server{
		hub: hub, queue: q, client: c, cfg: cfg, devices: newDevices(cfg.Token), assets: sub,
		diffSlots: make(chan struct{}, 2), artifacts: newArtifacts(),
		catalog: newCatalogSource(catalog.ExecRunner), now: time.Now,
	}
}

// A paired browser sends its device secret as a bearer token, and a browser
// that is pairing sends its pairing secret in X-Herdsman-Pair. No cookies:
// the app may be served from another origin, and a request without a header
// carries no authority, so another site cannot ride on a paired browser.
func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/session", s.session)
	mux.HandleFunc("GET /api/pair", s.pairStatus)
	mux.HandleFunc("POST /api/pair", s.pair)
	mux.Handle("GET /api/stream", s.auth(http.HandlerFunc(s.stream)))
	mux.Handle("GET /api/state", s.auth(http.HandlerFunc(s.stateJSON)))
	mux.Handle("GET /api/pane/{id}", s.auth(http.HandlerFunc(s.pane)))
	mux.Handle("GET /api/route/file", s.auth(http.HandlerFunc(s.routeFile)))
	mux.Handle("GET /api/route/diff", s.auth(http.HandlerFunc(s.routeDiff)))
	mux.Handle("GET /api/route/runstate", s.auth(http.HandlerFunc(s.routeRunState)))
	mux.Handle("POST /api/actions", s.auth(http.HandlerFunc(s.action)))
	// The app's explicit "the user is here" (A33): sent, throttled, from the
	// paired user's own clicks and keys, which may not make any request.
	mux.Handle("POST /api/activity", s.auth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.Handle("GET /api/artifacts", s.auth(http.HandlerFunc(s.listArtifacts)))
	mux.Handle("POST /api/artifacts/open", s.auth(http.HandlerFunc(s.openArtifact)))
	mux.HandleFunc("GET /artifact/{cap}/{path...}", s.serveArtifact)
	mux.Handle("GET /api/catalog", s.auth(http.HandlerFunc(s.catalogJSON)))
	mux.Handle("GET /api/choices", s.auth(http.HandlerFunc(s.choicesJSON)))
	mux.Handle("POST /api/routes", s.auth(http.HandlerFunc(s.createRoute)))
	mux.Handle("GET /api/routes/inbox", s.auth(http.HandlerFunc(s.inboxState)))
	mux.Handle("GET /api/routes/github", s.auth(http.HandlerFunc(s.githubAccess)))
	mux.Handle("GET /api/messages", s.auth(http.HandlerFunc(s.messages)))
	mux.Handle("GET /api/messages/{box}/{id}", s.auth(http.HandlerFunc(s.messageBody)))
	mux.HandleFunc("GET /", s.static)
	return s.guard(mux)
}

// guard rejects requests whose Host is not a name this server answers to
// (DNS rebinding), answers CORS preflights, and lets any origin read the
// API: every call needs a header secret that only a paired browser holds.
func (s *Server) guard(next http.Handler) http.Handler {
	allowed := map[string]bool{"127.0.0.1": true, "localhost": true, "::1": true}
	for _, h := range s.cfg.Hosts {
		allowed[strings.ToLower(strings.Trim(h, "[]"))] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !allowed[strings.ToLower(strings.Trim(host, "[]"))] {
			http.Error(w, "forbidden host: start herdsman web with --allow-host "+host+" to serve it", http.StatusForbidden)
			return
		}
		h := w.Header()
		if r.Header.Get("Origin") != "" {
			h.Set("Access-Control-Allow-Origin", "*")
			h.Set("Access-Control-Expose-Headers", "ETag")
		}
		if r.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, If-None-Match, X-Herdsman-Token, X-Herdsman-Pair")
			h.Set("Access-Control-Max-Age", "600")
			// A public page reaching a loopback or private address (Chrome's
			// private network access) must be let through by the server.
			if r.Header.Get("Access-Control-Request-Private-Network") == "true" {
				h.Set("Access-Control-Allow-Private-Network", "true")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if !strings.HasPrefix(r.URL.Path, "/artifact/") {
			h.Set("X-Frame-Options", "DENY")
		}
		next.ServeHTTP(w, r)
	})
}

// auth lets only paired browsers through.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.devices.isPaired(bearer(r)) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "this browser is not paired: open the URL that herdsman web printed"})
			return
		}
		if userAction(r) {
			s.recordActivity()
		}
		next.ServeHTTP(w, r)
	})
}

type pairState struct {
	Status  string `json:"status"` // "paired", "pairing" or "none"
	Error   string `json:"error,omitempty"`
	Left    int    `json:"left,omitempty"`
	Expires string `json:"expires,omitempty"`
	// Pair is the secret of a new pairing request, Device the secret of a
	// newly paired browser; each is sent once, to the browser that asked.
	Pair   string `json:"pair,omitempty"`
	Device string `json:"device,omitempty"`
}

const oldTokenMessage = "The token in this URL is wrong or from an older run."

// session takes the token from the URL fragment and opens a pairing request:
// the code goes to the terminal, never to the browser.
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	// A paired browser stays paired whatever link it opens: a wrong or old
	// token must not make it drop its device secret.
	if s.devices.isPaired(bearer(r)) {
		writeJSON(w, http.StatusOK, pairState{Status: "paired"})
		return
	}
	tok := r.Header.Get("X-Herdsman-Token")
	if !s.devices.validToken(tok) {
		writeJSON(w, http.StatusUnauthorized, pairState{Status: "none", Error: oldTokenMessage})
		return
	}
	if sec := r.Header.Get("X-Herdsman-Pair"); s.devices.isPending(sec) {
		writeJSON(w, http.StatusOK, pairState{Status: "pairing"})
		return
	}
	device := describeDevice(r.UserAgent())
	// begin checks the token again under the pairing lock: it may have been
	// replaced since the check above.
	sec, code, expires, err := s.devices.begin(tok, device)
	if errors.Is(err, errOldToken) {
		writeJSON(w, http.StatusUnauthorized, pairState{Status: "none", Error: oldTokenMessage})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusTooManyRequests, pairState{Status: "none", Error: err.Error()})
		return
	}
	s.cfg.OnPair(PairNotice{Code: code, Device: device, Remote: remoteIP(r.RemoteAddr), Expires: expires})
	writeJSON(w, http.StatusOK, pairState{Status: "pairing", Expires: expires.Format(time.RFC3339), Pair: sec})
}

func (s *Server) pairStatus(w http.ResponseWriter, r *http.Request) {
	switch {
	case s.devices.isPaired(bearer(r)):
		writeJSON(w, http.StatusOK, pairState{Status: "paired"})
	case s.devices.isPending(r.Header.Get("X-Herdsman-Pair")):
		writeJSON(w, http.StatusOK, pairState{Status: "pairing"})
	default:
		writeJSON(w, http.StatusOK, pairState{Status: "none"})
	}
}

// pair checks the code the person typed from the terminal.
func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, pairState{Status: "pairing", Error: "bad request"})
		return
	}
	code := strings.Map(func(c rune) rune {
		if c >= '0' && c <= '9' {
			return c
		}
		return -1
	}, body.Code)
	dev, err := s.devices.confirm(r.Header.Get("X-Herdsman-Pair"), code)
	if err != nil {
		e, _ := errors.AsType[*errPair](err)
		st := pairState{Status: "pairing", Error: err.Error()}
		if e != nil {
			st.Left = e.left
			if e.gone {
				st.Status = "none"
			}
			if e.token != "" {
				// Every link printed so far is dead; paired browsers keep working.
				s.cfg.OnRotate(e.token)
			}
		}
		writeJSON(w, http.StatusForbidden, st)
		return
	}
	writeJSON(w, http.StatusOK, pairState{Status: "paired", Device: dev})
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	if _, ok := w.(http.Flusher); !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	ch, state, entries := s.hub.subscribe()
	defer s.hub.unsubscribe(ch)
	// Each write must reach the browser within streamWriteTimeout: one that
	// stops reading ends its stream, and with it this handler and its
	// buffers, instead of blocking here for good.
	rc := http.NewResponseController(w)
	send := func(f func()) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
		f()
		return rc.Flush() == nil
	}
	write := func(event string, data []byte) {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	}
	if !send(func() {
		if state != nil {
			write("state", state)
		}
		if b, err := json.Marshal(entries); err == nil {
			write("entries", b)
		}
	}) {
		return
	}
	ping := time.NewTicker(streamPing)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case m, ok := <-ch:
			if !ok {
				return // dropped as stalled; the browser reconnects to a fresh snapshot
			}
			if !send(func() { write(m.event, m.data) }) {
				return
			}
		case <-ping.C:
			if !send(func() { fmt.Fprint(w, ": ping\n\n") }) {
				return
			}
		}
	}
}

// streamWriteTimeout bounds one write of the live stream; tests shorten it.
var streamWriteTimeout = 30 * time.Second

// streamPing is how often an idle stream sends a comment, so proxies keep it
// open; tests shorten it.
var streamPing = 15 * time.Second

func (s *Server) stateJSON(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.hub.Current())
}

// pane returns the last lines of a pane, with ?format=ansi in color. The
// ETag lets a browser that polls once a second get a bodyless 304 while
// nothing changes.
func (s *Server) pane(w http.ResponseWriter, r *http.Request) {
	lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if lines <= 0 || lines > 2000 {
		lines = 300
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	q := r.URL.Query()
	read, err := s.client.ReadPane(ctx, r.PathValue("id"), herdr.ReadOptions{
		Lines:   lines,
		ANSI:    q.Get("format") == "ansi",
		Visible: q.Get("source") == "visible",
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	sum := sha256.Sum256([]byte(read.Text))
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, read)
}

// routeFile serves one Markdown file from a route folder the hub knows.
func (s *Server) routeFile(w http.ResponseWriter, r *http.Request) {
	rv := findRoute(s.hub.Current().Routes, r.URL.Query().Get("route"))
	if rv == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown route"})
		return
	}
	b, err := route.ReadFile(rv.Dir, r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

func (s *Server) action(w http.ResponseWriter, r *http.Request) {
	var a Action
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		writeJSON(w, http.StatusBadRequest, Result{Code: "bad_request", Error: err.Error()})
		return
	}
	res := s.queue.Do(r.Context(), a)
	status := http.StatusOK
	switch {
	case res.Code == "needs_force":
		status = http.StatusConflict
	case res.Code == "invalid_action":
		status = http.StatusBadRequest
	case !res.OK:
		status = http.StatusBadGateway
	}
	writeJSON(w, status, res)
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" || strings.HasSuffix(p, "/") {
		p += "index.html"
	}
	if fi, err := fs.Stat(s.assets, p); err != nil {
		p = "index.html" // client-side routes
	} else if fi.IsDir() {
		// A page folder such as docs: its relative links need the slash.
		if _, err := fs.Stat(s.assets, path.Join(p, "index.html")); err == nil {
			to := "./" + path.Base(p) + "/"
			if r.URL.RawQuery != "" {
				to += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, to, http.StatusMovedPermanently)
			return
		}
		p = "index.html"
	}
	if _, err := fs.Stat(s.assets, "index.html"); err != nil {
		http.Error(w, "web assets are not built into this binary: run `make web` and build again", http.StatusNotFound)
		return
	}
	// Asset names carry no hash, so the browser revalidates every load.
	w.Header().Set("Cache-Control", "no-cache")
	if strings.HasSuffix(p, ".html") {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; connect-src 'self' http: https:; frame-src http: https:; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	}
	http.ServeFileFS(w, r, s.assets, p)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Listen binds to one address: loopback unless the user names another with
// --host. Port 0 picks a free port.
func Listen(host string, port int) (net.Listener, int, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, 0, err
	}
	return ln, ln.Addr().(*net.TCPAddr).Port, nil
}

// polled are the GET endpoints the app reads on its own timers; a request to
// any other paired endpoint is the user's action or navigation (A33).
var polled = []string{"/api/stream", "/api/state", "/api/pane/", "/api/route/diff", "/api/routes/inbox", "/api/messages", "/api/artifacts"}

// userAction says whether a paired request is the user's own: a change, or
// a GET outside the polled endpoints (opening New route, a file, a message).
func userAction(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return true
	}
	for _, p := range polled {
		if r.URL.Path == p || (strings.HasSuffix(p, "/") && strings.HasPrefix(r.URL.Path, p)) {
			return false
		}
	}
	// A message body is opened by the user; the message list is polled.
	return true
}

// activityEvery bounds how often the activity file is written.
const activityEvery = 10 * time.Second

// recordActivity writes the time of a user action for `herdsman afk`, at
// most once per activityEvery.
func (s *Server) recordActivity() {
	if s.cfg.ActivityFile == "" {
		return
	}
	now := s.now()
	last := s.lastActivity.Load()
	if now.Unix()-last < int64(activityEvery/time.Second) || !s.lastActivity.CompareAndSwap(last, now.Unix()) {
		return
	}
	if err := afk.WriteWebFile(s.cfg.ActivityFile, now); err != nil {
		log.Printf("herdsman web: cannot record activity: %v", err)
	}
}

// routeRunState pages a route's older run-state lines: up to runStateKept
// lines ending before line index `before`, within the newest route.Window
// records, read backward from the hub's snapshot of the file; a file that
// changed since gives 409 (A24).
func (s *Server) routeRunState(w http.ResponseWriter, r *http.Request) {
	rv := findRoute(s.hub.Current().Routes, r.URL.Query().Get("route"))
	if rv == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown route"})
		return
	}
	before, err := strconv.Atoi(r.URL.Query().Get("before"))
	if err != nil || before < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "before must be a whole number"})
		return
	}
	before = max(rv.tail.From(), min(before, rv.tail.Total))
	lines, err := rv.tail.Page(before, runStateKept)
	switch {
	case errors.Is(err, route.ErrChanged):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the route file changed: reload the route"})
		return
	case err != nil:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "the route file could not be read"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines, "older": before - len(lines), "from": rv.tail.From(), "history": rv.tail.History()})
}
