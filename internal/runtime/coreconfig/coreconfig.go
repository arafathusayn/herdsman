// Package coreconfig resolves the [runtime.core], [runtime.app] and
// [runtime.tui_view] keys of a config into the values a run on herdr-core uses,
// each with where it came from (herdr-core backend spec, sections 5, 7, 8 and
// 14). It reads only the config and hc's path rules: a surrounding agent's
// HC_SOCKET, HC_SESSION or HC_TERMINAL_ID never picks a route's endpoint.
package coreconfig

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/arafathusayn/herdsman/internal/hcpath"
	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
)

// The defaults of the keys, as the configuration reference names them.
const (
	DefaultBinary = "hc"
	// DefaultAppBinary matches herdrapp.DefaultBinary; a test holds them equal,
	// since this package must not import the app adapter (a server package).
	DefaultAppBinary         = "herdr-app"
	DefaultConnectTimeout    = 5 * time.Second
	DefaultReadyTimeout      = 90 * time.Second
	DefaultVisibilityTimeout = 15 * time.Second
)

// FromDefault is the source of a value no config set.
const FromDefault = "default"

// Setting is one resolved value and where it came from: "default", or the
// config layer that set it, such as "project file /repo/herdsman.config.toml".
type Setting struct {
	Value string
	From  string
}

func (s Setting) String() string { return s.Value + " (" + s.From + ")" }

// Endpoint is the canonical connection of a route to core: the socket it
// dials, the session that socket was resolved from (empty for an explicit
// socket), and the hc program Herdsman runs for the core. It is persisted with
// the route; an existing route keeps it.
type Endpoint struct {
	Socket  string `json:"socket"`
	Session string `json:"session,omitempty"`
	Binary  string `json:"binary,omitempty"`
}

// App is the app a route's agents are shown in: its program and its home.
// Empty fields are the daemon's defaults.
type App struct {
	Binary string `json:"binary,omitempty"`
	Home   string `json:"home,omitempty"`
}

// Settings are the resolved keys of one config.
type Settings struct {
	Binary            Setting
	Socket            Setting
	Session           Setting
	ConnectTimeout    Setting
	ReadyTimeout      Setting
	AppBinary         Setting
	AppHome           Setting
	VisibilityTimeout Setting
	TUIView           Setting
	TUIViewSocket     Setting

	// Connect, Ready and Visibility are the three timeouts as durations.
	Connect, Ready, Visibility time.Duration
	// Explicit is true when the config names the endpoint (a socket or a
	// session): only then can it disagree with a route's persisted one.
	Explicit bool
	// TimeoutsSet say which timeouts a config set, so a daemon's own
	// test values stand where the config is silent.
	ConnectSet, ReadySet, VisibilitySet bool
}

// Endpoint is the endpoint the settings resolve to.
func (s Settings) Endpoint() Endpoint {
	return Endpoint{Socket: s.Socket.Value, Session: s.Session.Value, Binary: s.Binary.Value}
}

// App is the app the settings select; a default is left empty, for the
// daemon's own to fill.
func (s Settings) App() App {
	var a App
	if s.AppBinary.From != FromDefault {
		a.Binary = s.AppBinary.Value
	}
	if s.AppHome.From != FromDefault {
		a.Home = s.AppHome.Value
	}
	return a
}

// Input is what Resolve reads.
type Input struct {
	// Runtime is the [runtime] section of the config in use; nil is none.
	Runtime *configv1.Runtime
	// Sources maps a dotted key to the layer that set it (config.Result's
	// Sources), and ProjectPath and UserPath name the files of the layers.
	Sources               map[string]string
	ProjectPath, UserPath string
	// Getenv reads the variables of hc's path rules (XDG_RUNTIME_DIR,
	// XDG_STATE_HOME, HOME) when a session is named; nil is the process
	// environment. HC_SOCKET and HC_SESSION are never read.
	Getenv func(string) string
	// DefaultSocket is the endpoint when the config names none: the
	// daemon's default session. Empty resolves hc's default session.
	DefaultSocket string
	// DefaultAppHome is the app home when the config names none.
	DefaultAppHome string
}

// ErrTUIView refuses an enabled TUI view: the view is not built in this
// release.
var ErrTUIView = errors.New("[runtime.tui_view] enabled = true: the Herdr TUI view of core agents is not available in this release; remove enabled, or set it to false (socket alone is accepted and changes nothing)")

// Resolve reads the keys, checks them and fills each default. A duration that
// does not parse or is not positive, a socket or an app home that is not
// absolute, a session that is not one path segment, a socket and a session
// that name different endpoints, and an enabled TUI view each fail with the
// key named.
func Resolve(in Input) (Settings, error) {
	rt := in.Runtime
	// A missing section reads as an empty one: every key unset.
	c, a, v := rt.GetCore(), rt.GetApp(), rt.GetTuiView()
	if c == nil {
		c = &configv1.RuntimeCore{}
	}
	if a == nil {
		a = &configv1.RuntimeApp{}
	}
	if v == nil {
		v = &configv1.RuntimeTuiView{}
	}
	from := func(key string) string {
		layer := in.Sources[key]
		switch layer {
		case "":
			return "config"
		case "project":
			if in.ProjectPath != "" {
				return "project file " + in.ProjectPath
			}
		case "user":
			if in.UserPath != "" {
				return "user file " + in.UserPath
			}
		}
		return layer
	}
	str := func(key string, set bool, value, def string) Setting {
		if set {
			return Setting{Value: value, From: from(key)}
		}
		return Setting{Value: def, From: FromDefault}
	}
	var s Settings
	if c.Binary != nil && c.GetBinary() == "" {
		return s, errors.New("[runtime.core] binary is empty; unset it for hc, or name the program")
	}
	s.Binary = str("runtime.core.binary", c.Binary != nil, c.GetBinary(), DefaultBinary)
	if a.Binary != nil && a.GetBinary() == "" {
		return s, errors.New("[runtime.app] binary is empty; unset it for herdr-app, or name the program")
	}
	s.AppBinary = str("runtime.app.binary", a.Binary != nil, a.GetBinary(), DefaultAppBinary)
	if a.Home != nil && !filepath.IsAbs(a.GetHome()) {
		return s, fmt.Errorf("[runtime.app] home %q is not an absolute path", a.GetHome())
	}
	s.AppHome = str("runtime.app.home", a.Home != nil, filepath.Clean(a.GetHome()), in.DefaultAppHome)

	var err error
	duration := func(key, value string, set bool, def time.Duration) (Setting, time.Duration, error) {
		if !set {
			return Setting{Value: def.String(), From: FromDefault}, def, nil
		}
		d, err := time.ParseDuration(value)
		if err != nil {
			return Setting{}, 0, fmt.Errorf("%s: %w", key, err)
		}
		if d <= 0 {
			return Setting{}, 0, fmt.Errorf("%s: %q must be positive", key, value)
		}
		return Setting{Value: d.String(), From: from(key)}, d, nil
	}
	if s.ConnectTimeout, s.Connect, err = duration("runtime.core.connect_timeout", c.GetConnectTimeout(), c.ConnectTimeout != nil, DefaultConnectTimeout); err != nil {
		return s, err
	}
	if s.ReadyTimeout, s.Ready, err = duration("runtime.core.ready_timeout", c.GetReadyTimeout(), c.ReadyTimeout != nil, DefaultReadyTimeout); err != nil {
		return s, err
	}
	if s.VisibilityTimeout, s.Visibility, err = duration("runtime.app.visibility_timeout", a.GetVisibilityTimeout(), a.VisibilityTimeout != nil, DefaultVisibilityTimeout); err != nil {
		return s, err
	}
	s.ConnectSet, s.ReadySet, s.VisibilitySet = c.ConnectTimeout != nil, c.ReadyTimeout != nil, a.VisibilityTimeout != nil

	if v.GetEnabled() {
		return s, ErrTUIView
	}
	s.TUIView = str("runtime.tui_view.enabled", v.Enabled != nil, "off", "off")
	s.TUIViewSocket = str("runtime.tui_view.socket", v.Socket != nil, v.GetSocket(), "")

	if err := s.endpoint(in, c, from); err != nil {
		return s, err
	}
	return s, nil
}

// endpoint resolves the socket: an explicit socket, else the session through
// hc's path rules, else the default. A socket and a session that lead to
// different places stop the run with both named.
func (s *Settings) endpoint(in Input, c *configv1.RuntimeCore, from func(string) string) error {
	if c.Socket != nil && !filepath.IsAbs(c.GetSocket()) {
		return fmt.Errorf("[runtime.core] socket %q is not an absolute path", c.GetSocket())
	}
	session := ""
	if c.Session != nil {
		if err := hcpath.ValidSession(c.GetSession()); err != nil {
			return fmt.Errorf("[runtime.core] session: %w", err)
		}
		var err error
		if session, err = hcpath.SessionSocket(c.GetSession(), in.Getenv); err != nil {
			return fmt.Errorf("[runtime.core] session %q: %w", c.GetSession(), err)
		}
	}
	switch {
	case c.Socket != nil && c.Session != nil:
		if filepath.Clean(c.GetSocket()) != filepath.Clean(session) {
			return fmt.Errorf("[runtime.core] socket %q and session %q contradict: session %q is the socket %q; set one of them", c.GetSocket(), c.GetSession(), c.GetSession(), session)
		}
		s.Socket = Setting{Value: filepath.Clean(c.GetSocket()), From: from("runtime.core.socket")}
		s.Session = Setting{Value: c.GetSession(), From: from("runtime.core.session")}
		s.Explicit = true
	case c.Socket != nil:
		s.Socket = Setting{Value: filepath.Clean(c.GetSocket()), From: from("runtime.core.socket")}
		s.Explicit = true
	case c.Session != nil:
		s.Socket = Setting{Value: session, From: from("runtime.core.session") + ", session " + c.GetSession()}
		s.Session = Setting{Value: c.GetSession(), From: from("runtime.core.session")}
		s.Explicit = true
	default:
		def := in.DefaultSocket
		if def == "" {
			var err error
			if def, err = hcpath.SessionSocket(hcpath.DefaultSession, in.Getenv); err != nil {
				return err
			}
		}
		s.Socket = Setting{Value: def, From: FromDefault + ", session " + hcpath.DefaultSession}
		s.Session = Setting{Value: "", From: FromDefault}
	}
	return nil
}
