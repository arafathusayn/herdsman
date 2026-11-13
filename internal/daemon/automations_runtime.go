package daemon

import (
	"context"
	"fmt"
	"strconv"

	"github.com/arafathusayn/herdsman/internal/automations"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// runtimeAutomationQueue bounds the runtime events that wait for the
// automation worker. A full queue drops the event and says so in the log:
// publishing a runtime event never waits for an automation.
const runtimeAutomationQueue = 64

// runtimeFirer is the part of an automation sink that runtime events reach.
// Runtime events are not in the operations' outbox, so they are delivered at
// most once: an event that a restart or a full queue loses fires nothing.
type runtimeFirer interface {
	FireRuntime(ctx context.Context, ev automations.Event)
}

// FireRuntime fires the automations of one runtime event.
func (s *AutomationSink) FireRuntime(ctx context.Context, ev automations.Event) {
	if s.Engine != nil {
		s.Engine.Fire(ctx, ev)
	}
}

// RuntimeAutomationKind is the automation event kind of a runtime event:
// `runtime.` and the event's kind, as in runtime.needs_attention.
func RuntimeAutomationKind(kind string) string { return "runtime." + kind }

// runtimeAutomationEvent is what an automation reads of a runtime event.
func runtimeAutomationEvent(ev *daemonv1.RuntimeEvent) automations.Event {
	values := map[string]string{}
	for k, v := range map[string]string{"route": ev.GetRoute(), "execution": ev.GetExecutionId(), "reason": ev.GetReason(),
		"notice": ev.GetNoticeId(), "outcome": ev.GetOutcome(), "detail": ev.GetDetail()} {
		if v != "" {
			values[k] = v
		}
	}
	key := fmt.Sprintf("runtime:%s:%s:%s:%s", ev.GetKind(), ev.GetExecutionId(), ev.GetNoticeId(), strconv.FormatInt(ev.GetTime(), 10))
	return automations.Event{Key: key, Kind: RuntimeAutomationKind(ev.GetKind()), Project: ev.GetRepo(), Values: values}
}

// fireRuntime hands a runtime event to the automation worker, which starts
// with the first event and ends with the daemon.
func (d *Daemon) fireRuntime(ev *daemonv1.RuntimeEvent) {
	firer, ok := d.cfg.Sink.(runtimeFirer)
	if !ok {
		return
	}
	d.autoOnce.Do(func() {
		d.autoQ = make(chan automations.Event, runtimeAutomationQueue)
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-d.done
			cancel()
		}()
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case e := <-d.autoQ:
					firer.FireRuntime(ctx, e)
				}
			}
		}()
	})
	select {
	case d.autoQ <- runtimeAutomationEvent(ev):
	default:
		d.logf("automations: runtime %s of %s dropped: %d events wait already", ev.GetKind(), ev.GetExecutionId(), runtimeAutomationQueue)
	}
}

// pauseOnCore makes the sink's route pause act through the daemon for a route
// on herdr-core: the route's own pause verb, which reaches each agent through
// the provider's rules and the notices, and types nothing in a pane. A route
// on the Herdr TUI keeps the interrupt key the built-in sends.
func (s *AutomationSink) pauseOnCore(onCore func(project, route string) bool, pause func(ctx context.Context, project, route string) error) {
	if s.Engine == nil || s.Engine.Builtins == nil {
		return
	}
	tui, ok := s.Engine.Builtins[automations.BuiltinRoutePause]
	if !ok {
		return
	}
	s.Engine.Builtins[automations.BuiltinRoutePause] = routePauseOnCore(tui, onCore, pause)
}

// routePauseOnCore is the route pause built-in for both backends.
func routePauseOnCore(tui automations.Builtin, onCore func(project, route string) bool, pause func(ctx context.Context, project, route string) error) automations.Builtin {
	return func(ctx context.Context, ev automations.Event) error {
		name := ev.Values["route"]
		if name == "" || !onCore(ev.Project, name) {
			return tui(ctx, ev)
		}
		if err := pause(ctx, ev.Project, name); err != nil {
			return fmt.Errorf("route pause: %s on herdr-core: %w", name, err)
		}
		return nil
	}
}

// pauseRoute runs the route's pause verb, as `herdsman pause` does.
func (d *Daemon) pauseRoute(ctx context.Context, project, name string) error {
	_, err := (&routeService{d: d}).Pause(ctx, &daemonv1.RouteServicePauseRequest{Route: &corev1.RouteId{Value: name}, Repo: project})
	return err
}
