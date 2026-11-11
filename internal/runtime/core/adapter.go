package core

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// Adapter is what the core provider knows of one qualified harness (spec
// section 9). Harness kinds on core come from this registry, never from the
// Herdr TUI's agent list. Each adapter declares its rest state, how its start
// line reaches it, what an interrupt does at rest and in a turn, and whether it
// is qualified for attended or unattended use.
type Adapter struct {
	Kind string
	// Programs are the process names that are this harness.
	Programs []string
	// RequiredArgs must be in the person's own launch command; Herdsman
	// refuses the launch and names the argument rather than adding it.
	RequiredArgs []string
	// Attended: a person is needed during a run. Codex on core holds no wait
	// across the end of a turn, and its queued wake is the owner's open
	// decision, so each notice for it raises needs_attention and a person
	// types the fixed line in its pane.
	Attended bool
	// HoldsWait: the harness keeps `herdsman notice wait` as a background
	// command and starts a turn when it returns (Claude Code, N20, N21).
	HoldsWait bool
	// InterruptResumable: an interrupted turn can be resumed with no person,
	// so a pause may interrupt a turn under way.
	InterruptResumable bool
	// RestState is the program status the harness reports at rest (N19).
	RestState string
	// StartPath says how the start line reaches the harness. No start-line
	// argument is ever added to a launch command while the owner's decision
	// is open, so every qualified adapter's path is "person": up raises
	// needs_attention and a person submits the line in the app.
	StartPath string
	// InterruptAtRest and InterruptInTurn say what one Esc does there. No
	// interrupt is ever sent at rest (Esc is half a rewind in both).
	InterruptAtRest, InterruptInTurn string
	// StartLine is the adapter's fixed start line, with no task content, and
	// StartLineVersion its version.
	StartLine        string
	StartLineVersion int
	// RestrictedOnCore: the restricted profile may launch this harness on
	// core. No harness qualifies yet.
	RestrictedOnCore bool
}

// StartPathPerson: a person submits the start line in the app.
const StartPathPerson = "person"

// ClaudeStartLine is Claude Code's start line, version 1 (spec section 10,
// "The start line"): wait first and end the turn, do what each wait prints,
// and an interrupt is the operator's pause, not a request to stop.
const ClaudeStartLine = "You are managed by herdsman. Start `herdsman notice wait` as a background command now and end your turn. " +
	"When it returns, do what it prints, and start the next wait it names before you begin the work. " +
	"An interrupt is a pause by the operator, not a request to stop: after one, do not ask what to do; follow your wait's output."

// CodexStartLine is Codex's start line, version 1: each later message names a
// command to run. Codex is attended on core, so a person types each later
// line when a notice waits.
const CodexStartLine = "You are managed by herdsman. Each later message names a command to run; run it and do what it prints. " +
	"Now run `herdsman notice read` and do what it prints."

// adapters is the registry, by kind.
var adapters = map[string]Adapter{
	"claude": {Kind: "claude", Programs: []string{"claude"}, HoldsWait: true, InterruptResumable: true, RestState: "done", StartPath: StartPathPerson,
		InterruptAtRest: "opens the rewind view: never sent", InterruptInTurn: "ends the turn; the claim stays open and the background wait stays attached",
		StartLine: ClaudeStartLine, StartLineVersion: 1},
	"codex": {Kind: "codex", Programs: []string{"codex"}, RequiredArgs: []string{"--no-daemon"}, Attended: true, RestState: "done", StartPath: StartPathPerson,
		InterruptAtRest: "opens the rewind view: never sent", InterruptInTurn: "stops the model, not its command; its queue is held until a person types a line",
		StartLine: CodexStartLine, StartLineVersion: 1},
}

// Unattended reports an adapter qualified for unattended use on core 0.1.20:
// it is woken with no terminal input and resumes an interrupted turn with no
// person. Codex is attended while its queued wake stays the owner's open
// decision: each notice for it waits for a person's line.
func (a Adapter) Unattended() bool { return !a.Attended && a.HoldsWait }

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
