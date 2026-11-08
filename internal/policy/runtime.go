package policy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// TeardownReport says what a teardown did and what it deliberately left alone.
type TeardownReport struct {
	Stopped     []string
	LeftRunning []string
}

// Teardowner stops the processes and containers attributed exclusively to a
// scope, waiting at most grace before forcing them. Shared and unknown
// resources stay running and come back in the report. Nothing is deleted.
type Teardowner interface {
	Teardown(ctx context.Context, scope Scope, grace time.Duration) (TeardownReport, error)
}

// EventBudgetExhausted is written once each time a limit is reached.
const EventBudgetExhausted = "budget.exhausted"

// Event is a record of a limit being reached.
type Event struct {
	Kind        string
	Scope       Scope
	Exceeded    Exceeded
	Text        string
	LeftRunning []string
	// Failed holds the error when the first attempt to act did not succeed.
	Failed string
}

// ActionKind says how a reached limit was enforced.
type ActionKind string

// The enforcement actions.
const (
	ActionInterrupt ActionKind = "interrupt"
	ActionTeardown  ActionKind = "teardown"
)

// Action is one enforcement that ran.
type Action struct {
	Kind     ActionKind
	Scope    Scope
	Exceeded Exceeded
	Report   TeardownReport
}

type limitKey struct {
	scope     Scope
	dimension Dimension
}

// Enforcer turns usage samples into at most one action per reached limit. A
// machine limit runs teardown; wall, tokens and cost interrupt. An action that
// failed is retried on the next sample, and raising a budget rearms its limit.
// Calls are serialized, so two samples can never start two teardowns.
type Enforcer struct {
	Policy    Policy
	Grace     time.Duration
	Now       func() time.Time
	Interrupt Interrupter
	Teardown  Teardowner
	Emit      func(Event)

	mu        sync.Mutex
	acted     map[limitKey]bool
	announced map[limitKey]bool
}

// SetPolicy replaces the policy, as when a person raises a budget.
func (e *Enforcer) SetPolicy(p Policy) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Policy = p
}

// Observe applies one usage sample for a scope. It returns the actions that
// ran; failures are joined so one limit's error cannot hide another's action.
func (e *Enforcer) Observe(ctx context.Context, scope Scope, usage Usage) ([]Action, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.acted == nil {
		e.acted, e.announced = map[limitKey]bool{}, map[limitKey]bool{}
	}
	now := time.Now
	if e.Now != nil {
		now = e.Now
	}
	exceeded := e.Policy.Budget.Check(usage, now())
	e.rearm(scope, exceeded)

	var (
		actions []Action
		errs    []error
	)
	for _, over := range exceeded {
		key := limitKey{scope, over.Dimension}
		if e.acted[key] {
			continue
		}
		action, err := e.act(ctx, scope, over)
		if err == nil {
			e.acted[key] = true
			actions = append(actions, action)
		} else {
			errs = append(errs, err)
		}
		if !e.announced[key] {
			e.announced[key] = true
			e.emit(scope, over, action, err)
		}
	}
	return actions, errors.Join(errs...)
}

func (e *Enforcer) rearm(scope Scope, exceeded []Exceeded) {
	active := map[Dimension]bool{}
	for _, over := range exceeded {
		active[over.Dimension] = true
	}
	for _, dimension := range []Dimension{DimensionWall, DimensionMachine, DimensionTokens, DimensionCost} {
		if !active[dimension] {
			key := limitKey{scope, dimension}
			delete(e.acted, key)
			delete(e.announced, key)
		}
	}
}

func (e *Enforcer) act(ctx context.Context, scope Scope, over Exceeded) (Action, error) {
	if over.Dimension == DimensionMachine {
		if e.Teardown == nil {
			return Action{}, fmt.Errorf("policy: no teardown port to enforce %s", describe(scope, over))
		}
		grace := e.Grace
		if grace <= 0 {
			grace = DefaultGrace
		}
		report, err := e.Teardown.Teardown(ctx, scope, grace)
		if err != nil {
			return Action{}, fmt.Errorf("policy: teardown of %s: %w", scope, err)
		}
		return Action{Kind: ActionTeardown, Scope: scope, Exceeded: over, Report: report}, nil
	}
	if e.Interrupt == nil {
		return Action{}, fmt.Errorf("policy: no interrupt port to enforce %s", describe(scope, over))
	}
	if err := e.Interrupt.Interrupt(ctx, scope); err != nil {
		return Action{}, fmt.Errorf("policy: interrupt of %s: %w", scope, err)
	}
	return Action{Kind: ActionInterrupt, Scope: scope, Exceeded: over}, nil
}

func (e *Enforcer) emit(scope Scope, over Exceeded, action Action, err error) {
	if e.Emit == nil {
		return
	}
	event := Event{
		Kind: EventBudgetExhausted, Scope: scope, Exceeded: over,
		Text: describe(scope, over), LeftRunning: action.Report.LeftRunning,
	}
	if err != nil {
		event.Failed = err.Error()
	}
	e.Emit(event)
}
