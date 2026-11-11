// Package runtime is the execution contract every backend implements: the
// provider operations, the execution record and the normalized observations
// (herdr-core backend spec, sections 4 to 6 and 9). Orchestration depends on
// these types, never on a backend's own RPC names.
package runtime

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// The execution backends.
const (
	// BackendCore runs each agent as a herdr-core terminal, shown in the app.
	BackendCore = "herdr-core"
	// BackendTUI runs each agent in a Herdr TUI pane, as every release before
	// the core backend did.
	BackendTUI = "herdr-tui"
)

// Backends lists every backend name, in the order they are reported.
var Backends = []string{BackendCore, BackendTUI}

// Valid reports whether name is a backend.
func Valid(name string) bool { return slices.Contains(Backends, name) }

// LegacyBackend is the backend of a route or a record written before backends
// existed: those were all Herdr TUI routes.
func LegacyBackend(persisted string) string {
	if persisted == "" {
		return BackendTUI
	}
	return persisted
}

// Codes of the errors this package returns. Callers branch on these.
const (
	CodeBackendMigrationRequired = "backend_migration_required"
	CodeBackendUnavailable       = "backend_unavailable"
	CodeUnknownBackend           = "unknown_backend"
)

// Error is a refusal with a stable code.
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

// Is matches another *Error with the same code, so errors.Is(err,
// ErrMigrationRequired) works on any wrapped refusal of that kind.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code && t.Msg == ""
}

// Sentinels for errors.Is; they carry only the code.
var (
	ErrMigrationRequired = &Error{Code: CodeBackendMigrationRequired}
	ErrUnavailable       = &Error{Code: CodeBackendUnavailable}
	ErrUnknownBackend    = &Error{Code: CodeUnknownBackend}
)

// CodeOf returns the code of a runtime refusal, or "" for any other error.
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// Source says where a selection came from, strongest first.
type Source string

// The selection sources, in precedence order for a new route; an existing
// route's own backend wins over all of them.
const (
	SourceRoute        Source = "route"
	SourceFlag         Source = "flag"
	SourceConfig       Source = "config"
	SourceInstallation Source = "installation"
)

// Selection is the backend a run uses and why.
type Selection struct {
	Backend string
	Source  Source
	// ConfigPath is the file that set it, when Source is SourceConfig.
	ConfigPath string
}

// Inputs is everything backend selection reads. Every caller that starts
// executions (up, task launches, automations, setup) resolves through Resolve
// with the inputs it has.
type Inputs struct {
	// Flag is --backend, or the request field that carries it.
	Flag string
	// Config is runtime.backend of the config file in use, and ConfigPath
	// that file.
	Config, ConfigPath string
	// Installation is the account's installation default.
	Installation string
	// Persisted is the backend an existing route started on: LegacyBackend
	// of what its records say. Empty for a new route.
	Persisted string
}

// Resolve picks the backend (herdr-core backend spec, section 5): an existing
// route keeps its own, and a flag that names another is refused; a new route
// takes the flag, then the config file, then the installation default. A
// value that names no backend is refused wherever it comes from, so a
// misspelling never falls through to the next source. Nothing here falls back
// from core to the TUI.
func Resolve(in Inputs) (Selection, error) {
	for _, v := range []struct{ name, value string }{
		{"--backend", in.Flag}, {"runtime.backend", in.Config}, {"the installation default", in.Installation}, {"the route's backend", in.Persisted},
	} {
		if v.value != "" && !Valid(v.value) {
			return Selection{}, &Error{Code: CodeUnknownBackend, Msg: fmt.Sprintf("%s is %q; it must be one of %s", v.name, v.value, strings.Join(Backends, ", "))}
		}
	}
	if in.Persisted != "" {
		if in.Flag != "" && in.Flag != in.Persisted {
			return Selection{}, &Error{Code: CodeBackendMigrationRequired, Msg: fmt.Sprintf(
				"the route runs on %s and keeps it; --backend %s would move it. Stop the route and start it again under another name to run it on %s",
				in.Persisted, in.Flag, in.Flag)}
		}
		return Selection{Backend: in.Persisted, Source: SourceRoute}, nil
	}
	switch {
	case in.Flag != "":
		return Selection{Backend: in.Flag, Source: SourceFlag}, nil
	case in.Config != "":
		return Selection{Backend: in.Config, Source: SourceConfig, ConfigPath: in.ConfigPath}, nil
	case in.Installation != "":
		return Selection{Backend: in.Installation, Source: SourceInstallation}, nil
	}
	return Selection{}, &Error{Code: CodeUnknownBackend, Msg: "no backend is configured and the account has no installation default"}
}

// Unavailable is the refusal for a backend this build or machine cannot run.
// It always names the line that selects the Herdr TUI, since there is no
// automatic fallback.
func Unavailable(backend, why string) error {
	return &Error{Code: CodeBackendUnavailable, Msg: fmt.Sprintf(
		"%s is not available %s; set backend = %q in [runtime] of the config file, pass --backend %s, or run: herdsman runtime default %s",
		backend, why, BackendTUI, BackendTUI, BackendTUI)}
}
