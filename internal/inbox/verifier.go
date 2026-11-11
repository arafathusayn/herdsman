package inbox

import (
	"context"
	"strings"

	"github.com/arafathusayn/herdsman/internal/herdr"
)

// The backend names a reference can carry. They match internal/runtime's,
// which this package cannot import: the channel commands are clients.
const (
	BackendTUI  = "herdr-tui"
	BackendCore = "herdr-core"
)

// Ref names the agent whose identity is checked: a pane on the Herdr TUI, an
// execution on a backend without panes.
type Ref struct {
	// Backend is BackendTUI, BackendCore, or empty for a pane-only caller,
	// which is a TUI caller.
	Backend   string
	Pane      string
	Execution string
}

// PaneRef is the reference of a TUI caller known by its pane.
func PaneRef(pane string) Ref { return Ref{Backend: BackendTUI, Pane: pane} }

// executionPane is the prefix of the pane slot of an execution's identity: a
// typed reference to the execution, which no Herdr TUI pane id can be.
const executionPane = "exec:"

// ExecutionIdentity is the registry identity of a herdr-core execution: the
// pane slot holds a typed reference to the execution, and the session is the
// execution, recorded by herdr-core. It fits the registry's identity shape,
// so the released files keep their form; a new execution of the same agent is
// another identity, so a replaced execution's binding no longer matches.
func ExecutionIdentity(harness, execution string) Identity {
	return Identity{Pane: executionPane + execution, Session: Session{Agent: harness, Kind: "id", Source: BackendCore, Value: execution}}
}

// ExecutionOf returns the execution an identity names, when it is an
// execution's identity.
func ExecutionOf(id Identity) (string, bool) {
	exec, ok := strings.CutPrefix(id.Pane, executionPane)
	if !ok || id.Session.Source != BackendCore || id.Session.Value != exec || exec == "" {
		return "", false
	}
	return exec, true
}

// Verifier answers one question for every identity consumer: is the agent at
// ref live, and who is it? The results are LiveIdentity's: LiveOK with the
// identity, LiveGone when the backend says the agent is gone, LiveUnknown
// otherwise, with a note for an unavailable backend.
type Verifier interface {
	Live(ctx context.Context, ref Ref) (Identity, int, string)
}

// TUIVerifier is the Herdr TUI's verifier: herdr's agent record decoded
// strictly by LiveIdentity, as before backends existed. A reference of
// another backend is never a TUI identity, so it is unknown here.
type TUIVerifier struct{ Client *herdr.Client }

// Live implements Verifier.
func (v TUIVerifier) Live(ctx context.Context, ref Ref) (Identity, int, string) {
	if ref.Backend != "" && ref.Backend != BackendTUI {
		return Identity{}, LiveUnknown, "the identity of a " + ref.Backend + " execution cannot be verified through the Herdr TUI"
	}
	return LiveIdentity(ctx, v.Client, ref.Pane)
}

// VerifierFor is the verifier of a caller that has a herdr client and no other
// backend: the TUI's.
func VerifierFor(c *herdr.Client) Verifier { return TUIVerifier{Client: c} }
