package launch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

// CoreDescriptor is everything the core wrapper needs to start one harness in
// a herdr-core terminal (herdr-core backend spec, sections 6, 8 and 13). The
// daemon hands it over the launch socket once the wrapper is authenticated by
// process; nothing of it is on the terminal's command line, in its labels or
// in its core environment. It holds no secret: SecretRefs are references, and
// the wrapper reads their values itself after authentication.
type CoreDescriptor struct {
	Intent    string `json:"intent"`
	Execution string `json:"execution"`
	Kind      string `json:"kind"`
	// Harness is the agent's argv. Argv[0] is the absolute path core sees,
	// resolved in preflight through core.
	Harness []string `json:"harness"`
	// Prelude are the launch's prelude steps with the numbers their marker
	// files carry in StepsDir.
	Prelude   []NumberedStep `json:"prelude,omitempty"`
	StepsDir  string         `json:"steps_dir,omitempty"`
	TimeoutMS int64          `json:"timeout_ms,omitempty"`
	Cwd       string         `json:"cwd"`
	// Env are the harness variables the launch policy sets: the agent's own
	// variables and the route's channel variables. Nothing secret.
	Env map[string]string `json:"env,omitempty"`
	// Login are the login values and PATH, and LoginFrom says where they
	// came from.
	Login     map[string]string `json:"login,omitempty"`
	LoginFrom string            `json:"login_from"`
	// SecretRefs lists the keychain references the policy grants, in the
	// form secrets.ParseRefs reads.
	SecretRefs string `json:"secret_refs,omitempty"`
}

// NumberedStep is one prelude step and the number of its marker files.
type NumberedStep struct {
	N    int  `json:"n"`
	Step Step `json:"step"`
}

// LoginKeys are the login values a core launch takes from its descriptor.
// Everything else the core's own environment holds is dropped (section 13).
var LoginKeys = []string{"HOME", "USER", "LOGNAME", "SHELL", "PATH", "LANG", "LC_ALL", "LC_CTYPE", "LC_MESSAGES", "TMPDIR", "TZ"}

// terminalKeys come from the core terminal itself: they describe the pty.
var terminalKeys = []string{"TERM", "COLORTERM", "TERM_PROGRAM", "TERM_PROGRAM_VERSION"}

// agentMarkers are variables that name another agent, a pane or a caller.
// A core launch never passes one, even when a launch line sets it.
var agentMarkers = []string{
	"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SSE_PORT", "CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED",
	"CODEX_THREAD_ID", "CODEX_SESSION_ID", "HERDSMAN_CALLER", "HERDSMAN_ROUTE", "HERDSMAN_SCOPE",
}

// AgentMarker reports a variable that identifies another agent, a herdr pane
// or a caller: HERDR_*, a core terminal's HC_* variables outside the core's
// own, and the harness session markers.
func AgentMarker(name string) bool {
	return strings.HasPrefix(name, "HERDR_") || slices.Contains(agentMarkers, name)
}

// Validate refuses a descriptor the wrapper cannot run safely.
func (d CoreDescriptor) Validate() error {
	switch {
	case d.Intent == "" || d.Execution == "":
		return errors.New("launch: the descriptor names no intent or execution")
	case len(d.Harness) == 0 || !filepath.IsAbs(d.Harness[0]):
		return errors.New("launch: the descriptor's harness program is not an absolute path")
	case !filepath.IsAbs(d.Cwd):
		return fmt.Errorf("launch: the descriptor's folder %q is not absolute", d.Cwd)
	}
	for _, word := range d.Harness {
		if err := checkText(word); err != nil {
			return fmt.Errorf("launch: harness: %w", err)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(d.Env)) {
		if AgentMarker(name) || strings.HasPrefix(name, "HC_") {
			return fmt.Errorf("launch: the launch may not set %s: it names another agent, pane or terminal", name)
		}
		if err := validateEnv(name, d.Env[name]); err != nil {
			return err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(d.Login)) {
		if !slices.Contains(LoginKeys, name) {
			return fmt.Errorf("launch: %s is not a login value", name)
		}
	}
	if len(d.Prelude) > 0 && d.StepsDir == "" {
		return errors.New("launch: prelude steps need a steps folder")
	}
	for _, s := range d.Prelude {
		if err := checkStepNumber(s.N); err != nil {
			return err
		}
		if err := s.Step.validate(); err != nil {
			return fmt.Errorf("launch: step %d: %w", s.N, err)
		}
	}
	return nil
}

// Digest is a stable digest of the descriptor, the launch's policy digest.
func (d CoreDescriptor) Digest() string {
	b, err := json.Marshal(d)
	if err != nil {
		panic(err) // strings, numbers and maps only
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ArgvDigest is the digest of the harness argv alone.
func (d CoreDescriptor) ArgvDigest() string {
	sum := sha256.Sum256([]byte(strings.Join(d.Harness, "\x00")))
	return hex.EncodeToString(sum[:])
}

// CoreEnv builds the harness environment from nothing (section 13): the
// login values of the descriptor, the pty's own variables and core's HC_*
// variables from own (the wrapper's environment, which is core's), the
// launch's harness variables, and last the granted secrets as NAME=value.
// Nothing else of own survives, so another agent's markers, tokens or base
// URLs in the core's environment never reach the harness.
func CoreEnv(d CoreDescriptor, own []string, granted []string) []string {
	env := map[string]string{}
	for _, k := range LoginKeys {
		if v, ok := d.Login[k]; ok {
			env[k] = v
		}
	}
	for _, kv := range own {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if strings.HasPrefix(name, "HC_") || slices.Contains(terminalKeys, name) {
			env[name] = value
		}
	}
	for name, value := range d.Env {
		if !AgentMarker(name) && !strings.HasPrefix(name, "HC_") {
			env[name] = value
		}
	}
	for _, kv := range granted {
		if name, value, ok := strings.Cut(kv, "="); ok {
			env[name] = value
		}
	}
	out := make([]string, 0, len(env))
	for _, k := range slices.Sorted(maps.Keys(env)) {
		out = append(out, k+"="+env[k])
	}
	return out
}
