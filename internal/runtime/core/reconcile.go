package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/runtime"
)

// Event kinds the reconcilers raise on the daemon's event stream.
const (
	EventExecutionLost     = "execution_lost"
	EventExecutionFinished = "execution_finished"
	EventRecoveryPlan      = "recovery_plan"
	EventEndpointMismatch  = "endpoint_mismatch"
	EventResync            = "resync"
	EventVisibility        = "visibility"
	EventInventoryFault    = "inventory_fault"
)

// Event is one thing a reconciler found.
type Event struct {
	Kind       string
	Repo       string
	Route      string
	Execution  string
	Address    string
	Detail     string
	Executions []string
	At         time.Time
}

// Reconciler keeps execution records true to one core (spec section 11). It
// runs the execution, identity, inventory and visibility reconcilers: a full
// pass on start and on every reconnect or missed snapshot, an immediate pass
// for a loss, an end or a foreground change, ordinary updates coalesced, and
// a safety pass every 30 seconds. A core whose identity changed freezes every
// mutation until a person settles it.
type Reconciler struct {
	Socket string
	// ExpectCore is the core id the records were made on.
	ExpectCore string
	Records    Records
	Procs      ProcessTable
	App        *herdrapp.App
	// AppFor, when set, is the app an execution's route shows its agents in
	// (its [runtime.app] binary and home); nil from it, or no AppFor, is App.
	AppFor   func(runtime.Execution) *herdrapp.App
	Bindings Bindings
	// HarnessNames are the program names that count as a coding agent.
	HarnessNames []string
	// Restricted reports whether a route runs under the restricted profile.
	Restricted func(repo, route string) bool
	// Sessions are the harnesses' session folders a live execution's
	// conversation evidence is read from; nil reads nothing.
	Sessions *SessionRoots
	// Marks are the ids of the notices issued to an execution, which
	// settle a choice between sessions of one folder; nil has none.
	Marks func(execution string) []string
	Emit  func(Event)
	Now   func() time.Time
	// Safety, Coalesce: the periodic pass and the coalescing window
	// (defaults 30 s and 100 ms).
	Safety, Coalesce time.Duration
	Options          hc.Options

	mu          sync.Mutex
	frozen      string
	plans       map[string]bool
	faults      map[string]string
	inventories map[string]Inventory
}

func routeKey(repo, route string) string { return repo + "\x00" + route }

func (r *Reconciler) defaults() {
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.Safety == 0 {
		r.Safety = 30 * time.Second
	}
	if r.Coalesce == 0 {
		r.Coalesce = 100 * time.Millisecond
	}
	if r.Emit == nil {
		r.Emit = func(Event) {}
	}
	if r.Procs == nil {
		r.Procs = SystemProcs{}
	}
	r.mu.Lock()
	if r.plans == nil {
		r.plans, r.faults, r.inventories = map[string]bool{}, map[string]string{}, map[string]Inventory{}
	}
	r.mu.Unlock()
}

// Frozen reports whether mutations are frozen, and why.
func (r *Reconciler) Frozen() (bool, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.frozen != "", r.frozen
}

func (r *Reconciler) freeze(why string) {
	r.mu.Lock()
	first := r.frozen == ""
	r.frozen = why
	r.mu.Unlock()
	if first {
		r.Emit(Event{Kind: EventEndpointMismatch, Detail: why, At: r.Now()})
	}
}

// RouteInventory returns the last inventory of one route.
func (r *Reconciler) RouteInventory(repo, route string) (Inventory, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inv, ok := r.inventories[routeKey(repo, route)]
	return inv, ok
}

// urgent reports the event types that are processed at once.
func urgent(typ string) bool {
	return slices.Contains([]string{hc.EventLost, hc.EventFinished, hc.EventRestarted, hc.EventRemoved, hc.EventForeground, hc.EventLabels}, typ)
}

// Run follows the core until ctx ends or its identity changes.
func (r *Reconciler) Run(ctx context.Context) error {
	r.defaults()
	now, later := make(chan struct{}, 1), make(chan struct{}, 1)
	poke := func(ch chan struct{}) {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		safety := time.NewTicker(r.Safety)
		defer safety.Stop()
		var coalesce <-chan time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-now:
			case <-later:
				if coalesce == nil {
					coalesce = time.After(r.Coalesce)
				}
				continue
			case <-coalesce:
			case <-safety.C:
			}
			coalesce = nil
			if err := r.Pass(ctx); err != nil {
				var changed *hc.CoreChangedError
				if errors.As(err, &changed) {
					cancel()
					return
				}
			}
		}
	}()
	opts := r.Options
	opts.ExpectCore = r.ExpectCore
	if opts.ConnectTimeout <= 0 {
		opts.ConnectTimeout = DefaultConnect
	}
	err := hc.Watch(ctx, r.Socket, hc.Filter{}, opts, func(u hc.Update) error {
		switch u.Kind {
		case hc.UpdateSnapshot, hc.UpdateMissed:
			r.Emit(Event{Kind: EventResync, Detail: fmt.Sprintf("epoch %d", u.Epoch), At: r.Now()})
			poke(now)
		case hc.UpdateEvent:
			if urgent(u.Event.Type) {
				poke(now)
			} else {
				poke(later)
			}
		}
		return nil
	})
	var changed *hc.CoreChangedError
	if errors.As(err, &changed) {
		r.freeze(changed.Error())
	}
	if frozen, why := r.Frozen(); frozen {
		return refuse(CodeFrozen, "%s", why)
	}
	return err
}

// Pass runs every reconciler once against fresh records from core.
func (r *Reconciler) Pass(ctx context.Context) error {
	r.defaults()
	opts := r.Options
	opts.ExpectCore = r.ExpectCore
	if opts.ConnectTimeout <= 0 {
		opts.ConnectTimeout = DefaultConnect
	}
	c, err := hc.Dial(ctx, r.Socket, opts)
	if err != nil {
		var changed *hc.CoreChangedError
		if errors.As(err, &changed) {
			r.freeze(changed.Error())
		}
		return err
	}
	defer c.Close()
	if r.ExpectCore == "" {
		r.ExpectCore = c.Core().ID
	}
	execs, err := r.Records.List(ctx, "", "")
	if err != nil {
		return err
	}
	lost := map[string][]runtime.Execution{}
	routes := map[string][2]string{}
	for i, e := range execs {
		if !running(e) || !registered(e) || e.EndpointID != "" && e.EndpointID != c.Core().ID {
			continue
		}
		routes[routeKey(e.Repo, e.Route)] = [2]string{e.Repo, e.Route}
		next, gone, err := r.execution(ctx, c, e)
		if err != nil {
			return err
		}
		if gone {
			lost[routeKey(e.Repo, e.Route)] = append(lost[routeKey(e.Repo, e.Route)], next)
		}
		if next.Observation.Process == runtime.ProcessLive && r.appOf(next) != nil && next.App != nil {
			next = r.visibility(ctx, c, r.appOf(next), next)
		}
		// Conversation evidence: the harness writes its session once a
		// person has started it, so a live execution without it is looked
		// up again each pass until exactly one session is its own.
		next, _, _ = withConversation(r.Sessions, r.Marks, execs, next)
		if _, err := r.Records.Put(ctx, next); err != nil {
			return err
		}
		execs[i] = next // a conversation recorded now is taken for the rest
	}
	r.recoveryPlans(lost)
	return r.inventory(ctx, c, routes)
}

// execution checks one execution's identity: the address must run the
// registered process. Another process there is a loss (a recovery shell); an
// ended terminal is an external stop, never restarted.
func (r *Reconciler) execution(ctx context.Context, c *hc.Client, e runtime.Execution) (runtime.Execution, bool, error) {
	now := r.Now()
	t, err := c.Get(ctx, e.TerminalAddress)
	if err != nil && !hc.IsKind(err, hc.KindNotFound) {
		return e, false, err
	}
	obs := &e.Observation
	obs.ObservedAt = now
	switch {
	case err != nil || !t.Live():
		if obs.Process != runtime.ProcessFinished {
			r.Emit(Event{Kind: EventExecutionFinished, Repo: e.Repo, Route: e.Route, Execution: e.ID, Address: e.TerminalAddress,
				Detail: "the terminal ended; the execution is stopped and is not restarted", At: now})
		}
		obs.Process, obs.Readiness, obs.Orchestration = runtime.ProcessFinished, runtime.ReadinessIdentityUnknown, runtime.OrchestrationStopped
		e.Desired = runtime.DesiredStopped
		return e, false, nil
	}
	alive, err := Alive(r.Procs, e.Evidence)
	if err != nil {
		return e, false, err
	}
	if t.PID == nil || int(*t.PID) != e.Evidence.PID || !alive {
		was := obs.Process
		obs.Process, obs.Readiness = runtime.ProcessLost, runtime.ReadinessIdentityUnknown
		if was != runtime.ProcessLost {
			r.Emit(Event{Kind: EventExecutionLost, Repo: e.Repo, Route: e.Route, Execution: e.ID, Address: e.TerminalAddress,
				Detail: "another process runs at the address; it is left alone and nothing is typed into it", At: now})
			return e, true, nil
		}
		return e, false, nil
	}
	obs.Process = runtime.ProcessLive
	if t.Status != nil {
		obs.Program, obs.RawProgram = runtime.ProgramOf(t.Status.State), t.Status.State
	}
	e.LastVerifiedAt = now
	return e, false, nil
}

// recoveryPlans offers one plan per route with new losses, naming every lost
// execution of the route together.
func (r *Reconciler) recoveryPlans(lost map[string][]runtime.Execution) {
	for key, list := range lost {
		r.mu.Lock()
		offered := r.plans[key]
		r.plans[key] = true
		r.mu.Unlock()
		if offered {
			continue
		}
		var ids []string
		for _, e := range list {
			ids = append(ids, e.ID)
		}
		r.Emit(Event{Kind: EventRecoveryPlan, Repo: list[0].Repo, Route: list[0].Route, Executions: ids, At: r.Now(),
			Detail: "the lost executions get new executions in new terminals and tabs when a person resumes the route; each shell is left as it is"})
	}
}

// appOf is the app an execution is shown in.
func (r *Reconciler) appOf(e runtime.Execution) *herdrapp.App {
	if r.AppFor != nil {
		if a := r.AppFor(e); a != nil {
			return a
		}
	}
	return r.App
}

// visibility reconciles one live execution's place in the app, respecting
// what a person did: a park holds dispatch, a move or a rename is kept.
func (r *Reconciler) visibility(ctx context.Context, c *hc.Client, shown *herdrapp.App, e runtime.Execution) runtime.Execution {
	t, err := c.Get(ctx, e.TerminalAddress)
	if err != nil {
		return e
	}
	placement := herdrapp.PlacementIDs{Space: e.App.SpaceID, Tab: e.App.TabID}
	if p, ok := placementOf(t); ok && e.App.Visibility != runtime.VisibilityMissing {
		placement = p
	}
	owned := herdrapp.Owned{Terminal: e.TerminalAddress, Execution: e.ID, Placement: placement}
	obs, err := shown.Observe(ctx, owned)
	if err != nil {
		obs = herdrapp.Observation{AppClosed: true}
	}
	bindingValid := false
	if r.Bindings != nil {
		if b, ok, err := r.Bindings.Load(shown.Home, c.Core().ID); err == nil && ok && b.Valid(herdrapp.Core{ID: c.Core().ID, Endpoint: b.Endpoint}, shown.Home) == nil {
			bindingValid = true
		}
	}
	parked := t.Labels[herdrapp.LabelExecution] == e.ID && (t.Labels[herdrapp.LabelSpace] == "" || t.Labels[herdrapp.LabelTab] == "")
	rep := herdrapp.Next(herdrapp.Visibility(e.App.Visibility), herdrapp.Facts{
		BindingValid: bindingValid, LabelsReadBack: t.Labels[herdrapp.LabelExecution] == e.ID, Observation: obs, Parked: parked, TimedOut: !obs.AppClosed,
	})
	prev := e.App.Visibility
	app := *e.App
	app.Visibility = runtime.Visibility(rep.State)
	if obs.PanePresent {
		app.SpaceID, app.TabID, app.PaneID = obs.Where.Space, obs.Where.Tab, obs.Where.Pane
	}
	e.App = &app
	e.Observation.Visibility = app.Visibility
	e.Observation.App = runtime.AppOpen
	if rep.AppClosed {
		e.Observation.App = runtime.AppClosed
	}
	if prev != app.Visibility {
		r.Emit(Event{Kind: EventVisibility, Repo: e.Repo, Route: e.Route, Execution: e.ID, Address: e.TerminalAddress,
			Detail: fmt.Sprintf("%s -> %s", prev, app.Visibility), At: r.Now()})
	}
	return e
}

func placementOf(t hc.Terminal) (herdrapp.PlacementIDs, bool) {
	p := herdrapp.PlacementIDs{Space: t.Labels[herdrapp.LabelSpace], Tab: t.Labels[herdrapp.LabelTab]}
	return p, p.Space != "" && p.Tab != ""
}

// inventory classifies every live terminal for each core route.
func (r *Reconciler) inventory(ctx context.Context, c *hc.Client, routes map[string][2]string) error {
	if len(routes) == 0 {
		return nil
	}
	live, err := c.List(ctx, nil, false)
	if err != nil {
		return err
	}
	execs, err := r.Records.List(ctx, "", "")
	if err != nil {
		return err
	}
	procs, _ := r.Procs.List(ctx)
	for key, rr := range routes {
		// The route's opaque id is what its members' own terminals carry.
		routeID := ""
		for _, t := range live.Terminals {
			for _, e := range execs {
				if e.Repo == rr[0] && e.Route == rr[1] && t.Labels[herdrapp.LabelExecution] == e.ID {
					routeID = t.Labels[herdrapp.LabelRoute]
				}
			}
		}
		restricted := r.Restricted != nil && r.Restricted(rr[0], rr[1])
		inv := Classify(InventoryInput{Terminals: live.Terminals, Executions: execs, Repo: rr[0], Route: rr[1], RouteID: routeID,
			Procs: procs, HarnessNames: r.HarnessNames, Restricted: restricted})
		text := ""
		if !inv.OK() {
			text = inv.FaultText()
		}
		r.mu.Lock()
		r.inventories[key] = inv
		changed := r.faults[key] != text
		r.faults[key] = text
		r.mu.Unlock()
		if changed && text != "" {
			r.Emit(Event{Kind: EventInventoryFault, Repo: rr[0], Route: rr[1], Detail: text, At: r.Now()})
		}
	}
	return nil
}
