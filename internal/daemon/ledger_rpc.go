package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/arafathusayn/herdsman/internal/ledger"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// ledgerRPC serves the generated LedgerService over the ledger the verbs use.
//
// inspect is set on a daemon that must change nothing: opening a repository's
// state can migrate it, and a plan notes what it saw.
type ledgerRPC struct {
	d       *Daemon
	inspect bool
}

// request refuses a call an inspect daemon may not serve, or one that names no
// repository or task.
func (l ledgerRPC) request(commonDir, task string) (LedgerRequest, error) {
	if l.inspect {
		return LedgerRequest{}, rpc.Errorf(rpc.FailedPrecondition, "this daemon runs in inspect mode and changes nothing")
	}
	if !filepath.IsAbs(commonDir) {
		return LedgerRequest{}, rpc.Errorf(rpc.InvalidArgument, "common_dir %q is not absolute", commonDir)
	}
	if task == "" {
		return LedgerRequest{}, rpc.Errorf(rpc.InvalidArgument, "the request names no task")
	}
	return LedgerRequest{CommonDir: commonDir, Task: task}, nil
}

func wireRecord(record ledger.Record) *daemonv1.ResourceRecord {
	start := ""
	if !record.Start.IsZero() {
		start = record.Start.UTC().Format(time.RFC3339)
	}
	return &daemonv1.ResourceRecord{
		Task: record.Task, Execution: record.Execution, Kind: record.Kind, Id: record.ID,
		Start: start, Command: record.Command, Adopted: record.Adopted,
	}
}

func wirePlan(plan ledger.Plan) *daemonv1.TeardownPlan {
	wire := &daemonv1.TeardownPlan{SchemaVersion: plan.Schema.Version, Task: plan.Task, Execution: plan.Execution, Worktree: plan.Worktree}
	for _, line := range plan.Lines {
		signals := make([]string, 0, len(line.Signals))
		for _, signal := range line.Signals {
			signals = append(signals, string(signal))
		}
		wire.Lines = append(wire.Lines, &daemonv1.ResourceLine{
			Kind: line.Kind, Id: line.ID, Identity: line.Identity, Level: string(line.Level), Class: line.Class,
			Action: string(line.Action), Signals: signals, SharedWith: line.SharedWith, Role: line.Role,
		})
	}
	return wire
}

// Resources lists a task's records and the live plan built from them.
func (l ledgerRPC) Resources(ctx context.Context, req *daemonv1.ResourcesRequest) (*daemonv1.ResourcesResponse, error) {
	request, err := l.request(req.GetCommonDir(), req.GetTask())
	if err != nil {
		return nil, err
	}
	resources, err := ledgerService.Resources(ctx, request)
	if err != nil {
		return nil, rpc.Errorf(rpc.Internal, "%v", err)
	}
	res := &daemonv1.ResourcesResponse{Plan: wirePlan(resources.Plan)}
	for _, record := range resources.Records {
		res.Records = append(res.Records, wireRecord(record))
	}
	return res, nil
}

// Plan builds the teardown plan for a task.
func (l ledgerRPC) Plan(ctx context.Context, req *daemonv1.PlanRequest) (*daemonv1.PlanResponse, error) {
	request, err := l.request(req.GetCommonDir(), req.GetTask())
	if err != nil {
		return nil, err
	}
	plan, err := ledgerService.Plan(ctx, request)
	if err != nil {
		return nil, rpc.Errorf(rpc.Internal, "%v", err)
	}
	return &daemonv1.PlanResponse{Plan: wirePlan(plan)}, nil
}

// AddResource records authority over a resource for one execution.
func (l ledgerRPC) AddResource(ctx context.Context, req *daemonv1.AddResourceRequest) (*daemonv1.AddResourceResponse, error) {
	request, err := l.request(req.GetCommonDir(), req.GetTask())
	if err != nil {
		return nil, err
	}
	record, err := ledgerService.AddResource(ctx, request, ledger.Record{
		Execution: req.GetExecution(), Kind: req.GetKind(), ID: req.GetId(), Adopted: req.GetAdopted(),
	}, req.GetSource())
	if err != nil {
		return nil, rpc.Errorf(rpc.Internal, "%v", err)
	}
	return &daemonv1.AddResourceResponse{Record: wireRecord(record)}, nil
}

// admit gives a verb the daemon's own lifetime: a teardown that has begun must
// not stop because the command that asked for it went away.
func (l ledgerRPC) admit() (context.Context, *WorktreeService, error) {
	if l.inspect {
		return nil, nil, rpc.Errorf(rpc.FailedPrecondition, "this daemon runs in inspect mode and changes nothing")
	}
	run, e := l.d.recoveryContext()
	if e != nil {
		return nil, nil, rpc.Errorf(rpc.FailedPrecondition, "%s", e.Message)
	}
	l.d.mu.Lock()
	defer l.d.mu.Unlock()
	if l.d.worktrees == nil {
		return nil, nil, rpc.Errorf(rpc.FailedPrecondition, "the daemon is not accepting operations now")
	}
	return run, l.d.worktrees, nil
}

// TeardownPlans builds one plan per selected task.
func (l ledgerRPC) TeardownPlans(_ context.Context, req *daemonv1.TeardownPlansRequest) (*daemonv1.TeardownPlansResponse, error) {
	run, service, err := l.admit()
	if err != nil {
		return nil, err
	}
	planned, err := service.TeardownPlan(run, WorktreeTeardownPlanRequest{Dir: req.GetDir(), Selector: req.GetSelector(), Route: req.GetRoute(), All: req.GetAll()})
	if err != nil {
		return nil, rpc.Errorf(rpc.FailedPrecondition, "%v", err)
	}
	res := &daemonv1.TeardownPlansResponse{Hooks: planned.Hooks}
	for _, plan := range planned.Plans {
		document, err := json.Marshal(plan)
		if err != nil {
			return nil, rpc.Errorf(rpc.Internal, "%v", err)
		}
		res.Plans = append(res.Plans, document)
	}
	return res, nil
}

// Teardown runs approved plans.
func (l ledgerRPC) Teardown(_ context.Context, req *daemonv1.TeardownRequest) (*daemonv1.TeardownResponse, error) {
	run, service, err := l.admit()
	if err != nil {
		return nil, err
	}
	request := WorktreeTeardownRequest{Dir: req.GetDir(), GraceSeconds: int(req.GetGraceSeconds()), IncludeAssociated: req.GetIncludeAssociated()}
	for _, document := range req.GetPlans() {
		var plan ledger.Plan
		if err := json.Unmarshal(document, &plan); err != nil {
			return nil, rpc.Errorf(rpc.InvalidArgument, "a plan is not a plan document: %v", err)
		}
		request.Plans = append(request.Plans, plan)
	}
	done, err := service.Teardown(run, request)
	if err != nil {
		return nil, rpc.Errorf(rpc.FailedPrecondition, "%v", err)
	}
	res := &daemonv1.TeardownResponse{}
	for _, report := range done.Reports {
		wire := &daemonv1.TeardownReport{Task: report.Task, Error: report.Error}
		for _, outcome := range report.Outcomes {
			wire.Outcomes = append(wire.Outcomes, &daemonv1.TeardownOutcome{Kind: outcome.Kind, Id: outcome.ID, Level: string(outcome.Level), Result: outcome.Result})
		}
		res.Reports = append(res.Reports, wire)
	}
	return res, nil
}

// RegisterResource records a resource for the named or the current task.
func (l ledgerRPC) RegisterResource(_ context.Context, req *daemonv1.RegisterResourceRequest) (*daemonv1.RegisterResourceResponse, error) {
	run, service, err := l.admit()
	if err != nil {
		return nil, err
	}
	record, err := service.ResourceAdd(run, WorktreeResourceAddRequest{Dir: req.GetDir(), Task: req.GetTask(), Execution: req.GetExecution(), Kind: req.GetKind(), ID: req.GetId()})
	if err != nil {
		return nil, rpc.Errorf(rpc.FailedPrecondition, "%v", err)
	}
	return &daemonv1.RegisterResourceResponse{Record: wireRecord(record)}, nil
}

// LedgerHealth lists what the folder's task still runs that it alone owns.
func (l ledgerRPC) LedgerHealth(_ context.Context, req *daemonv1.LedgerHealthRequest) (*daemonv1.LedgerHealthResponse, error) {
	run, service, err := l.admit()
	if err != nil {
		return nil, err
	}
	health, err := service.LedgerHealth(run, WorktreeLedgerHealthRequest{Dir: req.GetDir()})
	if err != nil {
		return nil, rpc.Errorf(rpc.FailedPrecondition, "%v", err)
	}
	res := &daemonv1.LedgerHealthResponse{}
	for _, line := range health.Running {
		res.Running = append(res.Running, &daemonv1.ResourceLine{Kind: line.Kind, Id: line.ID, Identity: line.Identity, Level: string(line.Level), Class: line.Class, Action: string(line.Action)})
	}
	return res, nil
}
