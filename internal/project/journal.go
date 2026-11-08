package project

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/ops"
)

// The operation kind and step effects of a change that reaches folder configs.
const (
	opKind        = "project.config"
	effectConfig  = "project.config"
	effectRecords = "project.records"
)

// statement is one write of a planned change, kept so that a restart can
// finish the change.
type statement struct {
	SQL  string `json:"sql"`
	Args []any  `json:"args"`
}

// recordingConn notes each write that succeeds on the connection it wraps.
type recordingConn struct {
	store.Conn
	writes *[]statement
}

func (c recordingConn) Exec(query string, args ...any) (int64, error) {
	n, err := c.Conn.Exec(query, args...)
	if err == nil {
		*c.writes = append(*c.writes, statement{SQL: query, Args: args})
	}
	return n, err
}

// errPlanned rolls back the transaction a change was planned in.
var errPlanned = errors.New("planned")

// txFiles plans a change in a transaction. A change that touches no file
// commits there. Any other is rolled back and run as one operation: each config
// edit, then the recorded writes, so a crash at any point finishes on restart.
func (s *Store) txFiles(fn func(store.Conn, *fileEdits) error) error {
	var files fileEdits
	var writes []statement
	err := s.db.Tx(func(c store.Conn) error {
		if err := fn(recordingConn{Conn: c, writes: &writes}, &files); err != nil {
			return err
		}
		if len(files.list) > 0 {
			return errPlanned
		}
		return nil
	})
	if !errors.Is(err, errPlanned) {
		return err
	}
	return s.runPlan(&files, writes)
}

func (s *Store) runPlan(files *fileEdits, writes []statement) error {
	spec, err := planSpec(files, writes)
	if err != nil {
		return err
	}
	ctx := context.Background()
	op, err := s.engine.Submit(ctx, spec)
	if err != nil {
		return err
	}
	if op, err = s.engine.Drive(ctx, op.ID); err != nil {
		return err
	}
	switch op.State {
	case ops.OpDone:
		return nil
	case ops.OpFailed:
		for i, edit := range files.list {
			edit.applied = op.Steps[i].State == ops.StepDone
		}
		return errors.Join(errors.New(op.Reason), files.undo())
	}
	return fmt.Errorf("%w: operation %s is %s: %s", ErrRefused, op.ID, op.State, op.Reason)
}

func planSpec(files *fileEdits, writes []statement) (ops.Spec, error) {
	spec := ops.Spec{Kind: opKind, Plan: "edit folder configs, then commit the project records"}
	for _, edit := range files.list {
		raw, err := json.Marshal(configStep{Path: edit.path, Before: edit.before, Existed: edit.existed, After: edit.after, Remove: edit.remove, Mode: uint32(edit.mode)})
		if err != nil {
			return spec, err
		}
		spec.Locks = append(spec.Locks, "project-config:"+edit.path)
		spec.Steps = append(spec.Steps, ops.StepSpec{Name: "config " + edit.path, Effect: effectConfig, Key: "config:" + edit.path, Spec: string(raw), Retry: ops.RetryIdempotent})
	}
	raw, err := json.Marshal(writes)
	if err != nil {
		return spec, err
	}
	spec.Steps = append(spec.Steps, ops.StepSpec{Name: "records", Effect: effectRecords, Key: "records", Spec: string(raw)})
	return spec, nil
}

// Effects returns the step kinds of this package, for the account's engine: an
// engine that lacks them stops these operations on restart.
func Effects(db store.Conn) map[string]ops.Effect {
	return map[string]ops.Effect{effectConfig: configEffect{}, effectRecords: recordsEffect{db: db}}
}

// configStep is one config edit as the journal holds it.
type configStep struct {
	Path    string `json:"path"`
	Before  string `json:"before"`
	Existed bool   `json:"existed"`
	After   string `json:"after"`
	Remove  bool   `json:"remove"`
	Mode    uint32 `json:"mode"`
}

type configEffect struct{}

// Apply makes the edit, unless the file is no longer what was planned from.
func (configEffect) Apply(ctx context.Context, st ops.Step) error {
	var c configStep
	if err := json.Unmarshal([]byte(st.Spec), &c); err != nil {
		return err
	}
	v, detail, err := c.probe()
	if err != nil {
		return err
	}
	switch v {
	case ops.Matches:
		return nil
	case ops.Different:
		return errors.New(detail)
	}
	if c.Remove {
		return os.Remove(c.Path)
	}
	return replaceFile(c.Path, c.After, os.FileMode(c.Mode))
}

// Probe compares the file with the two contents the operation allows.
func (configEffect) Probe(ctx context.Context, st ops.Step) (ops.Verdict, string, error) {
	var c configStep
	if err := json.Unmarshal([]byte(st.Spec), &c); err != nil {
		return ops.Unknown, "", err
	}
	return c.probe()
}

func (c configStep) probe() (ops.Verdict, string, error) {
	text, exists, err := readConfig(c.Path)
	if err != nil {
		return ops.Unknown, "", err
	}
	switch {
	case c.Remove && !exists, !c.Remove && exists && text == c.After:
		return ops.Matches, "", nil
	case !c.Existed && !exists, c.Existed && exists && text == c.Before:
		return ops.Absent, "", nil
	}
	return ops.Different, c.Path + " changed since the operation read it", nil
}

type recordsEffect struct {
	db store.Conn
}

// Apply commits the recorded writes and the mark that they happened.
func (e recordsEffect) Apply(ctx context.Context, st ops.Step) error {
	writes, err := decodeWrites(st.Spec)
	if err != nil {
		return err
	}
	return e.db.Tx(func(c store.Conn) error {
		for _, w := range writes {
			if _, err := c.Exec(w.SQL, w.Args...); err != nil {
				return err
			}
		}
		_, err := c.Exec("INSERT INTO project_commits (op_id, created) VALUES (?, ?)", st.OpID, nowMillis())
		return err
	})
}

// Probe looks for the mark.
func (e recordsEffect) Probe(ctx context.Context, st ops.Step) (ops.Verdict, string, error) {
	marks, err := column(e.db, "SELECT op_id FROM project_commits WHERE op_id = ?", st.OpID)
	if err != nil {
		return ops.Unknown, "", err
	}
	if len(marks) > 0 {
		return ops.Matches, "", nil
	}
	return ops.Absent, "", nil
}

// decodeWrites restores the argument types: every number written is an integer.
func decodeWrites(spec string) ([]statement, error) {
	var writes []statement
	dec := json.NewDecoder(bytes.NewReader([]byte(spec)))
	dec.UseNumber()
	if err := dec.Decode(&writes); err != nil {
		return nil, err
	}
	for _, w := range writes {
		for i, a := range w.Args {
			n, ok := a.(json.Number)
			if !ok {
				continue
			}
			v, err := n.Int64()
			if err != nil {
				return nil, fmt.Errorf("project: journal argument %s: %w", n, err)
			}
			w.Args[i] = v
		}
	}
	return writes, nil
}
