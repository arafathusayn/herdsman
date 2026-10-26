package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Request bounds (9.1, 9.3).
const (
	maxBody         = 160 << 10
	maxStreamsParty = 4
	maxStreamsView  = 16
	maxStreamsAll   = 256
	writeDeadline   = 10 * time.Second
)

// handler is the daemon's HTTP API (5.1).
func (d *Daemon) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/hello", d.hello)
	h := func(pattern string, fn func(w http.ResponseWriter, r *http.Request) *APIError) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if e := CheckVersion(r.Header, d.cfg.Version, Protocol); e != nil {
				writeError(w, e)
				return
			}
			if e := fn(w, r); e != nil {
				writeError(w, e)
			}
		})
	}
	h("GET /v1/health", d.health)
	h("POST /v1/shutdown", d.shutdown)
	h("GET /v1/routes", d.listRoutes)
	h("POST /v1/routes", d.createRoute)
	h("POST /v1/routes/{route}/adopt", d.adoptRoute)
	h("DELETE /v1/routes/{route}", d.removeRoute)
	h("POST /v1/routes/{route}/reset-events", d.resetEvents)
	h("GET /v1/routes/{route}/status", d.routeStatus)
	h("GET /v1/routes/{route}/setup", d.getSetup)
	h("PUT /v1/routes/{route}/setup", d.putSetup)
	h("POST /v1/routes/{route}/enroll", d.enroll)
	h("POST /v1/routes/{route}/pair/approve", d.approve)
	h("POST /v1/routes/{route}/pair/retire", d.retire)
	h("POST /v1/routes/{route}/messages", d.send)
	h("POST /v1/routes/{route}/claims", d.claim)
	h("POST /v1/routes/{route}/claims/{token}/acks", d.ack)
	h("DELETE /v1/routes/{route}/claims/{token}", d.release)
	h("PUT /v1/routes/{route}/holds/{name}", d.hold)
	h("DELETE /v1/routes/{route}/holds/{name}", d.unhold)
	h("GET /v1/routes/{route}/events", d.partyEvents)
	h("GET /v1/events", d.viewEvents)
	h("GET /v1/routes/{route}/authority", d.authority)
	h("POST /v1/routes/{route}/credentials/{name}/revoke", d.revokeCredential)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, Errf("not_found", "no such request: %s %s", r.Method, r.URL.Path))
	})
	return d.limit(mux)
}

// limit is the request semaphore (9.1): at most 32 requests in flight
// before a body is read. Streams give their slot back once attached.
func (d *Daemon) limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case d.sem <- struct{}{}:
		case <-time.After(2 * time.Second):
			writeError(w, Errf("busy", "too many requests in flight; retry"))
			return
		}
		released := false
		release := func() {
			if !released {
				released = true
				<-d.sem
			}
		}
		defer release()
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), releaseKey{}, release)))
	})
}

type releaseKey struct{}

// releaseSlot gives a stream's semaphore slot back.
func releaseSlot(r *http.Request) {
	if f, ok := r.Context().Value(releaseKey{}).(func()); ok {
		f()
	}
}

// decode reads a strict JSON body of at most 160 KiB.
func decode(r *http.Request, v any) *APIError {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return Errf("bad_request", "body: %v", err)
	}
	if len(b) > maxBody {
		return Errf("bad_request", "body larger than %d bytes", maxBody)
	}
	if err := strictDecode(b, v); err != nil {
		return Errf("bad_request", "strict JSON: %v", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := encode(v)
	_, _ = w.Write(append(b, '\n'))
}

func (d *Daemon) helloBody() Hello {
	features := []string{}
	opts := credentialOptions()
	if len(opts) == 3 {
		features = append(features, "keychain")
	}
	return Hello{DaemonVersion: d.cfg.Version, Protocol: Protocol, MinProtocol: Protocol, Features: features,
		CredentialOptions: opts, DefaultCredential: "file"}
}

func (d *Daemon) hello(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, d.helloBody())
}

// Health is GET /v1/health.
type Health struct {
	Status string        `json:"status"`
	Routes []RouteHealth `json:"routes"`
	Herdr  []HerdrHealth `json:"herdr"`
}

// RouteHealth is one route's health.
type RouteHealth struct {
	Route  string   `json:"route"`
	Status string   `json:"status"`
	Flags  []string `json:"flags"`
	Reason string   `json:"reason,omitempty"`
}

// HerdrHealth is one Herdr link's health.
type HerdrHealth struct {
	Socket string `json:"socket"`
	Status string `json:"status"`
}

func (d *Daemon) healthBody(ctx context.Context) Health {
	h := Health{Status: "ok", Routes: []RouteHealth{}, Herdr: []HerdrHealth{}}
	d.mu.Lock()
	routes := make([]*Route, 0, len(d.routes))
	for _, r := range d.routes {
		routes = append(routes, r)
	}
	for n, why := range d.held {
		status := "unowned"
		if why == "adopting" {
			status = "adopting"
		}
		h.Routes = append(h.Routes, RouteHealth{Route: n, Status: status, Flags: []string{}, Reason: why})
	}
	d.mu.Unlock()
	for _, r := range routes {
		rh := RouteHealth{Route: r.name, Status: "busy", Flags: []string{}}
		_ = r.do(ctx, func() *APIError {
			rh.Status, rh.Flags = r.routeStatus(), append([]string{}, r.flags...)
			return nil
		})
		if r.degraded.Load() {
			rh.Status = "degraded"
		}
		h.Routes = append(h.Routes, rh)
	}
	sort.Slice(h.Routes, func(i, j int) bool { return h.Routes[i].Route < h.Routes[j].Route })
	sockets := map[string]bool{}
	if rf, err := ReadRoutes(d.cfg.PrivateHome); err == nil {
		for _, e := range rf.Routes {
			s := e.HerdrSocket
			if s == "" {
				s = d.cfg.HerdrSocket
			}
			if s != "" {
				sockets[s] = true
			}
		}
	}
	for s := range sockets {
		st := "ok"
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		if _, err := d.herdr(s).Check(cctx); err != nil {
			st = "unreachable"
		}
		cancel()
		h.Herdr = append(h.Herdr, HerdrHealth{Socket: s, Status: st})
	}
	sort.Slice(h.Herdr, func(i, j int) bool { return h.Herdr[i].Socket < h.Herdr[j].Socket })
	for _, r := range h.Routes {
		if r.Status != "ok" {
			h.Status = "degraded"
		}
	}
	return h
}

func (d *Daemon) health(w http.ResponseWriter, r *http.Request) *APIError {
	writeJSON(w, 200, d.healthBody(r.Context()))
	return nil
}

// ShutdownRequest is POST /v1/shutdown.
type ShutdownRequest struct {
	Reason string `json:"reason"`
}

func (d *Daemon) shutdown(w http.ResponseWriter, r *http.Request) *APIError {
	var q ShutdownRequest
	if e := decode(r, &q); e != nil {
		return e
	}
	if q.Reason != "stop" && q.Reason != "restart" {
		return Errf("bad_request", "reason is stop or restart")
	}
	writeJSON(w, 202, struct{}{})
	go d.Stop("stop")
	return nil
}

// RouteInfo is one route of GET /v1/routes.
type RouteInfo struct {
	Route   string `json:"route"`
	Inbox   string `json:"inbox"`
	Owner   string `json:"owner"`
	Epoch   string `json:"epoch"`
	LastSeq int64  `json:"last_seq"`
	Status  string `json:"status"`
}

func (d *Daemon) listRoutes(w http.ResponseWriter, r *http.Request) *APIError {
	rf, err := ReadRoutes(d.cfg.PrivateHome)
	if err != nil {
		return Errf("internal", "%v", err)
	}
	out := struct {
		Routes []RouteInfo `json:"routes"`
	}{Routes: []RouteInfo{}}
	names := make([]string, 0, len(rf.Routes))
	for n := range rf.Routes {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		info := RouteInfo{Route: n, Inbox: rf.Routes[n].Inbox, Owner: "daemon", Status: "unowned"}
		if rt, e := d.route(n); e == nil {
			_ = rt.do(r.Context(), func() *APIError {
				info.Epoch, info.LastSeq, info.Status = rt.epoch, rt.visible, rt.routeStatus()
				return nil
			})
		}
		out.Routes = append(out.Routes, info)
	}
	writeJSON(w, 200, out)
	return nil
}

// CreateRequest is POST /v1/routes.
type CreateRequest struct {
	Repo        string `json:"repo"`
	Route       string `json:"route"`
	HerdrSocket string `json:"herdr_socket,omitempty"`
}

// CreateResult is a route creation's answer.
type CreateResult struct {
	Route string `json:"route"`
	Inbox string `json:"inbox"`
	Epoch string `json:"epoch"`
}

// newOwner is a fresh owner record.
func (d *Daemon) newOwner(adopted bool) Owner {
	o := Owner{Owner: "daemon", Epoch: randomHex(16), Contract: contractDaemon, Protocol: Protocol, MinWriter: d.cfg.Version}
	if adopted {
		f := "file"
		o.AdoptedFrom = &f
	}
	return o
}

func (d *Daemon) createRoute(w http.ResponseWriter, r *http.Request) *APIError {
	var q CreateRequest
	if e := decode(r, &q); e != nil {
		return e
	}
	if !validName(q.Repo) || !validName(q.Route) {
		return Errf("bad_request", "repo and route must be names of letters, digits, '.', '_' or '-'")
	}
	if q.HerdrSocket != "" && !filepath.IsAbs(q.HerdrSocket) {
		return Errf("bad_request", "herdr_socket must be an absolute path")
	}
	d.regMu.Lock()
	defer d.regMu.Unlock()
	rf, err := ReadRoutes(d.cfg.PrivateHome)
	if err != nil {
		return Errf("internal", "%v", err)
	}
	if _, ok := rf.Routes[q.Route]; ok {
		return Errf("route_exists", "route %s is already registered", q.Route)
	}
	dir := filepath.Join(d.cfg.PrivateHome, q.Repo+"-"+q.Route)
	if err := mkdirDurable(dir); err != nil {
		if errors.Is(err, os.ErrExist) {
			return Errf("route_exists", "inbox %s exists; a new route never shares an inbox", dir)
		}
		return Errf("internal", "inbox: %v", err)
	}
	if err := mkdirs(dir, "state", "ids", "enrolled", "alerts"); err != nil {
		return Errf("internal", "inbox: %v", err)
	}
	o := d.newOwner(false)
	b, _ := encode(o)
	if err := writeDurable(filepath.Join(dir, "state", "owner.json"), append(b, '\n'), 0o600); err != nil {
		return Errf("internal", "owner: %v", err)
	}
	rf.Routes[q.Route] = RouteEntry{Inbox: dir, Repo: q.Repo, HerdrSocket: q.HerdrSocket, Created: d.now().Unix()}
	if err := d.writeRoutes(rf); err != nil {
		return Errf("internal", "routes.json: %v", err)
	}
	d.openRoute(q.Route, rf.Routes[q.Route])
	writeJSON(w, 201, CreateResult{Route: q.Route, Inbox: dir, Epoch: o.Epoch})
	return nil
}

func (d *Daemon) removeRoute(w http.ResponseWriter, r *http.Request) *APIError {
	name := r.PathValue("route")
	d.regMu.Lock()
	defer d.regMu.Unlock()
	rf, err := ReadRoutes(d.cfg.PrivateHome)
	if err != nil {
		return Errf("internal", "%v", err)
	}
	if _, ok := rf.Routes[name]; !ok {
		return Errf("unknown_route", "route %s is not registered", name)
	}
	delete(rf.Routes, name)
	if err := d.writeRoutes(rf); err != nil {
		return Errf("internal", "routes.json: %v", err)
	}
	d.mu.Lock()
	rt := d.routes[name]
	delete(d.routes, name)
	delete(d.held, name)
	d.mu.Unlock()
	if rt != nil {
		_ = rt.do(r.Context(), func() *APIError {
			for s := range rt.subs {
				s.push(frame{ev: &Event{Type: "route_removed", Route: name}, last: s.party != ""})
				if s.party != "" {
					s.close()
				}
			}
			rt.subs = map[*subscriber]struct{}{}
			rt.removed = true
			return nil
		})
		rt.stop()
		for _, l := range rt.retained {
			releaseLock(l)
		}
	}
	d.indexCredentials(name, nil)
	writeJSON(w, 200, map[string]bool{"removed": true, "inbox_kept": true})
	return nil
}

// ResetRequest is POST /v1/routes/{route}/reset-events.
type ResetRequest struct {
	ConfirmRoute string `json:"confirm_route"`
}

func (d *Daemon) resetEvents(w http.ResponseWriter, r *http.Request) *APIError {
	name := r.PathValue("route")
	var q ResetRequest
	if e := decode(r, &q); e != nil {
		return e
	}
	if q.ConfirmRoute != name {
		return Errf("bad_request", "confirm_route must name the route")
	}
	rt, e := d.route(name)
	if e != nil {
		return e
	}
	var res struct {
		Epoch         string `json:"epoch"`
		CheckpointSeq int64  `json:"checkpoint_seq"`
	}
	if e := rt.do(r.Context(), func() *APIError {
		if err := rt.resetEpoch(d.newOwner(false).Epoch); err != nil {
			return Errf("internal", "reset: %v", err)
		}
		res.Epoch, res.CheckpointSeq = rt.epoch, rt.visible
		return nil
	}); e != nil {
		return e
	}
	writeJSON(w, 200, res)
	return nil
}

// resetEpoch starts a new event epoch (4.3): the old journal goes, a new
// one starts with a checkpoint rebuilt from the files, and every attached
// stream gets reset then a snapshot of the new epoch (5.4).
func (r *Route) resetEpoch(epoch string) error {
	// The old journal goes first: a crash before the new epoch is written
	// leaves a route whose journal is lost, which open rebuilds under a
	// new epoch of its own.
	r.j.close()
	r.j = nil
	for _, f := range []string{journalPrev, journalCurrent} {
		if err := os.Remove(filepath.Join(r.path("state", "events"), f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := syncDir(r.path("state", "events")); err != nil {
		return err
	}
	if err := r.setEpoch(epoch); err != nil {
		return err
	}
	j, _, err := openJournal(r.path("state", "events"), r.epoch)
	if err != nil {
		return err
	}
	r.j, r.ring, r.oldest = j, nil, 0
	if err := r.writeCheckpoint(false); err != nil {
		return err
	}
	for s := range r.subs {
		if !s.push(frame{ev: r.resetEvent("epoch_changed"), route: r.name}) || !s.push(frame{ev: r.snapshot(s.party), route: r.name}) {
			delete(r.subs, s)
		}
	}
	return nil
}

func (d *Daemon) routeStatus(w http.ResponseWriter, r *http.Request) *APIError {
	rt, e := d.route(r.PathValue("route"))
	if e != nil {
		return e
	}
	if r.Header.Get(HeaderPane) != "" {
		if _, e := d.authParty(r, rt); e != nil {
			return e
		}
	}
	var st StatusReport
	if e := rt.do(r.Context(), func() *APIError { st = rt.status(); return nil }); e != nil {
		return e
	}
	writeJSON(w, 200, st)
	return nil
}

// SetupInventory is GET /v1/routes/{route}/setup: the live inventory of
// the route's Herdr server and the current proof, for the setup form.
type SetupInventory struct {
	Inventory []InventoryAgent `json:"inventory"`
	Proof     *SetupV2         `json:"proof,omitempty"`
}

// InventoryAgent is one live agent of the inventory.
type InventoryAgent struct {
	Pane    string          `json:"pane"`
	Session json.RawMessage `json:"session"`
	Agent   string          `json:"agent"`
}

func (d *Daemon) getSetup(w http.ResponseWriter, r *http.Request) *APIError {
	name := r.PathValue("route")
	rt, e := d.route(name)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	agents, err := d.herdrFor(name).AgentList(ctx)
	if err != nil {
		return Errf("herdr_unreachable", "herdr: %v", err)
	}
	out := SetupInventory{Inventory: []InventoryAgent{}}
	for _, a := range agents {
		ia := InventoryAgent{Pane: a.PaneID, Session: a.AgentSession}
		if a.Agent.Agent != nil {
			ia.Agent = *a.Agent.Agent
		}
		if len(ia.Session) == 0 {
			ia.Session = json.RawMessage("null")
		}
		out.Inventory = append(out.Inventory, ia)
	}
	_ = rt.do(r.Context(), func() *APIError {
		if doc, ok := rt.readSetup(); ok {
			out.Proof = doc
		}
		return nil
	})
	writeJSON(w, 200, out)
	return nil
}

func (d *Daemon) putSetup(w http.ResponseWriter, r *http.Request) *APIError {
	rt, e := d.route(r.PathValue("route"))
	if e != nil {
		return e
	}
	var q SetupRequest
	if e := decode(r, &q); e != nil {
		return e
	}
	unlock, e := rt.inputLock()
	if e != nil {
		return e
	}
	defer unlock()
	var res SetupResult
	if e := rt.do(r.Context(), func() *APIError {
		var e *APIError
		res, e = rt.setup(q)
		return e
	}); e != nil {
		return e
	}
	writeJSON(w, 200, res)
	return nil
}

// EnrollRequest is POST /v1/routes/{route}/enroll.
type EnrollRequest struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// liveIdentity reads a pane's identity from Herdr (10 s deadline).
func (d *Daemon) liveIdentity(ctx context.Context, route, pane string) (inbox.Identity, *APIError) {
	if pane == "" {
		return inbox.Identity{}, Errf("unauthorized", "the %s header (HERDR_PANE_ID) is required", HeaderPane)
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	id, code, note := inbox.LiveIdentity(cctx, d.herdrFor(route), pane)
	switch code {
	case inbox.LiveOK:
		return id, nil
	case inbox.LiveGone:
		return inbox.Identity{}, Errf("identity_unavailable", "herdr reports no agent in pane %s", pane)
	}
	if strings.HasPrefix(note, "agent get unavailable") {
		return inbox.Identity{}, Errf("herdr_unreachable", "%s", note)
	}
	return inbox.Identity{}, Errf("identity_unavailable", "herdr reports no complete, ready identity for pane %s (start the coordinator with herdr agent start)", pane)
}

func (d *Daemon) enroll(w http.ResponseWriter, r *http.Request) *APIError {
	name := r.PathValue("route")
	rt, e := d.route(name)
	if e != nil {
		return e
	}
	var q EnrollRequest
	if e := decode(r, &q); e != nil {
		return e
	}
	id, e := d.liveIdentity(r.Context(), name, r.Header.Get(HeaderPane))
	if e != nil {
		return e
	}
	var sum string
	if e := rt.do(r.Context(), func() *APIError {
		var e *APIError
		sum, e = rt.enroll(q.Name, q.Role, id)
		return e
	}); e != nil {
		return e
	}
	writeJSON(w, 202, map[string]string{"pending_sha256": sum})
	return nil
}

func (d *Daemon) approve(w http.ResponseWriter, r *http.Request) *APIError {
	rt, e := d.route(r.PathValue("route"))
	if e != nil {
		return e
	}
	var q ApproveRequest
	if e := decode(r, &q); e != nil {
		return e
	}
	var res ApproveResult
	if e := rt.do(r.Context(), func() *APIError {
		var e *APIError
		res, e = rt.approve(q)
		return e
	}); e != nil {
		return e
	}
	d.logf("route %s: pair approved (%s)", rt.name, approvedBy(q.Interaction))
	writeJSON(w, 200, res)
	return nil
}

func approvedBy(interaction string) string {
	if interaction == "" {
		return "terminal"
	}
	return "interaction " + interaction
}

// RetireRequest is POST /v1/routes/{route}/pair/retire.
type RetireRequest struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

func (d *Daemon) retire(w http.ResponseWriter, r *http.Request) *APIError {
	rt, e := d.route(r.PathValue("route"))
	if e != nil {
		return e
	}
	var q RetireRequest
	if e := decode(r, &q); e != nil {
		return e
	}
	if e := rt.do(r.Context(), func() *APIError {
		_, e := rt.retire(q.Name, q.SHA256)
		return e
	}); e != nil {
		return e
	}
	writeJSON(w, 200, map[string]any{"retired": q.Name, "revoked": true})
	return nil
}

func (d *Daemon) revokeCredential(w http.ResponseWriter, r *http.Request) *APIError {
	rt, e := d.route(r.PathValue("route"))
	if e != nil {
		return e
	}
	name := r.PathValue("name")
	if e := rt.do(r.Context(), func() *APIError { return rt.revoke(name) }); e != nil {
		return e
	}
	writeJSON(w, 200, map[string]bool{"revoked": true})
	return nil
}

// authParty proves a party request (5.1, 8): the credential (or, for the
// identity option, the name header) names one approved party of this
// route, and Herdr's live identity of the caller's pane is that party's
// enrolled identity.
func (d *Daemon) authParty(r *http.Request, rt *Route) (string, *APIError) {
	pane := r.Header.Get(HeaderPane)
	if pane == "" {
		return "", Errf("unauthorized", "the %s header (HERDR_PANE_ID) is required", HeaderPane)
	}
	name := r.Header.Get(HeaderName)
	auth := r.Header.Get("Authorization")
	party := ""
	if tok, ok := strings.CutPrefix(auth, "Bearer "); ok && tok != "" {
		d.mu.Lock()
		ref, found := d.credIndex[sha256Hex([]byte(tok))]
		d.mu.Unlock()
		if !found {
			return "", Errf("unauthorized", "unknown or revoked credential")
		}
		if ref.route != rt.name {
			return "", Errf("wrong_route", "this credential belongs to another route")
		}
		if name != "" && name != ref.name {
			return "", Errf("unauthorized", "the credential is %s's, not %s's", ref.name, name)
		}
		party = ref.name
	} else if auth != "" {
		return "", Errf("unauthorized", "malformed Authorization header")
	} else {
		var opt credential
		var known bool
		_ = rt.do(r.Context(), func() *APIError {
			opt, known = rt.creds[name]
			return nil
		})
		if !known || opt.Option != "identity" {
			return "", Errf("unauthorized", "a credential is required for %q (HERDSMAN_NAME and HERDSMAN_ROUTE locate it)", name)
		}
		party = name
	}
	peer, ok := rt.root().Peer(party)
	if !ok {
		return "", Errf("unauthorized", "%s is not an approved party of route %s", party, rt.name)
	}
	id, e := d.liveIdentity(r.Context(), rt.name, pane)
	if e != nil {
		return "", e
	}
	if !id.Same(peer.Identity) || pane != peer.Pane {
		return "", Errf("identity_mismatch", "pane %s does not host %s's enrolled session; enroll a fresh name", pane, party)
	}
	return party, nil
}

func (d *Daemon) send(w http.ResponseWriter, r *http.Request) *APIError {
	rt, e := d.route(r.PathValue("route"))
	if e != nil {
		return e
	}
	var q SendRequest
	if e := decode(r, &q); e != nil {
		return e
	}
	me, e := d.authParty(r, rt)
	if e != nil {
		return e
	}
	if q.Kind == "side_agent_data" {
		// The input lock from the boundary check through the enqueue
		// (3.7); lock order: input lock, then the route actor.
		unlock, e := rt.inputLock()
		if e != nil {
			return e
		}
		defer unlock()
		var auth string
		if e := rt.do(r.Context(), func() *APIError {
			auth, _ = rt.authority(r.Context(), d.herdrFor(rt.name))
			return nil
		}); e != nil {
			return e
		}
		if auth != "RELAY" {
			return Errf("human_boundary", "HUMAN boundary; side-agent data not queued")
		}
	}
	var res SendResult
	if e := rt.do(r.Context(), func() *APIError {
		var e *APIError
		res, e = rt.send(me, q)
		return e
	}); e != nil {
		return e
	}
	status := 201
	if res.Duplicate {
		status = 200
	}
	writeJSON(w, status, res)
	return nil
}

func (d *Daemon) claim(w http.ResponseWriter, r *http.Request) *APIError {
	rt, e := d.route(r.PathValue("route"))
	if e != nil {
		return e
	}
	var q ClaimRequest
	if e := decode(r, &q); e != nil {
		return e
	}
	me, e := d.authParty(r, rt)
	if e != nil {
		return e
	}
	var res ClaimResult
	if e := rt.do(r.Context(), func() *APIError {
		var e *APIError
		res, e = rt.claim(me, q)
		return e
	}); e != nil {
		return e
	}
	writeJSON(w, 200, res)
	return nil
}

func (d *Daemon) ack(w http.ResponseWriter, r *http.Request) *APIError {
	rt, e := d.route(r.PathValue("route"))
	if e != nil {
		return e
	}
	var q AckRequest
	if e := decode(r, &q); e != nil {
		return e
	}
	me, e := d.authParty(r, rt)
	if e != nil {
		return e
	}
	var res AckResult
	if e := rt.do(r.Context(), func() *APIError {
		var e *APIError
		res, e = rt.ack(me, r.PathValue("token"), q)
		return e
	}); e != nil {
		return e
	}
	status := 201
	if res.Already {
		status = 200
	}
	writeJSON(w, status, res)
	return nil
}

func (d *Daemon) release(w http.ResponseWriter, r *http.Request) *APIError {
	rt, e := d.route(r.PathValue("route"))
	if e != nil {
		return e
	}
	me, e := d.authParty(r, rt)
	if e != nil {
		return e
	}
	if e := rt.do(r.Context(), func() *APIError { return rt.release(me, r.PathValue("token")) }); e != nil {
		return e
	}
	writeJSON(w, 200, map[string]bool{"released": true})
	return nil
}

func (d *Daemon) holdOp(w http.ResponseWriter, r *http.Request, set bool) *APIError {
	rt, e := d.route(r.PathValue("route"))
	if e != nil {
		return e
	}
	var reason *string
	if set {
		var q HoldRequest
		if e := decode(r, &q); e != nil {
			return e
		}
		reason = &q.Reason
	}
	me, e := d.authParty(r, rt)
	if e != nil {
		return e
	}
	unlock, e := rt.inputLock()
	if e != nil {
		return e
	}
	defer unlock()
	var res HoldResult
	if e := rt.do(r.Context(), func() *APIError {
		var e *APIError
		res, e = rt.hold(me, r.PathValue("name"), reason)
		return e
	}); e != nil {
		return e
	}
	writeJSON(w, 200, res)
	return nil
}

func (d *Daemon) hold(w http.ResponseWriter, r *http.Request) *APIError { return d.holdOp(w, r, true) }
func (d *Daemon) unhold(w http.ResponseWriter, r *http.Request) *APIError {
	return d.holdOp(w, r, false)
}

// AuthorityResult is GET /v1/routes/{route}/authority.
type AuthorityResult struct {
	Authority string   `json:"authority"`
	Reasons   []string `json:"reasons"`
}

func (d *Daemon) authority(w http.ResponseWriter, r *http.Request) *APIError {
	rt, e := d.route(r.PathValue("route"))
	if e != nil {
		return e
	}
	if r.Header.Get(HeaderPane) != "" {
		if _, e := d.authParty(r, rt); e != nil {
			return e
		}
	}
	// The check records what it observes under the input lock (8).
	unlock, e := rt.inputLock()
	if e != nil {
		return e
	}
	defer unlock()
	var res AuthorityResult
	if e := rt.do(r.Context(), func() *APIError {
		res.Authority, res.Reasons = rt.authority(r.Context(), d.herdrFor(rt.name))
		return nil
	}); e != nil {
		return e
	}
	writeJSON(w, 200, res)
	return nil
}

// streamSlot admits a new stream under the caps of 9.3.
func (d *Daemon) streamSlot(key string) (func(), *APIError) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.upgrading {
		return nil, Errf("busy", "the daemon is stopping; reconnect")
	}
	limit := maxStreamsParty
	if key == "view" {
		limit = maxStreamsView
	}
	if d.streams[key] >= limit || d.streams["all"] >= maxStreamsAll {
		return nil, Errf("busy", "too many open streams")
	}
	d.streams[key]++
	d.streams["all"]++
	return func() {
		d.mu.Lock()
		d.streams[key]--
		d.streams["all"]--
		d.mu.Unlock()
	}, nil
}

// runStream writes frames to an SSE response until the queue closes, the
// client goes or a write misses its deadline.
func runStream(w http.ResponseWriter, r *http.Request, s *subscriber, first []frame, routeOf func(frame) string) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	write := func(f frame) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(writeDeadline))
		if writeFrame(w, routeOf(f), f) != nil || rc.Flush() != nil {
			return false
		}
		return !f.last
	}
	if rc.Flush() != nil {
		return
	}
	for _, f := range first {
		if !write(f) {
			return
		}
	}
	var ping <-chan time.Time
	if s.party == "" {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		ping = t.C
	}
	for {
		select {
		case f, ok := <-s.q:
			if !ok || !write(f) {
				return
			}
		case <-ping:
			_ = rc.SetWriteDeadline(time.Now().Add(writeDeadline))
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (d *Daemon) partyEvents(w http.ResponseWriter, r *http.Request) *APIError {
	rt, e := d.route(r.PathValue("route"))
	if e != nil {
		return e
	}
	me, e := d.authParty(r, rt)
	if e != nil {
		return e
	}
	done, e := d.streamSlot(rt.name + "/" + me)
	if e != nil {
		return e
	}
	defer done()
	cursor := r.Header.Get("Last-Event-ID")
	if c := r.URL.Query().Get("after"); c != "" {
		cursor = c
	}
	s := newSubscriber(rt.name, me)
	var first []frame
	if e := rt.do(r.Context(), func() *APIError {
		var e *APIError
		first, e = rt.subscribe(s, cursor)
		return e
	}); e != nil {
		return e
	}
	defer func() {
		s.close()
		_ = rt.do(context.Background(), func() *APIError { rt.unsubscribe(s); return nil })
	}()
	releaseSlot(r)
	runStream(w, r, s, first, func(frame) string { return rt.name })
	return nil
}

// attachView subscribes the view stream to one route: its snapshot first,
// then its live events (5.4 view stream).
func (d *Daemon) attachView(rt *Route, s *subscriber) {
	_ = rt.do(context.Background(), func() *APIError {
		first, _ := rt.subscribe(s, "")
		for _, f := range first {
			if !s.push(f) {
				delete(rt.subs, s)
			}
		}
		return nil
	})
}

func (d *Daemon) viewEvents(w http.ResponseWriter, r *http.Request) *APIError {
	done, e := d.streamSlot("view")
	if e != nil {
		return e
	}
	defer done()
	s := newSubscriber("", "")
	// Subscribing to the registry and listing it is one step: a route
	// created later attaches itself through d.views (openRoute).
	d.mu.Lock()
	d.views[s] = struct{}{}
	routes := make([]*Route, 0, len(d.routes))
	for _, rt := range d.routes {
		routes = append(routes, rt)
	}
	d.mu.Unlock()
	sort.Slice(routes, func(i, j int) bool { return routes[i].name < routes[j].name })
	defer func() {
		d.mu.Lock()
		delete(d.views, s)
		attached := make([]*Route, 0, len(d.routes))
		for _, rt := range d.routes {
			attached = append(attached, rt)
		}
		d.mu.Unlock()
		s.close()
		// Every route the stream was attached to, at its start or later,
		// forgets it.
		for _, rt := range attached {
			_ = rt.do(context.Background(), func() *APIError { rt.unsubscribe(s); return nil })
		}
	}()
	reg := &Event{Type: "registry", Routes: []RouteRef{}}
	for _, rt := range routes {
		var ep string
		_ = rt.do(r.Context(), func() *APIError { ep = rt.epoch; return nil })
		reg.Routes = append(reg.Routes, RouteRef{Route: rt.name, Epoch: ep})
	}
	first := []frame{{ev: reg}}
	for _, rt := range routes {
		d.attachView(rt, s)
	}
	releaseSlot(r)
	runStream(w, r, s, first, func(f frame) string {
		if f.route != "" {
			return f.route
		}
		if f.ev != nil {
			return f.ev.Route
		}
		return ""
	})
	return nil
}
