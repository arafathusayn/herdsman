package core

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// Adapter is what the core provider knows of one qualified harness (spec
// section 9). Harness kinds on core come from this registry, never from the
// Herdr TUI's agent list.
type Adapter struct {
	Kind string
	// Programs are the process names that are this harness.
	Programs []string
	// RequiredArgs must be in the person's own launch command; Herdsman
	// refuses the launch and names the argument rather than adding it.
	RequiredArgs []string
	// Attended: a person may be needed during a run (Codex on core: nothing
	// is queued into it, so its start line and recoveries are a person's).
	Attended bool
	// InterruptResumable: an interrupted turn can be resumed with no person,
	// so a pause may interrupt a turn under way.
	InterruptResumable bool
	// StartLineByPerson: the adapter's start line must be submitted by a
	// person in the pane. No start-line argument is ever added to a launch
	// command, so every qualified adapter says yes for now.
	StartLineByPerson bool
	// RestrictedOnCore: the restricted profile may launch this harness on
	// core. No harness qualifies yet.
	RestrictedOnCore bool
}

// adapters is the registry, by kind.
var adapters = map[string]Adapter{
	"claude": {Kind: "claude", Programs: []string{"claude"}, InterruptResumable: true, StartLineByPerson: true},
	"codex":  {Kind: "codex", Programs: []string{"codex"}, RequiredArgs: []string{"--no-daemon"}, Attended: true, StartLineByPerson: true},
}

// Kinds lists the harness kinds the core provider can launch, sorted.
func Kinds() []string {
	out := make([]string, 0, len(adapters))
	for k := range adapters {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// AdapterFor returns the adapter of a kind, or an error naming the qualified
// kinds.
func AdapterFor(kind string) (Adapter, error) {
	a, ok := adapters[kind]
	if !ok {
		return Adapter{}, fmt.Errorf("%q is not a harness qualified for herdr-core; qualified: %s", kind, strings.Join(Kinds(), ", "))
	}
	return a, nil
}

// CheckArgs refuses a launch command that lacks an argument the adapter
// requires. It never adds one: the person's command is the authority.
func (a Adapter) CheckArgs(args []string) error {
	for _, want := range a.RequiredArgs {
		if !slices.Contains(args, want) {
			return fmt.Errorf("%s on herdr-core must run with %s in its command, so that its work stays in its terminal's process tree; add %s to the %s command", a.Kind, want, want, a.Kind)
		}
	}
	return nil
}

// IsProgram reports whether a process name or path is this harness.
func (a Adapter) IsProgram(name string) bool {
	return slices.Contains(a.Programs, filepath.Base(name))
}
