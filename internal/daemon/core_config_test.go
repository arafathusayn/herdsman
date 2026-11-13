package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/coreconfig"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// writeCoreConfig replaces the core harness's config file with the opt-in
// plus extra lines in [runtime.core] and further sections.
func writeCoreConfig(t *testing.T, h *harness, core, rest string) string {
	t.Helper()
	path := filepath.Join(h.ph, "herdsman.config.toml")
	text := "[runtime]\nbackend = \"herdr-core\"\n\n[runtime.core]\nexperimental = true\n" + core + "\n" + rest
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func coreRepo(t *testing.T, h *harness) string {
	t.Helper()
	repo := filepath.Join(h.ph, "proj")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	return repo
}

// journaledStart is the route's latest journaled start.
func journaledStart(t *testing.T, h *harness, repo, name string) *ops.Operation {
	t.Helper()
	starts, err := (&routeService{d: h.d}).starts(route.UpKey(repo, name))
	if err != nil || len(starts) == 0 {
		t.Fatalf("no start of %s: %v", name, err)
	}
	return starts[len(starts)-1]
}

// journaledCore is the core data of a start's launch steps, as journaled.
type journaledCore struct {
	Timeouts struct{ Register, Ready, Visibility, Connect time.Duration } `json:"timeouts"`
	Endpoint *coreconfig.Endpoint                                         `json:"endpoint"`
	App      *coreconfig.App                                              `json:"app"`
}

func coreStepData(t *testing.T, op *ops.Operation, effect string) journaledCore {
	t.Helper()
	for _, st := range op.Steps {
		if st.Effect != effect {
			continue
		}
		var d struct {
			Core *journaledCore `json:"core"`
		}
		if err := json.Unmarshal([]byte(st.Spec), &d); err != nil || d.Core == nil {
			t.Fatalf("step %s: %v", st.Key, err)
		}
		return *d.Core
	}
	t.Fatalf("no %s step", effect)
	return journaledCore{}
}

// The core endpoint comes from the config or the documented default, never
// from the daemon's environment: HC_SOCKET, HC_SESSION and HC_TERMINAL_ID
// of an agent the daemon was started under pick nothing.
func TestCoreEndpointIgnoresTheDaemonsEnvironment(t *testing.T) {
	runDir := shortDir(t)
	env := map[string]string{"HC_SOCKET": "/agent/core.sock", "HC_SESSION": "agent", "HC_TERMINAL_ID": "t9", "XDG_RUNTIME_DIR": runDir}
	getenv := func(k string) string { return env[k] }
	h := newAccountHarnessWith(t, func(_ string, cfg *Config) {
		cfg.Core.Socket, cfg.Core.Getenv = "", getenv
	})
	if got, want := h.d.coreRT().cfg.Socket, filepath.Join(runDir, "herdr-core", "default.sock"); got != want {
		t.Fatalf("the daemon's default endpoint is %s; want %s", got, want)
	}
	t.Setenv("HC_SOCKET", "/agent/core.sock")
	t.Setenv("HC_SESSION", "agent")
	t.Setenv("HC_TERMINAL_ID", "t9")
	optInCore(t, h)
	repo := coreRepo(t, h)
	rc, err := h.d.readRuntimeConfig(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if rc.Settings.Socket.Value != h.d.coreRT().cfg.Socket || rc.Settings.Socket.From != "default, session default" || rc.Settings.Explicit {
		t.Fatalf("a config naming no endpoint: %+v", rc.Settings.Socket)
	}
	// A session the config names goes through hc's path rules, not HC_SESSION.
	writeCoreConfig(t, h, "session = \"work\"", "")
	if rc, err = h.d.readRuntimeConfig(repo, ""); err != nil || rc.Settings.Socket.Value != filepath.Join(runDir, "herdr-core", "work.sock") {
		t.Fatalf("a named session: %+v %v", rc.Settings.Socket, err)
	}
}

// connect_timeout, ready_timeout and visibility_timeout are journaled with
// each launch step, which hands them to the provider; the endpoint and the
// app go with them, and the daemon's own register timeout stays.
func TestCoreTimeoutsAndAppReachTheLaunch(t *testing.T) {
	h, w := newCoreHarness(t)
	c := routeClient(t, h)
	repo := coreRepo(t, h)
	home := filepath.Join(h.ph, "app-home")
	writeCoreConfig(t, h, "connect_timeout = \"3s\"\nready_timeout = \"7s\"", "[runtime.app]\nbinary = \"/opt/herdr-app\"\nhome = \""+home+"\"\nvisibility_timeout = \"2s\"\n")
	if _, err := c.Route.Up(context.Background(), coreUp(repo)); err != nil {
		t.Fatal(err)
	}
	waitHalt(t, c, repo)
	got := coreStepData(t, journaledStart(t, h, repo, "harbor"), "core.visibility")
	if got.Timeouts.Connect != 3*time.Second || got.Timeouts.Ready != 7*time.Second || got.Timeouts.Visibility != 2*time.Second || got.Timeouts.Register != 5*time.Second {
		t.Errorf("journaled timeouts %+v", got.Timeouts)
	}
	if got.Endpoint == nil || got.Endpoint.Socket != w.Core.Socket || got.Endpoint.Binary != "hc" {
		t.Errorf("journaled endpoint %+v", got.Endpoint)
	}
	if got.App == nil || *got.App != (coreconfig.App{Binary: "/opt/herdr-app", Home: home}) {
		t.Errorf("journaled app %+v", got.App)
	}
	// The route's provider shows its agents in that app.
	if p := h.d.coreRT().routeProvider(repo, "harbor"); p.Socket != w.Core.Socket || p.App.Home != home {
		t.Errorf("the route's provider: %s %+v", p.Socket, p.App)
	}
	// The app's program replaces the installed one's.
	c2 := &coreRuntime{cfg: CoreConfig{App: herdrapp.ExecRunner{}, AppHome: "/default"}, app: herdrapp.New(herdrapp.ExecRunner{}, "/default")}
	if a := c2.appOf(coreconfig.App{Binary: "/opt/herdr-app"}); a.Home != "/default" || a.Runner.(herdrapp.ExecRunner).Binary != "/opt/herdr-app" {
		t.Errorf("app binary: %+v", a)
	}
	if a := c2.appOf(coreconfig.App{}); a != c2.app {
		t.Error("an empty choice is not the daemon's own app")
	}
	// A config silent on a timeout keeps the daemon's configured value; one
	// with neither takes the default.
	silent := coreconfig.Settings{Connect: 5 * time.Second, Ready: 90 * time.Second, Visibility: 15 * time.Second}
	if got := h.d.coreRT().timeouts(silent); got.Ready != 5*time.Second || got.Visibility != 300*time.Millisecond || got.Connect != 5*time.Second {
		t.Errorf("silent config: %+v", got)
	}
}

// An existing route keeps the endpoint it started on: a config that names
// another is refused with backend_migration_required, one that names the
// same is accepted, and a new route takes the config's.
func TestCoreRouteKeepsItsPersistedEndpoint(t *testing.T) {
	h, w := newCoreHarness(t)
	c := routeClient(t, h)
	ctx := context.Background()
	repo := coreRepo(t, h)
	if _, err := c.Route.Up(ctx, coreUp(repo)); err != nil {
		t.Fatal(err)
	}
	waitHalt(t, c, repo)
	again := coreUp(repo)
	again.Continue = true
	elsewhere := filepath.Join(h.ph, "elsewhere.sock")
	writeCoreConfig(t, h, "socket = \""+elsewhere+"\"", "")
	_, err := c.Route.Up(ctx, again)
	if codeOf(err) != rpc.FailedPrecondition || !strings.Contains(err.Error(), runtime.CodeBackendMigrationRequired) || !strings.Contains(err.Error(), w.Core.Socket) || !strings.Contains(err.Error(), elsewhere) {
		t.Fatalf("another endpoint for an existing route: %v", err)
	}
	writeCoreConfig(t, h, "socket = \""+w.Core.Socket+"\"", "")
	if _, err := c.Route.Up(ctx, again); err != nil {
		t.Fatalf("the same endpoint: %v", err)
	}
	waitHalt(t, c, repo)
	// A new route plans on the config's endpoint.
	writeCoreConfig(t, h, "socket = \""+elsewhere+"\"", "")
	fresh := coreUp(repo)
	fresh.Route, fresh.DryRun = &corev1.RouteId{Value: "other"}, true
	res, err := c.Route.Up(ctx, fresh)
	if err != nil || !strings.Contains(res.GetStart().GetPlan(), "core: "+elsewhere) {
		t.Fatalf("a new route: %v\n%s", err, res.GetStart().GetPlan())
	}
}

// Config mistakes in [runtime.core], [runtime.app] and [runtime.tui_view]
// stop a run with the key named; a TUI view socket alone is accepted.
func TestCoreConfigMistakesStopTheRun(t *testing.T) {
	h, _ := newCoreHarness(t)
	c := routeClient(t, h)
	ctx := context.Background()
	repo := coreRepo(t, h)
	dry := coreUp(repo)
	dry.DryRun = true
	for name, tc := range map[string]struct{ core, rest, want string }{
		"contradiction": {"socket = \"/srv/hc.sock\"\nsession = \"work\"", "", `"/srv/hc.sock" and session "work" contradict`},
		"duration":      {"ready_timeout = \"soon\"", "", "runtime.core.ready_timeout"},
		"visibility":    {"", "[runtime.app]\nvisibility_timeout = \"0s\"\n", "runtime.app.visibility_timeout"},
		"app home":      {"", "[runtime.app]\nhome = \"relative\"\n", "[runtime.app] home"},
		"tui view":      {"", "[runtime.tui_view]\nenabled = true\n", "absolute socket"},
	} {
		writeCoreConfig(t, h, tc.core, tc.rest)
		_, err := c.Route.Up(ctx, dry)
		if codeOf(err) != rpc.InvalidArgument || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), runtime.CodeInvalidRuntimeConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
	writeCoreConfig(t, h, "", "[runtime.tui_view]\nsocket = \"/srv/herdr.sock\"\n")
	if _, err := c.Route.Up(ctx, dry); err != nil {
		t.Fatalf("a TUI view socket alone: %v", err)
	}
}

// runtime doctor reports each resolved value and where it came from.
func TestRuntimeDoctorReportsEachValueAndItsSource(t *testing.T) {
	h, w := newCoreHarness(t)
	repo := coreRepo(t, h)
	path := writeCoreConfig(t, h, "ready_timeout = \"7s\"", "[runtime.tui_view]\nsocket = \"/srv/herdr.sock\"\n")
	doc, err := (&runtimeService{d: h.d}).Doctor(context.Background(), &daemonv1.RuntimeServiceDoctorRequest{Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, ch := range doc.GetChecks() {
		got[ch.GetName()] = ch.GetDetail()
	}
	for name, want := range map[string]string{
		"core-socket": w.Core.Socket + " (default", "core-binary": "hc (default)", "core-session": "none (default)",
		"connect-timeout": "5s (default)", "ready-timeout": "7s (project file " + path + ")", "app-binary": "herdr-app (default)",
		"app-home": w.AppHome + " (default)", "visibility-timeout": "15s (default)", "tui-view": "optional attachment",
	} {
		if !strings.Contains(got[name], want) {
			t.Errorf("%s: %q; want %q", name, got[name], want)
		}
	}
	if !strings.Contains(got["tui-view"], "/srv/herdr.sock") {
		t.Errorf("tui-view: %q", got["tui-view"])
	}
}
