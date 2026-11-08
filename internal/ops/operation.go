// Package ops runs every change that reaches outside the store as a journaled
// operation: the work is written down before the first effect, each step is
// recorded as about to happen before it happens, and a restart compares the
// journal with the world before it repeats anything.
package ops

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
)

// OpState is an operation's state. Done, failed and stale are terminal; the
// others keep the operation's locks.
type OpState string

// The operation states.
const (
	OpRunning  OpState = "running"
	OpDone     OpState = "done"
	OpFailed   OpState = "failed"   // nothing uncertain is left behind
	OpConflict OpState = "conflict" // the world differs from the plan; a person decides
	OpStopped  OpState = "stopped"  // an effect may have happened and cannot be told; a person decides
	OpStale    OpState = "stale"    // a precondition changed before the first effect
)

// StepState is one step's state.
type StepState string

// The step states.
const (
	StepPending  StepState = "pending"
	StepIntended StepState = "intended"
	StepDone     StepState = "done"
	StepConflict StepState = "conflict"
)

// Retry says whether a step may be repeated when its effect cannot be observed.
type Retry string

// The retry policies; an empty policy means RetryNever.
const (
	RetryNever      Retry = "never"
	RetryIdempotent Retry = "idempotent"
)

// Verdict is what a probe found for a step's idempotency key.
type Verdict int

// The probe verdicts.
const (
	Unknown   Verdict = iota // the probe cannot tell
	Absent                   // the effect did not happen
	Matches                  // the effect happened and is this operation's
	Different                // something holds the key but it is not this operation's
)

func (v Verdict) String() string {
	return [...]string{"unknown", "absent", "matches", "different"}[v]
}

// Effect is the code behind one kind of step.
type Effect interface {
	Apply(ctx context.Context, s Step) error
	// Probe looks for the step's key and checks identity and specification,
	// not only existence.
	Probe(ctx context.Context, s Step) (Verdict, string, error)
}

// Event is a message a step publishes once its effect is recorded.
type Event struct {
	Topic   string `json:"topic"`
	Payload string `json:"payload"`
}

// StepSpec is one planned step.
type StepSpec struct {
	Name   string  `json:"name"`
	Effect string  `json:"effect"`
	Key    string  `json:"key"`
	Spec   string  `json:"spec"`
	Retry  Retry   `json:"retry"`
	Events []Event `json:"events,omitempty"`
}

// Step is a planned step with its journal state.
type Step struct {
	StepSpec
	OpID    string    `json:"op_id"`
	Ordinal int       `json:"ordinal"`
	State   StepState `json:"state"`
	Detail  string    `json:"detail,omitempty"`
}

// Precondition is one fact a plan assumed. Name is "<check>:<subject>".
type Precondition struct {
	Name string `json:"name"`
	Want string `json:"want"`
}

// Spec describes an operation to submit.
type Spec struct {
	Key           string         `json:"key,omitempty"`
	Kind          string         `json:"kind"`
	Plan          string         `json:"plan"`
	Preconditions []Precondition `json:"preconditions,omitempty"`
	Locks         []string       `json:"locks,omitempty"`
	Steps         []StepSpec     `json:"steps"`
}

// Operation is a journaled operation.
type Operation struct {
	ID            string         `json:"id"`
	Key           string         `json:"key,omitempty"`
	Kind          string         `json:"kind"`
	Plan          string         `json:"plan"`
	State         OpState        `json:"state"`
	Reason        string         `json:"reason,omitempty"`
	Created       int64          `json:"created"`
	Updated       int64          `json:"updated"`
	Preconditions []Precondition `json:"preconditions"`
	Steps         []Step         `json:"steps"`
}

// Errors of the engine.
var (
	ErrNotFound       = errors.New("operation not found")
	ErrKeyReused      = errors.New("idempotency key already names different work")
	ErrPrecondition   = errors.New("a precondition of the plan changed; make a new plan")
	ErrNeedsReconcile = errors.New("the operation has an intended step; reconcile first")
	ErrBusy           = errors.New("the operation is already being driven")
)

// Options configures an Engine.
type Options struct {
	Effects map[string]Effect
	Checks  map[string]Check
	Now     func() time.Time
	// Fault is called at each step boundary; the crash harness kills the
	// process from it.
	Fault func(point string)
}

// Engine runs operations over one writer connection, which it serializes.
type Engine struct {
	mu      sync.Mutex
	db      store.Conn
	opt     Options
	driving map[string]bool
	// draining counts exports waiting for the account lock; e.mu guards it.
	draining int
}

// NewEngine makes an engine on the account database's writer.
func NewEngine(db store.Conn, o Options) *Engine {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Engine{db: db, opt: o, driving: map[string]bool{}}
}

// Read runs fn on the engine's connection, between its writes.
func (e *Engine) Read(fn func(store.Conn) error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return fn(e.db)
}

func (e *Engine) fault(format string, args ...any) {
	if e.opt.Fault != nil {
		e.opt.Fault(fmt.Sprintf(format, args...))
	}
}

func (e *Engine) tx(fn func(store.Conn) error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.db.Tx(fn)
}

func (e *Engine) now() int64 { return e.opt.Now().Unix() }

// normalize fills defaults and refuses a spec the engine could not run.
func (e *Engine) normalize(s *Spec) error {
	if s.Kind == "" {
		return errors.New("ops: an operation needs a kind")
	}
	if len(s.Steps) == 0 {
		return errors.New("ops: an operation needs a step")
	}
	seen := map[string]bool{}
	for i := range s.Steps {
		st := &s.Steps[i]
		if st.Retry == "" {
			st.Retry = RetryNever
		}
		if st.Retry != RetryNever && st.Retry != RetryIdempotent {
			return fmt.Errorf("ops: step %q: unknown retry %q", st.Name, st.Retry)
		}
		if st.Key == "" || seen[st.Key] {
			return fmt.Errorf("ops: step %q: the idempotency key %q is empty or repeated", st.Name, st.Key)
		}
		seen[st.Key] = true
		if _, ok := e.opt.Effects[st.Effect]; !ok {
			return fmt.Errorf("ops: step %q: unknown effect %q", st.Name, st.Effect)
		}
	}
	for _, p := range s.Preconditions {
		if _, _, err := e.check(p); err != nil {
			return err
		}
	}
	return nil
}

func specSHA(s Spec) string {
	b, _ := json.Marshal(s)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Submit journals an operation and takes its locks, before any effect. A
// repeated key returns the operation it already named.
func (e *Engine) Submit(ctx context.Context, s Spec) (*Operation, error) {
	if err := e.normalize(&s); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sha := specSHA(s)
	var existing, id string
	if s.Key != "" {
		var err error
		if existing, err = e.findKey(s.Key, sha); err != nil {
			return nil, err
		}
	}
	if existing != "" {
		return e.Get(existing)
	}
	if err := e.checkAll(ctx, s.Preconditions); err != nil {
		return nil, err
	}
	err := e.tx(func(c store.Conn) error {
		if s.Key != "" {
			var err error
			if existing, err = keyLookup(c, s.Key, sha); err != nil || existing != "" {
				return err
			}
		}
		if err := checkLocks(c, s.Locks, e.draining > 0); err != nil {
			return err
		}
		id = newULID(e.opt.Now())
		if err := insertOperation(c, id, sha, s, e.now()); err != nil {
			return err
		}
		return takeLocks(c, id, s.Locks)
	})
	if err != nil {
		return nil, err
	}
	if existing != "" {
		return e.Get(existing)
	}
	e.fault("submit")
	return e.Get(id)
}

// Drive runs a running operation's remaining steps, in order. It refuses an
// operation with an intended step: only Reconcile may decide about those.
func (e *Engine) Drive(ctx context.Context, id string) (*Operation, error) {
	e.mu.Lock()
	if e.driving[id] {
		e.mu.Unlock()
		return nil, ErrBusy
	}
	e.driving[id] = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.driving, id)
		e.mu.Unlock()
	}()

	op, err := e.Get(id)
	if err != nil || op.State != OpRunning {
		return op, err
	}
	if !op.started() {
		if err := e.checkAll(ctx, op.Preconditions); err != nil {
			if errors.Is(err, ErrPrecondition) {
				if ferr := e.finish(id, OpStale, err.Error()); ferr != nil {
					return nil, ferr
				}
				op, gerr := e.Get(id)
				if gerr != nil {
					return nil, gerr
				}
				return op, err
			}
			return op, err
		}
	}
	for i := range op.Steps {
		st := op.Steps[i]
		switch st.State {
		case StepDone:
			continue
		case StepIntended:
			return op, fmt.Errorf("%w: step %d of %s", ErrNeedsReconcile, st.Ordinal, id)
		case StepConflict:
			return op, fmt.Errorf("ops: step %d of %s is in conflict", st.Ordinal, id)
		}
		if err := ctx.Err(); err != nil {
			return op, err
		}
		fx, ok := e.opt.Effects[st.Effect]
		if !ok {
			if err := e.stop(id, st.Ordinal, OpStopped, "no effect "+st.Effect+" in this binary; "+resumeLine(id, st.Ordinal)); err != nil {
				return nil, err
			}
			return e.Get(id)
		}
		if err := e.setStep(id, st.Ordinal, StepPending, StepIntended, ""); err != nil {
			return nil, err
		}
		e.fault("step:%d:intended", st.Ordinal)
		st.State = StepIntended
		if aerr := fx.Apply(ctx, st); aerr != nil {
			cont, err := e.afterFailure(ctx, op, st, aerr)
			if err != nil {
				return nil, err
			}
			if !cont {
				return e.Get(id)
			}
			continue
		}
		e.fault("step:%d:applied", st.Ordinal)
		if err := e.markDone(id, st); err != nil {
			return nil, err
		}
		e.fault("step:%d:done", st.Ordinal)
	}
	if err := e.finish(id, OpDone, ""); err != nil {
		return nil, err
	}
	e.fault("finish")
	return e.Get(id)
}

func (op *Operation) started() bool {
	for _, s := range op.Steps {
		if s.State != StepPending {
			return true
		}
	}
	return false
}

// afterFailure asks the probe what a failed Apply left behind. It reports
// whether the operation may go on.
func (e *Engine) afterFailure(ctx context.Context, op *Operation, st Step, aerr error) (bool, error) {
	v, detail, err := e.opt.Effects[st.Effect].Probe(ctx, st)
	if err != nil {
		v, detail = Unknown, err.Error()
	}
	switch {
	case v == Matches:
		return true, e.markDone(op.ID, st)
	case v == Absent || (v == Unknown && st.Retry == RetryIdempotent):
		if err := e.setStep(op.ID, st.Ordinal, StepIntended, StepPending, aerr.Error()); err != nil {
			return false, err
		}
		return false, e.finish(op.ID, OpFailed, fmt.Sprintf("step %d: %v", st.Ordinal, aerr))
	case v == Different:
		return false, e.stopConflict(op.ID, st, detail)
	}
	return false, e.stop(op.ID, st.Ordinal, OpStopped, fmt.Sprintf("step %d failed (%v) and its effect cannot be told; %s", st.Ordinal, aerr, resumeLine(op.ID, st.Ordinal)))
}

func resumeLine(id string, ordinal int) string {
	return fmt.Sprintf("check it, then resolve with: ops resolve %s %d done|rerun", id, ordinal)
}

// newULID is a sortable id: 48 bits of milliseconds and 80 random bits.
func newULID(t time.Time) string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // the system's random source does not fail
	}
	ms := uint64(t.UnixMilli())
	var sb strings.Builder
	for i := 9; i >= 0; i-- {
		sb.WriteByte(alphabet[(ms>>(uint(i)*5))&31])
	}
	var acc, bits uint
	for _, x := range b {
		acc, bits = acc<<8|uint(x), bits+8
		for bits >= 5 {
			bits -= 5
			sb.WriteByte(alphabet[(acc>>bits)&31])
		}
	}
	return sb.String()
}
