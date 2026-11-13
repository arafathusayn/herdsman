package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/hc/hclive"
	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

// The app gates open a real herdr app window, on a private core only, with
// an app home of their own. They are opt-in twice over: the live core gates
// (HERDSMAN_HC_LIVE=1) and the app (HERDSMAN_APP_LIVE=1). The person's own
// window, their app home and their core are never read or touched: the
// window is started with HERDR_APP_HOME set to a fresh folder and with the
// private core's HC_SESSION and XDG folders, and only that window is quit.
// Nothing is typed into any terminal.
//
//	HERDSMAN_HC_LIVE=1 HERDSMAN_APP_LIVE=1 go test -run LiveNative ./internal/runtime/core
//
// HERDSMAN_APP_SHOTS names a folder for the window's screenshots; without
// it they go to the test's temporary folder and are removed with it.

// appLiveEnv opts in to the gates that open the herdr app.
const appLiveEnv = "HERDSMAN_APP_LIVE"

// privateWindow is one herdr app window started by a test.
type privateWindow struct {
	t    *testing.T
	home string
	cmd  *exec.Cmd
	done chan error
	// ended is set once the process has been waited for.
	ended bool
}

// requireApp skips t unless the app gates are asked for and the app runs.
func requireApp(t *testing.T) string {
	t.Helper()
	if os.Getenv(appLiveEnv) != "1" {
		t.Skipf("set %s=1 (with %s=1) to open a private herdr app window", appLiveEnv, hclive.EnvVar)
	}
	if goruntime.GOOS != "darwin" || goruntime.GOARCH != "arm64" {
		t.Skipf("the live app gates run on darwin/arm64 only; this is %s/%s", goruntime.GOOS, goruntime.GOARCH)
	}
	bin, err := exec.LookPath(herdrapp.DefaultBinary)
	if err != nil {
		t.Skip("herdr-app is not installed")
	}
	return bin
}

// herdrPIDs are the running herdr app processes: the person's windows,
// which must outlive the test.
func herdrPIDs(t *testing.T, bin string) []int {
	t.Helper()
	target, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatal(err)
	}
	procs, err := core.SystemProcs{}.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for _, p := range procs {
		if len(p.Argv) > 0 && p.Argv[0] == target {
			pids = append(pids, p.PID)
		}
	}
	return pids
}

// openWindow starts a herdr app window on c with the app home home, waits
// until it answers, and quits it when the test ends, before c is stopped.
func openWindow(t *testing.T, bin string, c *hclive.Core, home string) *privateWindow {
	t.Helper()
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	// A fresh home greets with the welcome over the terminals, and its
	// first page does not answer overlay.close. The welcome records its
	// outcome in the home's shared state; a private home starts with it
	// skipped.
	shared := filepath.Join(home, "shared.json")
	if _, err := os.Stat(shared); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(shared, []byte(`{"frame.welcomed":"skipped"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(bin)
	cmd.Env = append(slices.Clone(c.Env), "HERDR_APP_HOME="+home)
	cmd.Dir = c.Dir
	log, err := os.Create(filepath.Join(c.Dir, "app-"+filepath.Base(home)+".log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w := &privateWindow{t: t, home: home, cmd: cmd, done: make(chan error, 1)}
	go func() { w.done <- cmd.Wait(); _ = log.Close() }()
	t.Cleanup(w.quit)
	deadline := time.Now().Add(30 * time.Second)
	for {
		out, err := w.run("ls")
		if err == nil && out.Exit == 0 {
			return w
		}
		if time.Now().After(deadline) {
			t.Fatalf("the private window at %s did not answer: %v %s", home, err, out.Stderr)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// run runs herdr-app against this window only.
func (w *privateWindow) run(args ...string) (herdrapp.Output, error) {
	return herdrapp.ExecRunner{}.Run(context.Background(), herdrapp.Invocation{Home: w.home, Args: args})
}

// status is `herdr-app status` of this window.
func (w *privateWindow) status() windowStatus {
	w.t.Helper()
	out, err := w.run("status")
	if err != nil || out.Exit != 0 {
		w.t.Fatalf("status: %v exit %d %s", err, out.Exit, out.Stderr)
	}
	var s windowStatus
	if err := json.Unmarshal(out.Stdout, &s); err != nil {
		w.t.Fatalf("status: %v: %s", err, out.Stdout)
	}
	return s
}

type windowStatus struct {
	Space       string            `json:"space"`
	Tab         string            `json:"tab"`
	FocusedPane string            `json:"focused_pane"`
	Agent       string            `json:"agent"`
	Overlays    []string          `json:"overlays"`
	Floats      map[string]any    `json:"floats"`
	Errors      []json.RawMessage `json:"errors"`
}

// shot saves the window's last frame as name in the shots folder.
func (w *privateWindow) shot(dir, name string) string {
	w.t.Helper()
	path := filepath.Join(dir, name)
	out, err := w.run("shot", path)
	if err != nil || out.Exit != 0 {
		w.t.Errorf("shot: %v exit %d %s", err, out.Exit, out.Stderr)
		return ""
	}
	w.t.Logf("screenshot: %s", path)
	return path
}

// quit quits this window with its own app.quit, and ends the process the
// test started if that does not; no other window is asked.
func (w *privateWindow) quit() {
	if w.ended {
		return
	}
	select {
	case <-w.done:
		w.ended = true
		return
	default:
	}
	if out, err := w.run("do", "app.quit"); err != nil || out.Exit != 0 {
		w.t.Logf("app.quit on %s: %v exit %d %s", w.home, err, out.Exit, out.Stderr)
	}
	select {
	case <-w.done:
	case <-time.After(15 * time.Second):
		w.t.Errorf("the private window %d did not quit; ending it", w.cmd.Process.Pid)
		_ = w.cmd.Process.Signal(syscall.SIGKILL)
		<-w.done
	}
	w.ended = true
}

func shotsDir(t *testing.T) string {
	if d := os.Getenv("HERDSMAN_APP_SHOTS"); d != "" {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		return d
	}
	return t.TempDir()
}

// Native visibility and the app half of endpoint mismatch, against a real
// herdr app window on private cores (spec section 17): a window on another
// core that shows a same-address terminal never verifies the binding; a
// window on the selected core shows a coordinator and a worker, launched
// through the real launch transaction with the real app adapter, in the
// route's space and their own tabs, listed as agents, and each can be
// brought to the front as the focused pane with nothing over it.
func TestLiveNativeVisibility(t *testing.T) {
	hclive.Require(t)
	bin := requireApp(t)
	shots := shotsDir(t)
	theirs := herdrPIDs(t, bin)
	t.Cleanup(func() {
		alive := herdrPIDs(t, bin)
		for _, pid := range theirs {
			if !slices.Contains(alive, pid) {
				t.Errorf("herdr app process %d, not this test's, is gone", pid)
			}
		}
	})

	w := newLive(t, hclive.Options{})
	ctx := testCtx(t)
	home := filepath.Join(w.core.Dir, "apphome")
	w.p.App = herdrapp.New(herdrapp.ExecRunner{}, home)
	w.appHome = home
	coord := w.gLaunch("probe", "probe-coordinator", "orchestrator", "claude")
	worker := w.gLaunch("probe", "probe-worker", "implementer", "claude")
	coordRec, workerRec := w.upLaunch(coord), w.upLaunch(worker)

	// Endpoint mismatch, app half: the window is on another core, which has
	// a terminal at the coordinator's address with the same name and
	// ownership labels in another placement. The binding is never verified.
	other := hclive.Start(t, hclive.Options{})
	oc, err := hc.Dial(ctx, other.Socket, hc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	labels, _ := coord.Labels().Map()
	labels[herdrapp.LabelSpace], labels[herdrapp.LabelTab] = "hs-elsewhere-space", "hs-elsewhere-tab"
	// Both cores are fresh, so the first terminal of each has the same
	// address.
	same, err := oc.Run(ctx, hc.RunRequest{Argv: []string{"/bin/sleep", w.mark}, Cwd: other.Dir, Name: coord.Name, Labels: labels, Keep: true})
	if err != nil {
		t.Fatalf("a same-named terminal on the other core: %v", err)
	}
	if same.Address != coordRec.TerminalAddress {
		t.Fatalf("the other core gave %s, not the coordinator's address %s", same.Address, coordRec.TerminalAddress)
	}
	_ = oc.Close()
	wrong := openWindow(t, bin, other, home)
	// The window adopts labeled terminals a moment after it opens.
	for deadline := time.Now().Add(15 * time.Second); ; {
		layout, err := w.p.App.List(ctx)
		if err == nil && len(layout.Locate(coordRec.TerminalAddress)) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the window on the other core does not show its %s: %v %+v", coordRec.TerminalAddress, err, layout)
		}
		time.Sleep(250 * time.Millisecond)
	}
	if _, err := w.p.VisibilityStep(ctx, coord, core.Timeouts{Visibility: 3 * time.Second, Poll: 250 * time.Millisecond}); core.CodeOf(err) != core.CodeWaitingVisibility {
		t.Fatalf("visibility against a window on another core: %v", err)
	}
	if _, ok, _ := w.bindings.Load(home, w.coreID); ok {
		t.Fatal("a binding was verified against a window on another core")
	}
	wrong.shot(shots, "endpoint-mismatch.png")
	wrong.quit()

	// Native visibility: the same app home, now opened on the selected core.
	win := openWindow(t, bin, w.core, home)
	timeouts := core.Timeouts{Visibility: 20 * time.Second, Ready: 20 * time.Second, Poll: 250 * time.Millisecond}
	for _, l := range []core.Launch{coord, worker} {
		rec, err := w.p.VisibilityStep(ctx, l, timeouts)
		if err != nil {
			t.Fatalf("visibility of %s: %v", l.Role, err)
		}
		if rec.Observation.Visibility != runtime.VisibilityVerified || rec.App == nil || rec.App.PaneID == "" {
			t.Fatalf("%s: visibility %s, app %+v", l.Role, rec.Observation.Visibility, rec.App)
		}
		if rec, err = w.p.ReadyStep(ctx, l, timeouts); err != nil || rec.Observation.Readiness != runtime.ReadinessReady {
			t.Fatalf("readiness of %s: %v %s", l.Role, err, rec.Observation.Readiness)
		}
	}
	if b, ok, _ := w.bindings.Load(home, w.coreID); !ok || b.CoreID != w.coreID {
		t.Fatalf("the binding to the selected core was not persisted: %+v", b)
	}
	if _, err := w.p.Enrollable(ctx, []string{coord.Intent, worker.Intent}); err != nil {
		t.Fatalf("the dispatch gate: %v", err)
	}

	// The window's own layout: one route space, named after the route,
	// local, and one tab per agent, named after its role, each showing that
	// agent's terminal as a reporting agent.
	layout, err := w.p.App.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pretty, _ := json.MarshalIndent(layout, "", "  ")
	t.Logf("herdr-app ls:\n%s", pretty)
	if len(layout.Spaces) != 1 {
		t.Fatalf("the window shows %d spaces, want the route's one", len(layout.Spaces))
	}
	space := layout.Spaces[0]
	if space.ID != coord.Placement.Space || space.Name != coord.RouteName || !space.Local() {
		t.Fatalf("space %q named %q (local %v), want %q named %q", space.ID, space.Name, space.Local(), coord.Placement.Space, coord.RouteName)
	}
	for _, x := range []struct {
		l   core.Launch
		rec runtime.Execution
	}{{coord, coordRec}, {worker, workerRec}} {
		locs := layout.Locate(x.rec.TerminalAddress)
		if len(locs) != 1 || locs[0].Space != x.l.Placement.Space || locs[0].Tab != x.l.Placement.Tab || !locs[0].Local {
			t.Fatalf("%s at %s is shown at %+v, want space %s tab %s", x.l.Role, x.rec.TerminalAddress, locs, x.l.Placement.Space, x.l.Placement.Tab)
		}
		tab := space.Tabs[slices.IndexFunc(space.Tabs, func(tb herdrapp.Tab) bool { return tb.ID == x.l.Placement.Tab })]
		if tab.Name != x.l.Role || len(tab.Terminals) != 1 || tab.Settled {
			t.Fatalf("%s tab = %+v, want one terminal and the name %q", x.l.Role, tab, x.l.Role)
		}
		if got := w.gRecord(x.l.Intent).App.PaneID; got != locs[0].Pane {
			t.Fatalf("%s record pane %s, window pane %s", x.l.Role, got, locs[0].Pane)
		}
		obs, err := w.p.App.Observe(ctx, herdrapp.Owned{Terminal: x.rec.TerminalAddress, Execution: x.rec.ID, Placement: x.l.Placement})
		if err != nil || !obs.PanePresent || !obs.AgentListed || obs.Rearranged {
			t.Fatalf("%s observed %+v: %v", x.l.Role, obs, err)
		}
		if v, err := w.p.EnsureVisible(ctx, runtime.Target{ExecutionID: x.rec.ID}); err != nil || v != runtime.VisibilityVerified {
			t.Fatalf("%s ensure visible: %s %v", x.l.Role, v, err)
		}
	}

	// Any dialog of the app's own over the terminals is closed; that types
	// nothing into any terminal.
	for deadline := time.Now().Add(10 * time.Second); ; {
		s := win.status()
		if len(s.Overlays) == 0 {
			break
		}
		if time.Now().After(deadline) {
			win.shot(shots, "overlay-stuck.png")
			t.Fatalf("the window keeps an overlay open: %v", s.Overlays)
		}
		t.Logf("closing the app's overlay %s", s.Overlays[0])
		if out, err := win.run("do", "overlay.close", `{"id":"`+s.Overlays[0]+`"}`); err != nil || out.Exit != 0 {
			t.Fatalf("overlay.close: %v exit %d %s %s", err, out.Exit, out.Stdout, out.Stderr)
		}
		time.Sleep(500 * time.Millisecond)
	}

	// The selected terminal is interactive, as far as the window says
	// without typing: Show brings each to the front, and the window then
	// reports its pane focused, in its space and tab, with no dialog,
	// float or error over it. The window has sized the terminal to its
	// pane, so the core terminal is attached to it, not left at the size
	// of a terminal no window shows.
	dc := w.direct()
	hidden, err := dc.Run(ctx, hc.RunRequest{Argv: []string{"/bin/sleep", w.mark}, Cwd: w.core.Dir})
	if err != nil {
		t.Fatal(err)
	}
	hiddenTerm, err := dc.Get(ctx, hidden.Address)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range []struct {
		l   core.Launch
		rec runtime.Execution
	}{{worker, workerRec}, {coord, coordRec}} {
		if err := w.p.Show(ctx, runtime.Target{ExecutionID: x.rec.ID}); err != nil {
			t.Fatalf("show %s: %v", x.l.Role, err)
		}
		pane := w.gRecord(x.l.Intent).App.PaneID
		var s windowStatus
		deadline := time.Now().Add(5 * time.Second)
		for {
			s = win.status()
			if s.FocusedPane == pane || time.Now().After(deadline) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Logf("%s focused: %+v", x.l.Role, s)
		if s.FocusedPane != pane || s.Space != x.l.Placement.Space || s.Tab != x.l.Placement.Tab {
			t.Fatalf("%s: the window focuses pane %s in %s/%s, want %s in %s/%s", x.l.Role, s.FocusedPane, s.Space, s.Tab, pane, x.l.Placement.Space, x.l.Placement.Tab)
		}
		if len(s.Overlays) > 0 || len(s.Floats) > 0 || len(s.Errors) > 0 {
			t.Fatalf("%s: something is over the focused terminal: %+v", x.l.Role, s)
		}
		term, err := dc.Get(ctx, x.rec.TerminalAddress)
		if err != nil {
			t.Fatal(err)
		}
		if !term.Live() || term.Status == nil {
			t.Fatalf("%s: terminal %+v is not a live reporting agent", x.l.Role, term)
		}
		if term.Cols == hiddenTerm.Cols && term.Rows == hiddenTerm.Rows {
			t.Fatalf("%s: the terminal is %dx%d, the size of one no window shows", x.l.Role, term.Cols, term.Rows)
		}
		t.Logf("%s: terminal %s is %dx%d (an unshown one is %dx%d)", x.l.Role, term.Address, term.Cols, term.Rows, hiddenTerm.Cols, hiddenTerm.Rows)
		win.shot(shots, "native-visibility-"+x.l.Role+".png")
	}
	// The agent list follows the last shown agent.
	if s := win.status(); s.Agent != coordRec.TerminalAddress {
		t.Fatalf("the window's selected agent is %q, want the coordinator %s", s.Agent, coordRec.TerminalAddress)
	}
}
