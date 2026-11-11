package daemon

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/launch"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

// coreExec is the part of a core client the doctor runs commands through:
// machine.exec, so what it reads is what core's own process can read.
type coreExec interface {
	Exec(ctx context.Context, req hc.ExecRequest) (hc.ExecResult, error)
}

// doctor checks what a launch on herdr-core needs, for one route when one is
// named (spec section 16): the endpoint, its version against the tested one
// and its identity, the app's verified binding and when it was verified,
// where a launch's environment comes from and the agent markers in the
// daemon's and in core's own environment, the repository and worktree roots
// as core reads them, and the inventory rule in force with the foreign agents
// and the terminals at fault. It changes nothing.
func (c *coreRuntime) doctor(ctx context.Context, repo, name string, check func(name, state, detail string)) (reachable bool, reason string) {
	endpoint := c.cfg.Socket
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	client, err := hc.Dial(cctx, endpoint, hc.Options{})
	if err != nil {
		check("core-endpoint", "fail", err.Error())
		check("core-version", "skip", compatibleVersions())
		return false, "herdr-core does not answer: " + err.Error()
	}
	defer client.Close()
	info := client.Core()
	check("core-endpoint", "ok", fmt.Sprintf("%s (core %s)", endpoint, info.ID))
	hello := client.Hello()
	if hello.Version == hc.TestedVersion {
		check("core-version", "ok", fmt.Sprintf("herdr-core %s at protocol generation %d; %s", hello.Version, hello.Generation, compatibleVersions()))
	} else {
		check("core-version", "warn", fmt.Sprintf("herdr-core %s at protocol generation %d is not the tested version; %s", hello.Version, hello.Generation, compatibleVersions()))
	}
	if r, _ := c.reconciler(endpoint); r != nil {
		if frozen, why := r.Frozen(); frozen {
			check("core-identity", "fail", why)
		}
	}
	var execs []runtime.Execution
	if repo != "" && name != "" {
		execs, _ = c.records.List(ctx, repo, name)
		for _, e := range execs {
			if e.Backend == runtime.BackendCore && e.EndpointID != "" && e.EndpointID != info.ID {
				check("core-identity", "fail", fmt.Sprintf("%s: route %s was started on core %s and %s now leads to core %s; nothing is adopted or started until a person settles it",
					core.CodeEndpointMismatch, name, e.EndpointID, endpoint, info.ID))
				break
			}
		}
	}
	c.doctorBinding(info.ID, check)
	c.doctorEnvironment(cctx, client, check)
	c.doctorRoots(cctx, client, repo, execs, check)
	if repo != "" && name != "" {
		switch inv, err := c.routeInventory(ctx, repo, name); {
		case err != nil:
			check("inventory", "warn", err.Error())
		case inv.OK():
			check("inventory", "ok", inventoryText(inv))
		default:
			check("inventory", "fail", inventoryText(inv))
		}
	}
	c.doctorNotices(ctx, repo, name, check)
	return true, ""
}

// compatibleVersions names the combination this release was tested with.
func compatibleVersions() string {
	return fmt.Sprintf("tested: herdr-core %s at protocol generation %d, herdr-app %s (oldest supported %s)",
		hc.TestedVersion, hc.Generation, herdrapp.TestedVersion, herdrapp.MinVersion)
}

// doctorBinding says whether the app-to-core binding was verified, when, and
// with which app.
func (c *coreRuntime) doctorBinding(coreID string, check func(name, state, detail string)) {
	b, ok, err := c.bindings.Load(c.app.Home, coreID)
	switch {
	case err != nil:
		check("app-binding", "fail", err.Error())
	case !ok:
		check("app-binding", "warn", fmt.Sprintf("%s: the app at %s was never verified to show core %s; the first launch waits until a person opens it",
			core.CodeWaitingVisibility, c.app.Home, coreID))
	default:
		app := ""
		if b.AppVersion != "" {
			app = " (herdr-app " + b.AppVersion + ")"
		}
		check("app-binding", "ok", fmt.Sprintf("the app at %s%s shows core %s; verified %s", c.app.Home, app, coreID, b.VerifiedAt.UTC().Format(time.RFC3339)))
	}
}

// agentMarkers lists the agent markers of an environment, sorted and unique.
func agentMarkers(environ []string) []string {
	var markers []string
	for _, kv := range environ {
		if k, _, _ := strings.Cut(kv, "="); launch.AgentMarker(k) && !slices.Contains(markers, k) {
			markers = append(markers, k)
		}
	}
	sort.Strings(markers)
	return markers
}

// doctorEnvironment says where a launch's environment comes from and names
// the agent markers in the daemon's environment and in core's own. No launch
// passes either: the wrapper builds the harness environment from empty plus
// an allowlist (spec section 13). A marker in core's environment means core
// was started under an agent, which is worth a person's look.
func (c *coreRuntime) doctorEnvironment(ctx context.Context, x coreExec, check func(name, state, detail string)) {
	environ := c.cfg.Environ
	if environ == nil {
		environ = os.Environ()
	}
	source := fmt.Sprintf("a core launch takes its login values (%s) from the daemon's environment and its secrets from the launch policy, through the authenticated wrapper",
		strings.Join(launch.LoginKeys, ", "))
	if markers := agentMarkers(environ); len(markers) > 0 {
		check("launch-environment", "ok", fmt.Sprintf("%s; the daemon's environment holds %s, and no core launch passes them", source, strings.Join(markers, ", ")))
	} else {
		check("launch-environment", "ok", source+"; nothing in it names another agent")
	}
	res, err := x.Exec(ctx, hc.ExecRequest{Argv: []string{"/usr/bin/env"}, TimeoutSeconds: 10})
	if err != nil || res.Code == nil || *res.Code != 0 {
		why := "it exited with an error"
		if err != nil {
			why = err.Error()
		}
		check("core-environment", "warn", "core's own environment could not be read through machine.exec: "+why)
		return
	}
	if markers := agentMarkers(strings.Split(string(res.Stdout), "\n")); len(markers) > 0 {
		check("core-environment", "warn", fmt.Sprintf("core's own environment holds %s: core was started under an agent. No managed harness receives them, but a terminal a person opens through core inherits them; restart core from a plain shell",
			strings.Join(markers, ", ")))
	} else {
		check("core-environment", "ok", "core's own environment names no agent")
	}
}

// doctorRoots checks, through core, that core can read the repository and the
// folders the route's executions run in. macOS privacy grants belong to Herdr
// Core, not to the app or the terminal Herdsman runs in, so a repository
// under Documents, Desktop, Downloads or an external volume can be readable
// here and not to core.
func (c *coreRuntime) doctorRoots(ctx context.Context, x coreExec, repo string, execs []runtime.Execution, check func(name, state, detail string)) {
	var roots []string
	if repo != "" {
		roots = append(roots, repo)
	}
	for _, e := range execs {
		if e.Backend == runtime.BackendCore && e.Cwd != "" && !slices.Contains(roots, e.Cwd) {
			roots = append(roots, e.Cwd)
		}
	}
	for i, root := range roots {
		name := "core-repository"
		if i > 0 {
			name = "core-worktree"
		}
		if err := core.CheckRoot(ctx, x, root); err != nil {
			check(name, "fail", err.Error())
		} else {
			check(name, "ok", "core reads "+root)
		}
	}
}

// inventoryText names the rule, the foreign agents the core holds, and the
// terminals at fault.
func inventoryText(inv core.Inventory) string {
	foreign := fmt.Sprintf("%d foreign agents on the core", inv.Foreign)
	if inv.Rule == core.RuleRoute {
		foreign += ", outside the route's inventory under the default profile"
	} else {
		foreign += ", each a fault under the restricted profile"
	}
	return inv.FaultText() + "; " + foreign
}

// doctorNotices says how each qualified harness is woken on core before a
// route starts and which of its steps need a person, from the adapter, in
// the words up uses: Claude Code through its own background wait, Codex by
// a person (Codex's queued wake is an open owner decision, so every notice
// for a Codex execution waits for a typed line). It also names the worker
// socket and the wait budget in use.
func (c *coreRuntime) doctorNotices(ctx context.Context, repo, name string, check func(name, state, detail string)) {
	if svc := c.d.noticeSvc(); svc != nil {
		check("notice-channel", "ok", fmt.Sprintf("agents reach notices at %s; %d of %d waits held", NoticeSocketPath(c.d.cfg.PrivateHome), svc.Waits(), svc.MaxWaits()))
	}
	for _, kind := range core.Kinds() {
		a, err := core.AdapterFor(kind)
		if err != nil {
			continue
		}
		state := "ok"
		if !a.Unattended() {
			state = "warn"
		}
		check("harness-"+kind, state, a.PersonSummary())
	}
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
