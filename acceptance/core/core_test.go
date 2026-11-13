//go:build acceptance

// Package core is the real-platform gate for the herdr-core backend: a real
// core in a private session, the real kernel's record of who connected to the
// launch socket, a real wrapper process that becomes its harness, and a real
// stop. The app is never opened: nothing here runs herdr-app, and a launch
// stops before the visibility step, which is proved with the fake app.
//
// The rest of the spec's section 17 gates that a real core can show run as
// opt-in live tests beside the fake-core ones, with the fake app following a
// private core: `make live-core`, which is
// HERDSMAN_HC_LIVE=1 go test -count=1 -p 1 -run Live ./internal/hc/... ./internal/runtime/core/
package core

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/launch"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

// TestMain doubles as the core wrapper: core starts this test binary as
// "<binary> launch --socket <path> --intent <id>" in the reserved terminal.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "launch" {
		var socket, intent string
		for i := 2; i+1 < len(os.Args); i++ {
			switch os.Args[i] {
			case "--socket":
				socket = os.Args[i+1]
			case "--intent":
				intent = os.Args[i+1]
			}
		}
		os.Exit((&launch.Wrapper{Socket: socket, Intent: intent}).Main(context.Background()))
	}
	os.Exit(m.Run())
}

// privateCore starts the installed hc in a session of its own, with every
// XDG directory under a temporary folder, and stops it when the test ends.
// The user's own core is never touched.
func privateCore(t *testing.T) (socket, dir string) {
	t.Helper()
	bin, err := exec.LookPath("hc")
	if err != nil {
		t.Fatalf("hc is declared provided and is not on the path: %v", err)
	}
	// Under /tmp: core refuses socket paths over 103 bytes.
	dir, err = os.MkdirTemp("/tmp", "hsa")
	if err != nil {
		t.Fatal(err)
	}
	session := fmt.Sprintf("hsa%d", os.Getpid())
	env := []string{"HC_SESSION=" + session}
	for _, kv := range [][2]string{{"XDG_RUNTIME_DIR", "run"}, {"XDG_STATE_HOME", "state"}, {"XDG_DATA_HOME", "data"}, {"XDG_CONFIG_HOME", "config"}} {
		p := filepath.Join(dir, kv[1])
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		env = append(env, kv[0]+"="+p)
	}
	for _, e := range os.Environ() {
		name, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(name, "HC_") || strings.HasPrefix(name, "XDG_") || strings.HasPrefix(name, "HERDR_") {
			continue
		}
		env = append(env, e)
	}
	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = env
		return cmd.CombinedOutput()
	}
	if out, err := run("server", "start", "--json"); err != nil {
		_ = os.RemoveAll(dir)
		t.Fatalf("start a private core: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if out, err := run("server", "stop", "--json"); err != nil {
			t.Errorf("stop the private core: %v\n%s", err, out)
		}
		_ = os.RemoveAll(dir)
	})
	return filepath.Join(dir, "run", "herdr-core", session+".sock"), dir
}

func ctxFor(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// A launch on a real private core: core starts the wrapper, the kernel names
// the process that connected, the wrapper registers as the terminal's own
// process and becomes the harness with the same pid, the inventory calls it a
// member, and a stop ends it and verifies that.
func TestRealCoreLaunchRegistersAndStops(t *testing.T) {
	socket, dir := privateCore(t)
	ctx := ctxFor(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := core.NewRegistrar(filepath.Join(dir, "l.sock"), core.SystemProcs{})
	if err != nil {
		t.Fatal(err)
	}
	go reg.Serve(ctx)
	defer reg.Close()
	records := &core.MemRecords{}
	p := &core.Provider{Socket: socket, Records: records, Procs: core.SystemProcs{}, Registrar: reg,
		App: herdrapp.New(refusingApp{t}, filepath.Join(dir, "apphome")), Bindings: &core.FileBindings{Path: filepath.Join(dir, "b.json")}}

	execID := runtime.NewExecutionID()
	intent := "up:/repo:gate:gate-orchestrator:" + execID
	work := filepath.Join(dir, "work")
	// macOS shows no other process's environment for a platform binary, so
	// the harness writes its own before it becomes sleep.
	envFile := filepath.Join(dir, "harness.env")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	l := core.Launch{
		Intent: intent, Execution: execID, Owner: "gate", RouteID: core.UUIDOf("gate", "/repo", "gate"), AgentID: core.UUIDOf("gate", "/repo", "gate", "o"),
		Repo: "/repo", RouteName: "gate", AgentName: "gate-orchestrator", Role: "orchestrator", Kind: "claude",
		Placement: herdrapp.PlacementIDs{Space: core.OpaqueID("gate", "space"), Tab: core.OpaqueID("tab", execID)}, Name: core.CoreName("gate", "orchestrator", execID),
		Wrapper: []string{self, "launch", "--socket", reg.Path, "--intent", intent}, Cwd: work,
		Descriptor: launch.CoreDescriptor{Intent: intent, Execution: execID, Kind: "claude", Harness: []string{"/bin/sh", "-c", "/usr/bin/env > " + envFile + "; exec /bin/sleep 600"}, Cwd: work,
			Login: map[string]string{"PATH": "/usr/bin:/bin", "HOME": dir}, LoginFrom: "daemon", Env: map[string]string{"GATE_SECRET_FREE": "1"}},
	}
	rec, err := p.SpawnStep(ctx, l)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	c, err := hc.Dial(ctx, socket, hc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Endpoint mismatch: the same socket with another expected core is refused.
	if _, err := hc.Dial(ctx, socket, hc.Options{ExpectCore: "not-this-core"}); err == nil {
		t.Error("a core with another identity was accepted")
	}
	rec, err = p.RegisterStep(ctx, l, core.Timeouts{Register: time.Minute, Poll: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	term, err := c.Get(ctx, rec.TerminalAddress)
	if err != nil || term.PID == nil || int(*term.PID) != rec.Evidence.PID {
		t.Fatalf("the registered pid %d is not the terminal's program %+v (%v)", rec.Evidence.PID, term.PID, err)
	}
	// The wrapper became the harness: the same pid now runs sleep, after
	// the harness's shell wrote its environment.
	var argv string
	for range 100 {
		out, _ := exec.Command("ps", "-o", "command=", "-p", fmt.Sprint(rec.Evidence.PID)).Output()
		if argv = strings.TrimSpace(string(out)); strings.HasPrefix(argv, "/bin/sleep") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.HasPrefix(argv, "/bin/sleep") {
		t.Fatalf("pid %d runs %q, not the harness", rec.Evidence.PID, argv)
	}
	// Environment: the harness has the descriptor's values and nothing that
	// names another agent, though core was asked for no environment at all.
	if out, err := os.ReadFile(envFile); err != nil {
		t.Errorf("the harness wrote no environment: %v", err)
	} else if env := string(out); !strings.Contains(env, "GATE_SECRET_FREE=1") || strings.Contains(env, "HERDR_") || !strings.Contains(env, "PATH=/usr/bin:/bin") {
		t.Errorf("the harness environment is wrong: %s", env)
	}
	list, err := c.List(ctx, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	inv := core.Classify(core.InventoryInput{Terminals: list.Terminals, Executions: []runtime.Execution{rec}, Repo: "/repo", Route: "gate", RouteID: l.RouteID,
		Procs: mustProcs(t, ctx), HarnessNames: []string{"claude", "codex"}})
	if !inv.OK() || len(inv.Entries) == 0 || inv.Entries[0].Class != core.ClassMember {
		t.Fatalf("inventory: %s %+v", inv.FaultText(), inv.Entries)
	}
	if _, err := p.Stop(ctx, runtime.Target{ExecutionID: rec.ID}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if alive, _ := core.Alive(core.SystemProcs{}, rec.Evidence); alive {
		t.Errorf("pid %d still runs after the stop", rec.Evidence.PID)
	}
}

func mustProcs(t *testing.T, ctx context.Context) []core.Proc {
	t.Helper()
	procs, err := core.SystemProcs{}.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return procs
}

// refusingApp fails the gate if anything tries to run the app.
type refusingApp struct{ t *testing.T }

func (r refusingApp) Run(context.Context, herdrapp.Invocation) (herdrapp.Output, error) {
	r.t.Error("the gate ran the herdr app")
	return herdrapp.Output{Exit: 1}, fmt.Errorf("the app is not run here")
}
