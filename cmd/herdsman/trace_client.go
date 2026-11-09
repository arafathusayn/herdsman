package main

import (
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/sdk/client"
	"github.com/arafathusayn/herdsman/sdk/rpc"
	tracev1 "github.com/arafathusayn/herdsman/sdk/trace/v1"
)

// dialTrace reaches the daemon through the SDK; tests replace it.
var dialTrace = func() (tracev1.TraceService, func(), error) {
	sdk, err := client.Dial(client.Options{})
	if err != nil {
		return nil, nil, err
	}
	return sdk.Trace, sdk.Close, nil
}

// rpcText is a failure as a person should read it: the service's own words,
// without the wire code in front.
func rpcText(err error) error {
	var coded *rpc.Error
	if errors.As(err, &coded) && coded.Message != "" {
		return errors.New(coded.Message)
	}
	text := err.Error()
	for _, code := range rpc.Codes() {
		if rest, ok := strings.CutPrefix(text, string(code)+": "); ok {
			return errors.New(rest)
		}
	}
	return err
}

// absolutePaths resolves folders against this process, since the daemon's
// working folder is its own.
func absolutePaths(paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		out = append(out, abs)
	}
	return out, nil
}

// The JSON a person or a script reads from trace and eval. These shapes are the
// published format, so they stay as they are whatever the wire does.
type (
	traceSchema struct {
		Version string `json:"version"`
	}
	traceSpanView struct {
		TraceID          string    `json:"trace_id,omitempty"`
		SpanID           string    `json:"span_id,omitempty"`
		ParentID         string    `json:"parent_id,omitempty"`
		Kind             string    `json:"kind"`
		Name             string    `json:"name"`
		Task             string    `json:"task,omitempty"`
		Start            time.Time `json:"start"`
		DurationMS       float64   `json:"duration_ms"`
		Exit             *int32    `json:"exit,omitempty"`
		Tokens           *int64    `json:"tokens,omitempty"`
		CostMicros       *int64    `json:"cost_micros,omitempty"`
		Args             string    `json:"args,omitempty"`
		StartApproximate bool      `json:"start_approximate,omitempty"`
	}
	traceView struct {
		Schema  traceSchema     `json:"schema"`
		Route   string          `json:"route"`
		TraceID string          `json:"trace_id"`
		Spans   []traceSpanView `json:"spans"`
		Skipped int32           `json:"skipped"`
	}
	intervalView struct {
		Low  float64 `json:"low"`
		High float64 `json:"high"`
	}
	armView struct {
		Arm        string        `json:"arm"`
		Runs       int32         `json:"runs"`
		Passed     int32         `json:"passed"`
		PassRate   *intervalView `json:"pass_rate"`
		DurationMS *intervalView `json:"duration_ms"`
		CostRuns   int32         `json:"cost_runs"`
		CostMicros *intervalView `json:"cost_micros"`
	}
	controlView struct {
		Arm                  string        `json:"arm"`
		RunsA                int32         `json:"runs_a"`
		RunsB                int32         `json:"runs_b"`
		PassRateDifference   *intervalView `json:"pass_rate_difference"`
		DurationMSDifference *intervalView `json:"duration_ms_difference"`
		CostMicrosDifference *intervalView `json:"cost_micros_difference"`
	}
	reviewerView struct {
		Reviewer  string `json:"reviewer"`
		Recorded  int32  `json:"recorded"`
		Open      int32  `json:"open"`
		Accepted  int32  `json:"accepted"`
		Fixed     int32  `json:"fixed"`
		Dismissed int32  `json:"dismissed"`
	}
	findingsView struct {
		Recorded  int32          `json:"recorded"`
		Labeled   int32          `json:"labeled"`
		Open      int32          `json:"open"`
		Reviewers []reviewerView `json:"reviewers"`
	}
	rankView struct {
		Refused    bool   `json:"refused"`
		LabeledSet bool   `json:"labeled_set"`
		Reason     string `json:"reason"`
	}
	evalView struct {
		Schema       traceSchema   `json:"schema"`
		Experimental string        `json:"experimental"`
		Suite        string        `json:"suite"`
		Arms         []armView     `json:"arms"`
		Controls     []controlView `json:"controls"`
		Findings     *findingsView `json:"findings,omitempty"`
		Rank         *rankView     `json:"rank,omitempty"`
		Skipped      int32         `json:"skipped"`
	}
)

func traceFromProto(in *tracev1.RouteTrace) (traceView, error) {
	out := traceView{Schema: traceSchema{Version: in.GetVersion()}, Route: in.GetRoute(), TraceID: in.GetTraceId(), Spans: []traceSpanView{}, Skipped: in.GetSkipped()}
	for _, span := range in.GetSpans() {
		start, err := time.Parse(time.RFC3339Nano, span.GetStart())
		if err != nil {
			return traceView{}, err
		}
		out.Spans = append(out.Spans, traceSpanView{
			TraceID: span.GetTraceId(), SpanID: span.GetSpanId(), ParentID: span.GetParentId(), Kind: span.GetKind(), Name: span.GetName(), Task: span.GetTask(),
			Start: start, DurationMS: span.GetDurationMs(), Exit: span.Exit, Tokens: span.Tokens, CostMicros: span.CostMicros, Args: span.GetArgs(), StartApproximate: span.GetStartApproximate(),
		})
	}
	return out, nil
}

func intervalFromProto(in *tracev1.Interval) *intervalView {
	if in == nil {
		return nil
	}
	return &intervalView{Low: in.GetLow(), High: in.GetHigh()}
}

func evalFromProto(in *tracev1.EvalReport, suite string) evalView {
	out := evalView{Schema: traceSchema{Version: in.GetVersion()}, Experimental: in.GetExperimental(), Suite: suite, Arms: []armView{}, Controls: []controlView{}, Skipped: in.GetSkipped()}
	for _, arm := range in.GetArms() {
		out.Arms = append(out.Arms, armView{Arm: arm.GetArm(), Runs: arm.GetRuns(), Passed: arm.GetPassed(), PassRate: intervalFromProto(arm.GetPassRate()), DurationMS: intervalFromProto(arm.GetDurationMs()), CostRuns: arm.GetCostRuns(), CostMicros: intervalFromProto(arm.GetCostMicros())})
	}
	for _, control := range in.GetControls() {
		out.Controls = append(out.Controls, controlView{Arm: control.GetArm(), RunsA: control.GetRunsA(), RunsB: control.GetRunsB(), PassRateDifference: intervalFromProto(control.GetPassRateDifference()), DurationMSDifference: intervalFromProto(control.GetDurationMsDifference()), CostMicrosDifference: intervalFromProto(control.GetCostMicrosDifference())})
	}
	if found := in.GetFindings(); found != nil {
		view := &findingsView{Recorded: found.GetRecorded(), Labeled: found.GetLabeled(), Open: found.GetOpen(), Reviewers: []reviewerView{}}
		for _, row := range found.GetReviewers() {
			view.Reviewers = append(view.Reviewers, reviewerView{Reviewer: row.GetReviewer(), Recorded: row.GetRecorded(), Open: row.GetOpen(), Accepted: row.GetAccepted(), Fixed: row.GetFixed(), Dismissed: row.GetDismissed()})
		}
		out.Findings = view
	}
	if rank := in.GetRank(); rank != nil {
		out.Rank = &rankView{Refused: rank.GetRefused(), LabeledSet: rank.GetLabeledSet(), Reason: rank.GetReason()}
	}
	return out
}
