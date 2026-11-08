package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/arafathusayn/herdsman/internal/automations"
	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/ops"
)

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
func RoutePause(client *herdr.Client, agents func(route string) ([]string, error)) automations.Builtin {
	return func(ctx context.Context, ev automations.Event) error {
		route := ev.Values["route"]
		if route == "" {
			return errors.New("route pause: the event names no route")
		}
		names, err := agents(route)
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
