package route

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/runtime"
)

// The supporting services of a route on herdr-core (spec section 13). On the
// TUI each is a shell command typed into a pane; on core none is typed: each
// is registered once with the daemon, which owns it. One owner per service:
// no pane copy runs beside the daemon's.
const (
	// ServiceCheckpoint checkpoints the orchestrator by notice.
	ServiceCheckpoint = "checkpoint"
	// ServiceCourier moves the pair's messages: on core the daemon's own
	// channel and durable outbox are the courier.
	ServiceCourier = "courier"
	// ServiceCardWatch watches the members' screens for a dialog that only a
	// person ends, and raises needs_attention; it types nothing.
	ServiceCardWatch = "card-watch"
)

// ServiceSpec is one supporting service of a core route, with the typed
// execution reference it acts on: the target agent, resolved to its current
// execution generation at each pass, never a pane.
type ServiceSpec struct {
	Repo   string `json:"repo"`
	Route  string `json:"route"`
	Folder string `json:"folder"`
	Inbox  string `json:"inbox,omitempty"`
	Name   string `json:"name"`
	// Agent is the logical agent the service acts on (the checkpoint's
	// target); empty for a service of the whole route.
	Agent string `json:"agent,omitempty"`
	// Kind and Args are the agent's harness and launch command.
	Kind string   `json:"kind,omitempty"`
	Args []string `json:"args,omitempty"`
	// IntervalMS and Limit are the checkpoint's interval and limit, as
	// INTERVAL and LIMIT on the TUI.
	IntervalMS int64 `json:"interval_ms,omitempty"`
	Limit      int64 `json:"limit,omitempty"`
}

// Key is the service's one owner key.
func (s ServiceSpec) Key() string { return s.Repo + "\x00" + s.Route + "\x00" + s.Name }

// Services runs the supporting services of core routes, as the daemon's own.
type Services interface {
	// Start registers and starts a service; a repeat is a no-op.
	Start(ctx context.Context, s ServiceSpec) error
	// Stop stops a service and forgets it; a service not running is fine.
	Stop(ctx context.Context, repo, route, name string) error
	// Running reports whether a service is registered and running.
	Running(repo, route, name string) bool
}

// The checkpoint's defaults, as up gives the TUI's pane: every 30 minutes,
// with the limit of 1,000,000 tokens.
const (
	coreCheckpointInterval = 30 * time.Minute
	coreCheckpointLimit    = 1000000
)

// coreServiceSpecs are the services of a route on core: the checkpoint when
// the orchestrator's harness has a transcript it reads, the courier when the
// route has a channel, and the card watch.
func coreServiceSpecs(q UpRequest, p *UpPlan, orchestrator compiled) []ServiceSpec {
	base := ServiceSpec{Repo: q.Repo, Route: q.Route, Folder: p.Folder, Inbox: p.Inbox}
	var out []ServiceSpec
	if orchestrator.kind == "claude" || orchestrator.kind == "codex" {
		s := base
		s.Name, s.Agent, s.Kind, s.Args = ServiceCheckpoint, orchestrator.name, orchestrator.kind, append([]string(nil), orchestrator.spec.Command[1:]...)
		s.IntervalMS, s.Limit = coreCheckpointInterval.Milliseconds(), coreCheckpointLimit
		out = append(out, s)
	}
	if p.Inbox != "" {
		s := base
		s.Name = ServiceCourier
		out = append(out, s)
	}
	s := base
	s.Name = ServiceCardWatch
	return append(out, s)
}

func coreServices(deps Deps) (Services, error) {
	if deps.Services == nil {
		return nil, runtime.Unavailable(runtime.BackendCore, "for supporting services in this daemon")
	}
	return deps.Services, nil
}

// applyCoreService registers one service with its owner, the daemon.
func applyCoreService(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	svc, err := coreServices(deps)
	if err != nil {
		return err
	}
	if d.Core == nil || d.Core.Service == nil {
		return errors.New("the step names no service")
	}
	return svc.Start(ctx, *d.Core.Service)
}

func probeCoreService(_ context.Context, deps Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	svc, err := coreServices(deps)
	if err != nil {
		return ops.Unknown, err.Error(), nil
	}
	if d.Core == nil || d.Core.Service == nil {
		return ops.Unknown, "the step names no service", nil
	}
	s := d.Core.Service
	if svc.Running(s.Repo, s.Route, s.Name) {
		return ops.Matches, "", nil
	}
	return ops.Absent, "", nil
}

// applyCoreServiceStop stops one service: a pause quiets the checkpoint, a
// stop ends every service of the route. The control row records it.
func applyCoreServiceStop(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	svc, err := coreServices(deps)
	if err != nil {
		return err
	}
	if err := svc.Stop(ctx, d.Repo, d.Route, d.Label); err != nil {
		return err
	}
	return writeRow(d, AgentRow{Name: d.Label, Status: "stopped by the daemon; nothing was typed"})
}

func probeCoreServiceStop(_ context.Context, deps Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	svc, err := coreServices(deps)
	if err != nil {
		return ops.Unknown, err.Error(), nil
	}
	if svc.Running(d.Repo, d.Route, d.Label) {
		return ops.Absent, "", nil
	}
	if _, err := os.Stat(rowPath(d)); err == nil {
		return ops.Matches, "", nil
	}
	return ops.Absent, "", nil
}

// serviceSteps are the start's service steps, journaled, by name.
func serviceSteps(op *ops.Operation) map[string]stepData {
	out := map[string]stepData{}
	for _, st := range op.Steps {
		if st.Effect != effectCoreService {
			continue
		}
		if d, err := decode(st); err == nil && d.Core != nil && d.Core.Service != nil {
			out[d.Core.Service.Name] = d
		}
	}
	return out
}

func serviceStepName(s ServiceSpec) string {
	if s.Agent != "" {
		return fmt.Sprintf("start the %s service for %s", s.Name, s.Agent)
	}
	return "start the " + s.Name + " service"
}
