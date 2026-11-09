package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// Request bounds (9.1, 9.3).
const (
	maxBody         = 160 << 10
	maxStreamsParty = 4
	// Event streams and their long polls share one key, so they get their own
	// limit: half of all streams, which leaves the rest to the parties.
	maxStreamsEvents = 128
	maxStreamsAll    = 256
)

// handler is the daemon's HTTP API: the RPC path and nothing else.
func (d *Daemon) handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle(rpc.BasePath, d.rpcHandler())
	return d.limit(mux)
}

// limit is the request semaphore (9.1): at most 32 requests in flight
// before a body is read. Streams give their slot back once attached.
func (d *Daemon) limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case d.sem <- struct{}{}:
		case <-time.After(2 * time.Second):
			rpc.WriteError(w, rpcFailure(r.Context(), Errf("busy", "too many requests in flight; retry")))
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
func releaseSlot(r *http.Request) { releaseContext(r.Context()) }

// releaseContext gives back the semaphore slot of the request behind ctx.
func releaseContext(ctx context.Context) {
	if f, ok := ctx.Value(releaseKey{}).(func()); ok {
		f()
	}
}

func (d *Daemon) helloBody() Hello {
	features := []string{}
	opts := credentialOptions()
	if len(opts) == 3 {
		features = append(features, "keychain")
	}
	return Hello{DaemonVersion: d.cfg.Version, Features: features, CredentialOptions: opts, DefaultCredential: "file"}
}

// Health is every route's and Herdr link's health.
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
		switch why {
		case "adopting":
			status = "adopting"
		case heldInspect:
			status = heldInspect
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
	routeSocket := map[string]string{}
	if rf, err := ReadRoutes(d.cfg.PrivateHome); err == nil {
		for name, e := range rf.Routes {
			s := e.HerdrSocket
			if s == "" {
				s = d.cfg.HerdrSocket
			}
			if s != "" {
				sockets[s] = true
				routeSocket[name] = s
			}
		}
	}
	down := map[string]bool{}
	if d.cfg.Inspect {
		// A health probe would reach another process.
		sockets = nil
	}
	for s := range sockets {
		st := "ok"
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		if _, err := d.herdr(s).Check(cctx); err != nil {
			st = "unreachable"
			down[s] = true
		}
		cancel()
		h.Herdr = append(h.Herdr, HerdrHealth{Socket: s, Status: st})
	}
	// A route whose Herdr does not answer cannot verify identities or
	// relay: it is degraded, and so is the daemon.
	for i := range h.Routes {
		if down[routeSocket[h.Routes[i].Route]] {
			if !slices.Contains(h.Routes[i].Flags, "herdr-unreachable") {
				h.Routes[i].Flags = append(h.Routes[i].Flags, "herdr-unreachable")
			}
			if h.Routes[i].Status == "ok" {
				h.Routes[i].Status = "degraded"
			}
		}
	}
	sort.Slice(h.Herdr, func(i, j int) bool { return h.Herdr[i].Socket < h.Herdr[j].Socket })
	for _, r := range h.Routes {
		if r.Status != "ok" && r.Status != heldInspect {
			h.Status = "degraded"
		}
	}
	return h
}

func (d *Daemon) shutdown(reason string) *APIError {
	if reason != "stop" && reason != "restart" {
		return Errf("bad_request", "reason is stop or restart")
	}
	go d.Stop("stop")
	return nil
}

// RouteInfo is one registered route.
type RouteInfo struct {
	Route   string `json:"route"`
	Inbox   string `json:"inbox"`
	Owner   string `json:"owner"`
	Epoch   string `json:"epoch"`
	LastSeq int64  `json:"last_seq"`
	Status  string `json:"status"`
}

func (d *Daemon) listRoutes(ctx context.Context) ([]RouteInfo, *APIError) {
	rf, err := ReadRoutes(d.cfg.PrivateHome)
	if err != nil {
		return nil, Errf("internal", "%v", err)
	}
	out := []RouteInfo{}
	names := make([]string, 0, len(rf.Routes))
	for n := range rf.Routes {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		info := RouteInfo{Route: n, Inbox: rf.Routes[n].Inbox, Owner: "daemon", Status: "unowned"}
		if rt, e := d.route(n); e == nil {
			_ = rt.do(ctx, func() *APIError {
				info.Epoch, info.LastSeq, info.Status = rt.epoch, rt.visible, rt.routeStatus()
				return nil
			})
		}
		out = append(out, info)
	}
	return out, nil
}

// CreateRequest registers a new route.
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

// admitCreate runs every check of a create that changes nothing. The caller
// holds regMu.
func (d *Daemon) admitCreate(q CreateRequest) (RoutesFile, *APIError) {
	if !validName(q.Repo) || !validName(q.Route) {
		return RoutesFile{}, Errf("bad_request", "repo and route must be names of letters, digits, '.', '_' or '-'")
	}
	if q.HerdrSocket != "" && !filepath.IsAbs(q.HerdrSocket) {
		return RoutesFile{}, Errf("bad_request", "herdr_socket must be an absolute path")
	}
	rf, err := ReadRoutes(d.cfg.PrivateHome)
	if err != nil {
		return RoutesFile{}, Errf("internal", "%v", err)
	}
	if _, ok := rf.Routes[q.Route]; ok {
		return RoutesFile{}, Errf("route_exists", "route %s is already registered", q.Route)
	}
	if e := d.nameTaken(rf, q.Route, ""); e != nil {
		return RoutesFile{}, e
	}
	return rf, nil
}

func (d *Daemon) createRoute(q CreateRequest, res *CreateResult) *APIError {
	d.regMu.Lock()
	defer d.regMu.Unlock()
	rf, e := d.admitCreate(q)
	if e != nil {
		return e
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
	*res = CreateResult{Route: q.Route, Inbox: dir, Epoch: o.Epoch}
	return nil
}

func (d *Daemon) removeRoute(name string) *APIError {
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
	d.view.remove(name)
	removed := func() *Event { return &Event{Type: "route_removed", Time: d.now().Unix(), Route: name} }
	if rt != nil {
		// After this turn the route sends nothing more and refuses every
		// attachment; it runs even on a degraded route.
		rt.always(func() {
			for s := range rt.subs {
				if s.party != "" {
					s.push(frame{ev: removed(), last: true})
					s.close()
				}
			}
			rt.subs = map[*subscriber]struct{}{}
			rt.removed = true
		})
	}
	if rt != nil {
		rt.stop()
		for _, l := range rt.retained {
			releaseLock(l)
		}
	}
	d.indexCredentials(name, nil)
	return nil
}

// ResetRequest asks for a new event epoch.
type ResetRequest struct {
	ConfirmRoute string `json:"confirm_route"`
}

// ResetResult is the new epoch and its checkpoint.
type ResetResult struct {
	Epoch         string `json:"epoch"`
	CheckpointSeq int64  `json:"checkpoint_seq"`
}

func (d *Daemon) resetEvents(ctx context.Context, name string, q ResetRequest, res *ResetResult) *APIError {
	if q.ConfirmRoute != name {
		return Errf("bad_request", "confirm_route must name the route")
	}
	rt, e := d.route(name)
	if e != nil {
		return e
	}
	return rt.do(ctx, func() *APIError {
		if err := rt.resetEpoch(d.newOwner(false).Epoch); err != nil {
			return Errf("internal", "reset: %v", err)
		}
		res.Epoch, res.CheckpointSeq = rt.epoch, rt.visible
		return nil
	})
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

// SetupInventory is the live inventory of
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

func (d *Daemon) getSetup(parent context.Context, name string, out *SetupInventory) *APIError {
	rt, e := d.route(name)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	agents, err := d.herdrFor(name).AgentList(ctx)
	if err != nil {
		return Errf("herdr_unreachable", "herdr: %v", err)
	}
	out.Inventory = []InventoryAgent{}
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
	_ = rt.do(parent, func() *APIError {
		if doc, ok := rt.readSetup(); ok {
			out.Proof = doc
		}
		return nil
	})
	return nil
}

func (d *Daemon) putSetup(ctx context.Context, name string, q SetupRequest, res *SetupResult) *APIError {
	rt, e := d.route(name)
	if e != nil {
		return e
	}
	unlock, e := rt.inputLock()
	if e != nil {
		return e
	}
	defer unlock()
	return rt.do(ctx, func() *APIError {
		var e *APIError
		*res, e = rt.setup(q)
		return e
	})
}

// EnrollRequest proposes the calling pane as a party.
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

func (d *Daemon) enroll(ctx context.Context, name string, q EnrollRequest, sum *string) *APIError {
	rt, e := d.route(name)
	if e != nil {
		return e
	}
	id, e := d.liveIdentity(ctx, name, callHeader(ctx).Get(HeaderPane))
	if e != nil {
		return e
	}
	return rt.do(ctx, func() *APIError {
		var e *APIError
		*sum, e = rt.enroll(q.Name, q.Role, id)
		return e
	})
}

func (d *Daemon) approve(ctx context.Context, name string, q ApproveRequest, res *ApproveResult) *APIError {
	rt, e := d.route(name)
	if e != nil {
		return e
	}
	if e := rt.do(ctx, func() *APIError {
		var e *APIError
		*res, e = rt.approveAsked(q, true)
		return e
	}); e != nil {
		return e
	}
	d.logf("route %s: pair approved (%s)", rt.name, approvedBy(q.Interaction, q.AskedBy))
	return nil
}

func approvedBy(interaction, askedBy string) string {
	if interaction == "" {
		return "terminal"
	}
	if askedBy != "" {
		return "interaction " + interaction + " asked by " + askedBy
	}
	return "interaction " + interaction
}

// RetireRequest retires a party by name and registry checksum.
type RetireRequest struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

func (d *Daemon) retire(ctx context.Context, name string, q RetireRequest) *APIError {
	rt, e := d.route(name)
	if e != nil {
		return e
	}
	return rt.do(ctx, func() *APIError {
		_, e := rt.retire(q.Name, q.SHA256)
		return e
	})
}

func (d *Daemon) revokeCredential(ctx context.Context, route, name string) *APIError {
	rt, e := d.route(route)
	if e != nil {
		return e
	}
	return rt.do(ctx, func() *APIError { return rt.revoke(name) })
}

// authParty proves a party request (5.1, 8): the credential (or, for the
// identity option, the name header) names one approved party of this
// route, and Herdr's live identity of the caller's pane is that party's
// enrolled identity.
func (d *Daemon) authParty(ctx context.Context, rt *Route) (string, *APIError) {
	header := callHeader(ctx)
	pane := header.Get(HeaderPane)
	if pane == "" {
		return "", Errf("unauthorized", "the %s header (HERDR_PANE_ID) is required", HeaderPane)
	}
	name := header.Get(HeaderName)
	auth := header.Get("Authorization")
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
		_ = rt.do(ctx, func() *APIError {
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
	id, e := d.liveIdentity(ctx, rt.name, pane)
	if e != nil {
		return "", e
	}
	if !id.Same(peer.Identity) || pane != peer.Pane {
		return "", Errf("identity_mismatch", "pane %s does not host %s's enrolled session; enroll a fresh name", pane, party)
	}
	return party, nil
}

func (d *Daemon) send(ctx context.Context, route string, q SendRequest, res *SendResult) *APIError {
	rt, e := d.route(route)
	if e != nil {
		return e
	}
	me, e := d.authParty(ctx, rt)
	if e != nil {
		return e
	}
	// A retry of a committed send is answered from its key before any
	// admission check that applies only to a new send (5.4); send itself
	// still binds the key to its sender and content.
	known := false
	if e := rt.do(ctx, func() *APIError {
		if _, known = rt.sendKeys[q.Key]; !known {
			return nil
		}
		// Answered in the same actor turn as the lookup, so the key
		// cannot leave the window between them.
		var e *APIError
		*res, e = rt.send(me, q)
		return e
	}); e != nil {
		return e
	}
	if !known {
		if q.Kind == "side_agent_data" {
			// The input lock from the boundary check through the enqueue
			// (3.7); lock order: input lock, then the route actor.
			unlock, e := rt.inputLock()
			if e != nil {
				return e
			}
			defer unlock()
			var auth string
			if e := rt.do(ctx, func() *APIError {
				auth, _ = rt.authority(ctx, d.herdrFor(rt.name))
				return nil
			}); e != nil {
				return e
			}
			if auth != "RELAY" {
				return Errf("human_boundary", "HUMAN boundary; side-agent data not queued")
			}
		}
		if e := rt.do(ctx, func() *APIError {
			var e *APIError
			*res, e = rt.send(me, q)
			return e
		}); e != nil {
			return e
		}
	}
	return nil
}

func (d *Daemon) claim(ctx context.Context, route string, q ClaimRequest, res *ClaimResult) *APIError {
	rt, e := d.route(route)
	if e != nil {
		return e
	}
	me, e := d.authParty(ctx, rt)
	if e != nil {
		return e
	}
	return rt.do(ctx, func() *APIError {
		var e *APIError
		*res, e = rt.claim(me, q)
		return e
	})
}

func (d *Daemon) ack(ctx context.Context, route, token string, q AckRequest, res *AckResult) *APIError {
	rt, e := d.route(route)
	if e != nil {
		return e
	}
	me, e := d.authParty(ctx, rt)
	if e != nil {
		return e
	}
	return rt.do(ctx, func() *APIError {
		var e *APIError
		*res, e = rt.ack(me, token, q)
		return e
	})
}

func (d *Daemon) release(ctx context.Context, route, token string) *APIError {
	rt, e := d.route(route)
	if e != nil {
		return e
	}
	me, e := d.authParty(ctx, rt)
	if e != nil {
		return e
	}
	return rt.do(ctx, func() *APIError { return rt.release(me, token) })
}

// holdOp places a hold (reason set) or removes the caller's hold (reason nil).
func (d *Daemon) holdOp(ctx context.Context, route, target string, reason *string, res *HoldResult) *APIError {
	rt, e := d.route(route)
	if e != nil {
		return e
	}
	me, e := d.authParty(ctx, rt)
	if e != nil {
		return e
	}
	unlock, e := rt.inputLock()
	if e != nil {
		return e
	}
	defer unlock()
	return rt.do(ctx, func() *APIError {
		var e *APIError
		*res, e = rt.hold(me, target, reason)
		return e
	})
}

// AuthorityResult says whether relayed messages carry authority.
type AuthorityResult struct {
	Authority string   `json:"authority"`
	Reasons   []string `json:"reasons"`
}

func (d *Daemon) authority(ctx context.Context, route string, res *AuthorityResult) *APIError {
	rt, e := d.route(route)
	if e != nil {
		return e
	}
	if callHeader(ctx).Get(HeaderPane) != "" {
		if _, e := d.authParty(ctx, rt); e != nil {
			return e
		}
	}
	// The check records what it observes under the input lock (8).
	unlock, e := rt.inputLock()
	if e != nil {
		return e
	}
	defer unlock()
	return rt.do(ctx, func() *APIError {
		res.Authority, res.Reasons = rt.authority(ctx, d.herdrFor(rt.name))
		return nil
	})
}

// streamSlot admits a waiting call under the caps of 9.3.
func (d *Daemon) streamSlot(key string) (func(), *APIError) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.upgrading {
		return nil, Errf("busy", "the daemon is stopping; reconnect")
	}
	limit := maxStreamsParty
	if key == eventSlotKey {
		limit = maxStreamsEvents
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
