package ops

import "github.com/arafathusayn/herdsman/internal/daemon/store"

// MarkIntendedForTest leaves a step as a crash after its intent would.
func (e *Engine) MarkIntendedForTest(id string, ordinal int) error {
	return e.setStep(id, ordinal, StepPending, StepIntended, "")
}

// EngineConnForTest shares an engine's connection with a second engine.
func EngineConnForTest(e *Engine) store.Conn { return e.db }
