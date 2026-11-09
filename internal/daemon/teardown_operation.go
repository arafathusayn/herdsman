package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/arafathusayn/herdsman/internal/ledger"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/wtree"
)

// The steps of a teardown, in the order they run.
const (
	effectTeardownBefore = "teardown-before-hooks"
	effectTeardownStop   = "teardown-stop"
	effectTeardownAfter  = "teardown-after-hooks"
)

// teardownStep is what every step needs to run again in a new process.
type teardownStep struct {
	Dir               string      `json:"dir"`
	Plan              ledger.Plan `json:"plan"`
	GraceSeconds      int         `json:"grace_seconds,omitempty"`
	IncludeAssociated bool        `json:"include_associated,omitempty"`
}

// teardownJournal runs teardowns as operations. What it holds in memory is only
// how a step that ran here ended: a restart forgets it, and then a probe must
// answer from the world.
type teardownJournal struct {
	service *WorktreeService
	mu      sync.Mutex
	failed  map[string]bool
	stopped map[string][]ledger.Outcome
}

func newTeardownJournal(service *WorktreeService) *teardownJournal {
	return &teardownJournal{service: service, failed: map[string]bool{}, stopped: map[string][]ledger.Outcome{}}
}

// effects are the operation engine's handlers for the three steps.
func (j *teardownJournal) effects() map[string]ops.Effect {
	return map[string]ops.Effect{
		effectTeardownBefore: teardownEffect{j, runBeforeHooks},
		effectTeardownStop:   teardownEffect{j, runStop},
		effectTeardownAfter:  teardownEffect{j, runAfterHooks},
	}
}

func (j *teardownJournal) noteFailed(st ops.Step) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.failed[st.OpID+st.Key] = true
}

func (j *teardownJournal) ranAndFailed(st ops.Step) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.failed[st.OpID+st.Key]
}

func (j *teardownJournal) keep(opID string, outcomes []ledger.Outcome) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.stopped[opID] = outcomes
}

// take hands over a finished operation's outcomes and forgets how it ran.
func (j *teardownJournal) take(op *ops.Operation) []ledger.Outcome {
	j.mu.Lock()
	defer j.mu.Unlock()
	outcomes := j.stopped[op.ID]
	delete(j.stopped, op.ID)
	for _, st := range op.Steps {
		delete(j.failed, op.ID+st.Key)
	}
	return outcomes
}

type teardownApply func(ctx context.Context, j *teardownJournal, opID string, step teardownStep) error

// teardownEffect is one step of a teardown.
type teardownEffect struct {
	journal *teardownJournal
	run     teardownApply
}

func decodeTeardownStep(st ops.Step) (teardownStep, error) {
	var step teardownStep
	if err := json.Unmarshal([]byte(st.Spec), &step); err != nil {
		return step, fmt.Errorf("teardown step %s: %w", st.Key, err)
	}
	return step, nil
}

// Apply runs the step. A failure it saw for itself is remembered, so that the
// probe can tell it from a step cut off by a crash.
func (fx teardownEffect) Apply(ctx context.Context, st ops.Step) error {
	step, err := decodeTeardownStep(st)
	if err != nil {
		return err
	}
	if err := fx.run(ctx, fx.journal, st.OpID, step); err != nil {
		fx.journal.noteFailed(st)
		return err
	}
	return nil
}

// Probe says what a step left behind. A hook's effects cannot be read back, so
// an interrupted one stays undecided and a person resolves it. A stop can be:
// nothing left to stop means it finished.
func (fx teardownEffect) Probe(ctx context.Context, st ops.Step) (ops.Verdict, string, error) {
	if fx.journal.ranAndFailed(st) {
		return ops.Absent, "the step ran here and failed", nil
	}
	if st.Effect != effectTeardownStop {
		return ops.Unknown, "whether the hook finished cannot be read back", nil
	}
	step, err := decodeTeardownStep(st)
	if err != nil {
		return ops.Unknown, "", err
	}
	current, err := fx.journal.service.currentPlan(ctx, step.Dir, step.Plan.Task)
	if err != nil {
		return ops.Unknown, "", err
	}
	for _, line := range current.Lines {
		if line.Action == ledger.ActionStop || line.Action == ledger.ActionAsk && step.IncludeAssociated {
			return ops.Unknown, line.Kind + " " + line.ID + " still runs", nil
		}
	}
	return ops.Matches, "", nil
}

// hookContext opens the repository a step's hooks belong to.
func (s *WorktreeService) hookContext(ctx context.Context, dir, task string) (*repository, wtree.Row, wtree.Vars, error) {
	r, err := s.open(ctx, dir)
	if err != nil {
		return nil, wtree.Row{}, wtree.Vars{}, err
	}
	row, err := wtree.ResolveSelector(wtree.Selector{Kind: wtree.SelectName, Value: task}, r.selectContext(""))
	if err != nil {
		r.close()
		return nil, wtree.Row{}, wtree.Vars{}, err
	}
	return r, row, r.vars(row.Task, row.Branch, row.Path, "feat", "", row.Head), nil
}

func (s *WorktreeService) currentPlan(ctx context.Context, dir, task string) (ledger.Plan, error) {
	r, err := s.open(ctx, dir)
	if err != nil {
		return ledger.Plan{}, err
	}
	defer r.close()
	return ledgerService.Plan(ctx, LedgerRequest{CommonDir: r.common, Task: task})
}

func runBeforeHooks(ctx context.Context, j *teardownJournal, _ string, step teardownStep) error {
	r, row, vars, err := j.service.hookContext(ctx, step.Dir, step.Plan.Task)
	if err != nil {
		return err
	}
	defer r.close()
	plan, err := j.service.plan(r, "pre-teardown", vars)
	if err != nil {
		return err
	}
	_, err = r.runner(vars, row.Path).Run(ctx, "pre-teardown", plan)
	return err
}

func runStop(ctx context.Context, j *teardownJournal, opID string, step teardownStep) error {
	r, err := j.service.open(ctx, step.Dir)
	if err != nil {
		return err
	}
	defer r.close()
	outcomes, err := ledgerService.Teardown(ctx, TeardownRequest{
		LedgerRequest: LedgerRequest{CommonDir: r.common, Task: step.Plan.Task}, Approved: step.Plan,
		Grace: time.Duration(step.GraceSeconds) * time.Second, IncludeAssociated: step.IncludeAssociated,
	})
	j.keep(opID, outcomes)
	return err
}

func runAfterHooks(ctx context.Context, j *teardownJournal, _ string, step teardownStep) error {
	r, row, vars, err := j.service.hookContext(ctx, step.Dir, step.Plan.Task)
	if err != nil {
		return err
	}
	defer r.close()
	plan, err := j.service.plan(r, "post-teardown", vars)
	if err != nil {
		return err
	}
	_, err = r.runner(vars, row.Path).Start("post-teardown", plan)
	return err
}

// teardownSteps lists one task's steps. Hooks that were not approved stop it
// here, before anything is journaled or run.
func (s *WorktreeService) teardownSteps(ctx context.Context, r *repository, req WorktreeTeardownRequest, plan ledger.Plan) ([]ops.StepSpec, error) {
	row, err := wtree.ResolveSelector(wtree.Selector{Kind: wtree.SelectName, Value: plan.Task}, r.selectContext(""))
	if err != nil {
		return nil, err
	}
	vars := r.vars(row.Task, row.Branch, row.Path, "feat", "", row.Head)
	before, err := s.plan(r, "pre-teardown", vars)
	if err != nil {
		return nil, err
	}
	after, err := s.plan(r, "post-teardown", vars)
	if err != nil {
		return nil, err
	}
	spec, err := json.Marshal(teardownStep{Dir: req.Dir, Plan: plan, GraceSeconds: req.GraceSeconds, IncludeAssociated: req.IncludeAssociated})
	if err != nil {
		return nil, err
	}
	var steps []ops.StepSpec
	if len(before) > 0 {
		steps = append(steps, ops.StepSpec{Name: "pre-teardown hooks of " + plan.Task, Effect: effectTeardownBefore, Key: "before", Spec: string(spec), Retry: ops.RetryNever})
	}
	// Repeating this step is safe: each signal is proved again on its own.
	steps = append(steps, ops.StepSpec{Name: "stop what " + plan.Task + " runs", Effect: effectTeardownStop, Key: "stop", Spec: string(spec), Retry: ops.RetryIdempotent})
	if len(after) > 0 {
		steps = append(steps, ops.StepSpec{Name: "post-teardown hooks of " + plan.Task, Effect: effectTeardownAfter, Key: "after", Spec: string(spec), Retry: ops.RetryNever})
	}
	return steps, nil
}

// teardownOne runs one task's teardown as a task-locked operation.
func (s *WorktreeService) teardownOne(ctx context.Context, r *repository, req WorktreeTeardownRequest, plan ledger.Plan) ([]ledger.Outcome, error) {
	if s.engine == nil {
		return nil, errors.New("teardown runs only in the daemon, which journals it")
	}
	steps, err := s.teardownSteps(ctx, r, req, plan)
	if err != nil {
		return nil, err
	}
	op, err := s.engine.Submit(ctx, ops.Spec{Kind: "teardown", Plan: "teardown of " + plan.Task, Locks: []string{ops.TaskLock(plan.Task)}, Steps: steps})
	var locked *ops.LockedError
	if errors.As(err, &locked) {
		return nil, fmt.Errorf("task %q is busy with operation %s; if that one stopped, resolve it first", plan.Task, locked.Holder)
	}
	if err != nil {
		return nil, err
	}
	done, err := s.engine.Drive(ctx, op.ID)
	if err != nil {
		return nil, err
	}
	outcomes := s.journal.take(done)
	if done.State != ops.OpDone {
		return outcomes, errors.New(done.Reason)
	}
	return outcomes, nil
}

// useTeardownOperations gives the service its journal and engine.
func (s *WorktreeService) useTeardownOperations(journal *teardownJournal, engine *ops.Engine) {
	s.journal, s.engine = journal, engine
}
