package daemon

import (
	"fmt"
	"path/filepath"

	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	"github.com/arafathusayn/herdsman/internal/runtime/coreconfig"
	"github.com/arafathusayn/herdsman/sdk/config"
)

// A route on herdr-core resolves its endpoint once, from the config in use,
// and keeps it: the launch steps journal the canonical connection, and every
// execution record carries the id of the core it reached. The config's
// timeouts and app are read for each launch. Nothing here reads HC_SOCKET,
// HC_SESSION or HC_TERMINAL_ID: a daemon started under an agent must not
// take that agent's core as a route's authority.

// runtimeConfig is the [runtime] section of the config a run uses, read and
// checked: the backend, the file that set it, the experiment's marker and the
// resolved core and app settings.
type runtimeConfig struct {
	Backend, Path string
	Experimental  bool
	Settings      coreconfig.Settings
}

// readRuntimeConfig loads the config of a run in repo (configPath first) and
// resolves its runtime keys against this daemon's defaults. An unknown key, a
// duration that does not parse, an endpoint that contradicts itself and an
// enabled TUI view without an explicit endpoint stop here.
func (d *Daemon) readRuntimeConfig(repo, configPath string) (runtimeConfig, error) {
	in := coreconfig.Input{Getenv: d.cfg.Core.Getenv}
	if c := d.coreRT(); c != nil {
		in.DefaultSocket, in.DefaultAppHome = c.cfg.Socket, c.cfg.AppHome
	}
	if repo == "" && configPath == "" {
		s, err := coreconfig.Resolve(in)
		return runtimeConfig{Settings: s}, err
	}
	if configPath != "" && !filepath.IsAbs(configPath) && repo != "" {
		configPath = filepath.Join(repo, configPath)
	}
	result, err := config.Load(config.Options{ConfigPath: configPath, RepoRoot: repo})
	if err != nil {
		return runtimeConfig{}, err
	}
	rc := runtimeConfig{Backend: result.Config.GetRuntime().GetBackend(), Experimental: result.Config.GetRuntime().GetCore().GetExperimental()}
	if rc.Backend != "" {
		rc.Path = result.ProjectPath
		if configPath != "" {
			rc.Path = configPath
		}
		if rc.Path == "" {
			rc.Path = result.UserPath
		}
	}
	in.Runtime, in.Sources = result.Config.GetRuntime(), result.Sources
	in.ProjectPath, in.UserPath = result.ProjectPath, result.UserPath
	if configPath != "" {
		in.ProjectPath = configPath
	}
	if rc.Settings, err = coreconfig.Resolve(in); err != nil {
		return runtimeConfig{}, &runtime.Error{Code: runtime.CodeInvalidRuntimeConfig, Msg: err.Error()}
	}
	return rc, nil
}

// keptEndpoint is the endpoint a run of a route on core uses: the one its
// latest start persisted, for a route that exists, else the config's. A
// config that names another endpoint for an existing route is refused with
// backend_migration_required: the route never moves to another core.
func keptEndpoint(rc runtimeConfig, latest *ops.Operation, persisted string) (coreconfig.Endpoint, error) {
	want := rc.Settings.Endpoint()
	if latest == nil || persisted != runtime.BackendCore {
		return want, nil
	}
	kept, ok := route.CoreEndpointOf(latest)
	if !ok {
		return want, nil
	}
	if rc.Settings.Explicit && filepath.Clean(kept.Socket) != filepath.Clean(want.Socket) {
		return kept, &runtime.Error{Code: runtime.CodeBackendMigrationRequired, Msg: fmt.Sprintf(
			"the route runs on the core at %s, persisted with its start; the config names %s (%s). Remove socket and session from [runtime.core], or stop the route and start it again under another name to run it there",
			kept.Socket, want.Socket, rc.Settings.Socket.From)}
	}
	if kept.Binary == "" {
		kept.Binary = want.Binary
	}
	return kept, nil
}

// timeouts are a launch's timeouts: what the config sets, else what the
// daemon was configured with, else the defaults.
func (c *coreRuntime) timeouts(s coreconfig.Settings) core.Timeouts {
	t := c.cfg.Timeouts
	if s.ConnectSet || t.Connect == 0 {
		t.Connect = s.Connect
	}
	if s.ReadySet || t.Ready == 0 {
		t.Ready = s.Ready
	}
	if s.VisibilitySet || t.Visibility == 0 {
		t.Visibility = s.Visibility
	}
	return t
}

// launchFor is the core half of a run with these settings on endpoint.
func (c *coreRuntime) launchFor(s coreconfig.Settings, endpoint coreconfig.Endpoint) *route.CoreLaunch {
	l := c.launch()
	l.Socket, l.Endpoint, l.App, l.Timeouts = endpoint.Socket, endpoint, s.App(), c.timeouts(s)
	return l
}

// appOf is the app of an [runtime.app] choice: its program over the daemon's
// runner and its home, or the daemon's own for what it leaves empty.
func (c *coreRuntime) appOf(a coreconfig.App) *herdrapp.App {
	if a == (coreconfig.App{}) {
		return c.app
	}
	home := a.Home
	if home == "" {
		home = c.cfg.AppHome
	}
	runner := c.cfg.App
	if a.Binary != "" {
		switch r := runner.(type) {
		case herdrapp.ExecRunner:
			r.Binary = a.Binary
			runner = r
		case interface {
			WithBinary(string) herdrapp.Runner
		}:
			runner = r.WithBinary(a.Binary)
		}
	}
	return herdrapp.New(runner, home)
}

// routeCore is what a route on core persisted with its start: its endpoint
// and its app.
type routeCore struct {
	endpoint coreconfig.Endpoint
	app      coreconfig.App
}

// remember records what a route's start persisted, as it is journaled.
func (c *coreRuntime) remember(repo, name string, endpoint coreconfig.Endpoint, app coreconfig.App) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.routes == nil {
		c.routes = map[string]routeCore{}
	}
	c.routes[repo+"\x00"+name] = routeCore{endpoint: endpoint, app: app}
}

// routeOf is what a route on core persisted: from memory, else from its
// latest start in the journal.
func (c *coreRuntime) routeOf(repo, name string) (routeCore, bool) {
	if repo == "" || name == "" {
		return routeCore{}, false
	}
	c.mu.Lock()
	got, ok := c.routes[repo+"\x00"+name]
	c.mu.Unlock()
	if ok {
		return got, true
	}
	starts, err := (&routeService{d: c.d}).starts(route.UpKey(repo, name))
	if err != nil || len(starts) == 0 {
		return routeCore{}, false
	}
	latest := starts[len(starts)-1]
	ep, ok := route.CoreEndpointOf(latest)
	if !ok {
		return routeCore{}, false
	}
	app := route.CoreAppOf(latest)
	c.remember(repo, name, ep, app)
	return routeCore{endpoint: ep, app: app}, true
}

// endpointOf is the socket of the core a route runs on: its persisted
// endpoint, else this daemon's default.
func (c *coreRuntime) endpointOf(repo, name string) string {
	if rc, ok := c.routeOf(repo, name); ok && rc.endpoint.Socket != "" {
		return rc.endpoint.Socket
	}
	return c.cfg.Socket
}

// coreApp is the app a herdr-core step of a route shows its agents in; nil
// before the core backend is ready, which leaves the provider's own.
func (d *Daemon) coreApp(a coreconfig.App) *herdrapp.App {
	if c := d.coreRT(); c != nil {
		return c.appOf(a)
	}
	return nil
}

// routeProvider is the core provider of a route: on the endpoint and with the
// app its start persisted.
func (c *coreRuntime) routeProvider(repo, name string) *core.Provider {
	p := c.provider(c.endpointOf(repo, name))
	if rc, ok := c.routeOf(repo, name); ok && rc.app != (coreconfig.App{}) {
		p.App = c.appOf(rc.app)
	}
	return p
}

// appFor is the app an execution's route shows its agents in, as its start
// journaled it; nil for the daemon's own.
func (c *coreRuntime) appFor(e runtime.Execution) *herdrapp.App {
	if e.Route == "" {
		// A task agent has no route start: its repository's config chose
		// its app when it launched.
		if rc, err := c.d.readRuntimeConfig(e.Repo, ""); err == nil && rc.Settings.App() != (coreconfig.App{}) {
			return c.appOf(rc.Settings.App())
		}
		return nil
	}
	rc, ok := c.routeOf(e.Repo, e.Route)
	if !ok || rc.app == (coreconfig.App{}) {
		return nil
	}
	return c.appOf(rc.app)
}
