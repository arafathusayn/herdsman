package daemon

import (
	"context"
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
type ledgerRPC struct{ inspect bool }

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
