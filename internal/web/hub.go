package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/route"
)

// maxRoutes bounds how many route folders are read on each refresh.
const maxRoutes = 8

// eventLog is how many recent events a new browser gets on connect.
const eventLog = 200

// structural lists the herdr events that need no pane id; any of them
// triggers a fresh snapshot.
var structural = []string{
	"workspace.created", "workspace.closed", "workspace.renamed",
	"tab.created", "tab.closed", "tab.renamed", "tab.focused",
	"pane.created", "pane.closed", "pane.updated", "pane.focused", "pane.moved",
	"pane.exited", "pane.agent_detected", "layout.updated",
}

// Entry is one line of the activity stream: a herdr event or a web action.
type Entry struct {
	At     time.Time       `json:"at"`
	Source string          `json:"source"` // "herdr" or "web"
	Kind   string          `json:"kind"`
	PaneID string          `json:"pane_id,omitempty"`
	Status string          `json:"status,omitempty"` // the agent status a status event moved to
	Text   string          `json:"text,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// RouteView is a route as the browser sees it.
type RouteView struct {
	*route.Route
	// ID names the route in links and requests: its folder name and a short
	// hash of its full path. It depends on nothing else, so it stays the same
	// when another project's route of the same name comes or goes.
	ID   string `json:"id"`
	Live bool   `json:"live"`
	// OlderRunState counts the run-state lines before RunState, which keeps
	// the newest runStateKept only; GET /api/route/runstate pages them (A24).
	OlderRunState int `json:"older_run_state,omitempty"`
	// runStateLast: the run state is the file's last section, so its older
	// lines can be read backward from the end.
	runStateLast bool
}

// runStateKept bounds the run-state lines the hub holds per route.
const runStateKept = 100

// routeID is the ID of the route in folder dir.
func routeID(dir, name string) string {
	sum := sha256.Sum256([]byte(dir))
	return name + "~" + hex.EncodeToString(sum[:3])
}

// findRoute is the route with this ID, or nil.
func findRoute(rs []RouteView, id string) *RouteView {
	for i := range rs {
		if rs[i].ID == id {
			return &rs[i]
		}
	}
	return nil
}

// State is everything the browser renders, sent whole on every change.
type State struct {
	Herdr    herdr.Pong     `json:"herdr"`
	Snapshot herdr.Snapshot `json:"snapshot"`
	Routes   []RouteView    `json:"routes"`
	Error    string         `json:"error,omitempty"`
}

// refreshEvery is how often the hub reads herdr and the route folders
// while a browser watches, so a route file's change shows within seconds.
const refreshEvery = 3 * time.Second

type Hub struct {
	client     *herdr.Client
	pong       herdr.Pong
	routeDirs  []string // from --route; searched before pane folders
	refreshReq chan struct{}
	// panesChanged wakes statusLoop when the set of agent panes changes.
	panesChanged chan struct{}
	every        time.Duration // refreshEvery; tests shorten it
	// The event stream's retry: retryBase doubling up to retryMax, waited
	// through retryWait (time.After); tests shorten and watch them.
	retryBase, retryMax time.Duration
	retryWait           func(time.Duration) <-chan time.Time

	// refreshMu serializes refreshes; last is when the latest one ended.
	refreshMu sync.Mutex
	last      time.Time
	lastPanes []string
	// routes caches each route folder's parsed file by its size and time,
	// so an unchanged route is not read again (A24); refreshMu guards it.
	routes map[string]cachedRoute

	mu      sync.Mutex
	state   State
	encoded []byte
	entries []Entry
	subs    map[chan message]struct{}
}

type message struct {
	event string
	data  []byte
}

func NewHub(c *herdr.Client, pong herdr.Pong, routeDirs []string) *Hub {
	return &Hub{
		client: c, pong: pong, routeDirs: routeDirs,
		refreshReq:   make(chan struct{}, 1),
		panesChanged: make(chan struct{}, 1),
		every:        refreshEvery,
		retryBase:    time.Second,
		retryMax:     30 * time.Second,
		retryWait:    time.After,
		subs:         map[chan message]struct{}{},
	}
}

// Run keeps the state current until ctx ends.
func (h *Hub) Run(ctx context.Context) {
	h.refresh(ctx)
	go h.subscribeLoop(ctx, func(context.Context) []map[string]any {
		subs := make([]map[string]any, len(structural))
		for i, t := range structural {
			subs[i] = map[string]any{"type": t}
		}
		return subs
	}, nil)
	go h.statusLoop(ctx)
	tick := time.NewTicker(h.every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.refreshReq:
			// Coalesce bursts: a split fires created, updated and layout at once.
			select {
			case <-time.After(100 * time.Millisecond):
			case <-ctx.Done():
				return
			}
			h.refresh(ctx)
		case <-tick.C:
			// Nobody watches: skip the herdr call and the route folder walk.
			// herdr's events still refresh, and the next browser or request
			// reads a fresh state (see fresh).
			if h.watched() {
				h.refresh(ctx)
			}
		}
	}
}

// watched says whether a browser follows the live stream.
func (h *Hub) watched() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs) > 0
}

// fresh refreshes the state when no refresh ran for one period, which
// happens only while no browser watched.
func (h *Hub) fresh() {
	h.refreshMu.Lock()
	stale := time.Since(h.last) >= h.every
	h.refreshMu.Unlock()
	if stale {
		h.refresh(context.Background())
	}
}

// Refresh asks for a new snapshot soon.
func (h *Hub) Refresh() {
	select {
	case h.refreshReq <- struct{}{}:
	default:
	}
}

func (h *Hub) subscribeLoop(ctx context.Context, subs func(context.Context) []map[string]any, stop <-chan struct{}) {
	backoff := h.retryBase
	for ctx.Err() == nil {
		sctx, cancel := context.WithCancel(ctx)
		if stop != nil {
			go func() {
				select {
				case <-stop:
					cancel()
				case <-sctx.Done():
				}
			}()
		}
		err := h.client.Subscribe(sctx, subs(sctx), func(ev herdr.Event) {
			backoff = h.retryBase
			h.record(ev)
			h.Refresh()
		})
		cancel()
		if stop != nil {
			select {
			case <-stop:
				return
			default:
			}
		}
		if ctx.Err() != nil {
			return
		}
		log.Printf("herdr event stream ended: %v; retrying in %s", err, backoff)
		select {
		case <-h.retryWait(backoff):
		case <-ctx.Done():
			return
		}
		backoff = min(backoff*2, h.retryMax)
	}
}

// statusLoop keeps one subscription to the status changes of every agent
// pane, and starts a new one when the set of agent panes changes, because
// herdr only offers that event per pane. A refresh that sees the set change
// wakes it; nothing else does.
func (h *Hub) statusLoop(ctx context.Context) {
	var current []string
	var stop chan struct{}
	for {
		panes := h.agentPanes()
		if !slices.Equal(panes, current) {
			if stop != nil {
				close(stop)
				stop = nil
			}
			current = panes
			if len(panes) > 0 {
				stop = make(chan struct{})
				subs := make([]map[string]any, len(panes))
				for i, p := range panes {
					subs[i] = map[string]any{"type": "pane.agent_status_changed", "pane_id": p}
				}
				go h.subscribeLoop(ctx, func(context.Context) []map[string]any { return subs }, stop)
			}
		}
		select {
		case <-ctx.Done():
			if stop != nil {
				close(stop)
			}
			return
		case <-h.panesChanged:
		}
	}
}

func (h *Hub) agentPanes() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return panesOf(h.state.Snapshot)
}

func panesOf(s herdr.Snapshot) []string {
	out := make([]string, 0, len(s.Agents))
	for _, a := range s.Agents {
		out = append(out, a.PaneID)
	}
	sort.Strings(out)
	return out
}

func (h *Hub) record(ev herdr.Event) {
	e := Entry{At: time.Now(), Source: "herdr", Kind: ev.Event, Data: ev.Data}
	var d struct {
		PaneID      string `json:"pane_id"`
		AgentStatus string `json:"agent_status"`
		Pane        struct {
			PaneID string `json:"pane_id"`
		} `json:"pane"`
	}
	if json.Unmarshal(ev.Data, &d) == nil {
		e.PaneID, e.Status = d.PaneID, d.AgentStatus
		if e.PaneID == "" {
			e.PaneID = d.Pane.PaneID
		}
	}
	h.Log(e)
}

// Log adds an entry to the activity stream and sends it to every browser.
func (h *Hub) Log(e Entry) {
	b, _ := json.Marshal(e)
	h.mu.Lock()
	h.entries = append(h.entries, e)
	if len(h.entries) > eventLog {
		h.entries = h.entries[len(h.entries)-eventLog:]
	}
	h.mu.Unlock()
	h.broadcast(message{"entry", b})
}

func (h *Hub) refresh(ctx context.Context) {
	h.refreshMu.Lock()
	defer h.refreshMu.Unlock()
	defer func() { h.last = time.Now() }()
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	snap, err := h.client.Snapshot(cctx)
	st := State{Herdr: h.pong, Snapshot: snap}
	if err != nil {
		h.mu.Lock()
		st.Snapshot = h.state.Snapshot
		h.mu.Unlock()
		st.Error = err.Error()
	}
	st.Routes = h.loadRoutes(st.Snapshot)
	b, _ := json.Marshal(st)
	h.mu.Lock()
	changed := !bytes.Equal(b, h.encoded)
	h.state, h.encoded = st, b
	h.mu.Unlock()
	if changed {
		h.broadcast(message{"state", b})
	}
	if panes := panesOf(st.Snapshot); !slices.Equal(panes, h.lastPanes) {
		h.lastPanes = panes
		select {
		case h.panesChanged <- struct{}{}:
		default:
		}
	}
}

func (h *Hub) loadRoutes(snap herdr.Snapshot) []RouteView {
	starts := slices.Clone(h.routeDirs)
	for _, p := range snap.Panes {
		if p.Cwd != nil {
			starts = append(starts, *p.Cwd)
		}
	}
	dirs := slices.Clone(h.routeDirs)
	for _, d := range route.Find(starts) {
		if !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	if len(dirs) > maxRoutes {
		dirs = dirs[:maxRoutes]
	}
	panes := map[string]bool{}
	for _, p := range snap.Panes {
		panes[p.ID] = true
	}
	agentAt := map[string]string{}
	for _, a := range snap.Agents {
		if a.Name != nil {
			agentAt[a.PaneID] = *a.Name
		}
	}
	// dirs is newest first, so a pane id that an old route also names (pane
	// ids are reused) counts for the newest route only.
	claimed := map[string]bool{}
	var out []RouteView
	for _, d := range dirs {
		tail, ok := h.loadRoute(d)
		if !ok {
			continue
		}
		r := tail.Route
		out = append(out, RouteView{Route: r, ID: routeID(r.Dir, r.Name), Live: isLive(r, panes, agentAt, claimed),
			OlderRunState: tail.Total - len(r.RunState), runStateLast: tail.RunStateLast()})
	}
	for d := range h.routes {
		if !slices.Contains(dirs, d) {
			delete(h.routes, d)
		}
	}
	return out
}

// cachedRoute is one parsed route file and the size and time it had.
type cachedRoute struct {
	size int64
	mod  time.Time
	tail *route.Tail
}

// loadRoute reads a route folder's file only when its size or time
// changed since the last refresh, and then only what was appended when it
// grew (route.LoadBounded); the run state keeps the newest runStateKept.
func (h *Hub) loadRoute(dir string) (*route.Tail, bool) {
	st, err := os.Stat(filepath.Join(dir, route.RouteFile))
	if err != nil {
		delete(h.routes, dir)
		return nil, false
	}
	c, ok := h.routes[dir]
	if !ok || c.size != st.Size() || !c.mod.Equal(st.ModTime()) {
		tail, err := route.LoadBounded(dir, runStateKept, c.tail)
		if err != nil {
			delete(h.routes, dir)
			return nil, 0, false
		}
		if h.routes == nil {
			h.routes = map[string]cachedRoute{}
		}
		c = cachedRoute{size: st.Size(), mod: st.ModTime(), tail: tail}
		h.routes[dir] = c
	}
	return c.tail, true
}

// isLive says whether a route's agents run now: a task row whose agent name
// runs in the row's pane, or an orchestrator pane no newer route claimed.
func isLive(r *route.Route, panes map[string]bool, agentAt map[string]string, claimed map[string]bool) bool {
	live := false
	for _, t := range r.Tasks {
		// A newer route that names the same pane owns it, even when the agent's name is reused too.
		if t.Pane != "" && t.Agent != "" && agentAt[t.Pane] == t.Agent && !claimed[t.Pane] {
			live = true
		}
	}
	if !live && r.OrchestratorPane != "" && panes[r.OrchestratorPane] && !claimed[r.OrchestratorPane] {
		live = true
	}
	if live {
		claimed[r.OrchestratorPane] = true
		for _, t := range r.Tasks {
			claimed[t.Pane] = true
		}
	}
	return live
}

// Current returns the latest state.
func (h *Hub) Current() State {
	h.fresh()
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

func (h *Hub) subscribe() (chan message, []byte, []Entry) {
	h.fresh()
	ch := make(chan message, 64)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subs[ch] = struct{}{}
	return ch, h.encoded, slices.Clone(h.entries)
}

func (h *Hub) unsubscribe(ch chan message) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

func (h *Hub) broadcast(m message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- m:
		default:
			// A stalled browser would miss this state for good if the session
			// then goes quiet, so its stream ends: it reconnects and starts
			// from the current snapshot.
			delete(h.subs, ch)
			close(ch)
		}
	}
}
