package policy

import "context"

// Interrupter asks a scope's agents to stop at their next turn boundary. This
// promise is best effort: an agent may finish the turn it is in, and the
// overshoot is reported.
type Interrupter interface {
	Interrupt(ctx context.Context, scope Scope) error
}
