package core_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/hc/hctest"
	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/launch"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

// The core path: preflight, reserve, spawn, authenticate, register,
// visibility, readiness and the dispatch gate, with no TUI anywhere.
func TestLaunchTransactionOnCore(t *testing.T) {
	w := newWorld(t)
	w.openApp()
	ctx := testCtx(t)
	c := w.dial()
	pre, err := core.Preflight(ctx, c, core.PreflightInput{Kind: "claude", Argv: []string{"claude", "--model", "opus"}, Roots: []string{"/repo"}})
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if pre.Program != "/usr/local/bin/claude" || pre.Core.ID != w.core.ID {
		t.Fatalf("preflight = %+v", pre)
	}
	l := w.launchOf("alpha", "alpha-orchestrator", "orchestrator", "claude")
	rec := w.spawn(l)
	if rec.ID != l.Execution || rec.TerminalAddress == "" || rec.Observation.Process != runtime.ProcessStarting {
		t.Fatalf("spawned record = %+v", rec)
	}
	var run map[string]any
	for _, r := range w.core.Requests() {
		if r.Method == hc.MethodRun {
			run = r.Params
		}
	}
	if run["no_parent"] != true || run["no_notify"] != true || run["rm"] != false || run["keep"] != true {
		t.Fatalf("terminal.run flags = %v", run)
	}
	if _, ok := run["env"]; ok {
		t.Fatalf("terminal.run carried an env: %v", run["env"])
	}
	labels, _ := run["labels"].(map[string]any)
	if labels[herdrapp.LabelExecution] != l.Execution || labels[herdrapp.LabelSpace] != l.Placement.Space || labels[herdrapp.LabelTab] != l.Placement.Tab {
		t.Fatalf("labels = %v", labels)
	}

	rec, wr, err := w.register(l, rec.TerminalAddress, nil, nil)
	if err != nil || wr.code != 0 {
		t.Fatalf("register: %v (wrapper %d)", err, wr.code)
	}
	if rec.Evidence.PID != w.pidOf(rec.TerminalAddress) || rec.Evidence.Start == "" || rec.Observation.Process != runtime.ProcessLive {
		t.Fatalf("registered record = %+v", rec)
	}
	if wr.argv[0] != "/usr/local/bin/claude" {
		t.Fatalf("exec argv = %v", wr.argv)
	}
	if v, _, _ := w.p.ProbeRegister(ctx, l); v != core.ProbeMatches {
		t.Fatalf("probe register = %v", v)
	}

	rec, err = w.p.VisibilityStep(ctx, l, core.Timeouts{Visibility: 2 * time.Second, Poll: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("visibility: %v", err)
	}
	if rec.Observation.Visibility != runtime.VisibilityVerified || rec.App.PaneID == "" {
		t.Fatalf("visibility record = %+v %+v", rec.Observation, rec.App)
	}
	if _, ok, _ := w.bindings.Load(w.appHome, w.core.ID); !ok {
		t.Fatal("the binding was not persisted")
	}

	if _, err := w.p.Enrollable(ctx, []string{l.Intent}); core.CodeOf(err) != core.CodeNeedsAttention {
		t.Fatalf("enrollable before readiness: %v", err)
	}
	w.core.SetStatus(rec.TerminalAddress, "idle")
	if rec, err = w.p.ReadyStep(ctx, l, core.Timeouts{Ready: 2 * time.Second, Poll: 20 * time.Millisecond}); err != nil {
		t.Fatalf("ready: %v", err)
	}
	if rec.Observation.Readiness != runtime.ReadinessReady {
		t.Fatalf("readiness = %+v", rec.Observation)
	}
	if _, err := w.p.Enrollable(ctx, []string{l.Intent}); err != nil {
		t.Fatalf("enrollable: %v", err)
	}
	for _, m := range []string{hc.MethodSend, "terminal.keys"} {
		if n := w.core.Calls(m); n != 0 {
			t.Fatalf("%s was called %d times", m, n)
		}
	}
}

func TestPreflightRefusals(t *testing.T) {
	w := newWorld(t)
	ctx := testCtx(t)
	c := w.dial()
	cases := []struct {
		name string
		in   core.PreflightInput
		code string
	}{
		{"endpoint", core.PreflightInput{Kind: "claude", Argv: []string{"claude"}, ExpectCore: "another-core"}, core.CodeEndpointMismatch},
		{"unqualified", core.PreflightInput{Kind: "gemini", Argv: []string{"gemini"}}, core.CodeUnqualified},
		{"codex without --no-daemon", core.PreflightInput{Kind: "codex", Argv: []string{"codex", "--model", "x"}}, core.CodeUnqualified},
		{"restricted", core.PreflightInput{Kind: "codex", Argv: []string{"codex", "--no-daemon"}, Restricted: true}, core.CodeRestricted},
	}
	for _, tc := range cases {
		_, err := core.Preflight(ctx, c, tc.in)
		if core.CodeOf(err) != tc.code {
			t.Errorf("%s: %v, want %s", tc.name, err, tc.code)
		}
	}
	if _, err := core.Preflight(ctx, c, core.PreflightInput{Kind: "codex", Argv: []string{"codex", "--model", "x"}}); err == nil || !strings.Contains(err.Error(), "--no-daemon") {
		t.Errorf("the refusal does not name --no-daemon: %v", err)
	}
	w.core.SetExec("/bin/ls", hctest.ExecAnswer{Code: 1, Stderr: "Operation not permitted"})
	if _, err := core.Preflight(ctx, c, core.PreflightInput{Kind: "claude", Argv: []string{"claude"}, Roots: []string{"/home/x/Documents/repo"}}); core.CodeOf(err) != core.CodeRootUnreadable {
		t.Errorf("unreadable root: %v", err)
	}
	w.core.SetExec("/bin/sh", hctest.ExecAnswer{Code: 1})
	if _, err := core.Preflight(ctx, c, core.PreflightInput{Kind: "claude", Argv: []string{"claude"}}); core.CodeOf(err) != core.CodeProgramNotFound {
		t.Errorf("unknown program: %v", err)
	}
}

// A granted secret reaches the prelude and the harness, a denied one neither,
// and no marker of it is in core's terminal record: env, labels or argv.
func TestSecretsAndPreludeEnvironment(t *testing.T) {
	w := newWorld(t)
	l := w.launchOf("beta", "beta-orchestrator", "orchestrator", "claude")
	steps := filepath.Join(w.dir, "steps")
	seen := filepath.Join(w.dir, "seen")
	l.Descriptor.StepsDir = steps
	l.Descriptor.SecretRefs = "GRANTED=hs-test/granted"
	l.Descriptor.Prelude = []launch.NumberedStep{{N: 1, Step: launch.Step{Command: launch.Command{Argv: []string{"/bin/sh", "-c", `printf '%s|%s' "$GRANTED" "$DENIED" > ` + seen}}}}}
	rec := w.spawn(l)
	const marker = "marker-secret-7e1"
	core := []string{"DENIED=denied-secret-55", "PATH=/core/bin"}
	rec, wr, err := w.register(l, rec.TerminalAddress, core, mapKeychain{"hs-test/granted": marker})
	if err != nil || wr.code != 0 {
		t.Fatalf("register: %v (%d)", err, wr.code)
	}
	b, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != marker+"|" {
		t.Fatalf("the prelude saw %q", b)
	}
	env := envMap(wr.execEnv)
	if env["GRANTED"] != marker || env["DENIED"] != "" || env["PATH"] != "/usr/bin:/bin" {
		t.Fatalf("harness env = %v", env)
	}
	record := fmt.Sprint(w.core.Terminal(rec.TerminalAddress))
	if strings.Contains(record, marker) || strings.Contains(record, "GRANTED") {
		t.Fatalf("core's record holds the secret: %s", record)
	}
	for _, r := range w.core.Requests() {
		if strings.Contains(fmt.Sprint(r.Params), marker) {
			t.Fatalf("a core request carried the secret: %s %v", r.Method, r.Params)
		}
	}
}

// A failed prelude step ends the wrapper with a code; the launch is never
// registered, ready or enrolled.
func TestFailedPreludeIsNeverRegistered(t *testing.T) {
	w := newWorld(t)
	l := w.launchOf("gamma", "gamma-orchestrator", "orchestrator", "claude")
	l.Descriptor.StepsDir = filepath.Join(w.dir, "steps")
	l.Descriptor.Prelude = []launch.NumberedStep{{N: 1, Step: launch.Step{Command: launch.Command{Argv: []string{"/bin/sh", "-c", "exit 3"}}}}}
	rec := w.spawn(l)
	_, wr, err := w.register(l, rec.TerminalAddress, nil, nil)
	if wr.code != launch.ExitPrelude || wr.argv != nil {
		t.Fatalf("wrapper = %+v", wr)
	}
	if core.CodeOf(err) != core.CodeLaunchFailed || !strings.Contains(err.Error(), "exited 3") {
		t.Fatalf("register: %v", err)
	}
	if got := w.record(l.Intent); got.Evidence.PID != 0 {
		t.Fatalf("a failed launch was registered: %+v", got.Evidence)
	}
	if v, _, _ := w.p.ProbeRegister(testCtx(t), l); v != core.ProbeAbsent {
		t.Fatalf("probe = %v", v)
	}
	if _, err := w.p.Enrollable(testCtx(t), []string{l.Intent}); core.CodeOf(err) != core.CodeIdentityNotProven {
		t.Fatalf("enrollable: %v", err)
	}
}

// A core started under another agent's environment leaks nothing of it.
func TestEnvironmentStartsEmpty(t *testing.T) {
	w := newWorld(t)
	l := w.launchOf("delta", "delta-orchestrator", "orchestrator", "claude")
	l.Descriptor.Env = map[string]string{"HERDSMAN_INBOX": "/inbox", "CLAUDE_CONFIG_DIR": "/cfg"}
	rec := w.spawn(l)
	core := []string{"CLAUDECODE=1", "HERDR_PANE_ID=p7", "OPENAI_BASE_URL=http://other", "ANTHROPIC_API_KEY=k", "CODEX_THREAD_ID=x",
		"HC_SOCKET=/run/hc.sock", "HC_TERMINAL_ID=t9", "TERM=xterm-ghostty", "PATH=/agent/bin", "HOME=/elsewhere"}
	_, wr, err := w.register(l, rec.TerminalAddress, core, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := envMap(wr.execEnv)
	want := map[string]string{"HC_SOCKET": "/run/hc.sock", "HC_TERMINAL_ID": "t9", "TERM": "xterm-ghostty", "PATH": "/usr/bin:/bin",
		"HOME": "/home/u", "HERDSMAN_INBOX": "/inbox", "CLAUDE_CONFIG_DIR": "/cfg"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("harness env = %v\nwant %v", got, want)
	}
	bad := l.Descriptor
	bad.Env = map[string]string{"HERDR_PANE_ID": "p1"}
	if err := bad.Validate(); err == nil {
		t.Fatal("a descriptor may set HERDR_PANE_ID")
	}
}

// A process that is not the reserved terminal's program is refused, and
// nothing is registered.
func TestWrapperOfAnotherProcessIsRefused(t *testing.T) {
	w := newWorld(t)
	l := w.launchOf("eps", "eps-orchestrator", "orchestrator", "claude")
	rec := w.spawn(l)
	stranger := w.procs.add(1, "sh")
	done := make(chan error, 1)
	go func() {
		_, err := w.p.RegisterStep(testCtx(t), l, core.Timeouts{Register: 5 * time.Second, Poll: 20 * time.Millisecond})
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	w.peer.Store(int64(stranger))
	wr := &launch.Wrapper{Socket: w.reg.Path, Intent: l.Intent, Environ: []string{}, Stderr: &strings.Builder{}, DialWait: time.Second,
		Exec: func(string, []string, []string) error { t.Fatal("a refused wrapper started its harness"); return nil }}
	if code := wr.Main(testCtx(t)); code != launch.ExitRefused {
		t.Fatalf("wrapper code = %d", code)
	}
	if err := <-done; core.CodeOf(err) != core.CodeLaunchFailed {
		t.Fatalf("register: %v", err)
	}
	if got := w.record(l.Intent); got.Evidence.PID != 0 {
		t.Fatalf("registered: %+v", got)
	}
	_ = rec
}

// Crash points: whatever was or was not done before the daemon died, a retry
// adopts the one verified terminal, refuses an ambiguous one, and never
// starts a second.
func TestUncertainSpawnIsReconciled(t *testing.T) {
	ctx := testCtx(t)
	t.Run("crash after the record, before the run", func(t *testing.T) {
		w := newWorld(t)
		l := w.launchOf("r1", "r1-o", "orchestrator", "claude")
		if v, _, _ := w.p.ProbeSpawn(ctx, l); v != core.ProbeAbsent {
			t.Fatalf("probe = %v", v)
		}
		w.spawn(l)
		if n := w.core.Calls(hc.MethodRun); n != 1 {
			t.Fatalf("runs = %d", n)
		}
	})
	t.Run("crash after the run, reply lost", func(t *testing.T) {
		w := newWorld(t)
		l := w.launchOf("r2", "r2-o", "orchestrator", "claude")
		c := w.dial()
		if _, err := core.Spawn(ctx, c, l); err != nil { // the run whose answer was lost
			t.Fatal(err)
		}
		if v, _, _ := w.p.ProbeSpawn(ctx, l); v != core.ProbeMatches {
			t.Fatalf("probe = %v", v)
		}
		rec, err := w.p.SpawnStep(ctx, l)
		if err != nil || rec.TerminalAddress == "" {
			t.Fatalf("spawn: %+v %v", rec, err)
		}
		if n := w.core.Calls(hc.MethodRun); n != 1 {
			t.Fatalf("a second terminal was started: %d runs", n)
		}
	})
	t.Run("two matches", func(t *testing.T) {
		w := newWorld(t)
		l := w.launchOf("r3", "r3-o", "orchestrator", "claude")
		c := w.dial()
		labels, _ := l.Labels().Map()
		for range 2 {
			if _, err := c.Run(ctx, hc.RunRequest{Argv: []string{"sh"}, Labels: labels, Keep: true}); err != nil {
				t.Fatal(err)
			}
		}
		if v, d, _ := w.p.ProbeSpawn(ctx, l); v != core.ProbeDifferent {
			t.Fatalf("probe = %v %s", v, d)
		}
		if _, err := w.p.SpawnStep(ctx, l); core.CodeOf(err) != core.CodeConflict {
			t.Fatalf("spawn: %v", err)
		}
		if n := w.core.Calls(hc.MethodRun); n != 2 {
			t.Fatalf("runs = %d", n)
		}
	})
	t.Run("copied labels", func(t *testing.T) {
		w := newWorld(t)
		l := w.launchOf("r4", "r4-o", "orchestrator", "claude")
		w.core.Add(map[string]string{herdrapp.LabelExecution: l.Execution, herdrapp.LabelRoute: "someone-else"})
		if _, err := w.p.SpawnStep(ctx, l); core.CodeOf(err) != core.CodeConflict {
			t.Fatalf("spawn: %v", err)
		}
	})
	t.Run("spawned and gone from core", func(t *testing.T) {
		w := newWorld(t)
		l := w.launchOf("r5", "r5-o", "orchestrator", "claude")
		rec := w.spawn(l)
		w.core.Mutate(rec.TerminalAddress, func(m map[string]any) { m["labels"] = map[string]string{} })
		if v, d, _ := w.p.ProbeSpawn(ctx, l); v != core.ProbeUnknown || !strings.Contains(d, core.CodeOutcomeUnknown) {
			t.Fatalf("probe = %v %s", v, d)
		}
		if _, err := w.p.SpawnStep(ctx, l); core.CodeOf(err) != core.CodeOutcomeUnknown {
			t.Fatalf("spawn: %v", err)
		}
		if n := w.core.Calls(hc.MethodRun); n != 1 {
			t.Fatalf("runs = %d", n)
		}
	})
	t.Run("registered, then another process at the address", func(t *testing.T) {
		w := newWorld(t)
		l, rec := w.up("r6", "r6-o", "orchestrator", "claude")
		w.setProcess(rec.TerminalAddress, w.procs.add(w.holder, "-zsh"))
		if v, d, _ := w.p.ProbeSpawn(ctx, l); v != core.ProbeDifferent || !strings.Contains(d, "recovery shell") {
			t.Fatalf("probe = %v %s", v, d)
		}
		if v, _, _ := w.p.ProbeRegister(ctx, l); v != core.ProbeDifferent {
			t.Fatalf("probe register = %v", v)
		}
	})
}

// The registrar's evidence is the store's: a second running execution with
// the same process is refused.
func TestRecordsRefuseOneProcessTwice(t *testing.T) {
	m := &core.MemRecords{}
	ctx := context.Background()
	ev := runtime.ProcessEvidence{PID: 7, Start: "s"}
	if _, err := m.Put(ctx, runtime.Execution{ID: "a", LaunchIntent: "i1", Agent: "x", Evidence: ev, Desired: runtime.DesiredRunning}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Put(ctx, runtime.Execution{ID: "b", LaunchIntent: "i2", Agent: "y", Evidence: ev, Desired: runtime.DesiredRunning}); err == nil {
		t.Fatal("one process became two executions")
	}
	got, _ := m.Put(ctx, runtime.Execution{ID: "c", LaunchIntent: "i1", Agent: "x"})
	if got.ID != "a" || got.Evidence != ev {
		t.Fatalf("a repeated intent is not the same execution: %+v", got)
	}
}

func TestCoreNamesAreBounded(t *testing.T) {
	n := core.CoreName(strings.Repeat("Route_Name!", 9), "orchestrator-of-everything", runtime.NewExecutionID())
	if len(n) > 48 || strings.ContainsFunc(n, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') }) {
		t.Fatalf("name %q", n)
	}
	if a, b := core.OpaqueID("a", "b"), core.OpaqueID("a", "b"); a != b || a == core.OpaqueID("ab") {
		t.Fatal("opaque ids are not stable and distinct")
	}
	if !slices.Equal(core.Kinds(), []string{"claude", "codex"}) {
		t.Fatalf("kinds = %v", core.Kinds())
	}
	if !errors.Is(&core.Error{Code: core.CodeConflict, Msg: "x"}, &core.Error{Code: core.CodeConflict}) {
		t.Fatal("errors.Is on a code")
	}
}
