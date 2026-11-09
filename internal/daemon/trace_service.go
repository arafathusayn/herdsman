package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/arafathusayn/herdsman/internal/trace"
	"github.com/arafathusayn/herdsman/sdk/rpc"
	tracev1 "github.com/arafathusayn/herdsman/sdk/trace/v1"
)

// traceRPC serves route traces and evaluation records. Paths in its requests
// are absolute because the daemon's working folder is not the caller's.
type traceRPC struct {
	inspect bool // A daemon in inspect mode changes nothing.
}

var _ tracev1.TraceService = (*traceRPC)(nil)

// NewTraceService returns the trace service for a caller in this process.
func NewTraceService(inspect bool) tracev1.TraceService { return &traceRPC{inspect: inspect} }

func (r *traceRPC) refuseInInspect(method string) error {
	if !r.inspect {
		return nil
	}
	return rpc.Errorf(rpc.FailedPrecondition, "this daemon runs in inspect mode and changes nothing; %s was refused", method)
}

func needAbsolute(what, path string) error {
	if !filepath.IsAbs(path) {
		return rpc.Errorf(rpc.InvalidArgument, "the %s must be an absolute path, not %q", what, path)
	}
	return nil
}

func traceFailure(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	switch {
	case errors.Is(err, trace.ErrNoControl):
		return rpc.Errorf(rpc.FailedPrecondition, "%s", err)
	case errors.Is(err, trace.ErrInvalid):
		return rpc.Errorf(rpc.InvalidArgument, "%s", err)
	case errors.Is(err, fs.ErrNotExist):
		return rpc.Errorf(rpc.NotFound, "%s", err)
	}
	return rpc.Errorf(rpc.Internal, "%s", err)
}

func (r *traceRPC) collect(route string, hookLogs []string, usage string) (trace.Trace, error) {
	if err := needAbsolute("route folder", route); err != nil {
		return trace.Trace{}, err
	}
	return trace.Collect(trace.Sources{Route: route, HookLogs: hookLogs, UsageFile: usage, ReadOnly: r.inspect})
}

func spanToProto(s trace.Span) *tracev1.Span {
	out := &tracev1.Span{
		TraceId: s.TraceID, SpanId: s.SpanID, ParentId: s.ParentID, Kind: s.Kind, Name: s.Name, Task: s.Task,
		Start: s.Start.Format(time.RFC3339Nano), DurationMs: s.DurationMS, Tokens: s.Tokens, CostMicros: s.CostMicros,
		Args: s.Args, StartApproximate: s.StartApproximate,
	}
	if s.Exit != nil {
		exit := int32(*s.Exit)
		out.Exit = &exit
	}
	return out
}

func spanFromProto(s *tracev1.Span) (trace.Span, error) {
	start, err := time.Parse(time.RFC3339Nano, s.GetStart())
	if err != nil {
		return trace.Span{}, fmt.Errorf("%w: the span's start: %v", trace.ErrInvalid, err)
	}
	out := trace.Span{Kind: s.GetKind(), Name: s.GetName(), Task: s.GetTask(), Start: start, DurationMS: s.GetDurationMs(), Tokens: s.Tokens, CostMicros: s.CostMicros, Args: s.GetArgs()}
	if s.Exit != nil {
		exit := int(*s.Exit)
		out.Exit = &exit
	}
	return out, nil
}

// Get serves one route's spans.
func (r *traceRPC) Get(ctx context.Context, req *tracev1.GetRequest) (*tracev1.GetResponse, error) {
	collected, err := r.collect(req.GetRoute(), req.GetHookLogs(), req.GetUsageFile())
	if err != nil {
		return nil, wrapTrace(ctx, err)
	}
	out := &tracev1.RouteTrace{Version: collected.Schema.Version, Route: collected.Route, TraceId: collected.TraceID, Skipped: int32(collected.Skipped)}
	for _, span := range collected.Spans {
		out.Spans = append(out.Spans, spanToProto(span))
	}
	return &tracev1.GetResponse{Trace: out}, nil
}

// wrapTrace leaves a failure that already has its code alone.
func wrapTrace(ctx context.Context, err error) error {
	var coded *rpc.Error
	if errors.As(err, &coded) {
		return err
	}
	return traceFailure(ctx, err)
}

// Export posts a route's spans to a collector.
func (r *traceRPC) Export(ctx context.Context, req *tracev1.ExportRequest) (*tracev1.ExportResponse, error) {
	if err := r.refuseInInspect("Export"); err != nil {
		return nil, err
	}
	collected, err := r.collect(req.GetRoute(), req.GetHookLogs(), req.GetUsageFile())
	if err == nil {
		err = trace.ExportOTLP(ctx, req.GetEndpoint(), collected)
	}
	if err != nil {
		return nil, wrapTrace(ctx, err)
	}
	return &tracev1.ExportResponse{Spans: int32(len(collected.Spans)), Skipped: int32(collected.Skipped)}, nil
}

// AddSpan records a span in the route's log.
func (r *traceRPC) AddSpan(ctx context.Context, req *tracev1.AddSpanRequest) (*tracev1.AddSpanResponse, error) {
	if err := r.refuseInInspect("AddSpan"); err != nil {
		return nil, err
	}
	if err := needAbsolute("route folder", req.GetRoute()); err != nil {
		return nil, err
	}
	span, err := spanFromProto(req.GetSpan())
	if err == nil {
		err = trace.Append(req.GetRoute(), span)
	}
	if err != nil {
		return nil, wrapTrace(ctx, err)
	}
	return &tracev1.AddSpanResponse{}, nil
}

func intervalToProto(i *trace.Interval) *tracev1.Interval {
	if i == nil {
		return nil
	}
	return &tracev1.Interval{Low: i.Low, High: i.High}
}

func reportToProto(report trace.Report) *tracev1.EvalReport {
	out := &tracev1.EvalReport{Version: report.Schema.Version, Experimental: report.Experimental, Suite: report.Suite, Skipped: int32(report.Skipped)}
	for _, arm := range report.Arms {
		out.Arms = append(out.Arms, &tracev1.ArmReport{Arm: arm.Arm, Runs: int32(arm.Runs), Passed: int32(arm.Passed), PassRate: intervalToProto(arm.PassRate), DurationMs: intervalToProto(arm.DurationMS), CostRuns: int32(arm.CostRuns), CostMicros: intervalToProto(arm.CostMicros)})
	}
	for _, control := range report.Controls {
		out.Controls = append(out.Controls, &tracev1.ControlReport{Arm: control.Arm, RunsA: int32(control.RunsA), RunsB: int32(control.RunsB), PassRateDifference: intervalToProto(control.PassRateDifference), DurationMsDifference: intervalToProto(control.DurationMSDifference), CostMicrosDifference: intervalToProto(control.CostMicrosDifference)})
	}
	if report.Findings != nil {
		counts := &tracev1.FindingCounts{Recorded: int32(report.Findings.Recorded), Labeled: int32(report.Findings.Labeled), Open: int32(report.Findings.Open)}
		for _, row := range report.Findings.Reviewers {
			counts.Reviewers = append(counts.Reviewers, &tracev1.ReviewerCounts{Reviewer: row.Reviewer, Recorded: int32(row.Recorded), Open: int32(row.Open), Accepted: int32(row.Accepted), Fixed: int32(row.Fixed), Dismissed: int32(row.Dismissed)})
		}
		out.Findings = counts
	}
	if report.Rank != nil {
		out.Rank = &tracev1.RankDecision{Refused: report.Rank.Refused, LabeledSet: report.Rank.LabeledSet, Reason: report.Rank.Reason}
	}
	return out
}

// Evaluate reports a suite's recorded runs and any findings asked for.
func (r *traceRPC) Evaluate(ctx context.Context, req *tracev1.EvaluateRequest) (*tracev1.EvaluateResponse, error) {
	if err := needAbsolute("suite folder", req.GetSuite()); err != nil {
		return nil, err
	}
	runs, skipped, err := trace.LoadRuns(req.GetSuite())
	if err != nil {
		return nil, traceFailure(ctx, err)
	}
	report, err := trace.Evaluate(req.GetSuite(), runs, skipped)
	if err != nil {
		return nil, traceFailure(ctx, err)
	}
	res := &tracev1.EvaluateResponse{}
	var found []trace.Finding
	for _, route := range req.GetFindingRoutes() {
		some, unreadable, err := trace.LoadFindings(route)
		if err != nil {
			return nil, traceFailure(ctx, err)
		}
		if unreadable > 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf("skipped %d unreadable finding lines in %s", unreadable, route))
		}
		found = append(found, some...)
	}
	counts := trace.CountFindings(found)
	if len(req.GetFindingRoutes()) > 0 {
		report.Findings = &counts
	}
	if req.GetRank() {
		decision := trace.DecideRank(counts)
		report.Rank = &decision
	}
	if report.Skipped > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("skipped %d unreadable run lines", report.Skipped))
	}
	res.Report = reportToProto(report)
	return res, nil
}

// RecordRun appends one run to a suite.
func (r *traceRPC) RecordRun(ctx context.Context, req *tracev1.RecordRunRequest) (*tracev1.RecordRunResponse, error) {
	if err := r.refuseInInspect("RecordRun"); err != nil {
		return nil, err
	}
	if err := needAbsolute("suite folder", req.GetSuite()); err != nil {
		return nil, err
	}
	pass, duration := req.GetRun().GetPass(), req.GetRun().GetDurationMs()
	run := trace.Run{Task: req.GetRun().GetTask(), Arm: req.GetRun().GetArm(), Control: req.GetRun().GetControl(), Pass: &pass, DurationMS: &duration, CostMicros: req.GetRun().CostMicros}
	if err := trace.AppendRun(req.GetSuite(), run); err != nil {
		return nil, traceFailure(ctx, err)
	}
	return &tracev1.RecordRunResponse{}, nil
}

// AddFinding records a reviewer's finding.
func (r *traceRPC) AddFinding(ctx context.Context, req *tracev1.AddFindingRequest) (*tracev1.AddFindingResponse, error) {
	if err := r.refuseInInspect("AddFinding"); err != nil {
		return nil, err
	}
	if err := needAbsolute("route folder", req.GetRoute()); err != nil {
		return nil, err
	}
	f := req.GetFinding()
	id, err := trace.AddFinding(req.GetRoute(), trace.Finding{ID: f.GetId(), Reviewer: f.GetReviewer(), Severity: f.GetSeverity(), Task: f.GetTask(), Head: f.GetHead(), File: f.GetFile(), Line: int(f.GetLine()), Title: f.GetTitle()}, time.Now().UTC())
	if err != nil {
		return nil, traceFailure(ctx, err)
	}
	return &tracev1.AddFindingResponse{Id: id}, nil
}

// SetOutcome records what later happened to a finding.
func (r *traceRPC) SetOutcome(ctx context.Context, req *tracev1.SetOutcomeRequest) (*tracev1.SetOutcomeResponse, error) {
	if err := r.refuseInInspect("SetOutcome"); err != nil {
		return nil, err
	}
	if err := needAbsolute("route folder", req.GetRoute()); err != nil {
		return nil, err
	}
	if err := trace.SetOutcome(req.GetRoute(), req.GetId(), req.GetOutcome(), time.Now().UTC()); err != nil {
		return nil, traceFailure(ctx, err)
	}
	return &tracev1.SetOutcomeResponse{}, nil
}
