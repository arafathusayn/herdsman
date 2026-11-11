package daemon

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/launch"
	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

// doctor checks what a launch on herdr-core needs, for one route when one is
// named: the endpoint and its identity, the app's verified binding, the
// markers no launch passes, the repository as core reads it, and the
// inventory rule in force with the terminals at fault. It changes nothing.
func (c *coreRuntime) doctor(ctx context.Context, repo, name string, check func(name, state, detail string)) (reachable bool, reason string) {
	endpoint := c.cfg.Socket
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	client, err := hc.Dial(cctx, endpoint, hc.Options{})
	if err != nil {
		check("core-endpoint", "fail", err.Error())
		return false, "herdr-core does not answer: " + err.Error()
	}
	defer client.Close()
	info := client.Core()
	check("core-endpoint", "ok", fmt.Sprintf("%s (core %s)", endpoint, info.ID))
	if r, _ := c.reconciler(endpoint); r != nil {
		if frozen, why := r.Frozen(); frozen {
			check("core-identity", "fail", why)
		}
	}
	if repo != "" && name != "" {
		execs, _ := c.records.List(ctx, repo, name)
		for _, e := range execs {
			if e.Backend == runtime.BackendCore && e.EndpointID != "" && e.EndpointID != info.ID {
				check("core-identity", "fail", fmt.Sprintf("%s: route %s was started on core %s and %s now leads to core %s; nothing is adopted or started until a person settles it",
					core.CodeEndpointMismatch, name, e.EndpointID, endpoint, info.ID))
				break
			}
		}
	}
	if _, ok, err := c.bindings.Load(c.app.Home, info.ID); err != nil {
		check("app-binding", "fail", err.Error())
	} else if !ok {
		check("app-binding", "warn", fmt.Sprintf("%s: the app at %s was never verified to show core %s; the first launch waits until a person opens it",
			core.CodeWaitingVisibility, c.app.Home, info.ID))
	} else {
		check("app-binding", "ok", fmt.Sprintf("the app at %s shows core %s", c.app.Home, info.ID))
	}
	environ := c.cfg.Environ
	if environ == nil {
		environ = os.Environ()
	}
	var markers []string
	for _, kv := range environ {
		if k, _, _ := strings.Cut(kv, "="); launch.AgentMarker(k) {
			markers = append(markers, k)
		}
	}
	if len(markers) > 0 {
		check("launch-environment", "ok", fmt.Sprintf("the daemon's environment holds %s; no core launch passes them", strings.Join(markers, ", ")))
	} else {
		check("launch-environment", "ok", "a core launch takes its login values from the daemon and nothing that names another agent")
	}
	if repo != "" {
		if err := core.CheckRoot(cctx, client, repo); err != nil {
			check("core-repository", "fail", err.Error())
		} else {
			check("core-repository", "ok", "core reads "+repo)
		}
	}
	if repo != "" && name != "" {
		switch inv, err := c.routeInventory(ctx, repo, name); {
		case err != nil:
			check("inventory", "warn", err.Error())
		case inv.OK():
			check("inventory", "ok", inv.FaultText())
		default:
			check("inventory", "fail", inv.FaultText())
		}
	}
	c.doctorNotices(ctx, repo, name, check)
	return true, ""
}

// doctorNotices says how each qualified harness is woken on core before a
// route starts: Claude Code through its own background wait, Codex by a
// person (Codex's queued wake is an open owner decision, so every notice
// for a Codex execution waits for a typed line). It also names the worker
// socket and the wait budget in use.
func (c *coreRuntime) doctorNotices(ctx context.Context, repo, name string, check func(name, state, detail string)) {
	if svc := c.d.noticeSvc(); svc != nil {
		check("notice-channel", "ok", fmt.Sprintf("agents reach notices at %s; %d of %d waits held", NoticeSocketPath(c.d.cfg.PrivateHome), svc.Waits(), svc.MaxWaits()))
	}
	check("harness-claude", "ok", "Claude Code steps run unattended: each holds a background `herdsman notice wait` and wakes when it returns")
	check("harness-codex", "warn", fmt.Sprintf("Codex steps need a person on herdr-core: each notice for a Codex execution raises needs_attention (%s) until a person types %q in its pane; a pause never interrupts Codex",
		notice.AttentionNoticeWaiting, notice.AttendedLine))
	if repo == "" || name == "" {
		return
	}
	execs, _ := c.records.List(ctx, repo, name)
	var attended []string
	for _, e := range execs {
		if a, err := core.AdapterFor(e.HarnessKind); err == nil && e.Desired == runtime.DesiredRunning && !a.Unattended() {
			attended = append(attended, e.Agent)
		}
	}
	if len(attended) > 0 {
		check("route-attendance", "warn", fmt.Sprintf("route %s has agents a person must wake: %s", name, strings.Join(attended, ", ")))
	}
}
