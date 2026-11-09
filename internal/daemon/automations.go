package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/arafathusayn/herdsman/internal/automations"
	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/policy"
)

// automationSink is what a daemon started from the command line delivers its
// outbox to. An inspecting daemon changes nothing, so it has none.
func (a *AdminEnv) automationSink(privateHome string, logf func(string)) ops.Sink {
	if a.Inspect {
		return nil
	}
	return newAutomationSink(privateHome, os.Environ(), herdr.New(herdrDefaultSocket()), a.Stderr, logf)
}

// newAutomationSink reads each event's project when the event arrives. Without
// an approvals store it still fires, and every firing ends unapproved.
func newAutomationSink(stateDir string, environ []string, client *herdr.Client, stderr io.Writer, logf func(string)) *AutomationSink {
	store, err := automations.DefaultApprovals(environ)
	if err != nil {
		logf(err.Error())
	}
	return &AutomationSink{Log: logf, Engine: &automations.Engine{
		Approvals: store, StateDir: stateDir, Stderr: stderr,
		Source: func(project string) (automations.Config, policy.Policy, error) {
			return automations.LoadProject(project, environ)
		},
		Builtins: map[string]automations.Builtin{automations.BuiltinRoutePause: RoutePause(client, routeAgents(client))},
	}}
}

// routeAgents finds a route's agents as the panes that share a workspace with
// its orchestrator, whose name the start fixed. Matching names by prefix would
// also take a route whose name begins with this one's.
func routeAgents(client *herdr.Client) func(context.Context, automations.Event) ([]string, error) {
	return func(ctx context.Context, ev automations.Event) ([]string, error) {
		agents, err := client.AgentList(ctx)
		if err != nil {
			return nil, err
		}
		orchestrator := filepath.Base(ev.Project) + "-" + ev.Values["route"] + "-orchestrator"
		workspace := ""
		for _, agent := range agents {
			if agent.Name != nil && *agent.Name == orchestrator {
				workspace = agent.WorkspaceID
			}
		}
		if workspace == "" {
			return nil, fmt.Errorf("no agent is named %s", orchestrator)
		}
		var panes []string
		for _, agent := range agents {
			if agent.WorkspaceID == workspace {
				panes = append(panes, agent.PaneID)
			}
		}
		return panes, nil
	}
}

// AutomationSink fires automations from the operations' outbox, then passes
// each event on. An automation's own failure is in its log and never returned:
// an error here would stop delivery of every later event.
type AutomationSink struct {
	Engine *automations.Engine
	Next   ops.Sink
	Log    func(string)
}

// automationPayload is the part of an event that automations read.
type automationPayload struct {
	Project string            `json:"project"`
	Values  map[string]string `json:"values"`
}

// Deliver implements ops.Sink.
func (s *AutomationSink) Deliver(ctx context.Context, ev ops.OutboxEvent) error {
	var payload automationPayload
	err := error(nil)
	if ev.Payload != "" {
		err = json.Unmarshal([]byte(ev.Payload), &payload)
	}
	if err != nil {
		if s.Log != nil {
			s.Log(fmt.Sprintf("automations: event %s has a bad payload: %v", ev.Key, err))
		}
	} else {
		s.Engine.Fire(ctx, automations.Event{Key: ev.Key, Kind: ev.Topic, Project: payload.Project, Values: payload.Values})
	}
	if s.Next == nil {
		return nil
	}
	return s.Next.Deliver(ctx, ev)
}

// RoutePause returns the built-in that sends each of a route's agents the
// interrupt key, which the agent acts on at its next turn boundary. An agent
// that cannot be reached does not stop the others; reaching none is a failure,
// so that the log never claims a pause that did not happen.
func RoutePause(client *herdr.Client, agents func(context.Context, automations.Event) ([]string, error)) automations.Builtin {
	return func(ctx context.Context, ev automations.Event) error {
		route := ev.Values["route"]
		if route == "" {
			return errors.New("route pause: the event names no route")
		}
		names, err := agents(ctx, ev)
		if err != nil {
			return fmt.Errorf("route pause: %w", err)
		}
		reached := 0
		var failures []error
		for _, name := range names {
			record, err := client.AgentGet(ctx, name)
			if err == nil {
				err = client.Call(ctx, "pane.send_input", map[string]any{"pane_id": record.PaneID, "text": "", "keys": []string{"esc"}}, nil)
			}
			if err != nil {
				failures = append(failures, fmt.Errorf("%s: %w", name, err))
				continue
			}
			reached++
		}
		if reached == 0 {
			return fmt.Errorf("route pause: no agent of %s was reached: %w", route, errors.Join(failures...))
		}
		return nil
	}
}
