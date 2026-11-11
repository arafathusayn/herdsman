package runtime

import (
	"context"
	"fmt"
	"time"
)

// Target names the execution an operation acts on. A backend reads the fields
// it understands: the TUI finds an agent by Name (or Pane), core by
// ExecutionID and TerminalAddress.
type Target struct {
	// Name is the logical agent's name as the backend knows it.
	Name            string
	Pane            string
	ExecutionID     string
	Generation      int64
	TerminalAddress string
}

// Capabilities is what a provider's endpoint is and can do.
type Capabilities struct {
	Backend string
	// EndpointID identifies the endpoint: core identity, or the TUI socket.
	EndpointID string
	Protocol   string
	Methods    []string
	// Harnesses are the harness kinds the provider can launch; empty means it
	// does not say.
	Harnesses []string
	// TypedInput is true when the provider types text into a terminal for an
	// automated delivery. Core never does on 0.1.19.
	TypedInput bool
}

// Intent is one durable launch intent: what EnsureExecution reconciles.
type Intent struct {
	ID          string // the launch intent id, stable across retries
	Name, Kind  string
	Role        string
	Pane        string // herdr-tui: the pane the agent starts in
	Args        []string
	Cwd         string
	Timeout     time.Duration
	ExecutionID string // the id a created execution gets
	// Place readies the launch site, called only when the execution must be
	// created: on the TUI it opens the agent's pane and returns it with the
	// arguments confined for it. Nil uses Pane and Args as they are.
	Place func(context.Context) (pane string, args []string, err error)
	// Launch is the backend's own reserved launch, opaque to the contract:
	// on herdr-core, the reservation of the launch transaction.
	Launch any
}

// Outcome is what EnsureExecution found or did.
type Outcome string

const (
	OutcomeAdopted  Outcome = "adopted"
	OutcomeCreated  Outcome = "created"
	OutcomePending  Outcome = "pending"
	OutcomeConflict Outcome = "conflict"
	OutcomeUnknown  Outcome = "unknown"
)

// Ensured is EnsureExecution's answer.
type Ensured struct {
	Outcome Outcome
	// Detail says why for a conflict or unknown outcome.
	Detail string
	// Execution is what is known of the execution after the call.
	Execution Inspection
}

// Inspection is a fresh look at one execution.
type Inspection struct {
	// Exists is false when the backend says the execution is not there.
	Exists bool
	Kind   string
	Pane   string
	// TerminalAddress is the core address.
	TerminalAddress string
	Evidence        ProcessEvidence
	Observation     Observation
	// Conversation is native conversation evidence, when the backend has it.
	Conversation *Conversation
}

// Snapshot is Observe's answer: the current inspection and the changes since
// the epoch the caller gave. A different epoch means the caller must resync
// from Current and drop what it had.
type Snapshot struct {
	Epoch   string
	Current Inspection
	Changes []Observation
}

// ReadRequest bounds a terminal read.
type ReadRequest struct {
	Lines    int
	MaxBytes int
	// Recent reads the newest output instead of the visible screen.
	Recent bool
}

// Text is a bounded terminal read with how complete it is.
type Text struct {
	Text      string
	Truncated bool
}

// Delivery is one instruction for an execution. On the TUI it is typed as a
// prompt, as before; on core it is a notice the execution claims (§10).
type Delivery struct {
	Text string
	// Tag says which delivery it is, for records.
	Tag string
}

// Delivered is Deliver's answer. Pending means the backend accepted it and has
// not confirmed it; Unresolved means nobody can tell whether it arrived.
type Delivered struct {
	Pending, Unresolved bool
}

// Interrupted is Interrupt's observed result. It never escalates to a kill.
type Interrupted struct {
	// Sent is true when the interrupt reached the execution.
	Sent bool
	// Pane is where it was sent, on the TUI.
	Pane    string
	Program Program
	// RawProgram is the status as the backend reported it.
	RawProgram string
}

// Stopped is Stop's verified result.
type Stopped struct {
	// Already is true when there was nothing left to stop.
	Already bool
}

// Provider is one backend's implementation of the runtime contract (§6). An
// operation acts only on the execution its target proves; nothing falls back
// to another backend.
type Provider interface {
	// Backend is the provider's backend name.
	Backend() string
	// Capabilities reports endpoint identity, protocol, methods and harness
	// qualifications.
	Capabilities(ctx context.Context) (Capabilities, error)
	// EnsureExecution reconciles one durable launch intent: adopted,
	// created, pending, conflict or unknown.
	EnsureExecution(ctx context.Context, in Intent) (Ensured, error)
	// Inspect reports fresh identity, process state, readiness, native status
	// and conversation evidence.
	Inspect(ctx context.Context, t Target) (Inspection, error)
	// Observe returns a snapshot and the changes since epoch.
	Observe(ctx context.Context, t Target, epoch string) (Snapshot, error)
	// Read returns bounded terminal text and whether it was cut.
	Read(ctx context.Context, t Target, r ReadRequest) (Text, error)
	// Deliver hands an instruction to the execution.
	Deliver(ctx context.Context, t Target, d Delivery) (Delivered, error)
	// Interrupt requests an interruption and reports what was observed.
	Interrupt(ctx context.Context, t Target) (Interrupted, error)
	// Stop ends only the proven owned execution and verifies it ended.
	Stop(ctx context.Context, t Target) (Stopped, error)
	// EnsureVisible establishes the execution's place in the viewer.
	EnsureVisible(ctx context.Context, t Target) (Visibility, error)
	// Show focuses an existing view of the execution; it never creates one.
	Show(ctx context.Context, t Target) error
}

// Factory opens a provider on an endpoint, such as a socket path.
type Factory func(endpoint string) (Provider, error)

// Registry holds the providers this build has. A backend with no factory is
// refused with Unavailable, naming the Herdr TUI.
type Registry map[string]Factory

// Open returns the provider of backend on endpoint.
func (r Registry) Open(backend, endpoint string) (Provider, error) {
	if !Valid(backend) {
		return nil, &Error{Code: CodeUnknownBackend, Msg: fmt.Sprintf("%q is not a backend", backend)}
	}
	f, ok := r[backend]
	if !ok {
		return nil, Unavailable(backend, "in this build")
	}
	return f(endpoint)
}

// Available reports whether this build has a provider for backend, and the
// refusal when it has none.
func (r Registry) Available(backend string) error {
	if !Valid(backend) {
		return &Error{Code: CodeUnknownBackend, Msg: fmt.Sprintf("%q is not a backend", backend)}
	}
	if _, ok := r[backend]; !ok {
		return Unavailable(backend, "in this build")
	}
	return nil
}
