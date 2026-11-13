package daemon

import (
	"context"
	"time"

	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/coreconfig"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// executionEndpoint also resolves standalone tasks, which have no route journal.
func (c *coreRuntime) executionEndpoint(e runtime.Execution) coreconfig.Endpoint {
	if e.App != nil && e.App.Socket != "" {
		return coreconfig.Endpoint{Socket: e.App.Socket, Binary: e.App.Binary}
	}
	if rc, ok := c.routeOf(e.Repo, e.Route); ok {
		return rc.endpoint
	}
	if rc, err := c.d.readRuntimeConfig(e.Repo, ""); err == nil {
		return rc.Settings.Endpoint()
	}
	return coreconfig.Endpoint{Socket: c.cfg.Socket}
}

func (c *coreRuntime) ensureView(ctx context.Context, e runtime.Execution, show bool) error {
	rc, err := c.d.readRuntimeConfig(e.Repo, "")
	if err != nil {
		return err
	}
	if rc.Settings.TUIView.Value != "on" {
		return nil
	}
	ep := c.executionEndpoint(e)
	_, err = c.views.Ensure(ctx, e, rc.Settings.TUIViewSocket.Value, ep.Socket, ep.Binary, show)
	return err
}

// Optional views have their own loop and failures. Neither their availability
// nor their metadata can authorize core dispatch or native-app visibility.
func (c *coreRuntime) runViews(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	faults := map[string]string{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		rows, err := c.records.List(ctx, "", "")
		if err != nil {
			continue
		}
		for _, e := range rows {
			if e.Backend != runtime.BackendCore || e.Desired != runtime.DesiredRunning || e.Evidence.PID == 0 {
				continue
			}
			step, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := c.ensureView(step, e, false)
			cancel()
			message := ""
			if err != nil {
				message = err.Error()
			}
			if message != faults[e.ID] {
				faults[e.ID] = message
				if message != "" {
					c.d.publishRuntime(&daemonv1.RuntimeEvent{Kind: "needs_attention", ExecutionId: e.ID, Reason: "tui_view", Detail: message})
				}
			}
		}
	}
}

// viewExecution finds the current persisted execution, never an RPC-supplied
// address or a pane label.
func (c *coreRuntime) viewExecution(ctx context.Context, id string) (runtime.Execution, bool) {
	rows, err := c.records.List(ctx, "", "")
	if err != nil {
		return runtime.Execution{}, false
	}
	for _, e := range rows {
		if e.ID == id {
			return e, true
		}
	}
	return runtime.Execution{}, false
}
