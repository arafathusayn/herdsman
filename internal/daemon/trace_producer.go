package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/trace"
)

// routeFolderEnv names the route folder a process works for. Route starts set
// it for the agents they launch.
const routeFolderEnv = "HERDSMAN_ROUTE_FOLDER"

// herdrWorking is the status herdr gives an agent in the middle of a turn.
const herdrWorking herdr.Status = "working"

// turnPollInterval is how often a route's agents are looked at. A turn shorter
// than this can be missed, and a recorded one is accurate to about this much.
const turnPollInterval = 5 * time.Second

// turnWatch remembers which agents were last seen working, and since when. A
// daemon restart forgets them, so a turn in progress then starts when it is
// next seen.
type turnWatch struct {
	mu      sync.Mutex
	working map[string]observedTurn
	every   time.Duration // Zero means turnPollInterval.
}

type observedTurn struct {
	since   time.Time
	name    string
	agent   string
	session json.RawMessage
}

// turnKind tells a review from other work by the agent's name: herdsman names
// every agent it starts for its role.
func turnKind(agent string) string {
	if strings.Contains(strings.ToLower(agent), "reviewer") {
		return trace.KindReview
	}
	return trace.KindTurn
}

// observeTurnsLater arms the next look at the routes' agents. A timer rather
// than a loop, so no goroutine waits between rounds.
func (d *Daemon) observeTurnsLater(ctx context.Context) {
	every := d.turns.every
	if every == 0 {
		every = turnPollInterval
	}
	time.AfterFunc(every, func() {
		d.mu.Lock()
		if d.upgrading || ctx.Err() != nil {
			d.mu.Unlock()
			return
		}
		d.bg.Add(1)
		d.mu.Unlock()
		defer d.bg.Done()
		d.observeTurns(ctx)
		d.observeTurnsLater(ctx)
	})
}

// observeTurns looks once at the agents of every route that is up.
func (d *Daemon) observeTurns(ctx context.Context) {
	if d.ops == nil {
		return
	}
	all, err := d.ops.List()
	if err != nil {
		return
	}
	services := &routeService{d: d}
	for _, op := range all {
		// TUI turn status is not part of core's protocol. Core launches,
		// notices and submissions have their own trace producers.
		if op.Kind != route.OperationKind || route.BackendOf(op) == runtime.BackendCore || startOf(op).GetState() != "done" {
			continue
		}
		name, repo := startOf(op).GetRoute().GetValue(), route.RepoOf(op)
		if control, err := services.controlState(repo, name, op.ID); err != nil || control != route.ControlRunning {
			continue
		}
		agents, err := herdr.New(route.SocketOf(op)).AgentList(ctx)
		if err != nil {
			continue
		}
		d.observeRoute(op.ID, filepath.Base(repo)+"-"+name+"-orchestrator", startOf(op).GetFolder(), agents)
	}
}

// observeRoute moves each of the route's agents along: one seen working starts
// a turn, and one seen doing anything else, or gone, ends it.
func (d *Daemon) observeRoute(id, orchestrator, folder string, agents []herdr.AgentRecord) {
	workspace := ""
	for _, agent := range agents {
		if agent.Name != nil && *agent.Name == orchestrator {
			workspace = agent.WorkspaceID
		}
	}
	now := time.Now().UTC()
	seen := map[string]bool{}
	var ended []observedTurn
	d.turns.mu.Lock()
	if d.turns.working == nil {
		d.turns.working = map[string]observedTurn{}
	}
	for _, agent := range agents {
		key := id + "\x00" + agent.PaneID
		if workspace == "" || agent.WorkspaceID != workspace || agent.Name == nil {
			continue
		}
		turn, open := d.turns.working[key]
		switch {
		case agent.Status == herdrWorking && !open:
			seen[key] = true
			kind := ""
			if agent.Agent.Agent != nil {
				kind = *agent.Agent.Agent
			}
			d.turns.working[key] = observedTurn{since: now, name: *agent.Name, agent: kind, session: agent.AgentSession}
		case agent.Status == herdrWorking:
			seen[key] = true
		case open:
			ended = append(ended, turn)
			seen[key] = true
			delete(d.turns.working, key)
		}
	}
	for key, turn := range d.turns.working {
		if strings.HasPrefix(key, id+"\x00") && !seen[key] {
			ended = append(ended, turn)
			delete(d.turns.working, key)
		}
	}
	d.turns.mu.Unlock()
	for _, turn := range ended {
		d.recordTurn(id, folder, turn, now)
	}
}

// recordTurn adds one finished turn to the route's trace, with what the
// harness measured when its session log can be read.
func (d *Daemon) recordTurn(id, folder string, turn observedTurn, end time.Time) {
	span := trace.Span{Kind: turnKind(turn.name), Name: turn.name, Start: turn.since, DurationMS: float64(end.Sub(turn.since)) / float64(time.Millisecond)}
	var session struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	if json.Unmarshal(turn.session, &session) == nil && session.Value != "" {
		if home, err := os.UserHomeDir(); err == nil {
			span.Tokens, span.CostMicros = trace.HarnessUsage(trace.HarnessSession{Agent: turn.agent, Kind: session.Kind, Value: session.Value, Home: home}, turn.since, end)
		}
	}
	if err := trace.Append(folder, span); err != nil {
		d.logf("route start %s: the trace has no record of this turn: %v", id, err)
	}
}
