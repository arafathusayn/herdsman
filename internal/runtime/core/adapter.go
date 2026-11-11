package core

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/arafathusayn/herdsman/internal/notice"
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
	// Facts are the harness's qualification facts (spec section 9), as the
	// probes found them for the version they name.
	Facts Facts
}

// The ways a harness is woken with no terminal input (spec section 9).
const (
	// WakeBackgroundWait: the harness holds `herdsman notice wait` as a
	// background command, and its return starts a turn.
	WakeBackgroundWait = "background_wait"
	// WakeQueue: one fixed line through the harness's own message queue.
	WakeQueue = "queue"
	// WakePerson: a person types the fixed line in the pane.
	WakePerson = "person"
)

// The levels a harness is qualified for on core.
const (
	LevelUnattended = "unattended"
	LevelAttended   = "attended"
)

// The needs_attention reasons of the steps that need a person (spec section
// 10). Each is raised on the event stream when it comes due.
const (
	AttentionStartLine     = "start_line"
	AttentionNoticeWaiting = notice.AttentionNoticeWaiting
	AttentionInterrupted   = notice.AttentionInterrupted
	AttentionCompactionDue = "compaction_due"
	AttentionGoalPaused    = "goal_paused"
)

// Compaction is how a harness compacts its own conversation: what the
// checkpointer's early checkpoint is set below.
type Compaction struct {
	// BySelf: the harness compacts in the middle of a turn and goes on.
	BySelf bool
	// BelowWindow: the harness compacts this many tokens below its context
	// window (Claude Code: 33,000, N25). Zero when it does not say so.
	BelowWindow int64
	// LaunchSetting names the setting of the launch's own command that sets
	// the level (Codex: model_auto_compact_token_limit, N32). The level when
	// none is set was not measured, so the checkpointer then issues its
	// notice on the interval only.
	LaunchSetting string
	// Window is the context window the level is counted from when the
	// launch says nothing else.
	Window int64
}

// Facts are one harness's answers to the questions of spec section 9.
type Facts struct {
	// Version is the harness version the probes ran on.
	Version string
	// IdleMeans lists when the harness reports idle.
	IdleMeans []string
	// Wake is how it is woken with no terminal input.
	Wake string
	// WaitingStatus is the status it reports while it only waits.
	WaitingStatus string
	// WorkingOutsideTurn says when it reports working outside a turn.
	WorkingOutsideTurn string
	// ResumesAfterInterrupt: after an interrupt in a turn, something reaches
	// the agent with no person (Claude Code: the attached wait returns the
	// open claim). Codex's queue is held until a person types a line.
	ResumesAfterInterrupt bool
	// StopReaches says when a stop request reaches a working agent.
	StopReaches string
	// Compaction is how the harness compacts its conversation.
	Compaction Compaction
	// PersonOnly lists the states that only a person ends.
	PersonOnly []string
	// Level is what the harness is qualified for on core.
	Level string
}

// The states only a person ends, as Facts.PersonOnly names them.
const (
	PersonOnlyInterrupted = "the state after any interrupt"
	PersonOnlyGoalPaused  = "a paused goal"
	// GoalResumeLine is the line a person submits to resume a paused goal.
	GoalResumeLine = "/goal resume"
)

// PersonStep is one step of a run on core that needs a person: named by up
// before the route starts, and raised as needs_attention when it comes due.
type PersonStep struct {
	// Reason is the needs_attention reason the step raises.
	Reason string
	// When says when it comes due.
	When string
	// What says what the person does.
	What string
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
		StartLine: ClaudeStartLine, StartLineVersion: 1,
		Facts: Facts{
			Version:               "2.1.322",
			IdleMeans:             []string{"before the first turn", "after an interrupt"},
			Wake:                  WakeBackgroundWait,
			WaitingStatus:         "done",
			WorkingOutsideTurn:    "during a compaction",
			ResumesAfterInterrupt: true,
			StopReaches:           "at the next tool boundary, when the wait was started before the work",
			Compaction:            Compaction{BySelf: true, BelowWindow: 33000, Window: 200000},
			Level:                 LevelUnattended,
		}},
	"codex": {Kind: "codex", Programs: []string{"codex"}, RequiredArgs: []string{"--no-daemon"}, Attended: true, RestState: "done", StartPath: StartPathPerson,
		InterruptAtRest: "opens the rewind view: never sent", InterruptInTurn: "stops the model, not its command; its queue is held until a person types a line",
		StartLine: CodexStartLine, StartLineVersion: 1,
		Facts: Facts{
			Version:   "0.164.0",
			IdleMeans: []string{"before the first turn", "after an interrupt", "after a person's slash command"},
			// The queued wake is the owner's open decision: until it is
			// settled a person types the fixed line.
			Wake:                  WakePerson,
			WaitingStatus:         "done",
			WorkingOutsideTurn:    "for as long as a command left in the background runs",
			ResumesAfterInterrupt: false,
			StopReaches:           "when the turn ends",
			Compaction:            Compaction{BySelf: true, LaunchSetting: "model_auto_compact_token_limit"},
			PersonOnly:            []string{PersonOnlyInterrupted, PersonOnlyGoalPaused},
			Level:                 LevelAttended,
		}},
}

// CompactionLevel is the context size at which the harness compacts by itself
// for a launch with args, and whether it is known. Claude Code compacts
// BelowWindow tokens below its window (window: the caller's, or the adapter's
// when zero); Codex at the launch's own model_auto_compact_token_limit, and at
// an unmeasured level when the launch sets none.
func (a Adapter) CompactionLevel(args []string, window int64) (int64, bool) {
	c := a.Facts.Compaction
	if !c.BySelf {
		return 0, false
	}
	if c.LaunchSetting != "" {
		return LaunchSetting(args, c.LaunchSetting)
	}
	if window <= 0 {
		window = c.Window
	}
	if c.BelowWindow <= 0 || window <= c.BelowWindow {
		return 0, false
	}
	return window - c.BelowWindow, true
}

// LaunchSetting reads a positive number set as `-c name=N` or `--config
// name=N` (also `-cname=N` and `--config=name=N`) in a launch command; the
// last one wins. A value that is not a positive number is not set.
func LaunchSetting(args []string, name string) (int64, bool) {
	var val string
	found := false
	take := func(kv string) {
		k, v, ok := strings.Cut(kv, "=")
		if ok && strings.TrimSpace(k) == name {
			val, found = strings.Trim(strings.TrimSpace(v), `"'`), true
		}
	}
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case (arg == "-c" || arg == "--config") && i+1 < len(args):
			take(args[i+1])
			i++
		case strings.HasPrefix(arg, "--config="):
			take(strings.TrimPrefix(arg, "--config="))
		case strings.HasPrefix(arg, "-c") && !strings.HasPrefix(arg, "--") && len(arg) > 2:
			take(arg[2:])
		}
	}
	if !found {
		return 0, false
	}
	n, err := strconv.ParseInt(val, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// PersonSteps are the steps of a run of this harness on core that need a
// person, under the open decisions: no start-line argument is added, so every
// start line is a person's; Codex is attended, so each of its notices waits
// for a person's line, and so do its interrupt and a paused goal.
// autoCompactOff says the user turned the harness's own compaction off.
func (a Adapter) PersonSteps(autoCompactOff bool) []PersonStep {
	out := []PersonStep{{Reason: AttentionStartLine, When: "once, before the first brief", What: "submit the start line in the agent's pane"}}
	if a.Facts.Wake == WakePerson {
		out = append(out, PersonStep{Reason: AttentionNoticeWaiting, When: "each time a notice waits for the agent",
			What: "type the line `" + notice.AttendedLine + "` in its pane"})
	}
	if !a.Facts.ResumesAfterInterrupt {
		out = append(out, PersonStep{Reason: AttentionInterrupted, When: "after an interrupt; a pause never sends this harness one", What: "type a line in its pane"})
	}
	if slices.Contains(a.Facts.PersonOnly, PersonOnlyGoalPaused) {
		out = append(out, PersonStep{Reason: AttentionGoalPaused, When: "when its goal is paused", What: "submit " + GoalResumeLine + " in its pane"})
	}
	if !a.Facts.Compaction.BySelf || autoCompactOff {
		out = append(out, PersonStep{Reason: AttentionCompactionDue, When: "when the context passes the checkpointer's limit", What: "submit the harness's compact command in its pane"})
	}
	return out
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
