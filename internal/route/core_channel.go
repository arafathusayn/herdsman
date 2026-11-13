package route

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/runtime"
)

// The pair channel of a route on herdr-core is the daemon's (spec section 6).
// The TUI's start enrolls each coordinator by typing nothing but running the
// channel command as its pane; a core agent has no pane, so the route
// registers its channel with the daemon, which identifies each party by the
// execution its process belongs to, and the pair's messages flow through the
// approved-pair channel and the durable outbox. Nothing is typed.
const (
	effectCoreChannel   = "core.channel"
	effectCoreHandshake = "core.handshake"
	effectCoreChanHold  = "core.channel-hold"
)

// ChannelOpen registers a core route's channel.
type ChannelOpen struct {
	Route, Repo, Inbox string
	// PolicyFiles are the sandbox policy files the setup proof pins.
	PolicyFiles []string
}

// CoreChannel is the daemon's channel for routes on herdr-core.
type CoreChannel interface {
	// Open registers the route's daemon-owned channel and its setup proof;
	// a repeat is a no-op.
	Open(ctx context.Context, q ChannelOpen) error
	// Enroll proposes an execution as the party named party: its registry
	// identity is the execution's. A name whose execution was replaced is
	// bound to the new one.
	Enroll(ctx context.Context, route, party, role string, e runtime.Execution) error
	// Approve approves the pending pair that up's caller attested: only
	// when it is exactly these executions under their agents' names.
	Approve(ctx context.Context, route string, pair []runtime.Execution) error
	// Approved reports a pair with no pending change.
	Approved(route string) bool
	// Handshake seeds the pair's first message; a retry is the same one.
	Handshake(ctx context.Context, route, from, to, body string) error
	// Hold holds or lifts a party's channel.
	Hold(ctx context.Context, route, party string, held bool) error
	// Held reports a party's channel held.
	Held(route, party string) bool
}

func coreChannel(deps Deps) (CoreChannel, error) {
	if deps.CoreChannel == nil {
		return nil, runtime.Unavailable(runtime.BackendCore, "for a route with a pair channel in this daemon")
	}
	return deps.CoreChannel, nil
}

func applyCoreChannel(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	ch, err := coreChannel(deps)
	if err != nil {
		return err
	}
	if err := checkInboxPlace(d); err != nil {
		return err
	}
	return ch.Open(ctx, ChannelOpen{Route: d.Route, Repo: d.Repo, Inbox: d.Inbox, PolicyFiles: d.PolicyFiles})
}

func probeCoreChannel(_ context.Context, _ Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	if _, err := os.Stat(filepath.Join(d.Inbox, "state", "owner.json")); err == nil {
		if _, err := os.Stat(filepath.Join(d.Inbox, "setup.json")); err == nil {
			return ops.Matches, "", nil
		}
	}
	return ops.Absent, "", nil
}

// enrollChannel enrolls the pair's executions in the route's channel and
// approves the pair, once up's caller has attested it.
func enrollChannel(ctx context.Context, deps Deps, d stepData, execs []runtime.Execution) error {
	if d.Inbox == "" {
		return nil
	}
	ch, err := coreChannel(deps)
	if err != nil {
		return err
	}
	for _, e := range execs {
		if err := ch.Enroll(ctx, d.Route, e.Agent, e.Role, e); err != nil {
			return fmt.Errorf("enroll %s: %w", e.Agent, err)
		}
	}
	return ch.Approve(ctx, d.Route, execs)
}

func channelApproved(deps Deps, d stepData) bool {
	if d.Inbox == "" {
		return true
	}
	ch, err := coreChannel(deps)
	return err == nil && ch.Approved(d.Route)
}

// applyCoreHandshake seeds the pair's first message from the adjutant.
func applyCoreHandshake(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	ch, err := coreChannel(deps)
	if err != nil {
		return err
	}
	body := fmt.Sprintf("handshake %s: adjutant %s, orchestrator %s\n", d.Route, d.Adjutant, d.Orchestrator)
	if err := ch.Handshake(ctx, d.Route, d.Adjutant, d.Orchestrator, body); err != nil {
		return fmt.Errorf("seed the handshake: %w", err)
	}
	return writeFileNew(handshakeBody(d)+".sent", nil)
}

// applyCoreChanHold holds (a pause) or lifts (a resume) one party's channel.
func applyCoreChanHold(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	ch, err := coreChannel(deps)
	if err != nil {
		return err
	}
	return ch.Hold(ctx, d.Route, d.Name, d.Control.Verb == VerbPause)
}

func probeCoreChanHold(_ context.Context, deps Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	ch, err := coreChannel(deps)
	if err != nil {
		return ops.Unknown, err.Error(), nil
	}
	if ch.Held(d.Route, d.Name) == (d.Control.Verb == VerbPause) {
		return ops.Matches, "", nil
	}
	return ops.Absent, "", nil
}
