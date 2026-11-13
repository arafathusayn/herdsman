package coreconfig_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/runtime/coreconfig"
	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
	"google.golang.org/protobuf/proto"
)

// The app default is a copy, kept here so the CLI can resolve config without
// the app adapter; it must not drift from the adapter's own.
func TestDefaultAppBinaryMatchesTheAdapter(t *testing.T) {
	if coreconfig.DefaultAppBinary != herdrapp.DefaultBinary {
		t.Fatalf("DefaultAppBinary %q, adapter default %q", coreconfig.DefaultAppBinary, herdrapp.DefaultBinary)
	}
}

// agentEnv is the environment of a daemon started under another agent: its
// core socket, session and terminal must never pick a route's endpoint.
func agentEnv(k string) string {
	return map[string]string{"HC_SOCKET": "/agent/core.sock", "HC_SESSION": "agent", "HC_TERMINAL_ID": "t9", "XDG_RUNTIME_DIR": "/run"}[k]
}

func resolve(t *testing.T, rt *configv1.Runtime) (coreconfig.Settings, error) {
	t.Helper()
	return coreconfig.Resolve(coreconfig.Input{Runtime: rt, Getenv: agentEnv, ProjectPath: "/repo/herdsman.config.toml",
		Sources: map[string]string{
			"runtime.core.binary": "project", "runtime.core.socket": "project", "runtime.core.session": "project",
			"runtime.core.connect_timeout": "project", "runtime.core.ready_timeout": "env", "runtime.app.binary": "project",
			"runtime.app.home": "project", "runtime.app.visibility_timeout": "project", "runtime.tui_view.socket": "project",
		}})
}

// With nothing set, every key has its documented default, said so, and the
// endpoint is hc's default session under the path rules, whatever HC_SOCKET
// and HC_SESSION say.
func TestDefaults(t *testing.T) {
	s, err := resolve(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]coreconfig.Setting{
		"binary": {Value: "hc", From: "default"}, "socket": {Value: "/run/herdr-core/default.sock", From: "default, session default"},
		"connect": {Value: "5s", From: "default"}, "ready": {Value: "1m30s", From: "default"}, "visibility": {Value: "15s", From: "default"},
		"app binary": {Value: "herdr-app", From: "default"}, "tui view": {Value: "off", From: "default"},
	}
	got := map[string]coreconfig.Setting{
		"binary": s.Binary, "socket": s.Socket, "connect": s.ConnectTimeout, "ready": s.ReadyTimeout, "visibility": s.VisibilityTimeout,
		"app binary": s.AppBinary, "tui view": s.TUIView,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %+v; want %+v", k, got[k], w)
		}
	}
	if s.Explicit || s.Connect != 5*time.Second || s.Ready != 90*time.Second || s.Visibility != 15*time.Second || s.ConnectSet || s.ReadySet || s.VisibilitySet {
		t.Errorf("settings %+v", s)
	}
	if s.App() != (coreconfig.App{}) {
		t.Errorf("app %+v", s.App())
	}
}

// Each key is read and says where it came from.
func TestEachKeyIsRead(t *testing.T) {
	s, err := resolve(t, &configv1.Runtime{
		Core: &configv1.RuntimeCore{Binary: proto.String("/opt/hc"), Socket: proto.String("/srv/hc.sock"), ConnectTimeout: proto.String("2s"), ReadyTimeout: proto.String("3m")},
		App:  &configv1.RuntimeApp{Binary: proto.String("/opt/herdr-app"), Home: proto.String("/srv/app"), VisibilityTimeout: proto.String("40s")},
	})
	if err != nil {
		t.Fatal(err)
	}
	project := "project file /repo/herdsman.config.toml"
	for name, c := range map[string]struct{ got, want coreconfig.Setting }{
		"binary":     {s.Binary, coreconfig.Setting{Value: "/opt/hc", From: project}},
		"socket":     {s.Socket, coreconfig.Setting{Value: "/srv/hc.sock", From: project}},
		"connect":    {s.ConnectTimeout, coreconfig.Setting{Value: "2s", From: project}},
		"ready":      {s.ReadyTimeout, coreconfig.Setting{Value: "3m0s", From: "env"}},
		"app binary": {s.AppBinary, coreconfig.Setting{Value: "/opt/herdr-app", From: project}},
		"app home":   {s.AppHome, coreconfig.Setting{Value: "/srv/app", From: project}},
		"visibility": {s.VisibilityTimeout, coreconfig.Setting{Value: "40s", From: project}},
	} {
		if c.got != c.want {
			t.Errorf("%s = %+v; want %+v", name, c.got, c.want)
		}
	}
	if !s.Explicit || s.Connect != 2*time.Second || s.Ready != 3*time.Minute || s.Visibility != 40*time.Second || !s.ConnectSet || !s.ReadySet || !s.VisibilitySet {
		t.Errorf("settings %+v", s)
	}
	if s.Endpoint() != (coreconfig.Endpoint{Socket: "/srv/hc.sock", Binary: "/opt/hc"}) || s.App() != (coreconfig.App{Binary: "/opt/herdr-app", Home: "/srv/app"}) {
		t.Errorf("endpoint %+v app %+v", s.Endpoint(), s.App())
	}
	// A session resolves through hc's path rules, not HC_SESSION.
	s, err = resolve(t, &configv1.Runtime{Core: &configv1.RuntimeCore{Session: proto.String("work")}})
	if err != nil || s.Socket.Value != "/run/herdr-core/work.sock" || s.Session.Value != "work" || !s.Explicit {
		t.Fatalf("session: %+v %v", s, err)
	}
	if s.Endpoint() != (coreconfig.Endpoint{Socket: "/run/herdr-core/work.sock", Session: "work", Binary: "hc"}) {
		t.Errorf("session endpoint %+v", s.Endpoint())
	}
	// A socket and a session that agree are one endpoint.
	if s, err = resolve(t, &configv1.Runtime{Core: &configv1.RuntimeCore{Session: proto.String("work"), Socket: proto.String("/run/herdr-core/work.sock")}}); err != nil || s.Socket.Value != "/run/herdr-core/work.sock" {
		t.Fatalf("agreeing socket and session: %+v %v", s, err)
	}
}

// A socket and a session that name different endpoints stop the run with
// both named.
func TestContradictoryEndpointFails(t *testing.T) {
	_, err := resolve(t, &configv1.Runtime{Core: &configv1.RuntimeCore{Socket: proto.String("/srv/hc.sock"), Session: proto.String("work")}})
	if err == nil || !strings.Contains(err.Error(), `"/srv/hc.sock"`) || !strings.Contains(err.Error(), `"work"`) || !strings.Contains(err.Error(), "contradict") {
		t.Fatalf("got %v", err)
	}
}

// Values that cannot be used fail with the key named.
func TestInvalidValuesFail(t *testing.T) {
	for name, tc := range map[string]struct {
		rt   *configv1.Runtime
		want string
	}{
		"connect":       {&configv1.Runtime{Core: &configv1.RuntimeCore{ConnectTimeout: proto.String("soon")}}, "runtime.core.connect_timeout"},
		"ready":         {&configv1.Runtime{Core: &configv1.RuntimeCore{ReadyTimeout: proto.String("90")}}, "runtime.core.ready_timeout"},
		"visibility":    {&configv1.Runtime{App: &configv1.RuntimeApp{VisibilityTimeout: proto.String("-1s")}}, "runtime.app.visibility_timeout"},
		"zero":          {&configv1.Runtime{Core: &configv1.RuntimeCore{ReadyTimeout: proto.String("0s")}}, "must be positive"},
		"socket":        {&configv1.Runtime{Core: &configv1.RuntimeCore{Socket: proto.String("hc.sock")}}, "socket"},
		"session":       {&configv1.Runtime{Core: &configv1.RuntimeCore{Session: proto.String("a/b")}}, "session"},
		"binary":        {&configv1.Runtime{Core: &configv1.RuntimeCore{Binary: proto.String("")}}, "binary"},
		"app binary":    {&configv1.Runtime{App: &configv1.RuntimeApp{Binary: proto.String("")}}, "[runtime.app] binary"},
		"relative home": {&configv1.Runtime{App: &configv1.RuntimeApp{Home: proto.String("app-home")}}, "[runtime.app] home"},
	} {
		if _, err := resolve(t, tc.rt); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The optional TUI view requires an explicit endpoint;
// a socket alone is accepted and changes nothing.
func TestTUIViewRequiresAnExplicitSocket(t *testing.T) {
	_, err := resolve(t, &configv1.Runtime{TuiView: &configv1.RuntimeTuiView{Enabled: proto.Bool(true)}})
	if !errors.Is(err, coreconfig.ErrTUIView) || !strings.Contains(err.Error(), "absolute socket") {
		t.Fatalf("enabled: %v", err)
	}
	enabled, err := resolve(t, &configv1.Runtime{TuiView: &configv1.RuntimeTuiView{Enabled: proto.Bool(true), Socket: proto.String("/srv/herdr.sock")}})
	if err != nil || enabled.TUIView.Value != "on" {
		t.Fatalf("enabled view: %+v %v", enabled, err)
	}
	s, err := resolve(t, &configv1.Runtime{TuiView: &configv1.RuntimeTuiView{Socket: proto.String("/srv/herdr.sock")}})
	if err != nil || s.TUIView.Value != "off" || s.TUIViewSocket.Value != "/srv/herdr.sock" {
		t.Fatalf("socket alone: %+v %v", s, err)
	}
	if _, err := resolve(t, &configv1.Runtime{TuiView: &configv1.RuntimeTuiView{Enabled: proto.Bool(false)}}); err != nil {
		t.Fatalf("enabled = false: %v", err)
	}
}
