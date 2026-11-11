package core_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/hc/hctest"
	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/herdrapp/apptest"
	"github.com/arafathusayn/herdsman/internal/launch"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	"github.com/arafathusayn/herdsman/internal/runtime/core/coretest"
)

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// world is one fake core, one fake app, the records, the process table and
// the launch socket.
type world struct {
	t        *testing.T
	core     *hctest.Core
	procs    *coretest.Procs
	records  *core.MemRecords
	reg      *core.Registrar
	appHome  string
	app      *apptest.Fake
	appCore  *apptest.Core
	bindings *core.FileBindings
	p        *core.Provider
	dir      string
	peer     atomic.Int64
	holder   int

	// appRemote is the core of the machine "remote", as the window shows
	// it, once gRemote has added one.
	appRemote *apptest.Core
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{t: t, core: hctest.Start(t), procs: coretest.NewProcs(), records: &core.MemRecords{}}
	dir, err := os.MkdirTemp("", "hsc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	w.dir = dir
	w.holder = w.procs.Add(1, "hc", "hold")
	reg, err := core.NewRegistrar(filepath.Join(dir, "l.sock"), w.procs)
	if err != nil {
		t.Fatal(err)
	}
	reg.Peer = func(net.Conn) (int, error) { return int(w.peer.Load()), nil }
	ctx, cancel := context.WithCancel(context.Background())
	go reg.Serve(ctx)
	t.Cleanup(func() { cancel(); _ = reg.Close() })
	w.reg = reg
	w.appHome = filepath.Join(dir, "apphome")
	if err := os.MkdirAll(w.appHome, 0o700); err != nil {
		t.Fatal(err)
	}
	w.app = apptest.New(w.appHome)
	w.appCore = apptest.NewCore(w.core.ID)
	w.bindings = &core.FileBindings{Path: filepath.Join(dir, "bindings.json")}
	w.p = &core.Provider{Socket: w.core.Socket, Records: w.records, Procs: w.procs, Registrar: reg,
		App: herdrapp.New(syncRunner{w}, w.appHome), Bindings: w.bindings}
	w.core.SetExec("/bin/sh", hctest.ExecAnswer{Stdout: "/usr/local/bin/claude\n"})
	w.core.SetExec("/bin/ls", hctest.ExecAnswer{})
	w.core.SetExec("/bin/test", hctest.ExecAnswer{})
	return w
}

// syncRunner mirrors the fake core's live terminals into the app's core
// before each app command, as the real app follows core.
type syncRunner struct{ w *world }

func (s syncRunner) Run(ctx context.Context, inv herdrapp.Invocation) (herdrapp.Output, error) {
	for _, t := range s.w.core.Live() {
		labels, _ := t["labels"].(map[string]string)
		name, _ := t["name"].(string)
		addr := t["address"].(string)
		// The window shows a remote record under its machine.
		if m := hc.MachineOf(addr); m != "" {
			if s.w.appRemote != nil {
				s.w.appRemote.Run(strings.TrimPrefix(addr, m+"/"), name, labels)
			}
			continue
		}
		s.w.appCore.Run(addr, name, labels)
	}
	return s.w.app.Run(ctx, inv)
}

// openApp opens the app window on the selected core.
func (w *world) openApp() { w.app.Open(w.appCore) }

// launchOf reserves a launch.
func (w *world) launchOf(route, agent, role, kind string) core.Launch {
	exec := runtime.NewExecutionID()
	intent := "up:/repo:" + route + ":" + agent + ":" + exec
	l := core.Launch{
		Intent: intent, Execution: exec, Owner: "install-1", RouteID: core.UUIDOf("install-1", "/repo", route),
		AgentID: core.UUIDOf("install-1", "/repo", route, agent), Repo: "/repo", RouteName: route, AgentName: agent, Role: role, Kind: kind,
		Placement: herdrapp.PlacementIDs{Space: core.OpaqueID("install-1", "/repo", route), Tab: core.OpaqueID("tab", exec)},
		Name:      core.CoreName(route, role, exec), Wrapper: []string{"/usr/local/bin/herdsman", "launch", "--socket", w.reg.Path, "--intent", intent},
		Cwd: "/repo",
	}
	l.Descriptor = launch.CoreDescriptor{Intent: intent, Execution: exec, Kind: kind, Harness: []string{"/usr/local/bin/" + kind}, Cwd: w.dir,
		Login: map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/home/u"}, LoginFrom: "daemon"}
	return l
}

// spawn runs the reserve and spawn steps and gives the terminal a process.
func (w *world) spawn(l core.Launch) runtime.Execution {
	w.t.Helper()
	rec, err := w.p.SpawnStep(testCtx(w.t), l)
	if err != nil {
		w.t.Fatalf("spawn: %v", err)
	}
	w.setProcess(rec.TerminalAddress, w.procs.Add(w.holder, "herdsman", "launch"))
	return rec
}

// setProcess makes core report pid as the terminal's program.
func (w *world) setProcess(address string, pid int) {
	w.core.Mutate(address, func(t map[string]any) { t["pid"] = uint32(pid) })
}

func (w *world) pidOf(address string) int {
	switch v := w.core.Terminal(address)["pid"].(type) {
	case uint32:
		return int(v)
	case int:
		return v
	}
	return 0
}

// wrapperRun is one in-process run of the core wrapper.
type wrapperRun struct {
	code    int
	execEnv []string
	argv    []string
}

// wrap runs the wrapper as the program of address.
func (w *world) wrap(l core.Launch, address string, environ []string, keychain mapKeychain) wrapperRun {
	w.peer.Store(int64(w.pidOf(address)))
	var out wrapperRun
	wr := &launch.Wrapper{Socket: w.reg.Path, Intent: l.Intent, Environ: environ, Keychain: keychain, Stderr: &strings.Builder{}, DialWait: 2 * time.Second,
		Exec: func(argv0 string, argv, env []string) error {
			out.argv, out.execEnv = slices.Clone(argv), slices.Clone(env)
			return nil
		}}
	out.code = wr.Main(testCtx(w.t))
	return out
}

// register runs the register step while the wrapper runs.
func (w *world) register(l core.Launch, address string, environ []string, keychain mapKeychain) (runtime.Execution, wrapperRun, error) {
	type result struct {
		rec runtime.Execution
		err error
	}
	done := make(chan result, 1)
	go func() {
		rec, err := w.p.RegisterStep(testCtx(w.t), l, core.Timeouts{Register: 10 * time.Second, Poll: 20 * time.Millisecond})
		done <- result{rec, err}
	}()
	// The step arms the registrar first; the wrapper retries its dial.
	time.Sleep(50 * time.Millisecond)
	run := w.wrap(l, address, environ, keychain)
	r := <-done
	return r.rec, run, r.err
}

type mapKeychain = coretest.Keychain

// up launches one agent through registration.
func (w *world) up(route, agent, role, kind string) (core.Launch, runtime.Execution) {
	w.t.Helper()
	l := w.launchOf(route, agent, role, kind)
	rec := w.spawn(l)
	rec, run, err := w.register(l, rec.TerminalAddress, nil, nil)
	if err != nil || run.code != 0 {
		w.t.Fatalf("register: %v (wrapper %d)", err, run.code)
	}
	return l, rec
}

func (w *world) dial() *hc.Client {
	w.t.Helper()
	c, err := hc.Dial(testCtx(w.t), w.core.Socket, hc.Options{})
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { _ = c.Close() })
	return c
}

func (w *world) record(intent string) runtime.Execution {
	w.t.Helper()
	rec, ok, err := w.records.ByIntent(context.Background(), intent)
	if err != nil || !ok {
		w.t.Fatalf("no record of %s: %v", intent, err)
	}
	return rec
}

func envMap(env []string) map[string]string {
	out := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		out[k] = v
	}
	return out
}
