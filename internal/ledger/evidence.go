package ledger

import (
	"slices"
	"time"
)

// Level is how strongly the evidence ties a resource to the task. Only
// LevelExclusive authorizes an automatic action.
type Level string

// The evidence levels, strongest first.
const (
	LevelExclusive  Level = "exclusive"
	LevelAssociated Level = "associated"
	LevelShared     Level = "shared"
	LevelUnknown    Level = "unknown"
)

// Signal is one piece of evidence.
type Signal string

// The signals the detectors produce.
const (
	SignalRecord            Signal = "record"
	SignalCwd               Signal = "cwd-under-worktree"
	SignalExecEnv           Signal = "exec-env"
	SignalExecLabel         Signal = "exec-label"
	SignalComposeProject    Signal = "compose-project"
	SignalComposeDir        Signal = "compose-working-dir"
	SignalBindMount         Signal = "bind-mount"
	SignalPortBlock         Signal = "port-block"
	SignalCandidateListener Signal = "candidate-listener"
	SignalConfigVar         Signal = "config-var"
	SignalRecordUnmatched   Signal = "record-unmatched"
)

// SharedOlder is the SharedWith entry of a resource older than the execution.
const SharedOlder = "older-than-execution"

// SharedUnproven is the SharedWith entry of a process with no proof of being
// the task's own: not its recorded command and not its role.
const SharedUnproven = "unproven-command"

// weak reports a signal that alone yields LevelUnknown: a config variable
// names a resource without an execution, and an unmatched record names nothing
// live.
func (s Signal) weak() bool {
	return s == SignalConfigVar || s == SignalRecordUnmatched
}

// Evidence is what the detectors found about one resource.
type Evidence struct {
	Signals    []Signal
	SharedWith []string
	// Recorded means a record of this execution matched the identity.
	Recorded bool
	// Unverified means the identity could not be read, as for a pid that lsof
	// showed and ps did not. Nothing unverified rises above LevelUnknown.
	Unverified bool
}

// Level applies the table. Shared evidence comes first because it overrides a
// record: a recorded resource someone else uses must not be stopped.
func (e Evidence) Level() Level {
	switch {
	case len(e.SharedWith) > 0:
		return LevelShared
	case e.Unverified:
		return LevelUnknown
	case e.Recorded:
		return LevelExclusive
	}
	for _, signal := range e.Signals {
		if !signal.weak() {
			return LevelAssociated
		}
	}
	return LevelUnknown
}

func (e *Evidence) add(signal Signal) { e.Signals = append(e.Signals, signal) }

func (e *Evidence) share(with string) { e.SharedWith = append(e.SharedWith, with) }

func (e Evidence) hit() bool { return len(e.Signals) > 0 }

// Record is explicit authority over a resource, written when it was started,
// registered or adopted. A variable a process merely inherited is no record.
type Record struct {
	Task      string
	Execution string
	Kind      string
	ID        string
	// Start is the process start or resource creation time.
	Start time.Time
	// Command is the command line of a recorded process, the proof that the
	// live pid runs what was started.
	Command string
	// Adopted resources may predate the execution; that is what adoption means.
	Adopted bool
}

// matches compares a record with a live identity. Containers and compose
// projects are unique by id, volumes and networks need the creation time since
// their names come back, and a process always needs its start time.
func (r Record) matches(id Identity) bool {
	if r.Kind != id.Kind || r.ID != id.ID {
		return false
	}
	switch r.Kind {
	case KindProcess, KindVolume, KindNetwork:
		return !r.Start.IsZero() && r.Start.Equal(id.Start)
	}
	return true
}

// matchedKind reports a kind that must match something live in the snapshot.
func matchedKind(kind string) bool {
	return slices.Contains([]string{KindProcess, KindContainer, KindCompose, KindVolume, KindNetwork}, kind)
}
