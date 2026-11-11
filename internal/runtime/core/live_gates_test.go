package core_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/hc/hclive"
	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/launch"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

// The gates of spec section 17 that only a real core can show, each against
// a private core of the installed hc. They are opt-in, like the rest of the
// live gates (live_test.go).

// Core path: a launch on a new installation uses core and succeeds with no
// TUI binary on the path and no TUI server, and the record keeps its backend.
func TestLiveCorePath(t *testing.T) {
	w := newLive(t, hclive.Options{})
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(w.core.Dir, "no-tui-server.sock"))
	if p, err := exec.LookPath("herdr"); err == nil {
		t.Fatalf("a herdr TUI binary is on the path: %s", p)
	}
	w.gOpenApp()
	ctx := testCtx(t)
	pre, err := core.Preflight(ctx, w.gDial(), core.PreflightInput{Kind: "claude", Argv: []string{"sleep"}, Roots: []string{w.core.Dir}})
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if !sameFile(pre.Program, "/bin/sleep") || pre.Core.ID != w.coreID {
		t.Fatalf("preflight = %+v", pre)
	}
	l, rec := w.gUp("alpha", "alpha-orchestrator", "orchestrator", "claude")
	raw, err := w.core.GetRaw(rec.TerminalAddress)
	if err != nil {
		t.Fatal(err)
	}
	if raw["keep"] != true || raw["rm"] != false || raw["parent"] != nil || raw["notify"] != false {
		t.Fatalf("core's record of the launch: keep %v rm %v parent %v notify %v", raw["keep"], raw["rm"], raw["parent"], raw["notify"])
	}
	if labels, _ := raw["labels"].(map[string]any); labels[herdrapp.LabelExecution] != l.Execution || labels[herdrapp.LabelTab] != l.Placement.Tab {
		t.Fatalf("labels = %v", raw["labels"])
	}
	rec = w.gReady(l)
	if rec.Observation.Readiness != runtime.ReadinessReady || rec.Observation.Visibility != runtime.VisibilityVerified || rec.App.PaneID == "" {
		t.Fatalf("ready record = %+v %+v", rec.Observation, rec.App)
	}
	if _, ok, _ := w.bindings.Load(w.appHome, w.coreID); !ok {
		t.Fatal("the binding was not persisted")
	}
	if _, err := w.p.Enrollable(ctx, []string{l.Intent}); err != nil {
		t.Fatalf("enrollable: %v", err)
	}
	if rec.Backend != runtime.BackendCore || rec.EndpointID != w.coreID || rec.Experiment != runtime.ExperimentLabel {
		t.Fatalf("persisted backend %q endpoint %q experiment %q", rec.Backend, rec.EndpointID, rec.Experiment)
	}
	for _, m := range []string{hc.MethodSend, "terminal.keys"} {
		if n := w.gCalls(m); n != 0 {
			t.Fatalf("%s was called %d times", m, n)
		}
	}
	if _, err := w.p.Stop(ctx, runtime.Target{ExecutionID: rec.ID}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if alive, _ := core.Alive(core.SystemProcs{}, rec.Evidence); alive {
		t.Fatalf("pid %d runs after the stop", rec.Evidence.PID)
	}
	waitFor(t, func() bool {
		term, err := w.direct().Get(ctx, rec.TerminalAddress)
		return err == nil && !term.Live()
	})
	if got := w.gRecord(l.Intent); got.Desired != runtime.DesiredStopped || got.Backend != runtime.BackendCore {
		t.Fatalf("after the stop: %s %s", got.Desired, got.Backend)
	}
}

func sameFile(a, b string) bool {
	x, err := os.Stat(a)
	if err != nil {
		return false
	}
	y, err := os.Stat(b)
	return err == nil && os.SameFile(x, y)
}

// Secrets at rest and the prelude's environment: a granted marker secret
// reaches the prelude and the harness; a denied value in core's own
// environment reaches neither; and the marker is in no record core keeps,
// as terminal.get and terminal.list read it and in its state folders, after
// the launch, after the loss of the program and after the finish. The
// recovery shell's environment does not hold it.
func TestLiveSecretsAndPreludeEnvironment(t *testing.T) {
	const marker = "hs-live-marker-secret-5d1e"
	const denied = "hs-live-denied-secret-9c4a"
	w := newLive(t, hclive.Options{Env: []string{"DENIED=" + denied}})
	ctx := testCtx(t)
	kc, _ := json.Marshal(map[string]string{"hs-test/granted": marker})
	if err := os.WriteFile(w.keychain, kc, 0o600); err != nil {
		t.Fatal(err)
	}
	seen := filepath.Join(w.core.Dir, "prelude-seen")
	l := w.gLaunch("beta", "beta-orchestrator", "orchestrator", "claude")
	l.Descriptor.StepsDir = filepath.Join(w.core.Dir, "steps")
	l.Descriptor.SecretRefs = "GRANTED=hs-test/granted"
	l.Descriptor.Prelude = []launch.NumberedStep{{N: 1, Step: launch.Step{Command: launch.Command{Argv: []string{"/bin/sh", "-c", `printf '%s|%s' "$GRANTED" "$DENIED" > ` + seen}}}}}
	rec := w.upLaunch(l)
	if b, err := os.ReadFile(seen); err != nil || string(b) != marker+"|" {
		t.Fatalf("the prelude saw %q (%v)", b, err)
	}
	env := w.harnessEnv(l.Execution)
	if env["GRANTED"] != marker || env["DENIED"] != "" {
		t.Fatalf("harness env: GRANTED %q DENIED %q", env["GRANTED"], env["DENIED"])
	}
	atRest := func(when string) {
		t.Helper()
		raw, err := w.core.GetRaw(rec.TerminalAddress)
		if err != nil {
			t.Fatal(err)
		}
		if s := fmt.Sprint(raw); strings.Contains(s, marker) || strings.Contains(s, "GRANTED") {
			t.Fatalf("%s: terminal.get holds the secret: %s", when, s)
		}
		ls, err := w.core.ListRaw()
		if err != nil || strings.Contains(string(ls), marker) {
			t.Fatalf("%s: terminal.list holds the secret (%v): %s", when, err, ls)
		}
		all, err := w.gDial().List(ctx, nil, true)
		if err != nil || strings.Contains(fmt.Sprintf("%+v", all), marker) {
			t.Fatalf("%s: the provider's list holds the secret (%v)", when, err)
		}
		if files := w.core.StateHolds(marker); len(files) != 0 {
			t.Fatalf("%s: core's folders hold the secret: %v", when, files)
		}
	}
	// Core keeps its records in the folders scanned: the scan can see.
	if len(w.core.StateHolds(l.Execution)) == 0 {
		t.Fatal("core's folders hold no record of the launch; the scan proves nothing")
	}
	atRest("after the launch")
	w.gLose(rec)
	atRest("after the loss")
	shell, ok := w.core.ShellEnv(rec.TerminalAddress)
	if !ok {
		t.Fatal("no recovery shell started")
	}
	if s := strings.Join(shell, "\n"); strings.Contains(s, marker) || strings.Contains(s, "GRANTED=") {
		t.Fatalf("the recovery shell holds the secret:\n%s", s)
	}
	// A person closes the shell: the terminal finishes.
	if _, err := w.direct().Kill(ctx, rec.TerminalAddress); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		term, err := w.direct().Get(ctx, rec.TerminalAddress)
		return err == nil && !term.Live()
	})
	atRest("after the finish")
	for _, r := range w.proxy.Requests() {
		if strings.Contains(r, marker) {
			t.Fatalf("a request to core carried the secret: %s", r)
		}
	}
}

// Prelude environment: a launch whose prelude fails is never registered,
// ready or enrolled, and its terminal finishes with no shell left.
func TestLiveFailedPreludeLeavesNoShell(t *testing.T) {
	w := newLive(t, hclive.Options{})
	ctx := testCtx(t)
	l := w.gLaunch("gamma", "gamma-orchestrator", "orchestrator", "claude")
	l.Descriptor.StepsDir = filepath.Join(w.core.Dir, "steps")
	l.Descriptor.Prelude = []launch.NumberedStep{{N: 1, Step: launch.Step{Command: launch.Command{Argv: []string{"/bin/sh", "-c", "exit 3"}}}}}
	rec, err := w.p.SpawnStep(ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.gRegister(l); core.CodeOf(err) != core.CodeLaunchFailed {
		t.Fatalf("register: %v", err)
	}
	if got := w.gRecord(l.Intent); got.Evidence.PID != 0 {
		t.Fatalf("a failed launch was registered: %+v", got.Evidence)
	}
	if v, _, _ := w.p.ProbeRegister(ctx, l); v != core.ProbeAbsent {
		t.Fatalf("probe = %v", v)
	}
	if _, err := w.p.Enrollable(ctx, []string{l.Intent}); core.CodeOf(err) != core.CodeIdentityNotProven {
		t.Fatalf("enrollable: %v", err)
	}
	var term hc.Terminal
	waitFor(t, func() bool {
		term, err = w.direct().Get(ctx, rec.TerminalAddress)
		return err == nil && !term.Live()
	})
	if term.ExitCode == nil || *term.ExitCode != launch.ExitPrelude {
		t.Fatalf("the wrapper's end: %+v", term)
	}
	// A recovery shell starts within about two seconds of a loss; none
	// comes after an exit with a code under keep.
	time.Sleep(3 * time.Second)
	if term, err = w.direct().Get(ctx, rec.TerminalAddress); err != nil || term.Live() {
		t.Fatalf("the terminal came back: %+v %v", term, err)
	}
	if _, ok := w.core.ShellEnv(rec.TerminalAddress); ok {
		t.Fatal("a shell started after the failed prelude")
	}
	// The finished record is found by its labels, so a retry of the step
	// adopts it and starts nothing.
	if v, d, _ := w.p.ProbeSpawn(ctx, l); v != core.ProbeMatches || !strings.Contains(d, "ended before the launch registered") {
		t.Fatalf("probe = %v %s", v, d)
	}
	if _, err := w.p.SpawnStep(ctx, l); err != nil {
		t.Fatalf("spawn again: %v", err)
	}
	if n := w.gCalls(hc.MethodRun); n != 1 {
		t.Fatalf("runs = %d", n)
	}
}

// Environment: a core started under another agent's environment passes none
// of it to a managed harness; argv[0] resolves on the core's PATH, not on the
// daemon's.
func TestLiveEnvironmentStartsEmpty(t *testing.T) {
	corebin := t.TempDir()
	if err := os.Symlink("/bin/sleep", filepath.Join(corebin, "hs-live-standin")); err != nil {
		t.Fatal(err)
	}
	polluted := map[string]string{
		"HERDR_PANE_ID": "p-polluted", "HERDR_ENV": "1", "CLAUDECODE": "1", "CLAUDE_CODE_ENTRYPOINT": "polluted",
		"CODEX_THREAD_ID": "thread-polluted", "OPENAI_BASE_URL": "http://polluted.invalid", "ANTHROPIC_API_KEY": "key-polluted",
		"HERDSMAN_INBOX": "/polluted/inbox", "HC_TERMINAL_ID": "t-polluted", "HC_SCOPE": "scope-polluted",
	}
	var env []string
	for k, v := range polluted {
		env = append(env, k+"="+v)
	}
	env = append(env, "PATH="+corebin+":/usr/bin:/bin")
	w := newLive(t, hclive.Options{Env: env})
	ctx := testCtx(t)
	pre, err := core.Preflight(ctx, w.gDial(), core.PreflightInput{Kind: "claude", Argv: []string{"hs-live-standin"}})
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if pre.Program != filepath.Join(corebin, "hs-live-standin") || !sameFile(pre.Program, "/bin/sleep") {
		t.Fatalf("argv[0] resolved to %q", pre.Program)
	}
	if _, err := exec.LookPath("hs-live-standin"); err == nil {
		t.Fatal("the test's own PATH finds the program; the check proves nothing")
	}
	l, rec := w.gUp("delta", "delta-orchestrator", "orchestrator", "claude")
	got := w.harnessEnv(l.Execution)
	for k, v := range polluted {
		if got[k] == v {
			t.Errorf("%s=%s reached the harness", k, v)
		}
	}
	if got["HC_TERMINAL_ID"] != rec.TerminalAddress || got["PATH"] != "/usr/bin:/bin" || got["HOME"] != w.core.Dir {
		t.Errorf("harness env: HC_TERMINAL_ID %q PATH %q HOME %q", got["HC_TERMINAL_ID"], got["PATH"], got["HOME"])
	}
	// The core was polluted: its recovery shell, which gets core's own
	// environment, shows it.
	w.gLose(rec)
	shell, ok := w.core.ShellEnv(rec.TerminalAddress)
	if !ok || envMap(shell)["HERDR_PANE_ID"] != "p-polluted" {
		t.Fatalf("the core did not run under the polluted environment: %v", ok)
	}
}

// Inventory: a member, a child agent that reports a status, a child that
// runs a known harness name and has reported nothing, an unclassified child,
// a declared command, a foreign agent and an impostor with the route's
// labels and no registration, read from a real core.
func TestLiveInventory(t *testing.T) {
	w := newLive(t, hclive.Options{})
	ctx := testCtx(t)
	bin := filepath.Join(w.core.Dir, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	codex := filepath.Join(bin, "codex")
	if err := os.Symlink("/bin/sleep", codex); err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(w.core.Dir, "agent.sh")
	if err := os.WriteFile(agent, []byte(osc("working")+"\nexec /bin/sleep "+w.mark+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w.children = []string{
		w.hcLine("run --bg --name inv-agent -- /bin/sh " + agent),
		w.hcLine("run --bg --name inv-codex -- " + codex + " " + w.mark),
		w.hcLine("run --bg --name inv-plain -- /bin/sleep " + w.mark),
		w.hcLine("run --bg --name inv-declared -- /bin/sleep " + w.mark),
	}
	l, rec := w.gUp("inv", "inv-o", "orchestrator", "claude")
	w.children = nil
	c := w.direct()
	labels, _ := l.Labels().Map()
	labels[herdrapp.LabelExecution] = runtime.NewExecutionID()
	impostor, err := c.Run(ctx, hc.RunRequest{Argv: []string{"/bin/sleep", w.mark}, Labels: labels, Name: "inv-impostor"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := c.Run(ctx, hc.RunRequest{Argv: []string{"/bin/sh", agent}, Name: "inv-foreign"})
	if err != nil {
		t.Fatal(err)
	}
	addr := map[string]string{}
	waitFor(t, func() bool {
		for _, name := range []string{"inv-agent", "inv-codex", "inv-plain", "inv-declared"} {
			term, err := c.Get(ctx, name)
			if err != nil || term.Parent != rec.TerminalAddress {
				return false
			}
			addr[name] = term.Address
		}
		a, _ := c.Get(ctx, "inv-agent")
		f, _ := c.Get(ctx, "inv-foreign")
		return a.Reported && f.Reported
	})
	want := map[string]core.Class{
		rec.TerminalAddress: core.ClassMember, addr["inv-agent"]: core.ClassAgentChild, addr["inv-codex"]: core.ClassAgentChild,
		addr["inv-plain"]: core.ClassUnclassifiedChild, addr["inv-declared"]: core.ClassDeclaredCommand,
		impostor.Address: core.ClassImpostor, foreign.Address: core.ClassForeignAgent,
	}
	faults := map[string]bool{addr["inv-agent"]: true, addr["inv-codex"]: true, impostor.Address: true}
	live, err := c.List(ctx, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	execs, _ := w.records.List(ctx, "", "")
	procs, err := core.SystemProcs{}.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	in := core.InventoryInput{Terminals: live.Terminals, Executions: execs, Repo: "/repo", Route: "inv", RouteID: l.RouteID,
		Declared: map[string]bool{addr["inv-declared"]: true}, Procs: procs, HarnessNames: []string{"claude", "codex"}}
	check := func(inv core.Inventory, faults map[string]bool) {
		t.Helper()
		got := map[string]core.Entry{}
		for _, e := range inv.Entries {
			got[e.Address] = e
		}
		for a, class := range want {
			if got[a].Class != class {
				t.Errorf("%s is %s, want %s", a, got[a].Class, class)
			}
			if got[a].Fault != faults[a] {
				t.Errorf("%s (%s): fault %v under rule %s", a, class, got[a].Fault, inv.Rule)
			}
		}
	}
	check(core.Classify(in), faults)
	in.Restricted = true
	strict := map[string]bool{addr["inv-plain"]: true, foreign.Address: true}
	for a := range faults {
		strict[a] = true
	}
	check(core.Classify(in), strict)

	// The reconciler reads the same inventory from the core and raises the
	// fault, naming the impostor and the agent children.
	ev := &events{}
	r := w.gReconciler(ev)
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	inv, ok := r.RouteInventory("/repo", "inv")
	if !ok || inv.OK() {
		t.Fatalf("the reconciler's inventory: %+v", inv)
	}
	text := ev.of(core.EventInventoryFault)
	if len(text) != 1 {
		t.Fatalf("faults raised: %+v", ev.list)
	}
	for _, a := range []string{impostor.Address, addr["inv-agent"], addr["inv-codex"]} {
		if !strings.Contains(text[0].Detail, a+" (") {
			t.Errorf("the fault does not name %s: %s", a, text[0].Detail)
		}
	}
}
