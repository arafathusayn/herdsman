package core_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/hc/hclive"
	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/herdrapp/apptest"
	"github.com/arafathusayn/herdsman/internal/launch"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	"github.com/arafathusayn/herdsman/internal/runtime/core/coretest"
)

// The live gates run the provider against a private real core of the
// installed hc (internal/hc/hclive), with this test binary as the core
// wrapper and a shell stand-in as the harness. The herdr app is never run:
// the fake app follows the real core. They are opt-in:
//
//	HERDSMAN_HC_LIVE=1 go test -run Live ./internal/runtime/core

// TestMain doubles as the core wrapper: core starts this test binary as
// "<binary> launch --socket <path> --intent <id> --keychain <file>" in the
// reserved terminal. The keychain is a file of the test's own, so the
// user's keychain is never read.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "launch" {
		var socket, intent, keychain string
		for i := 2; i+1 < len(os.Args); i++ {
			switch os.Args[i] {
			case "--socket":
				socket = os.Args[i+1]
			case "--intent":
				intent = os.Args[i+1]
			case "--keychain":
				keychain = os.Args[i+1]
			}
		}
		kc := coretest.Keychain{}
		if b, err := os.ReadFile(keychain); err == nil {
			_ = json.Unmarshal(b, &kc)
		}
		os.Exit((&launch.Wrapper{Socket: socket, Intent: intent, Keychain: kc}).Main(context.Background()))
	}
	os.Exit(m.Run())
}

// liveWorld is a private real core behind a counting proxy, the real launch
// socket with the kernel's peer, the machine's process table, the records,
// and the fake app following the real core.
type liveWorld struct {
	t        *testing.T
	core     *hclive.Core
	proxy    *hclive.Proxy
	coreID   string
	records  *core.MemRecords
	reg      *core.Registrar
	appHome  string
	app      *apptest.Fake
	appCore  *apptest.Core
	bindings *core.FileBindings
	p        *core.Provider
	self     string
	keychain string
	// mark tags every stand-in process of this world, so cleanup can end
	// any that outlived its terminal.
	mark string
	// children are added to every harness stand-in's script, before it
	// reports its status.
	children []string
}

func newLive(t *testing.T, opts hclive.Options) *liveWorld {
	t.Helper()
	c := hclive.Start(t, opts)
	w := &liveWorld{t: t, core: c, records: &core.MemRecords{}, appHome: filepath.Join(c.Dir, "apphome"), keychain: filepath.Join(c.Dir, "keychain.json"),
		mark: fmt.Sprintf("600.%d%d", os.Getpid()%10000, time.Now().UnixNano()%1000)}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	w.self = self
	w.proxy = hclive.NewProxy(t, filepath.Join(c.Dir, "p.sock"), c.Socket)
	direct := w.direct()
	w.coreID = direct.Core().ID
	reg, err := core.NewRegistrar(filepath.Join(c.Dir, "l.sock"), core.SystemProcs{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go reg.Serve(ctx)
	t.Cleanup(func() { cancel(); _ = reg.Close() })
	w.reg = reg
	if err := os.MkdirAll(w.appHome, 0o700); err != nil {
		t.Fatal(err)
	}
	w.app = apptest.New(w.appHome)
	w.appCore = apptest.NewCore(w.coreID)
	w.bindings = &core.FileBindings{Path: filepath.Join(c.Dir, "bindings.json")}
	w.p = &core.Provider{Socket: w.proxy.Path, Records: w.records, Procs: core.SystemProcs{}, Registrar: reg,
		App: herdrapp.New(liveRunner{w}, w.appHome), Bindings: w.bindings}
	// Runs after the core's stop (cleanups run last first): a stand-in
	// process that outlived its terminal is ended.
	t.Cleanup(w.endStrays)
	return w
}

// direct is a connection to the private core that bypasses the proxy: what
// the test reads through it is not counted as the provider's calls.
func (w *liveWorld) direct() *hc.Client {
	w.t.Helper()
	c, err := hc.Dial(testCtx(w.t), w.core.Socket, hc.Options{})
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { _ = c.Close() })
	return c
}

func (w *liveWorld) endStrays() {
	procs, err := core.SystemProcs{}.List(context.Background())
	if err != nil {
		return
	}
	for _, p := range procs {
		if slices.Contains(p.Argv, w.mark) {
			_ = syscall.Kill(p.PID, syscall.SIGKILL)
		}
	}
}

// liveRunner mirrors the real core's live terminals into the fake app's
// core before each app command, as the real app follows core.
type liveRunner struct{ w *liveWorld }

func (s liveRunner) Run(ctx context.Context, inv herdrapp.Invocation) (herdrapp.Output, error) {
	c, err := hc.Dial(ctx, s.w.core.Socket, hc.Options{})
	if err != nil {
		return herdrapp.Output{}, err
	}
	defer c.Close()
	live, err := c.List(ctx, nil, false)
	if err != nil {
		return herdrapp.Output{}, err
	}
	for _, t := range live.Terminals {
		s.w.appCore.Run(t.Address, t.Name, t.Labels)
	}
	return s.w.app.Run(ctx, inv)
}

// osc is an OSC 7501 status record as a harness prints it.
func osc(state string) string { return `printf '\033]7501;state=` + state + `:app=standin\007'` }

// standIn is the harness: it writes its environment where the test reads
// it, starts a child (a caller below it), runs the world's children, reports
// idle, and becomes a long sleep with the wrapper's pid.
func (w *liveWorld) standIn(exec string) []string {
	script := []string{
		"/usr/bin/env > " + w.envFile(exec),
		"/bin/sleep " + w.mark + " &",
	}
	script = append(script, w.children...)
	script = append(script, osc("idle"), "exec /bin/sleep "+w.mark)
	file := filepath.Join(w.core.Dir, "harness-"+exec+".sh")
	if err := os.WriteFile(file, []byte(strings.Join(script, "\n")+"\n"), 0o600); err != nil {
		w.t.Fatal(err)
	}
	return []string{"/bin/sh", file}
}

func (w *liveWorld) envFile(exec string) string {
	return filepath.Join(w.core.Dir, "harness-"+exec+".env")
}

// harnessEnv is the environment the stand-in of exec wrote.
func (w *liveWorld) harnessEnv(exec string) map[string]string {
	w.t.Helper()
	var b []byte
	for range 100 {
		var err error
		if b, err = os.ReadFile(w.envFile(exec)); err == nil && len(b) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(b) == 0 {
		w.t.Fatalf("the harness of %s wrote no environment", exec)
	}
	return envMap(strings.Split(strings.TrimSpace(string(b)), "\n"))
}

// hcLine is a command line that runs the installed hc against this world's
// private core only.
func (w *liveWorld) hcLine(args string) string {
	return "/usr/bin/env " + strings.Join(w.core.PrivateEnv(), " ") + " " + w.core.Bin + " " + args
}

func (w *liveWorld) T() *testing.T                 { return w.t }
func (w *liveWorld) gProvider() *core.Provider     { return w.p }
func (w *liveWorld) gRecords() *core.MemRecords    { return w.records }
func (w *liveWorld) gProcs() core.ProcessTable     { return core.SystemProcs{} }
func (w *liveWorld) gCoreID() string               { return w.coreID }
func (w *liveWorld) gOpenApp()                     { w.app.Open(w.appCore) }
func (w *liveWorld) gApp() *apptest.Fake           { return w.app }
func (w *liveWorld) gAppHome() string              { return w.appHome }
func (w *liveWorld) gBindings() *core.FileBindings { return w.bindings }
func (w *liveWorld) gCalls(method string) int      { return w.proxy.Calls(method) }
func (w *liveWorld) gOutsider() int                { return os.Getpid() }
func (w *liveWorld) gDrop()                        { w.proxy.Cut() }
func (w *liveWorld) gRecord(intent string) runtime.Execution {
	w.t.Helper()
	rec, ok, err := w.records.ByIntent(context.Background(), intent)
	if err != nil || !ok {
		w.t.Fatalf("no record of %s: %v", intent, err)
	}
	return rec
}

func (w *liveWorld) gDial() *hc.Client {
	w.t.Helper()
	c, err := hc.Dial(testCtx(w.t), w.proxy.Path, hc.Options{})
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { _ = c.Close() })
	return c
}

func (w *liveWorld) gLaunch(route, agent, role, kind string) core.Launch {
	w.t.Helper()
	exec := runtime.NewExecutionID()
	intent := "up:/repo:" + route + ":" + agent + ":" + exec
	work := filepath.Join(w.core.Dir, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		w.t.Fatal(err)
	}
	l := core.Launch{
		Intent: intent, Execution: exec, Owner: "install-live", RouteID: core.UUIDOf("install-live", "/repo", route),
		AgentID: core.UUIDOf("install-live", "/repo", route, agent), Repo: "/repo", RouteName: route, AgentName: agent, Role: role, Kind: kind,
		Placement: herdrapp.PlacementIDs{Space: core.OpaqueID("install-live", "/repo", route), Tab: core.OpaqueID("tab", exec)},
		Name:      core.CoreName(route, role, exec),
		Wrapper:   []string{w.self, "launch", "--socket", w.reg.Path, "--intent", intent, "--keychain", w.keychain},
		Cwd:       work,
	}
	l.Descriptor = launch.CoreDescriptor{Intent: intent, Execution: exec, Kind: kind, Harness: w.standIn(exec), Cwd: work,
		Login: map[string]string{"PATH": "/usr/bin:/bin", "HOME": w.core.Dir}, LoginFrom: "daemon"}
	return l
}

func (w *liveWorld) gRegister(l core.Launch) (runtime.Execution, error) {
	return w.p.RegisterStep(testCtx(w.t), l, core.Timeouts{Register: 30 * time.Second, Poll: 50 * time.Millisecond})
}

func (w *liveWorld) gUp(route, agent, role, kind string) (core.Launch, runtime.Execution) {
	w.t.Helper()
	l := w.gLaunch(route, agent, role, kind)
	return l, w.upLaunch(l)
}

// upLaunch runs the reserve, spawn and register steps of l.
func (w *liveWorld) upLaunch(l core.Launch) runtime.Execution {
	w.t.Helper()
	if _, err := w.p.SpawnStep(testCtx(w.t), l); err != nil {
		w.t.Fatalf("spawn: %v", err)
	}
	rec, err := w.gRegister(l)
	if err != nil {
		w.t.Fatalf("register: %v\nthe terminal shows:\n%s", err, w.screen(w.gRecord(l.Intent).TerminalAddress))
	}
	return rec
}

// screen is what a terminal shows, for a failure's message.
func (w *liveWorld) screen(address string) string {
	res, err := w.direct().Read(testCtx(w.t), address, 40)
	if err != nil {
		return err.Error()
	}
	return res.Text
}

func (w *liveWorld) gReady(l core.Launch) runtime.Execution {
	w.t.Helper()
	ctx := testCtx(w.t)
	if _, err := w.p.VisibilityStep(ctx, l, core.Timeouts{Visibility: 5 * time.Second, Poll: 50 * time.Millisecond}); err != nil {
		w.t.Fatalf("visibility: %v", err)
	}
	rec, err := w.p.ReadyStep(ctx, l, core.Timeouts{Ready: 15 * time.Second, Poll: 50 * time.Millisecond})
	if err != nil {
		w.t.Fatalf("ready: %v", err)
	}
	return rec
}

func (w *liveWorld) gReconciler(ev *events) *core.Reconciler {
	return &core.Reconciler{Socket: w.proxy.Path, ExpectCore: w.coreID, Records: w.records, Procs: core.SystemProcs{}, App: w.p.App, Bindings: w.bindings,
		HarnessNames: []string{"claude", "codex"}, Emit: ev.add, Safety: time.Hour}
}

// shellAt waits until address runs a live program other than pid: core's
// recovery shell.
func (w *liveWorld) shellAt(address string, pid int) int {
	w.t.Helper()
	c := w.direct()
	deadline := time.Now().Add(20 * time.Second)
	for {
		t, err := c.Get(testCtx(w.t), address)
		if err == nil && t.Live() && t.PID != nil && int(*t.PID) != pid {
			return int(*t.PID)
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("no shell came up at %s: %+v %v", address, t, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (w *liveWorld) gLose(rec runtime.Execution) int {
	w.t.Helper()
	if err := syscall.Kill(rec.Evidence.PID, syscall.SIGKILL); err != nil {
		w.t.Fatal(err)
	}
	return w.shellAt(rec.TerminalAddress, rec.Evidence.PID)
}

func (w *liveWorld) gRestartCore() {
	w.t.Helper()
	w.core.Restart()
	execs, _ := w.records.List(context.Background(), "", "")
	for _, e := range execs {
		if e.Evidence.PID != 0 {
			w.shellAt(e.TerminalAddress, e.Evidence.PID)
		}
	}
}

func (w *liveWorld) gDescendant(pid int) int {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		procs, _ := core.SystemProcs{}.List(context.Background())
		for _, p := range procs {
			if p.PPID == pid {
				return p.PID
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return pid
}

func (w *liveWorld) gChangeCore() {
	other := hclive.Start(w.t, hclive.Options{})
	w.proxy.Upstream(other.Socket)
	w.proxy.Cut()
}

func (w *liveWorld) gOtherApp(address string, labels map[string]string) *apptest.Core {
	w.t.Helper()
	other := hclive.Start(w.t, hclive.Options{})
	c, err := hc.Dial(testCtx(w.t), other.Socket, hc.Options{})
	if err != nil {
		w.t.Fatal(err)
	}
	defer c.Close()
	app := apptest.NewCore(c.Core().ID)
	for range 50 {
		term, err := c.Run(testCtx(w.t), hc.RunRequest{Argv: []string{"/bin/sleep", w.mark}, Labels: labels})
		if err != nil {
			w.t.Fatal(err)
		}
		if term.Address == address {
			app.Run(term.Address, "same-name", labels)
			return app
		}
	}
	w.t.Fatalf("the other core gave no terminal the address %s", address)
	return nil
}

var _ gate = (*liveWorld)(nil)

// liveGate is a fresh live world for gate bodies that take one per case.
func liveGate(t *testing.T) gate { return newLive(t, hclive.Options{}) }

// Identity replacement, against a real core: the harness is killed and
// core's own recovery shell takes its address.
func TestLiveRecoveryShellInheritsNothing(t *testing.T) {
	gateRecoveryShellInheritsNothing(liveGate(t))
}

// Recovery shells, against a real core stopped and started again.
func TestLiveCoreRestartLosesEveryExecutionOnePlan(t *testing.T) {
	w := newLive(t, hclive.Options{})
	launches := gateCoreRestartLosesEveryExecutionOnePlan(w)
	// The shells are core's, never typed into: each runs the shell core was
	// told to start, and still runs after every stop above.
	for _, l := range launches {
		rec := w.gRecord(l.Intent)
		env, ok := w.core.ShellEnv(rec.TerminalAddress)
		if !ok {
			t.Fatalf("no recovery shell started at %s", rec.TerminalAddress)
		}
		if m := envMap(env); m["HC_TERMINAL_ID"] != rec.TerminalAddress {
			t.Fatalf("the shell at %s is another terminal's: %v", rec.TerminalAddress, m["HC_TERMINAL_ID"])
		}
	}
}

// Caller identity, against the machine's process table.
func TestLiveCallerIdentityByProcess(t *testing.T) { gateCallerIdentityByProcess(liveGate(t)) }

// Endpoint mismatch, against two real cores.
func TestLiveEndpointMismatch(t *testing.T) {
	w := newLive(t, hclive.Options{})
	gateEndpointMismatch(w)
	// A provider pinned to this core refuses a socket that leads to another.
	other := hclive.Start(t, hclive.Options{})
	pinned := &core.Provider{Socket: other.Socket, ExpectCore: w.coreID, Records: w.records, Procs: core.SystemProcs{}}
	if _, err := pinned.Capabilities(testCtx(t)); err == nil {
		t.Fatal("a provider pinned to one core accepted another")
	}
}

// Reconnect: the connection is dropped mid-run, and the socket then leads
// to another real core.
func TestLiveReconnectAndIdentityChange(t *testing.T) { gateReconnectAndIdentityChange(liveGate(t)) }

// Launch crash points that a real core can show.
func TestLiveUncertainSpawn(t *testing.T) {
	hclive.Require(t)
	gateUncertainSpawn(t, liveGate)
}
