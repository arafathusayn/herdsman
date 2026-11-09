package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/internal/ledger"
	"github.com/arafathusayn/herdsman/internal/wtree"
)

// portBlockSize is how many ports a task's block holds, from wtree.HashPort.
const portBlockSize = 10

// LedgerService is the daemon's view of what tasks run and own.
type LedgerService struct {
	// Read fills a snapshot from the machine.
	Read func(ctx context.Context, base ledger.Snapshot) (ledger.Snapshot, error)
	// Run and Kill act on the machine.
	Run ledger.Runner
	// Kill sends a signal.
	Kill ledger.Kill
	// Engine names the container tool.
	Engine func(ctx context.Context) string
	// Facts reads a live process's start time and argv.
	Facts func(ctx context.Context, pid int) (time.Time, string, error)
	// Created reads when a named volume or network was made.
	Created func(ctx context.Context, kind, name string) (time.Time, error)
}

// NewLedgerService uses this machine.
func NewLedgerService() *LedgerService {
	reader := ledger.NewReader()
	return &LedgerService{Read: reader.Read, Run: reader.Run, Kill: syscall.Kill, Engine: reader.EngineName, Facts: reader.Facts, Created: reader.Created}
}

// LedgerRequest names one task of one repository.
type LedgerRequest struct {
	CommonDir string
	Task      string
}

// LedgerResources are a task's records and its live plan.
type LedgerResources struct {
	Records []ledger.Record
	Plan    ledger.Plan
}

// TeardownRequest is one approved stop.
type TeardownRequest struct {
	LedgerRequest
	Approved          ledger.Plan
	Grace             time.Duration
	IncludeAssociated bool
}

func resolved(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return path
}

func portBlock(task string) ledger.PortRange {
	first := wtree.HashPort(task)
	return ledger.PortRange{First: first, Last: first + portBlockSize - 1}
}

func recordedCompose(records []ledger.Record, task, execution string) string {
	for _, record := range records {
		if record.Task == task && record.Execution == execution && record.Kind == ledger.KindCompose {
			return record.ID
		}
	}
	return ""
}

// scopeOf builds what identifies one task. Its execution is the newest one
// recorded and the older ones are its own past.
func scopeOf(ctx context.Context, table *ledger.Table, records []ledger.Record, worktree wtree.Worktree) (ledger.Scope, []string, error) {
	executions, err := table.Executions(ctx, worktree.Task)
	if err != nil {
		return ledger.Scope{}, nil, err
	}
	execution := ""
	var earlier []string
	if len(executions) > 0 {
		execution, earlier = executions[0], executions[1:]
	}
	return ledger.Scope{
		Task: worktree.Task, Execution: execution, Worktree: resolved(worktree.Path),
		Compose: recordedCompose(records, worktree.Task, execution), Ports: portBlock(worktree.Task),
	}, earlier, nil
}

// assemble builds the part of a snapshot that the machine cannot give.
func assemble(ctx context.Context, state *wtree.State, task string) (ledger.Snapshot, error) {
	worktrees, err := state.Worktrees(ctx)
	if err != nil {
		return ledger.Snapshot{}, err
	}
	table := ledger.NewTable(state.DB())
	records, err := table.Records(ctx)
	if err != nil {
		return ledger.Snapshot{}, err
	}
	snapshot := ledger.Snapshot{Records: records}
	found := false
	for _, worktree := range worktrees {
		scope, earlier, err := scopeOf(ctx, table, records, worktree)
		if err != nil {
			return ledger.Snapshot{}, err
		}
		if worktree.Task == task {
			snapshot.Scope, snapshot.Earlier, found = scope, earlier, true
			snapshot.Started = ledger.ExecutionTime(scope.Execution)
			continue
		}
		snapshot.Others = append(snapshot.Others, scope)
	}
	if !found {
		return ledger.Snapshot{}, fmt.Errorf("ledger: no worktree for task %q", task)
	}
	return snapshot, nil
}

// snapshotOf opens the state and reads what the machine cannot give.
func (s *LedgerService) snapshotOf(ctx context.Context, request LedgerRequest) (ledger.Snapshot, *wtree.State, error) {
	state, err := wtree.OpenState(request.CommonDir)
	if err != nil {
		return ledger.Snapshot{}, nil, err
	}
	base, err := assemble(ctx, state, request.Task)
	if err != nil {
		state.Close()
		return ledger.Snapshot{}, nil, err
	}
	return base, state, nil
}

// Plan builds a task's teardown plan and notes what it saw, without acting on
// anything it lists.
func (s *LedgerService) Plan(ctx context.Context, request LedgerRequest) (ledger.Plan, error) {
	base, state, err := s.snapshotOf(ctx, request)
	if err != nil {
		return ledger.Plan{}, err
	}
	defer state.Close()
	live, err := s.Read(ctx, base)
	if err != nil {
		return ledger.Plan{}, err
	}
	plan := ledger.BuildPlan(live)
	if err := ledger.NewTable(state.DB()).Observe(ctx, plan); err != nil {
		return ledger.Plan{}, err
	}
	return plan, nil
}

// Resources lists a task's records and its live plan.
func (s *LedgerService) Resources(ctx context.Context, request LedgerRequest) (LedgerResources, error) {
	base, state, err := s.snapshotOf(ctx, request)
	if err != nil {
		return LedgerResources{}, err
	}
	defer state.Close()
	live, err := s.Read(ctx, base)
	if err != nil {
		return LedgerResources{}, err
	}
	own := LedgerResources{Plan: ledger.BuildPlan(live)}
	if err := ledger.NewTable(state.DB()).Observe(ctx, own.Plan); err != nil {
		return LedgerResources{}, err
	}
	for _, record := range base.Records {
		if record.Task == request.Task {
			own.Records = append(own.Records, record)
		}
	}
	return own, nil
}

// AddResource records authority over a resource. A process, volume or network
// record is filled from the machine, so that a later proof can tell it from a
// reused pid or name; one whose identity cannot be read is refused.
func (s *LedgerService) AddResource(ctx context.Context, request LedgerRequest, record ledger.Record, source string) (ledger.Record, error) {
	state, err := wtree.OpenState(request.CommonDir)
	if err != nil {
		return ledger.Record{}, err
	}
	defer state.Close()
	table := ledger.NewTable(state.DB())
	record.Task = request.Task
	if record.Execution == "" {
		executions, err := table.Executions(ctx, request.Task)
		if err != nil {
			return ledger.Record{}, err
		}
		if len(executions) == 0 {
			return ledger.Record{}, fmt.Errorf("ledger: task %q has no execution yet", request.Task)
		}
		record.Execution = executions[0]
	}
	if record.Kind == ledger.KindVolume || record.Kind == ledger.KindNetwork {
		if record.Start, err = s.Created(ctx, record.Kind, record.ID); err != nil {
			return ledger.Record{}, err
		}
	}
	if record.Kind == ledger.KindProcess {
		var pid int
		if _, err := fmt.Sscanf(record.ID, "%d", &pid); err != nil {
			return ledger.Record{}, fmt.Errorf("ledger: process id %q: %w", record.ID, err)
		}
		if record.Start, record.Command, err = s.Facts(ctx, pid); err != nil {
			return ledger.Record{}, err
		}
	}
	if err := table.Add(ctx, record, source); err != nil {
		return ledger.Record{}, err
	}
	return record, nil
}

// Teardown stops what the approved plan names, revalidating before each act.
func (s *LedgerService) Teardown(ctx context.Context, request TeardownRequest) ([]ledger.Outcome, error) {
	base, state, err := s.snapshotOf(ctx, request.LedgerRequest)
	if err != nil {
		return nil, err
	}
	defer state.Close()
	log, err := ledger.OpenLog(request.CommonDir, request.Task)
	if err != nil {
		return nil, err
	}
	defer log.Close()
	execution := &ledger.Execution{
		Plan: request.Approved, Kill: s.Kill, Run: s.Run, Engine: s.Engine(ctx), Compose: base.Compose,
		IncludeAssociated: request.IncludeAssociated, Grace: request.Grace, Log: log,
		// The records are read again each time, so that a claim another task
		// makes during the grace period is seen before the next signal.
		Read: func(ctx context.Context) (ledger.Snapshot, error) {
			current, err := assemble(ctx, state, request.Task)
			if err != nil {
				return ledger.Snapshot{}, err
			}
			return s.Read(ctx, current)
		},
	}
	return execution.Execute(ctx)
}

// ledgerService is the ledger the worktree verbs use; tests replace it.
var ledgerService = NewLedgerService()

// WorktreeTeardownPlanRequest names the tasks to plan for: one selector, every
// task of a route folder, or every task.
type WorktreeTeardownPlanRequest struct {
	Dir      string `json:"dir"`
	Selector string `json:"selector,omitempty"`
	Route    string `json:"route,omitempty"`
	All      bool   `json:"all,omitempty"`
}

// WorktreeTeardownPlanResponse holds one plan per task.
type WorktreeTeardownPlanResponse struct {
	Plans []ledger.Plan `json:"plans"`
	// Hooks says a teardown would run configured hooks even with nothing to stop.
	Hooks bool `json:"hooks,omitempty"`
}

// WorktreeTeardownRequest carries what a person approved.
type WorktreeTeardownRequest struct {
	Dir               string        `json:"dir"`
	Plans             []ledger.Plan `json:"plans"`
	GraceSeconds      int           `json:"grace_seconds,omitempty"`
	IncludeAssociated bool          `json:"include_associated,omitempty"`
}

// WorktreeTeardownReport is what happened to one task.
type WorktreeTeardownReport struct {
	Task     string           `json:"task"`
	Outcomes []ledger.Outcome `json:"outcomes"`
	Error    string           `json:"error,omitempty"`
}

// WorktreeTeardownResponse holds one report per plan.
type WorktreeTeardownResponse struct {
	Reports []WorktreeTeardownReport `json:"reports"`
}

// WorktreeResourceAddRequest registers a resource for a task.
type WorktreeResourceAddRequest struct {
	Dir       string `json:"dir"`
	Task      string `json:"task,omitempty"`
	Execution string `json:"execution,omitempty"`
	Kind      string `json:"kind"`
	ID        string `json:"id"`
}

// WorktreeLedgerHealthRequest asks which running resources a worktree holds.
type WorktreeLedgerHealthRequest struct {
	Dir string `json:"dir"`
}

// WorktreeLedgerHealthResponse lists the exclusive resources still running.
type WorktreeLedgerHealthResponse struct {
	Running []ledger.Line `json:"running"`
}

// targets resolves a request to worktree rows that have a state record.
func (r *repository) targets(ctx context.Context, req WorktreeTeardownPlanRequest) ([]wtree.Row, error) {
	if req.All || req.Route != "" {
		names, err := r.taskNames(ctx, req.Route)
		if err != nil {
			return nil, err
		}
		var rows []wtree.Row
		for _, row := range r.rows {
			if slices.Contains(names, row.Task) {
				rows = append(rows, row)
			}
		}
		return rows, nil
	}
	selector := wtree.Selector{Kind: wtree.SelectCurrent}
	if req.Selector != "" {
		var err error
		if selector, err = wtree.ParseSelector(req.Selector); err != nil {
			return nil, err
		}
	}
	row, err := wtree.ResolveSelector(selector, r.selectContext(""))
	if err != nil {
		return nil, err
	}
	return []wtree.Row{row}, nil
}

// taskNames lists every recorded worktree's task, or those of one route.
func (r *repository) taskNames(ctx context.Context, route string) ([]string, error) {
	if route != "" {
		return queryStrings(ctx, r.state, `SELECT task FROM tasks WHERE route = ? ORDER BY task`, route)
	}
	worktrees, err := r.state.Worktrees(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(worktrees))
	for _, worktree := range worktrees {
		names = append(names, worktree.Task)
	}
	return names, nil
}

func queryStrings(ctx context.Context, state *wtree.State, query string, args ...any) ([]string, error) {
	rows, err := state.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// TeardownPlan builds one plan per named task and changes nothing.
func (s *WorktreeService) TeardownPlan(ctx context.Context, req WorktreeTeardownPlanRequest) (WorktreeTeardownPlanResponse, error) {
	var resp WorktreeTeardownPlanResponse
	r, err := s.open(ctx, req.Dir)
	if err != nil {
		return resp, err
	}
	defer r.close()
	rows, err := r.targets(ctx, req)
	if err != nil {
		return resp, err
	}
	for _, row := range rows {
		plan, err := ledgerService.Plan(ctx, LedgerRequest{CommonDir: r.common, Task: row.Task})
		if err != nil {
			return resp, err
		}
		resp.Plans = append(resp.Plans, plan)
		hooked, err := r.hasTeardownHooks(row)
		if err != nil {
			return resp, err
		}
		resp.Hooks = resp.Hooks || hooked
	}
	return resp, nil
}

// hasTeardownHooks reports a task whose teardown would run a configured hook.
func (r *repository) hasTeardownHooks(row wtree.Row) (bool, error) {
	vars := r.vars(row.Task, row.Branch, row.Path, "feat", "", row.Head)
	for _, hook := range []string{"pre-teardown", "post-teardown"} {
		plan, err := r.hooks.Plan(hook, vars)
		if err != nil {
			return false, err
		}
		if len(plan) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// Teardown stops every approved plan. One task's failure is reported and does
// not stop the next, because their plans are independent.
func (s *WorktreeService) Teardown(ctx context.Context, req WorktreeTeardownRequest) (WorktreeTeardownResponse, error) {
	var resp WorktreeTeardownResponse
	r, err := s.open(ctx, req.Dir)
	if err != nil {
		return resp, err
	}
	defer r.close()
	for _, plan := range req.Plans {
		report := WorktreeTeardownReport{Task: plan.Task}
		var err error
		if report.Outcomes, err = s.teardownOne(ctx, r, req, plan); err != nil {
			report.Error = err.Error()
		}
		resp.Reports = append(resp.Reports, report)
	}
	return resp, nil
}

// ResourceAdd registers a resource for the named or the current task.
func (s *WorktreeService) ResourceAdd(ctx context.Context, req WorktreeResourceAddRequest) (ledger.Record, error) {
	r, err := s.open(ctx, req.Dir)
	if err != nil {
		return ledger.Record{}, err
	}
	defer r.close()
	task := req.Task
	if task == "" {
		row, err := wtree.ResolveSelector(wtree.Selector{Kind: wtree.SelectCurrent}, r.selectContext(""))
		if err != nil {
			return ledger.Record{}, err
		}
		task = row.Task
	}
	return ledgerService.AddResource(ctx, LedgerRequest{CommonDir: r.common, Task: task},
		ledger.Record{Execution: req.Execution, Kind: req.Kind, ID: req.ID}, "resource add")
}

// LedgerHealth lists what the worktree's task still runs with exclusive
// evidence, for the waiter's health check.
func (s *WorktreeService) LedgerHealth(ctx context.Context, req WorktreeLedgerHealthRequest) (WorktreeLedgerHealthResponse, error) {
	var resp WorktreeLedgerHealthResponse
	r, err := s.open(ctx, req.Dir)
	if err != nil {
		return resp, err
	}
	defer r.close()
	row, err := wtree.ResolveSelector(wtree.Selector{Kind: wtree.SelectCurrent}, r.selectContext(""))
	if err != nil {
		return resp, err
	}
	plan, err := ledgerService.Plan(ctx, LedgerRequest{CommonDir: r.common, Task: row.Task})
	if err != nil {
		return resp, err
	}
	for _, line := range plan.Lines {
		if line.Action == ledger.ActionStop {
			resp.Running = append(resp.Running, line)
		}
	}
	return resp, nil
}
