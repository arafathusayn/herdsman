package runtime

import (
	"crypto/rand"
	"fmt"
	"time"
)

// SchemaVersion is the version of the execution record this package writes.
const SchemaVersion = 1

// NewExecutionID is a random version 4 UUID: one harness launch.
func NewExecutionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand does not fail on the supported platforms
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Process is the lifecycle of an execution's process.
type Process string

const (
	ProcessStarting Process = "starting"
	ProcessLive     Process = "live"
	ProcessLost     Process = "lost"
	ProcessReplaced Process = "replaced"
	ProcessFinished Process = "finished"
	ProcessUnknown  Process = "unknown"
)

// Readiness says whether an execution may be given work. Ready needs fresh
// identity, adapter evidence and a status report since registration; a live
// PID alone is not ready.
type Readiness string

const (
	ReadinessWaiting         Readiness = "waiting"
	ReadinessReady           Readiness = "ready"
	ReadinessNeedsAttention  Readiness = "needs_attention"
	ReadinessIdentityUnknown Readiness = "identity_unknown"
)

// Program is the status the program in the terminal reports. "done" is not
// proof that a task passed, and "error" is not proof that the process exited.
type Program string

const (
	ProgramIdle    Program = "idle"
	ProgramWorking Program = "working"
	ProgramBlocked Program = "blocked"
	ProgramDone    Program = "done"
	ProgramError   Program = "error"
	ProgramUnknown Program = "unknown"
)

// ProgramOf maps a reported status to a Program. A status this build does not
// know is unknown, never idle; the caller keeps the raw value.
func ProgramOf(raw string) Program {
	switch p := Program(raw); p {
	case ProgramIdle, ProgramWorking, ProgramBlocked, ProgramDone, ProgramError:
		return p
	}
	return ProgramUnknown
}

// Visibility is the state of an execution's place in the native app (§7).
type Visibility string

const (
	VisibilityUnverified Visibility = "unverified"
	VisibilityPending    Visibility = "pending"
	VisibilityVerified   Visibility = "verified"
	VisibilityMissing    Visibility = "missing"
)

// App is whether the native app is reachable; empty when no app is involved.
type App string

const (
	AppNone        App = ""
	AppOpen        App = "open"
	AppClosed      App = "closed"
	AppUnreachable App = "unreachable"
)

// Orchestration is what Herdsman is doing with the execution.
type Orchestration string

const (
	OrchestrationActive   Orchestration = "active"
	OrchestrationPaused   Orchestration = "paused"
	OrchestrationStopping Orchestration = "stopping"
	OrchestrationStopped  Orchestration = "stopped"
	OrchestrationFailed   Orchestration = "failed"
)

// TaskOutcome is the review state of the execution's task; empty with none.
type TaskOutcome string

const (
	TaskNone           TaskOutcome = ""
	TaskPending        TaskOutcome = "pending"
	TaskReviewRequired TaskOutcome = "review_required"
	TaskAccepted       TaskOutcome = "accepted"
	TaskRejected       TaskOutcome = "rejected"
)

// Observation holds the independent fields of section 9. None is derived from
// another: each is set from its own evidence.
type Observation struct {
	Process       Process
	Readiness     Readiness
	Program       Program
	RawProgram    string
	Visibility    Visibility
	App           App
	Orchestration Orchestration
	TaskOutcome   TaskOutcome
	ObservedAt    time.Time
}

// Desired is what Herdsman wants of an execution.
type Desired string

const (
	DesiredRunning Desired = "running"
	DesiredStopped Desired = "stopped"
)

// AppBinding is where the native app shows an execution (§7).
type AppBinding struct {
	CoreID     string     `json:"core_id"`
	SpaceID    string     `json:"space_id,omitempty"`
	TabID      string     `json:"tab_id,omitempty"`
	PaneID     string     `json:"pane_id,omitempty"`
	Visibility Visibility `json:"visibility"`
}

// TUIBinding is an optional mirror of an execution in the Herdr TUI.
type TUIBinding struct {
	Socket string `json:"socket"`
	Pane   string `json:"pane"`
}

// Conversation is the harness's own conversation id with where it came from.
// It is evidence, never identity: a resumed conversation is a new execution.
type Conversation struct {
	Value      string `json:"value"`
	Provenance string `json:"provenance"`
}

// Process evidence: the harness's PID and its start time, read from the OS
// when the execution registered. A core address, a creation time, a name, a
// label or a status is never identity.
type ProcessEvidence struct {
	PID   int
	Start string
}

// Execution is one harness launch of one logical agent (§6).
type Execution struct {
	SchemaVersion int
	ID            string // a random UUID
	Repo, Route   string
	Agent, Role   string
	TaskID        string
	LaunchIntent  string
	Generation    int64
	Backend       string
	// EndpointID is the verified endpoint: core identity, or the TUI socket.
	EndpointID string
	// TerminalAddress is the core terminal address, never a friendly name.
	TerminalAddress string
	// Pane is the Herdr TUI pane, for herdr-tui only.
	Pane             string
	InitialCreatedMS int64
	Evidence         ProcessEvidence
	ArgvDigest, Cwd  string
	HarnessKind      string
	HarnessVersion   string
	PolicyDigest     string
	LaunchGuard      string
	Conversation     *Conversation
	Desired          Desired
	Observation      Observation
	App              *AppBinding
	TUI              *TUIBinding
	LastVerifiedAt   time.Time
	Created, Updated time.Time
}
