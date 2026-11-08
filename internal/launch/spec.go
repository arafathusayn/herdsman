// Package launch holds the launch spec of a route's coordinator: the marked
// prelude steps, the agent command, the convenience grammar that compiles to
// them, the shell line each step is submitted as, the marker files that line
// leaves behind, and the decision a resume takes from those markers.
//
// The package only computes and reads. It starts no process and writes
// nothing except the removal of stale markers in ClearMarkers and
// ClearProbeMarkers.
package launch

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// DefaultPreludeTimeout applies when a spec sets no prelude timeout.
const DefaultPreludeTimeout = 10 * time.Minute

// Retry says whether a prelude step may be repeated.
type Retry string

const (
	// RetryNever is the default: the command is not known to be safe to repeat.
	RetryNever Retry = "never"
	// RetryIdempotent marks a command that is safe to run again.
	RetryIdempotent Retry = "idempotent"
)

// EnvVar is one environment assignment, kept in the order written.
type EnvVar struct {
	Name  string
	Value string
}

// Command is a program, its arguments and its command-local environment.
type Command struct {
	Env  []EnvVar
	Argv []string
}

// Step is one prelude command.
type Step struct {
	Command Command
	// Retry is empty or RetryNever for a step that must not repeat.
	Retry Retry
	// Once is RetryNever plus the rule that a person cannot ask for a rerun.
	Once bool
	// Probe, when set, is a command whose zero exit establishes that the
	// step's effect already happened.
	Probe *Command
}

// Spec is the structured launch of one coordinator.
type Spec struct {
	Prelude []Step
	// Command is the agent executable and its arguments.
	Command []string
	// Env is the agent's environment, exported into its pane.
	Env map[string]string
	// Cwd is the folder the agent's pane starts in.
	Cwd string
	// PreludeTimeout bounds each prelude step; zero means the default.
	PreludeTimeout time.Duration
}

// Timeout returns the timeout of each prelude step.
func (s Spec) Timeout() time.Duration {
	if s.PreludeTimeout == 0 {
		return DefaultPreludeTimeout
	}
	return s.PreludeTimeout
}

// RetryPolicy returns the step's effective policy.
func (s Step) RetryPolicy() Retry {
	if s.Once || s.Retry == "" {
		return RetryNever
	}
	return s.Retry
}

// Validate reports the first problem that makes the spec unusable.
func (s Spec) Validate() error {
	if err := (Command{Argv: s.Command}).validate(); err != nil {
		return fmt.Errorf("command: %w", err)
	}
	names := make([]string, 0, len(s.Env))
	for name := range s.Env {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := validateEnv(name, s.Env[name]); err != nil {
			return fmt.Errorf("env: %w", err)
		}
	}
	if err := checkText(s.Cwd); err != nil {
		return fmt.Errorf("cwd: %w", err)
	}
	if s.PreludeTimeout < 0 {
		return errors.New("prelude timeout is negative")
	}
	for i, step := range s.Prelude {
		if err := step.validate(); err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
	}
	return nil
}

func (s Step) validate() error {
	if err := s.Command.validate(); err != nil {
		return err
	}
	switch s.Retry {
	case "", RetryNever, RetryIdempotent:
	default:
		return fmt.Errorf("retry %q is neither %q nor %q", s.Retry, RetryNever, RetryIdempotent)
	}
	if s.Once && s.Retry == RetryIdempotent {
		return errors.New("once cannot be combined with an idempotent retry")
	}
	if s.Probe != nil {
		if err := s.Probe.validate(); err != nil {
			return fmt.Errorf("probe: %w", err)
		}
	}
	return nil
}

func (c Command) validate() error {
	if len(c.Argv) == 0 {
		return errors.New("no program")
	}
	if c.Argv[0] == "" {
		return errors.New("empty program name")
	}
	for _, word := range c.Argv {
		if err := checkText(word); err != nil {
			return err
		}
	}
	for _, e := range c.Env {
		if err := validateEnv(e.Name, e.Value); err != nil {
			return err
		}
	}
	return nil
}

func validateEnv(name, value string) error {
	if !validName(name) {
		return fmt.Errorf("environment name %q is not valid", name)
	}
	return checkText(value)
}

func validName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		letter := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if !letter && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// checkText refuses what quoting cannot make safe. A step is typed into a
// terminal, where a control character acts on the line editor even inside
// quotes: a newline submits the line and Ctrl-U erases it.
func checkText(s string) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("%q is not valid UTF-8", s)
	}
	if strings.ContainsFunc(s, unicode.IsControl) {
		return fmt.Errorf("control character in %q", s)
	}
	return nil
}

// String renders the command for a shell. Every word is quoted, the program
// too: functions and builtins still resolve, aliases do not.
func (c Command) String() string {
	words := make([]string, 0, len(c.Env)+len(c.Argv))
	for _, e := range c.Env {
		words = append(words, e.Name+"="+quote(e.Value))
	}
	for _, word := range c.Argv {
		words = append(words, quote(word))
	}
	return strings.Join(words, " ")
}
