package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/launch"
	"github.com/arafathusayn/herdsman/internal/profile"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	"github.com/arafathusayn/herdsman/internal/runtime/legacytui"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// CoreConfig configures the herdr-core backend of a daemon. The zero value is
// the user's own: core's default session, the installed app and the system's
// process table. Tests replace every part.
type CoreConfig struct {
	// Socket is core's socket (default: the one hc resolves).
	Socket string
	// App runs the app command (default: the installed herdr-app).
	App herdrapp.Runner
	// AppHome is the app's state directory (default: the app's own).
	AppHome string
	// Procs is the process table (default: the system's).
	Procs core.ProcessTable
	// Timeouts bound the launch steps that wait (zero: the defaults).
	Timeouts core.Timeouts
	// Environ is where the login values of a launch come from (default:
	// the daemon's environment).
	Environ []string
	// NoReconcile keeps the reconcilers off, for tests that drive passes.
	NoReconcile bool
	// Peer reads a launch connection's peer pid (default: the kernel's
	// record). Tests whose wrapper runs in process replace it.
	Peer func(net.Conn) (int, error)
	// Person checks that a typed submit's caller is a person's terminal
	// (default: the check up's pair approval makes). Tests replace it.
	Person func(route.Caller) error
	// ServicePoll is how often a core route's services look again
	// (default 30 s).
	ServicePoll time.Duration
}

// coreRuntime is the daemon's half of herdr-core: the launch socket, the
// records over the account store and one reconciler per core endpoint.
type coreRuntime struct {
	d *Daemon
	// ctx lives as long as the daemon serves; the reconcilers run under it.
	ctx      context.Context
	cfg      CoreConfig
	owner    string
	records  core.Records
	app      *herdrapp.App
	bindings core.Bindings

	mu          sync.Mutex
	reconcilers map[string]*core.Reconciler
	// registrar is the launch socket, opened by the first core step: a
	// daemon whose routes never use core listens on nothing for it.
	registrar *core.Registrar
	// submits carries out a person's typed submits.
	submits *core.Submitter
	// services owns the supporting services of core routes.
	services *coreServices
}

// launchSocket is where the core wrappers find this daemon.
func (d *Daemon) launchSocket() string { return filepath.Join(d.runDir(), "launch.sock") }

// installationID is this installation's opaque id, the owner label of every
// core terminal it starts. It is made once and kept.
func (d *Daemon) installationID() (string, error) {
	path := filepath.Join(d.daemonDir(), "installation-id")
	if b, err := readSmall(path, 128); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw[:])
	if err := writeDurable(path, []byte(id+"\n"), 0o600); err != nil {
		return "", err
	}
	return id, nil
}

// startCore readies the herdr-core backend and watches the cores that routes
// already run on. The launch socket opens with the first core step.
func (d *Daemon) startCore(ctx context.Context) error {
	owner, err := d.installationID()
	if err != nil {
		return err
	}
	cfg := d.cfg.Core
	if cfg.Procs == nil {
		cfg.Procs = core.SystemProcs{}
	}
	if cfg.Socket == "" {
		if cfg.Socket, err = hc.SocketPath(""); err != nil {
			return err
		}
	}
	if cfg.App == nil {
		cfg.App = herdrapp.ExecRunner{}
	}
	if cfg.AppHome == "" {
		if cfg.AppHome, err = herdrapp.DefaultHome(); err != nil {
			return err
		}
	}
	c := &coreRuntime{d: d, ctx: ctx, cfg: cfg, owner: owner, records: storeRecords{d}, app: herdrapp.New(cfg.App, cfg.AppHome),
		bindings: &core.FileBindings{Path: filepath.Join(d.daemonDir(), "core-bindings.json")}, reconcilers: map[string]*core.Reconciler{}}
	c.services = &coreServices{c: c, poll: cfg.ServicePoll, running: map[string]*coreService{}}
	d.mu.Lock()
	d.core = c
	d.mu.Unlock()
	// The services recorded before a restart are the daemon's again.
	c.services.resume()
	// Routes already on core are watched from the start.
	if execs, err := c.records.List(ctx, "", ""); err == nil {
		for _, e := range execs {
			if e.Backend == runtime.BackendCore && e.EndpointID != "" {
				c.watch(ctx, c.cfg.Socket)
				break
			}
		}
	}
	return nil
}

// stopCore closes the launch socket.
func (d *Daemon) stopCore() {
	d.mu.Lock()
	c, cancel := d.core, d.coreCancel
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if c != nil {
		c.mu.Lock()
		reg := c.registrar
		c.mu.Unlock()
		if reg != nil {
			_ = reg.Close()
		}
	}
}

// launchRegistrar opens the launch socket once and serves it until the
// daemon stops. A wrapper that dials before it opens retries its dial.
func (c *coreRuntime) launchRegistrar() (*core.Registrar, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.registrar != nil {
		return c.registrar, nil
	}
	reg, err := core.NewRegistrar(c.d.launchSocket(), c.cfg.Procs)
	if err != nil {
		return nil, fmt.Errorf("the launch socket: %w", err)
	}
	if c.cfg.Peer != nil {
		reg.Peer = c.cfg.Peer
	}
	c.registrar = reg
	c.d.bg.Add(1)
	go func() {
		defer c.d.bg.Done()
		reg.Serve(c.ctx)
	}()
	return reg, nil
}

// coreRT is the daemon's core runtime, nil when it has none.
func (d *Daemon) coreRT() *coreRuntime {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.core
}

// provider is the core provider on endpoint.
func (c *coreRuntime) provider(endpoint string) *core.Provider {
	if endpoint == "" {
		endpoint = c.cfg.Socket
	}
	// The provider and reconcilers keep the wall clock, not the daemon's:
	// they compare their times with core's status and record times.
	p := &core.Provider{Socket: endpoint, Records: c.records, Procs: c.cfg.Procs, App: c.app, Bindings: c.bindings, Notices: c.d.noticeSvc(), Submits: c.submitter()}
	if reg, err := c.launchRegistrar(); err == nil {
		p.Registrar = reg // without it the register step says the socket is missing
	} else {
		c.d.logf("herdr-core: %v", err)
	}
	c.mu.Lock()
	p.Reconciler = c.reconcilers[endpoint]
	c.mu.Unlock()
	return p
}

// reconciler is the reconciler of an endpoint, made once.
func (c *coreRuntime) reconciler(endpoint string) (*core.Reconciler, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, ok := c.reconcilers[endpoint]; ok {
		return r, false
	}
	r := &core.Reconciler{Socket: endpoint, Records: c.records, Procs: c.cfg.Procs, App: c.app, Bindings: c.bindings,
		HarnessNames: route.HarnessNames, Restricted: c.restricted, Emit: c.emit}
	c.reconcilers[endpoint] = r
	return r, true
}

// watch runs the reconciler of an endpoint until ctx ends, once.
func (c *coreRuntime) watch(ctx context.Context, endpoint string) *core.Reconciler {
	r, fresh := c.reconciler(endpoint)
	if !fresh || c.cfg.NoReconcile {
		return r
	}
	c.d.bg.Add(1)
	go func() {
		defer c.d.bg.Done()
		// Core may be down or restarting: the reconciler follows it again
		// after a pause, until the daemon stops or core's identity changed,
		// which only a person settles.
		for {
			err := r.Run(ctx)
			if ctx.Err() != nil {
				return
			}
			c.d.logf("herdr-core reconciler on %s: %v", endpoint, err)
			if frozen, _ := r.Frozen(); frozen {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()
	return r
}

// restricted reports whether a route's repository runs the restricted profile.
func (c *coreRuntime) restricted(repo, _ string) bool {
	governing, _, err := repoPolicy(repo)
	if err != nil {
		return true // an unreadable policy fails closed: the stricter rule
	}
	return profile.ShapeFor(governing) == profile.ShapeOwnClone
}

// emit publishes what a reconciler found on the runtime topic, under the
// RuntimeEvent contract. A lost or finished execution returns its notices,
// and every finding is a gate change the notice waits look at again.
func (c *coreRuntime) emit(ev core.Event) {
	c.d.publishRuntime(&daemonv1.RuntimeEvent{Kind: ev.Kind, Repo: ev.Repo, Route: ev.Route, ExecutionId: ev.Execution, Detail: ev.Detail, Time: ev.At.UnixMilli()})
	svc := c.d.noticeSvc()
	if svc == nil {
		return
	}
	switch ev.Kind {
	case core.EventExecutionLost, core.EventExecutionFinished:
		if ev.Execution != "" {
			if _, err := svc.ReturnExecution(ev.Execution, ev.Kind); err != nil {
				c.d.logf("notices: return %s: %v", ev.Execution, err)
			}
		}
	}
	svc.Changed()
}

// launch is the core half of an up request.
func (c *coreRuntime) launch() *route.CoreLaunch {
	environ := c.cfg.Environ
	if environ == nil {
		environ = os.Environ()
	}
	login := map[string]string{}
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if ok && v != "" && isLoginKey(k) {
			login[k] = v
		}
	}
	return &route.CoreLaunch{Socket: c.cfg.Socket, Owner: c.owner, Wrapper: c.d.cfg.Executable, LaunchSocket: c.d.launchSocket(), NoticeSocket: NoticeSocketPath(c.d.cfg.PrivateHome),
		Login: login, LoginFrom: "daemon", Timeouts: c.cfg.Timeouts}
}

func isLoginKey(k string) bool {
	for _, l := range launch.LoginKeys {
		if l == k {
			return true
		}
	}
	return false
}

// callerExecution is the agent execution a caller's process belongs to, by
// process evidence alone.
func (c *coreRuntime) callerExecution(ctx context.Context, pid int) (runtime.Execution, bool) {
	execs, err := c.records.List(ctx, "", "")
	if err != nil {
		return runtime.Execution{}, false
	}
	return core.CallerExecution(c.cfg.Procs, pid, execs)
}

// coreInventory is a herdr-core route's inventory for the boundary check of
// its inbox: every terminal the section 6 rule calls a fault is an unknown
// agent, named by its address and class.
type coreInventory struct {
	c           *coreRuntime
	repo, route string
}

// Unknown implements inbox.Inventory.
func (i coreInventory) Unknown(ctx context.Context, _ map[string]any) ([]inbox.Unknown, error) {
	inv, err := i.c.routeInventory(ctx, i.repo, i.route)
	if err != nil {
		return nil, err
	}
	var out []inbox.Unknown
	for _, e := range inv.Faults() {
		out = append(out, inbox.Unknown{Pane: e.Address, Agent: string(e.Class), Session: json.RawMessage("null")})
	}
	return out, nil
}

// routeInventory classifies the core a route runs on, from the last pass or
// from a pass made now.
func (c *coreRuntime) routeInventory(ctx context.Context, repo, name string) (core.Inventory, error) {
	r, _ := c.reconciler(c.cfg.Socket)
	if inv, ok := r.RouteInventory(repo, name); ok {
		return inv, nil
	}
	if err := r.Pass(ctx); err != nil {
		return core.Inventory{}, err
	}
	if inv, ok := r.RouteInventory(repo, name); ok {
		return inv, nil
	}
	return core.Inventory{}, fmt.Errorf("no inventory of route %s on %s", name, c.cfg.Socket)
}

// routeOnCore reports whether a route's recorded executions are on core.
func (d *Daemon) routeOnCore(repo, name string) bool {
	if repo == "" {
		return false
	}
	var rows []store.Execution
	if err := d.ops.Read(func(c store.Conn) error {
		var err error
		rows, err = store.Executions(c, repo, name)
		return err
	}); err != nil {
		return false
	}
	for _, r := range rows {
		if r.Backend == runtime.BackendCore {
			return true
		}
	}
	return false
}

// boundaryInventory is the inventory a route's boundary check reads: the
// shared core's for a route on herdr-core, the TUI's agent list otherwise.
func (d *Daemon) boundaryInventory(name string, tui inbox.Inventory) inbox.Inventory {
	e, _ := d.routeEntry(name)
	if c := d.coreRT(); c != nil && d.routeOnCore(e.Repo, name) {
		return coreInventory{c: c, repo: e.Repo, route: name}
	}
	return tui
}

// storeRecords keeps core execution records in the account store, beside the
// journal of the route that launched them.
type storeRecords struct{ d *Daemon }

// Put implements core.Records.
func (s storeRecords) Put(_ context.Context, e runtime.Execution) (runtime.Execution, error) {
	var got store.Execution
	err := s.d.ops.Read(func(c store.Conn) error {
		var err error
		got, err = store.PutExecution(c, executionRow(e), s.d.now().UnixMilli())
		return err
	})
	if err != nil {
		return runtime.Execution{}, err
	}
	return executionOf(got), nil
}

// ByIntent implements core.Records.
func (s storeRecords) ByIntent(_ context.Context, intent string) (runtime.Execution, bool, error) {
	var row store.Execution
	found := false
	err := s.d.ops.Read(func(c store.Conn) error {
		var err error
		row, found, err = store.ExecutionByIntent(c, intent)
		return err
	})
	if err != nil || !found {
		return runtime.Execution{}, false, err
	}
	return executionOf(row), true, nil
}

// List implements core.Records.
func (s storeRecords) List(_ context.Context, repo, name string) ([]runtime.Execution, error) {
	var rows []store.Execution
	err := s.d.ops.Read(func(c store.Conn) error {
		var err error
		rows, err = store.Executions(c, repo, name)
		return err
	})
	out := make([]runtime.Execution, 0, len(rows))
	for _, r := range rows {
		out = append(out, executionOf(r))
	}
	return out, err
}

func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// executionOf is a stored row as an execution record.
func executionOf(r store.Execution) runtime.Execution {
	e := runtime.Execution{
		SchemaVersion: r.SchemaVersion, ID: r.ID, Repo: r.Repo, Route: r.Route, Agent: r.Agent, Role: r.Role, TaskID: r.TaskID,
		LaunchIntent: r.LaunchIntent, Generation: r.Generation, Backend: r.Backend, EndpointID: r.EndpointID,
		TerminalAddress: r.TerminalAddress, Pane: r.Pane, InitialCreatedMS: r.InitialCreatedMS,
		Evidence: runtime.ProcessEvidence{PID: int(r.PID), Start: r.ProcessStart}, ArgvDigest: r.ArgvDigest, Cwd: r.Cwd,
		HarnessKind: r.HarnessKind, HarnessVersion: r.HarnessVersion, PolicyDigest: r.PolicyDigest, LaunchGuard: r.LaunchGuard,
		Desired: runtime.Desired(r.DesiredState), LastVerifiedAt: fromMillis(r.LastVerifiedAt), Created: fromMillis(r.Created), Updated: fromMillis(r.Updated),
		Observation: runtime.Observation{
			Process: runtime.Process(r.Process), Readiness: runtime.Readiness(r.Readiness), Program: runtime.Program(r.Program),
			RawProgram: r.RawProgram, Visibility: runtime.Visibility(r.Visibility), App: runtime.App(r.App),
			Orchestration: runtime.Orchestration(r.Orchestration), TaskOutcome: runtime.TaskOutcome(r.TaskOutcome), ObservedAt: fromMillis(r.ObservedAt),
		},
	}
	if r.NativeConversation != "" {
		var conv runtime.Conversation
		if json.Unmarshal([]byte(r.NativeConversation), &conv) == nil {
			e.Conversation = &conv
		}
	}
	if r.AppBinding != "" {
		var app runtime.AppBinding
		if json.Unmarshal([]byte(r.AppBinding), &app) == nil {
			e.App = &app
		}
	}
	if r.TUIBinding != "" {
		var tui runtime.TUIBinding
		if json.Unmarshal([]byte(r.TUIBinding), &tui) == nil {
			e.TUI = &tui
		}
	}
	return e
}

// runtimeRegistry is the providers this daemon runs: the Herdr TUI, and
// herdr-core, whose provider needs the launch socket the daemon opens when it
// starts.
func (d *Daemon) runtimeRegistry() runtime.Registry {
	r := runtime.Registry{}
	legacytui.Register(r)
	r[runtime.BackendCore] = func(endpoint string) (runtime.Provider, error) {
		c := d.coreRT()
		if c == nil {
			return nil, runtime.Unavailable(runtime.BackendCore, "in this daemon: its launch socket is not open (a daemon that serves reads only has none)")
		}
		return c.provider(endpoint), nil
	}
	return r
}
