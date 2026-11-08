package ops

import (
	"context"
	"errors"
	"fmt"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
)

// Reconcile decides every intended step of every running operation from what
// is found in the world, and repeats nothing itself.
func (e *Engine) Reconcile(ctx context.Context) error {
	ids, err := e.running()
	if err != nil {
		return err
	}
	for _, id := range ids {
		op, err := e.Get(id)
		if err != nil {
			return err
		}
		for _, st := range op.Steps {
			if st.State != StepIntended {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			stopped, err := e.reconcileStep(ctx, op, st)
			if err != nil {
				return err
			}
			if stopped {
				break
			}
		}
	}
	return nil
}

func (e *Engine) running() ([]string, error) {
	var ids []string
	err := e.Read(func(c store.Conn) error {
		var err error
		ids, err = queryIDs(c, "SELECT id FROM operations WHERE state = 'running' ORDER BY id")
		return err
	})
	return ids, err
}

// reconcileStep applies one verdict and reports whether the operation stopped.
func (e *Engine) reconcileStep(ctx context.Context, op *Operation, st Step) (bool, error) {
	fx, ok := e.opt.Effects[st.Effect]
	if !ok {
		return true, e.stop(op.ID, st.Ordinal, OpStopped, fmt.Sprintf("step %d: no effect %s in this binary; %s", st.Ordinal, st.Effect, resumeLine(op.ID, st.Ordinal)))
	}
	v, detail, err := fx.Probe(ctx, st)
	if err != nil {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		v, detail = Unknown, err.Error()
	}
	switch v {
	case Matches:
		return false, e.markDone(op.ID, st)
	case Absent:
		return false, e.setStep(op.ID, st.Ordinal, StepIntended, StepPending, "")
	case Different:
		return true, e.stopConflict(op.ID, st, detail)
	}
	if st.Retry == RetryIdempotent {
		return false, e.setStep(op.ID, st.Ordinal, StepIntended, StepPending, "")
	}
	return true, e.stop(op.ID, st.Ordinal, OpStopped, fmt.Sprintf("step %d (%s) was interrupted and may not be repeated (%s); %s", st.Ordinal, st.Key, detail, resumeLine(op.ID, st.Ordinal)))
}

// Recover is the restart path: reconcile first, then finish what is left.
func (e *Engine) Recover(ctx context.Context) error {
	if err := e.Reconcile(ctx); err != nil {
		return err
	}
	ids, err := e.running()
	if err != nil {
		return err
	}
	var first error
	for _, id := range ids {
		if _, err := e.Drive(ctx, id); err != nil && !errors.Is(err, ErrPrecondition) && first == nil {
			first = fmt.Errorf("ops: recover %s: %w", id, err)
		}
	}
	return first
}

// Decision is a person's answer about a stopped step.
type Decision int

// The decisions: the effect happened, or it did not and may run again.
const (
	ResolveDone Decision = iota
	ResolveRerun
)

// Resolve records a person's decision and puts the operation back to running.
func (e *Engine) Resolve(id string, ordinal int, d Decision) error {
	return e.tx(func(c store.Conn) error {
		op, err := loadOperation(c, id)
		if err != nil {
			return err
		}
		if op.State != OpStopped && op.State != OpConflict {
			return fmt.Errorf("ops: operation %s is %s, nothing to resolve", id, op.State)
		}
		if ordinal < 0 || ordinal >= len(op.Steps) {
			return fmt.Errorf("ops: operation %s has no step %d", id, ordinal)
		}
		for _, earlier := range op.Steps[:ordinal] {
			if earlier.State != StepDone {
				return fmt.Errorf("ops: step %d of %s comes first", earlier.Ordinal, id)
			}
		}
		st := op.Steps[ordinal]
		// A pending step is here when this binary lacked its effect.
		if st.State != StepIntended && st.State != StepConflict && st.State != StepPending {
			return fmt.Errorf("ops: step %d of %s is %s", ordinal, id, st.State)
		}
		now := e.now()
		if d == ResolveDone {
			if err := moveStep(c, id, ordinal, st.State, StepDone, "resolved by a person", now); err != nil {
				return err
			}
			if err := enqueue(c, id, st, now); err != nil {
				return err
			}
		} else if err := moveStep(c, id, ordinal, st.State, StepPending, "", now); err != nil {
			return err
		}
		_, err = c.Exec("UPDATE operations SET state = 'running', reason = '', updated = ? WHERE id = ?", now, id)
		return err
	})
}
