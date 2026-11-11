package inbox

import (
	"context"

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
