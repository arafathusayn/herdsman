// Package coretest runs the herdr-core provider against a fake core, a fake
// app and a fake process table, for the tests of the packages that drive it.
// The wrapper runs in process: when the fake core starts a launch's terminal,
// the world gives it a process and runs the core wrapper as that process.
package coretest

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/hc/hctest"
	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/herdrapp/apptest"
	"github.com/arafathusayn/herdsman/internal/launch"
	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	"github.com/arafathusayn/herdsman/internal/sys"
)

// Procs is a process table: pids with start times, parents and argv.
type Procs struct {
	mu      sync.Mutex
	procs   map[int]*proc
	signals []string
	next    int
}

type proc struct {
	ppid       int
	start      time.Time
	argv       []string
	dead       bool
	ignoreTerm bool
}

// NewProcs is an empty table.
func NewProcs() *Procs { return &Procs{procs: map[int]*proc{}, next: 50000} }

// Put records a process under a pid of the caller's choosing, such as this
// test process, so a caller can be placed below an execution.
func (f *Procs) Put(pid, ppid int, argv ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.procs[pid] = &proc{ppid: ppid, start: time.Unix(1794000000, int64(pid)), argv: argv}
}

// Add starts a process under ppid and returns its pid.
func (f *Procs) Add(ppid int, argv ...string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.procs[f.next] = &proc{ppid: ppid, start: time.Unix(1794000000, int64(f.next)), argv: argv}
	return f.next
}

// Kill ends a process.
func (f *Procs) Kill(pid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.procs[pid]; p != nil {
		p.dead = true
	}
}

// IgnoreTerm keeps a process alive after SIGTERM.
func (f *Procs) IgnoreTerm(pid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.procs[pid]; p != nil {
		p.ignoreTerm = true
	}
}

// Signals lists the signals sent, in order.
func (f *Procs) Signals() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.signals)
}

// Identify implements core.ProcessTable.
func (f *Procs) Identify(pid int) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.procs[pid]
	if p == nil || p.dead {
		return time.Time{}, sys.ErrNoProcess
	}
	return p.start, nil
}

// Parent implements core.ProcessTable.
func (f *Procs) Parent(pid int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.procs[pid]
	if p == nil || p.dead {
		return 0, sys.ErrNoProcess
	}
	return p.ppid, nil
}

// List implements core.ProcessTable.
func (f *Procs) List(context.Context) ([]core.Proc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []core.Proc
	for pid, p := range f.procs {
		if !p.dead {
			out = append(out, core.Proc{PID: pid, PPID: p.ppid, Argv: p.argv})
		}
	}
	return out, nil
}

// Signal implements core.ProcessTable.
func (f *Procs) Signal(pid int, sig syscall.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signals = append(f.signals, sig.String())
	p := f.procs[pid]
	if p == nil || p.dead {
		return syscall.ESRCH
	}
	if sig == syscall.SIGTERM && p.ignoreTerm {
		return nil
	}
	p.dead = true
	return nil
}

// Keychain is a keychain in memory, keyed "service/account".
type Keychain map[string]string

// Load implements the wrapper's keychain.
func (k Keychain) Load(service, account string) (string, error) {
	v, ok := k[service+"/"+account]
	if !ok {
		return "", os.ErrNotExist
	}
	return v, nil
}

// World is one fake core, one fake app, the records, the process table and
// the launch socket.
type World struct {
	T         testing.TB
	Core      *hctest.Core
	Procs     *Procs
	Records   *core.MemRecords
	Registrar *core.Registrar
	AppHome   string
	App       *apptest.Fake
	AppCore   *apptest.Core
	Bindings  *core.FileBindings
	Provider  *core.Provider
	// Notices is the notice service over the world's records, in memory;
	// NoticeStore holds its state, so a test can open a second service on
	// it as a daemon restart would.
	Notices     *notice.Service
	NoticeStore *notice.MemStore
	// Events are what the notice service raised.
	Events []notice.Event
	// Submits holds a person's typed submits in memory.
	Submits *MemSubmits
	Dir     string
	// Holder is core's process, the parent of every terminal's program.
	Holder int
	// Environ is the environment the wrapper sees; Keychain its keychain.
	Environ  []string
	Keychain Keychain
	// Ready is the status the world gives a harness once the wrapper execs
	// it ("idle" by default; "" leaves it unset).
	Ready string

	peer    atomic.Int64
	mu      sync.Mutex
	execed  map[string][]string
	envs    map[string][]string
	wrapped map[string]bool
	pids    map[string]int
}

// New starts a world. The app is installed but closed.
func New(t testing.TB) *World {
	t.Helper()
	w := &World{T: t, Core: hctest.Start(t), Procs: NewProcs(), Records: &core.MemRecords{}, Ready: "idle", execed: map[string][]string{}, envs: map[string][]string{}, wrapped: map[string]bool{}, pids: map[string]int{}}
	// A short path: a unix socket's path is limited.
	dir, err := os.MkdirTemp("", "hsc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	w.Dir = dir
	w.Holder = w.Procs.Add(1, "hc", "hold")
	reg, err := core.NewRegistrar(filepath.Join(dir, "l.sock"), w.Procs)
	if err != nil {
		t.Fatal(err)
	}
	reg.Peer = func(net.Conn) (int, error) { return int(w.peer.Load()), nil }
	ctx, cancel := context.WithCancel(context.Background())
	go reg.Serve(ctx)
	t.Cleanup(func() { cancel(); _ = reg.Close() })
	w.Registrar = reg
	w.AppHome = filepath.Join(dir, "apphome")
	if err := os.MkdirAll(w.AppHome, 0o700); err != nil {
		t.Fatal(err)
	}
	w.App = apptest.New(w.AppHome)
	w.AppCore = apptest.NewCore(w.Core.ID)
	w.Bindings = &core.FileBindings{Path: filepath.Join(dir, "bindings.json")}
	w.NoticeStore = notice.NewMemStore()
	w.Notices = w.NewNotices(notice.Config{Poll: 200 * time.Millisecond, Boot: "boot-1"})
	w.Submits = &MemSubmits{}
	w.Provider = w.NewProvider(w.Core.Socket)
	w.Core.SetExec("/bin/sh", hctest.ExecAnswer{Stdout: "/usr/local/bin/claude\n"})
	w.Core.SetExec("/bin/ls", hctest.ExecAnswer{})
	w.Core.SetExec("/bin/test", hctest.ExecAnswer{})
	return w
}

// NewProvider is a provider on endpoint over the world's records, registrar,
// app and process table.
func (w *World) NewProvider(endpoint string) *core.Provider {
	return &core.Provider{Socket: endpoint, Records: w.Records, Procs: w.Procs, Registrar: w.Registrar,
		App: herdrapp.New(AppRunner{w}, w.AppHome), Bindings: w.Bindings, Notices: w.Notices, Submits: w.Submitter()}
}

// NewNotices opens a notice service on the world's notice store, as a daemon
// start would; cfg's zero values take the defaults. The adapters decide which
// harness is attended.
func (w *World) NewNotices(cfg notice.Config) *notice.Service {
	if cfg.Emit == nil {
		cfg.Emit = func(ev notice.Event) {
			w.mu.Lock()
			w.Events = append(w.Events, ev)
			w.mu.Unlock()
		}
	}
	if cfg.Attended == nil {
		cfg.Attended = func(kind string) bool {
			a, err := core.AdapterFor(kind)
			return err == nil && !a.Unattended()
		}
	}
	s := notice.New(w.NoticeStore, Executions{w.Records}, cfg)
	w.T.Cleanup(s.Close)
	return s
}

// EventsOf returns the notice events of one kind so far.
func (w *World) EventsOf(kind string) []notice.Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []notice.Event
	for _, ev := range w.Events {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

// Submitter is a typed-submit service on the world's core.
func (w *World) Submitter() *core.Submitter {
	return &core.Submitter{Store: w.Submits, Records: w.Records, Notices: w.Notices, Procs: w.Procs,
		Dial: func(ctx context.Context) (core.Sender, func(), error) {
			c, err := hc.Dial(ctx, w.Core.Socket, hc.Options{})
			if err != nil {
				return nil, nil, err
			}
			return c, func() { _ = c.Close() }, nil
		}}
}

// Executions reads the notice gates' executions from the world's records.
type Executions struct{ Records core.Records }

// Execution implements notice.Executions.
func (x Executions) Execution(ctx context.Context, id string) (runtime.Execution, bool, error) {
	all, err := x.Records.List(ctx, "", "")
	for _, e := range all {
		if e.ID == id {
			return e, true, err
		}
	}
	return runtime.Execution{}, false, err
}

// Running implements notice.Executions.
func (x Executions) Running(ctx context.Context) ([]runtime.Execution, error) {
	all, err := x.Records.List(ctx, "", "")
	var out []runtime.Execution
	for _, e := range all {
		if e.Desired == runtime.DesiredRunning {
			out = append(out, e)
		}
	}
	return out, err
}

// MemSubmits keeps typed submits in memory.
type MemSubmits struct {
	mu   sync.Mutex
	rows []core.SubmitRecord
}

// PutSubmit implements core.SubmitStore.
func (m *MemSubmits) PutSubmit(r core.SubmitRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, x := range m.rows {
		if x.ID == r.ID {
			m.rows[i] = r
			return nil
		}
	}
	m.rows = append(m.rows, r)
	return nil
}

// Submits implements core.SubmitStore.
func (m *MemSubmits) Submits(execution string) ([]core.SubmitRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []core.SubmitRecord
	for _, r := range m.rows {
		if execution == "" || r.Execution == execution {
			out = append(out, r)
		}
	}
	return out, nil
}

// AppRunner mirrors the fake core's live terminals into the app's core before
// each app command, as the real app follows core.
type AppRunner struct{ W *World }

// Run implements herdrapp.Runner.
func (s AppRunner) Run(ctx context.Context, inv herdrapp.Invocation) (herdrapp.Output, error) {
	for _, t := range s.W.Core.Live() {
		labels, _ := t["labels"].(map[string]string)
		name, _ := t["name"].(string)
		s.W.AppCore.Run(t["address"].(string), name, labels)
	}
	return s.W.App.Run(ctx, inv)
}

// Peer is the launch socket's peer as the kernel would report it: the
// process of the terminal whose wrapper connected last. A registrar of
// another package takes it as its Peer.
func (w *World) Peer(net.Conn) (int, error) { return int(w.peer.Load()), nil }

// OpenApp opens the app window on the selected core.
func (w *World) OpenApp() { w.App.Open(w.AppCore) }

// SetProcess makes core report pid as the terminal's program.
func (w *World) SetProcess(address string, pid int) {
	w.Core.Mutate(address, func(t map[string]any) { t["pid"] = uint32(pid) })
}

// Execed is the argv the wrapper of intent ran the harness with.
func (w *World) Execed(intent string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.execed[intent])
}

// ExecEnv is the environment the wrapper of intent ran the harness with.
func (w *World) ExecEnv(intent string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.envs[intent])
}

// AutoWrap runs the core wrapper in every launch terminal the fake core
// starts, as the terminal's own process, until ctx ends.
func (w *World) AutoWrap(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
			live := map[string]bool{}
			for _, t := range w.Core.Live() {
				addr, _ := t["address"].(string)
				live[addr] = true
			}
			// A terminal core ended takes its program with it.
			w.mu.Lock()
			for addr, pid := range w.pids {
				if !live[addr] {
					w.Procs.Kill(pid)
					delete(w.pids, addr)
				}
			}
			w.mu.Unlock()
			for _, t := range w.Core.Live() {
				argv, _ := t["argv"].([]string)
				intent := intentOf(argv)
				addr, _ := t["address"].(string)
				w.mu.Lock()
				seen := w.wrapped[addr]
				w.wrapped[addr] = true
				w.mu.Unlock()
				if intent == "" || seen {
					continue
				}
				go w.wrap(ctx, addr, argv, intent)
			}
		}
	}()
}

func intentOf(argv []string) string {
	for i, a := range argv {
		if a == "--intent" && i+1 < len(argv) && len(argv) > 2 && argv[1] == "launch" {
			return argv[i+1]
		}
	}
	return ""
}

func socketOf(argv []string) string {
	for i, a := range argv {
		if a == "--socket" && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

func (w *World) wrap(ctx context.Context, addr string, argv []string, intent string) {
	pid := w.Procs.Add(w.Holder, argv...)
	w.mu.Lock()
	w.pids[addr] = pid
	w.mu.Unlock()
	w.SetProcess(addr, pid)
	w.peer.Store(int64(pid))
	wr := &launch.Wrapper{Socket: socketOf(argv), Intent: intent, Environ: w.Environ, Keychain: w.Keychain, Stderr: &strings.Builder{}, DialWait: 5 * time.Second,
		Exec: func(argv0 string, args, env []string) error {
			w.mu.Lock()
			w.execed[intent] = slices.Clone(args)
			w.envs[intent] = slices.Clone(env)
			w.mu.Unlock()
			if w.Ready != "" {
				w.Core.SetStatus(addr, w.Ready)
			}
			return nil
		}}
	_ = wr.Main(ctx)
}
