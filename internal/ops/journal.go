package ops

import (
	"encoding/json"
	"fmt"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
)

func insertOperation(c store.Conn, id, sha string, s Spec, now int64) error {
	pre, err := json.Marshal(orEmpty(s.Preconditions))
	if err != nil {
		return err
	}
	var key any
	if s.Key != "" {
		key = s.Key
	}
	if _, err := c.Exec(`INSERT INTO operations(id, key, kind, plan, spec_sha256, preconditions, state, created, updated) VALUES (?,?,?,?,?,?,'running',?,?)`,
		id, key, s.Kind, s.Plan, sha, string(pre), now, now); err != nil {
		return err
	}
	for i, st := range s.Steps {
		ev, err := json.Marshal(orEmpty(st.Events))
		if err != nil {
			return err
		}
		if _, err := c.Exec(`INSERT INTO operation_steps(op_id, ordinal, name, effect, key, spec, retry, events, state) VALUES (?,?,?,?,?,?,?,?,'pending')`,
			id, i, st.Name, st.Effect, st.Key, st.Spec, string(st.Retry), string(ev)); err != nil {
			return err
		}
	}
	return nil
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// findKey looks a key up outside a transaction.
func (e *Engine) findKey(key, sha string) (string, error) {
	var id string
	err := e.Read(func(c store.Conn) error {
		var err error
		id, err = keyLookup(c, key, sha)
		return err
	})
	return id, err
}

func keyLookup(c store.Conn, key, sha string) (string, error) {
	var id, got string
	err := c.Query("SELECT id, spec_sha256 FROM operations WHERE key = ?", []any{key}, func(r store.Row) error {
		id, got = r.Text(0), r.Text(1)
		return nil
	})
	if err != nil {
		return "", err
	}
	if id != "" && got != sha {
		return "", fmt.Errorf("%w: %s (operation %s)", ErrKeyReused, key, id)
	}
	return id, nil
}

// Get reads one operation with its steps.
func (e *Engine) Get(id string) (*Operation, error) {
	var op *Operation
	err := e.Read(func(c store.Conn) error {
		var err error
		op, err = loadOperation(c, id)
		return err
	})
	return op, err
}

func loadOperation(c store.Conn, id string) (*Operation, error) {
	var op *Operation
	var pre string
	err := c.Query(`SELECT id, COALESCE(key,''), kind, plan, state, reason, created, updated, preconditions FROM operations WHERE id = ?`, []any{id}, func(r store.Row) error {
		op = &Operation{ID: r.Text(0), Key: r.Text(1), Kind: r.Text(2), Plan: r.Text(3), State: OpState(r.Text(4)), Reason: r.Text(5), Created: r.Int64(6), Updated: r.Int64(7)}
		pre = r.Text(8)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if op == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err := json.Unmarshal([]byte(pre), &op.Preconditions); err != nil {
		return nil, fmt.Errorf("ops: operation %s preconditions: %w", id, err)
	}
	var decodeErr error
	err = c.Query(`SELECT ordinal, name, effect, key, spec, retry, events, state, detail FROM operation_steps WHERE op_id = ? ORDER BY ordinal`, []any{id}, func(r store.Row) error {
		s := Step{OpID: id, Ordinal: int(r.Int64(0)), State: StepState(r.Text(7)), Detail: r.Text(8)}
		s.Name, s.Effect, s.Key, s.Spec, s.Retry = r.Text(1), r.Text(2), r.Text(3), r.Text(4), Retry(r.Text(5))
		if err := json.Unmarshal([]byte(r.Text(6)), &s.Events); err != nil {
			decodeErr = err
		}
		op.Steps = append(op.Steps, s)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return op, decodeErr
}

// List returns every operation, newest first.
func (e *Engine) List() ([]*Operation, error) {
	out := []*Operation{}
	err := e.Read(func(c store.Conn) error {
		ids, err := queryIDs(c, "SELECT id FROM operations ORDER BY id DESC")
		if err != nil {
			return err
		}
		for _, id := range ids {
			op, err := loadOperation(c, id)
			if err != nil {
				return err
			}
			out = append(out, op)
		}
		return nil
	})
	return out, err
}

func queryIDs(c store.Conn, q string, args ...any) ([]string, error) {
	var ids []string
	err := c.Query(q, args, func(r store.Row) error { ids = append(ids, r.Text(0)); return nil })
	return ids, err
}

// setStep moves a step between two states; any other current state is a bug
// or a second writer, so it is refused.
func (e *Engine) setStep(id string, ordinal int, from, to StepState, detail string) error {
	return e.tx(func(c store.Conn) error {
		return moveStep(c, id, ordinal, from, to, detail, e.now())
	})
}

func moveStep(c store.Conn, id string, ordinal int, from, to StepState, detail string, now int64) error {
	n, err := c.Exec("UPDATE operation_steps SET state = ?, detail = ? WHERE op_id = ? AND ordinal = ? AND state = ?", string(to), detail, id, ordinal, string(from))
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("ops: step %d of %s is not %s", ordinal, id, from)
	}
	_, err = c.Exec("UPDATE operations SET updated = ? WHERE id = ?", now, id)
	return err
}

// markDone records an effect and enqueues its events in one transaction.
func (e *Engine) markDone(id string, st Step) error {
	return e.tx(func(c store.Conn) error {
		return recordDone(c, id, st, e.now())
	})
}

func recordDone(c store.Conn, id string, st Step, now int64) error {
	if err := moveStep(c, id, st.Ordinal, StepIntended, StepDone, "", now); err != nil {
		return err
	}
	return enqueue(c, id, st, now)
}

// finish ends an operation and frees its locks.
func (e *Engine) finish(id string, state OpState, reason string) error {
	return e.tx(func(c store.Conn) error {
		if _, err := c.Exec("UPDATE operations SET state = ?, reason = ?, updated = ? WHERE id = ?", string(state), reason, e.now(), id); err != nil {
			return err
		}
		return releaseLocks(c, id)
	})
}

// stop parks an operation for a person; its locks stay.
func (e *Engine) stop(id string, _ int, state OpState, reason string) error {
	return e.tx(func(c store.Conn) error {
		_, err := c.Exec("UPDATE operations SET state = ?, reason = ?, updated = ? WHERE id = ?", string(state), reason, e.now(), id)
		return err
	})
}

func (e *Engine) stopConflict(id string, st Step, detail string) error {
	reason := fmt.Sprintf("step %d (%s): found %s; %s", st.Ordinal, st.Key, detail, resumeLine(id, st.Ordinal))
	return e.tx(func(c store.Conn) error {
		if err := moveStep(c, id, st.Ordinal, StepIntended, StepConflict, detail, e.now()); err != nil {
			return err
		}
		_, err := c.Exec("UPDATE operations SET state = ?, reason = ?, updated = ? WHERE id = ?", string(OpConflict), reason, e.now(), id)
		return err
	})
}
