package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/sys"
)

// A route on herdr-core carries its pair through the daemon's channel: the
// route is registered as a daemon-owned inbox, so the pair's messages flow
// through the approved-pair channel and the durable outbox as they do on the
// TUI's daemon path. What changes is who a caller is: a party is the
// execution its process belongs to (spec section 6), and its registry
// identity is that execution's. A replaced execution is another identity, so
// its stale binding fails; a change of view moves nothing in it.

// channelOnCore reports whether a route's channel serves herdr-core
// executions: its recorded executions are on core.
func (d *Daemon) channelOnCore(name string) bool {
	e, ok := d.routeEntry(name)
	return ok && d.routeOnCore(e.Repo, name)
}

// coreCaller is the registry identity of the caller of a core route's channel:
// the execution of this route its process belongs to, walked by process
// evidence. Anything the request carries is a hint for messages only.
func (d *Daemon) coreCaller(ctx context.Context, rt *Route) (inbox.Identity, *APIError) {
	c := d.coreRT()
	if c == nil {
		return inbox.Identity{}, Errf("identity_unavailable", "this daemon runs no herdr-core backend")
	}
	conn, ok := ctx.Value(connKey{}).(syscall.Conn)
	if !ok {
		return inbox.Identity{}, Errf("unauthorized", "the request's connection has no peer to identify")
	}
	peer, err := sys.PeerCredentials(conn)
	if err != nil {
		return inbox.Identity{}, Errf("unauthorized", "read the caller's credentials: %v", err)
	}
	return d.coreCallerOf(ctx, rt.name, peer.PID)
}

// coreCallerOf is coreCaller for a known peer process.
func (d *Daemon) coreCallerOf(ctx context.Context, name string, pid int) (inbox.Identity, *APIError) {
	c := d.coreRT()
	if c == nil {
		return inbox.Identity{}, Errf("identity_unavailable", "this daemon runs no herdr-core backend")
	}
	entry, _ := d.routeEntry(name)
	e, ok := c.callerExecution(ctx, pid)
	if !ok {
		return inbox.Identity{}, Errf("unauthorized", "the caller's process belongs to no registered execution; a shell at an agent's address is not the agent")
	}
	if e.Repo != entry.Repo || e.Route != name {
		return inbox.Identity{}, Errf("wrong_route", "the caller is an execution of route %s, not of %s", e.Route, name)
	}
	return inbox.ExecutionIdentity(e.HarnessKind, e.ID), nil
}

// executionReplaced reports whether an execution no longer runs: its record
// says it was stopped, replaced, lost or finished, or it is unknown.
func (d *Daemon) executionReplaced(id string) bool {
	c := d.coreRT()
	if c == nil {
		return false
	}
	all, err := c.records.List(context.Background(), "", "")
	if err != nil {
		return false
	}
	for _, e := range all {
		if e.ID != id {
			continue
		}
		if e.Desired != runtime.DesiredRunning {
			return true
		}
		switch e.Observation.Process {
		case runtime.ProcessLost, runtime.ProcessReplaced, runtime.ProcessFinished:
			return true
		}
		return false
	}
	return true
}

// enrollExecution enrolls an execution under a party name. The name stays the
// agent's across its executions: when the execution bound to it was replaced,
// the binding moves to the new one and the pair waits for approval again; a
// name bound to a running execution is never taken.
func (r *Route) enrollExecution(name, role string, id inbox.Identity, replaced func(string) bool) (string, *APIError) {
	if _, ok := inbox.ExecutionOf(id); !ok {
		return "", Errf("bad_request", "not an execution's identity")
	}
	enrolled := r.path("enrolled", name+".json")
	if exists(enrolled) {
		b, _ := readSmall(enrolled, 4096)
		var prev struct {
			Identity inbox.Identity `json:"identity"`
		}
		if json.Unmarshal(b, &prev) == nil && !prev.Identity.Same(id) {
			old, ok := inbox.ExecutionOf(prev.Identity)
			if !ok || !replaced(old) {
				return "", Errf("name_retired", "name %s is bound to execution %s, which still runs", name, old)
			}
			if err := os.Remove(enrolled); err != nil && !errors.Is(err, os.ErrNotExist) {
				return "", Errf("internal", "rebind %s: %v", name, err)
			}
		}
	}
	return r.enroll(name, role, id)
}

// coreBoundary is the boundary of a core route's setup proof: up's attested
// caller stands in for the human check of the TUI's setup, the inventory is
// the shared-core one of section 6, and no courier pane or lower-trust launch
// exists on core.
func coreBoundary() json.RawMessage {
	return json.RawMessage(`{"version":1,"inventory_current":true,"private_home":true,"human_checked":true,"protected_domain":true,"launch_admission":true,"courier_panes":[],"lower_trust":[]}`)
}

// coreChannel is the route package's view of the daemon's channel for core
// routes.
type coreChannel struct{ d *Daemon }

// Open implements route.CoreChannel: it registers the route's daemon-owned
// channel at inbox once, and writes its setup proof.
func (ch coreChannel) Open(ctx context.Context, q route.ChannelOpen) error {
	d := ch.d
	if !validName(q.Route) || !filepath.IsAbs(q.Inbox) || !filepath.IsAbs(q.Repo) {
		return fmt.Errorf("a core channel needs a route name and absolute inbox and repository paths")
	}
	d.regMu.Lock()
	rf, err := ReadRoutes(d.cfg.PrivateHome)
	if err != nil {
		d.regMu.Unlock()
		return err
	}
	if e, ok := rf.Routes[q.Route]; ok {
		d.regMu.Unlock()
		if e.Inbox != q.Inbox || e.Repo != q.Repo {
			return fmt.Errorf("route %s is already registered at %s for %s", q.Route, e.Inbox, e.Repo)
		}
	} else {
		if e := d.nameTaken(rf, q.Route, ""); e != nil {
			d.regMu.Unlock()
			return e
		}
		if err := os.MkdirAll(filepath.Dir(q.Inbox), 0o700); err != nil {
			d.regMu.Unlock()
			return err
		}
		if err := os.Mkdir(q.Inbox, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			d.regMu.Unlock()
			return err
		}
		if err := mkdirs(q.Inbox, "state", "ids", "enrolled", "alerts"); err != nil {
			d.regMu.Unlock()
			return err
		}
		if _, owned := ReadOwner(q.Inbox); !owned {
			b, _ := encode(d.newOwner(false))
			if err := writeDurable(filepath.Join(q.Inbox, "state", "owner.json"), append(b, '\n'), 0o600); err != nil {
				d.regMu.Unlock()
				return err
			}
		}
		rf.Routes[q.Route] = RouteEntry{Inbox: q.Inbox, Repo: q.Repo, Created: d.now().Unix()}
		if err := d.writeRoutes(rf); err != nil {
			d.regMu.Unlock()
			return err
		}
		d.regMu.Unlock()
		d.openRoute(q.Route, rf.Routes[q.Route])
	}
	rt, e := d.route(q.Route)
	if e != nil {
		return e
	}
	if rt.proofValid() {
		return nil
	}
	return errOf(rt.do(ctx, func() *APIError {
		_, e := rt.setup(SetupRequest{Rules: q.PolicyFiles, PolicyVerified: true, Boundary: coreBoundary()})
		return e
	}))
}

// Enroll implements route.CoreChannel: the execution is proposed as a party,
// with its execution's identity.
func (ch coreChannel) Enroll(ctx context.Context, name, party, role string, e runtime.Execution) error {
	rt, ae := ch.d.route(name)
	if ae != nil {
		return ae
	}
	id := inbox.ExecutionIdentity(e.HarnessKind, e.ID)
	return errOf(rt.do(ctx, func() *APIError {
		_, e := rt.enrollExecution(party, role, id, ch.d.executionReplaced)
		return e
	}))
}

// Approve implements route.CoreChannel: the pending pair, attested by up's
// caller, is approved with the identity credential option, since a core
// party is proven by its process and needs no bearer value.
func (ch coreChannel) Approve(ctx context.Context, name string, pair []runtime.Execution) error {
	rt, ae := ch.d.route(name)
	if ae != nil {
		return ae
	}
	return errOf(rt.do(ctx, func() *APIError {
		pb, ok := rt.registryBytes("peers")
		if !ok {
			return Errf("internal", "invalid pending registry")
		}
		peers, _ := inbox.ValidRegistry(pb)
		// The attested pair is exactly these executions under their
		// agents' names: a registry an agent changed is never approved.
		want := map[string]inbox.Identity{}
		for _, e := range pair {
			want[e.Agent] = inbox.ExecutionIdentity(e.HarnessKind, e.ID)
		}
		if len(peers) != len(want) {
			return Errf("pair_changed", "the pending pair is not the one up attested")
		}
		for _, p := range peers {
			if id, ok := want[p.Name]; !ok || !id.Same(p.Identity) {
				return Errf("pair_changed", "the pending pair is not the one up attested: %s", p.Name)
			}
		}
		if ab, ok := rt.registryBytes("peers.approved"); ok && string(ab) == string(pb) && len(rt.creds) > 0 {
			return nil
		}
		creds := map[string]string{}
		for _, p := range peers {
			creds[p.Name] = "identity"
		}
		_, e := rt.approveAsked(ApproveRequest{SHA256: sha256Hex(pb), Credentials: creds}, false)
		if e == nil {
			ch.d.logf("route %s: pair approved (herdsman up, herdr-core)", rt.name)
		}
		return e
	}))
}

// Approved implements route.CoreChannel.
func (ch coreChannel) Approved(name string) bool {
	rt, e := ch.d.route(name)
	if e != nil {
		return false
	}
	ok := false
	_ = rt.do(context.Background(), func() *APIError {
		pb, okP := rt.registryBytes("peers")
		ab, okA := rt.registryBytes("peers.approved")
		ok = okP && okA && string(pb) == string(ab) && len(rt.creds) > 0
		return nil
	})
	return ok
}

// Handshake implements route.CoreChannel: the pair's first message, from the
// adjutant to the orchestrator, keyed by the route so a retry is the same one.
func (ch coreChannel) Handshake(ctx context.Context, name, from, to, body string) error {
	rt, e := ch.d.route(name)
	if e != nil {
		return e
	}
	return errOf(rt.do(ctx, func() *APIError {
		_, e := rt.send(from, SendRequest{To: to, Kind: "observation", Body: body, Key: sha256Hex([]byte("handshake\x00" + name + "\x00" + body))[:32]})
		return e
	}))
}

// Hold implements route.CoreChannel: a pause holds a party's channel, a
// resume lifts it.
func (ch coreChannel) Hold(ctx context.Context, name, party string, held bool) error {
	rt, e := ch.d.route(name)
	if e != nil {
		return e
	}
	if !held && !ch.Held(name, party) {
		return nil
	}
	unlock, e := rt.inputLock()
	if e != nil {
		return e
	}
	defer unlock()
	return errOf(rt.do(ctx, func() *APIError {
		var e *APIError
		if held {
			reason := "the route is paused"
			_, e = rt.hold(party, party, &reason)
		} else {
			_, e = rt.hold(party, party, nil)
		}
		return e
	}))
}

// Held implements route.CoreChannel.
func (ch coreChannel) Held(name, party string) bool {
	rt, e := ch.d.route(name)
	if e != nil {
		return false
	}
	owners, ok := rt.holdOwners(party)
	return ok && len(owners) > 0
}

// errOf turns a route call's result into an error, so a nil *APIError is a
// nil error and never a non-nil interface holding nil.
func errOf(e *APIError) error {
	if e == nil {
		return nil
	}
	return e
}
