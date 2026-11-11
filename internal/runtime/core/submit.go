package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/runtime"
)

// A person's typed submit (spec section 10, rule 2): one person's explicit
// action naming one execution, carried out at once with `if` and force:false.
// Core checks the program's state once, before the paste; the Enter follows
// about 200 ms later with no second check. No automated path reaches this
// file: only RuntimeService.Submit, which refuses a caller that is an agent.

// The states of a typed submit. None is retired by time.
const (
	SubmitQueued              = "queued"
	SubmitPendingInCore       = "pending_in_core"
	SubmitTyped               = "typed"
	SubmitRejectedBeforeWrite = "rejected_before_write"
	SubmitUnresolved          = "unresolved"
)

// The refusal codes of a typed submit.
const (
	CodeSubmitOutstanding = "submit_outstanding"
	CodeSubmitNoStatus    = "submit_no_status"
	CodeSubmitState       = "submit_state"
	CodeRouteStopping     = "route_stopping"
	CodeSubmitGate        = "submit_gate"
)

// SubmitIf is the state both qualified harnesses accept a typed line in:
// they rest at done, which `if` cannot express on core 0.1.20, so a typed
// submit exists for them only while they report idle (before the first turn
// and after an interrupt).
const SubmitIf = "idle"

// MaxSubmitText bounds a typed line.
const MaxSubmitText = 4096

// SubmitConfirmation is what a person reads before a typed submit.
const SubmitConfirmation = "This types one line into the agent's terminal and presses Enter. herdr-core checks the program's state once, before the paste; " +
	"the Enter follows that check by about 200 ms with no second check, so a dialog that opens in between is confirmed by it. " +
	"Once core holds the line, nothing can recall it."

// SubmitRecord is one typed submit. Its text is never stored: only its digest.
type SubmitRecord struct {
	ID         string    `json:"id"`
	Execution  string    `json:"execution"`
	State      string    `json:"state"`
	Detail     string    `json:"detail,omitempty"`
	TextDigest string    `json:"text_digest"`
	Created    time.Time `json:"created"`
	Updated    time.Time `json:"updated"`
}

// Outstanding reports a submit that blocks the execution's next one.
func (r SubmitRecord) Outstanding() bool {
	return r.State == SubmitQueued || r.State == SubmitPendingInCore || r.State == SubmitUnresolved
}

// SubmitStore keeps typed submits durably.
type SubmitStore interface {
	PutSubmit(r SubmitRecord) error
	Submits(execution string) ([]SubmitRecord, error)
}

// Sender is the part of the core client a typed submit uses.
type Sender interface {
	TerminalReader
	Send(ctx context.Context, terminal, text, ifStatus string) (hc.SendResult, error)
}

// Submitter carries out typed submits.
type Submitter struct {
	Store   SubmitStore
	Records Records
	Notices *notice.Service
	Procs   ProcessTable
	Dial    func(ctx context.Context) (Sender, func(), error)
	// DialFor, when set, reaches the core an execution runs on (its
	// route's endpoint) and is used instead of Dial.
	DialFor func(ctx context.Context, e runtime.Execution) (Sender, func(), error)
	Now     func() time.Time

	// mu holds the decision that nothing is outstanding until the queued
	// record is written; it is never held across core's send.
	mu sync.Mutex
}

func (s *Submitter) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// SubmitRequest is one person's typed submit.
type SubmitRequest struct {
	Execution string
	Text      string
	// Again: the person sends again knowing that an unresolved line and
	// this one may both be typed.
	Again bool
}

func newSubmitID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "s-" + hex.EncodeToString(b)
}

// Submit checks the dispatch rules and the submit's own three, then types the
// line with `if` and force:false. A reply that does not come is not the end of
// the send in core: the record stays pending in core while the call is open,
// and becomes unresolved, never retried, when it cannot be told.
func (s *Submitter) Submit(ctx context.Context, req SubmitRequest) (SubmitRecord, error) {
	text := strings.TrimRight(req.Text, "\n")
	if text == "" || len(text) > MaxSubmitText || strings.ContainsAny(text, "\n\r") {
		return SubmitRecord{}, refuse(CodeSubmitGate, "a typed submit is one line of at most %d bytes", MaxSubmitText)
	}
	s.mu.Lock()
	locked := true
	defer func() {
		if locked {
			s.mu.Unlock()
		}
	}()
	e, err := s.execution(ctx, req.Execution)
	if err != nil {
		return SubmitRecord{}, err
	}
	// An execution the reconciler found lost or replaced is an identity
	// refusal, not a missing status: another program may run at its
	// address.
	if e.Observation.Readiness == runtime.ReadinessIdentityUnknown {
		return SubmitRecord{}, refuse(CodeIdentityNotProven, "execution %s is not proven to run in terminal %s (process %s); nothing is typed there", e.ID, e.TerminalAddress, e.Observation.Process)
	}
	// Rule 6: at least one status since registration (readiness says so).
	// It is checked before the dispatch gates: they refuse the same
	// execution with a less useful reason.
	if e.Observation.Readiness != runtime.ReadinessReady {
		return SubmitRecord{}, refuse(CodeSubmitNoStatus, "execution %s has reported no status since it registered (readiness %s); nothing is typed into a program that reports none", e.ID, e.Observation.Readiness)
	}
	if s.Notices != nil {
		if err := s.Notices.CheckSubmit(ctx, e.ID); err != nil {
			if notice.GateOf(err) == notice.GateStopping {
				return SubmitRecord{}, refuse(CodeRouteStopping, "the route is stopping: a typed submit is refused")
			}
			return SubmitRecord{}, refuse(CodeSubmitGate, "%v", err)
		}
	}
	// Rule 7: at most one outstanding per execution.
	prior, err := s.Store.Submits(e.ID)
	if err != nil {
		return SubmitRecord{}, err
	}
	for _, p := range prior {
		if !p.Outstanding() {
			continue
		}
		if p.State == SubmitUnresolved && req.Again {
			continue
		}
		return SubmitRecord{}, refuse(CodeSubmitOutstanding, "submit %s to execution %s is %s; keep waiting, stop the execution, or send again knowing both may be typed (--again)", p.ID, e.ID, p.State)
	}
	dial := s.Dial
	if s.DialFor != nil {
		dial = func(ctx context.Context) (Sender, func(), error) { return s.DialFor(ctx, e) }
	}
	c, closeSender, err := dial(ctx)
	if err != nil {
		return SubmitRecord{}, err
	}
	defer closeSender()
	t, ok, err := Proven(ctx, c, s.Procs, e)
	if err != nil {
		return SubmitRecord{}, err
	}
	if !ok {
		return SubmitRecord{}, refuse(CodeIdentityNotProven, "terminal %s does not run execution %s", e.TerminalAddress, e.ID)
	}
	// Rule 5: the state the adapter permits, before anything is recorded.
	if t.Status == nil || t.Status.State != SubmitIf {
		state := "no status"
		if t.Status != nil {
			state = t.Status.State
		}
		return SubmitRecord{}, refuse(CodeSubmitState, "the program reports %s, not %s: type in the app's pane instead", state, SubmitIf)
	}
	now := s.now()
	rec := SubmitRecord{ID: newSubmitID(), Execution: e.ID, State: SubmitQueued, TextDigest: notice.Digest(text), Created: now, Updated: now}
	if err := s.Store.PutSubmit(rec); err != nil {
		return SubmitRecord{}, err
	}
	rec.State, rec.Updated = SubmitPendingInCore, s.now()
	if err := s.Store.PutSubmit(rec); err != nil {
		return rec, err
	}
	// The record now blocks a second submit to this execution; core's wait
	// for a person to stop typing has no upper bound, so nothing else waits.
	s.mu.Unlock()
	locked = false
	res, err := c.Send(ctx, t.Address, text, SubmitIf)
	switch {
	case err == nil:
		rec.State = SubmitTyped
		if res.Started != nil && !*res.Started {
			rec.Detail = "core says the turn did not start; that does not prove nothing was typed"
		}
	case rejectedBeforeWrite(err):
		rec.State, rec.Detail = SubmitRejectedBeforeWrite, err.Error()
	default:
		rec.State, rec.Detail = SubmitUnresolved, fmt.Sprintf("core's answer was not received (%v); the line may still be typed", err)
	}
	rec.Updated = s.now()
	if perr := s.Store.PutSubmit(rec); perr != nil {
		return rec, perr
	}
	return rec, nil
}

// rejectedBeforeWrite is a core refusal that proves nothing was written: its
// condition failed, the program is blocked, or the terminal has finished.
func rejectedBeforeWrite(err error) bool {
	var e *hc.Error
	if !errors.As(err, &e) {
		return false
	}
	switch e.Kind {
	case hc.KindStatusMismatch, hc.KindBlocked, hc.KindFinished, hc.KindNotFound, hc.KindInvalidParams:
		return true
	}
	return false
}

func (s *Submitter) execution(ctx context.Context, id string) (runtime.Execution, error) {
	all, err := s.Records.List(ctx, "", "")
	if err != nil {
		return runtime.Execution{}, err
	}
	for _, e := range all {
		if e.ID == id && running(e) {
			return e, nil
		}
	}
	return runtime.Execution{}, refuse(CodeIdentityNotProven, "no running herdr-core execution %s is recorded", id)
}

// Draining reports what a pause says of an execution's typed submits:
// "dispatch_draining" while one is queued or pending in core, "unresolved"
// when one cannot be resolved, "" otherwise.
func (s *Submitter) Draining(execution string) (string, error) {
	if s == nil || s.Store == nil {
		return "", nil
	}
	list, err := s.Store.Submits(execution)
	if err != nil {
		return "", err
	}
	state := ""
	for _, r := range list {
		switch r.State {
		case SubmitQueued, SubmitPendingInCore:
			return "dispatch_draining", nil
		case SubmitUnresolved:
			state = SubmitUnresolved
		}
	}
	return state, nil
}

// Settle marks the submits a daemon restart found pending in core as
// unresolved: core's answer to them is lost, and no time retires them.
func (s *Submitter) Settle() ([]SubmitRecord, error) {
	list, err := s.Store.Submits("")
	if err != nil {
		return nil, err
	}
	var out []SubmitRecord
	for _, r := range list {
		if r.State != SubmitQueued && r.State != SubmitPendingInCore {
			continue
		}
		r.State, r.Detail, r.Updated = SubmitUnresolved, "the daemon restarted while core held the line; it may have been typed", s.now()
		if err := s.Store.PutSubmit(r); err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}
