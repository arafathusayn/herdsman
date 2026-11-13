package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/trace"
	"github.com/arafathusayn/herdsman/sdk/rpc"
	tracev1 "github.com/arafathusayn/herdsman/sdk/trace/v1"
)

func traceTransport(t *testing.T, inspect bool) tracev1.TraceService {
	t.Helper()
	server := httptest.NewServer((&Daemon{cfg: Config{Inspect: inspect}}).rpcHandler())
	t.Cleanup(server.Close)
	return tracev1.NewTraceServiceClient(server.Client(), server.URL)
}

func checkSpan(name string) *tracev1.Span {
	exit := int32(0)
	return &tracev1.Span{Kind: trace.KindCheck, Name: name, Start: "2026-01-01T00:00:00Z", DurationMs: 3, Exit: &exit}
}

func TestTraceServiceCarriesARouteOverTheTransport(t *testing.T) {
	client, ctx, route := traceTransport(t, false), context.Background(), t.TempDir()
	if _, err := client.AddSpan(ctx, &tracev1.AddSpanRequest{Route: route, Span: checkSpan("make check")}); err != nil {
		t.Fatal(err)
	}
	got, err := client.Get(ctx, &tracev1.GetRequest{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	spans := got.GetTrace().GetSpans()
	if len(spans) != 2 || spans[1].GetName() != "make check" || spans[1].Exit == nil || spans[1].Tokens != nil {
		t.Fatalf("spans = %v", spans)
	}
}

func TestTraceServiceExportsToACollector(t *testing.T) {
	var received atomic.Int32
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		received.Add(1)
	}))
	defer collector.Close()
	client, ctx, route := traceTransport(t, false), context.Background(), t.TempDir()
	if _, err := client.AddSpan(ctx, &tracev1.AddSpanRequest{Route: route, Span: checkSpan("c")}); err != nil {
		t.Fatal(err)
	}
	res, err := client.Export(ctx, &tracev1.ExportRequest{Route: route, Endpoint: collector.URL})
	if err != nil || res.GetSpans() != 2 || received.Load() != 1 {
		t.Fatalf("%v %v received %d", res, err, received.Load())
	}
}

func TestTraceServiceRefusesARelativePath(t *testing.T) {
	client, ctx := traceTransport(t, false), context.Background()
	calls := map[string]func() error{
		"Get": func() error { _, err := client.Get(ctx, &tracev1.GetRequest{Route: "r"}); return err },
		"AddSpan": func() error {
			_, err := client.AddSpan(ctx, &tracev1.AddSpanRequest{Route: "r", Span: checkSpan("c")})
			return err
		},
		"Evaluate":   func() error { _, err := client.Evaluate(ctx, &tracev1.EvaluateRequest{Suite: "s"}); return err },
		"RecordRun":  func() error { _, err := client.RecordRun(ctx, &tracev1.RecordRunRequest{Suite: "s"}); return err },
		"AddFinding": func() error { _, err := client.AddFinding(ctx, &tracev1.AddFindingRequest{Route: "r"}); return err },
		"SetOutcome": func() error { _, err := client.SetOutcome(ctx, &tracev1.SetOutcomeRequest{Route: "r"}); return err },
	}
	for name, call := range calls {
		if err := call(); err == nil || !strings.HasPrefix(err.Error(), string(rpc.InvalidArgument)+": ") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestTraceServiceInInspectWritesNothing(t *testing.T) {
	client, ctx, route := traceTransport(t, true), context.Background(), t.TempDir()
	if err := trace.Append(route, trace.Span{Kind: trace.KindCheck, Name: "c", Start: time.Unix(1, 0), DurationMS: 1}); err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(route, "trace", "route-id")
	first, err := client.Get(ctx, &tracev1.GetRequest{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(identity); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a read in inspect mode wrote the identity: %v", err)
	}
	second, err := client.Get(ctx, &tracev1.GetRequest{Route: route})
	if err != nil || first.GetTrace().GetTraceId() != second.GetTrace().GetTraceId() || first.GetTrace().GetTraceId() == "" {
		t.Fatalf("%v, %v and %v", err, first.GetTrace().GetTraceId(), second.GetTrace().GetTraceId())
	}
	if _, err := client.Evaluate(ctx, &tracev1.EvaluateRequest{Suite: filepath.Join(route, "absent")}); err == nil || !strings.HasPrefix(err.Error(), string(rpc.NotFound)+": ") {
		t.Fatalf("Evaluate is a read and must be served: %v", err)
	}
	before, _ := os.ReadFile(filepath.Join(route, "trace", "spans.jsonl"))
	calls := map[string]func() error{
		"Export": func() error { _, err := client.Export(ctx, &tracev1.ExportRequest{Route: route}); return err },
		"AddSpan": func() error {
			_, err := client.AddSpan(ctx, &tracev1.AddSpanRequest{Route: route, Span: checkSpan("x")})
			return err
		},
		"RecordRun":  func() error { _, err := client.RecordRun(ctx, &tracev1.RecordRunRequest{Suite: route}); return err },
		"AddFinding": func() error { _, err := client.AddFinding(ctx, &tracev1.AddFindingRequest{Route: route}); return err },
		"SetOutcome": func() error { _, err := client.SetOutcome(ctx, &tracev1.SetOutcomeRequest{Route: route}); return err },
	}
	for name, call := range calls {
		if err := call(); err == nil || !strings.HasPrefix(err.Error(), string(rpc.FailedPrecondition)+": ") {
			t.Errorf("%s: %v", name, err)
		}
	}
	after, _ := os.ReadFile(filepath.Join(route, "trace", "spans.jsonl"))
	if string(before) != string(after) {
		t.Fatal("inspect mode changed the span log")
	}
}

func recordRun(t *testing.T, client tracev1.TraceService, suite, control string, duration float64) {
	t.Helper()
	run := &tracev1.Run{Task: "t", Arm: "a", Control: control, Pass: true, DurationMs: duration}
	if _, err := client.RecordRun(context.Background(), &tracev1.RecordRunRequest{Suite: suite, Run: run}); err != nil {
		t.Fatal(err)
	}
}

func TestTraceServiceEvaluatesRecordedRuns(t *testing.T) {
	client, ctx, suite, route := traceTransport(t, false), context.Background(), t.TempDir(), t.TempDir()
	if _, err := client.Evaluate(ctx, &tracev1.EvaluateRequest{Suite: suite}); err == nil || !strings.HasPrefix(err.Error(), string(rpc.NotFound)+": ") {
		t.Fatalf("a suite without runs: %v", err)
	}
	recordRun(t, client, suite, "", 1)
	if _, err := client.Evaluate(ctx, &tracev1.EvaluateRequest{Suite: suite}); err == nil || !strings.HasPrefix(err.Error(), string(rpc.FailedPrecondition)+": ") {
		t.Fatalf("a suite without a control: %v", err)
	}
	for _, control := range []string{"", "a", "a", "b", "b"} {
		recordRun(t, client, suite, control, 2)
	}
	added, err := client.AddFinding(ctx, &tracev1.AddFindingRequest{Route: route, Finding: &tracev1.Finding{Reviewer: "r", Severity: "P1", Title: "t", Line: 3}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SetOutcome(ctx, &tracev1.SetOutcomeRequest{Route: route, Id: added.GetId(), Outcome: trace.OutcomeFixed}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(route, "trace", "findings.jsonl"), appendTo(t, filepath.Join(route, "trace", "findings.jsonl"), "garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suite, trace.RunsFile), appendTo(t, filepath.Join(suite, trace.RunsFile), "garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := client.Evaluate(ctx, &tracev1.EvaluateRequest{Suite: suite, FindingRoutes: []string{route}, Rank: true})
	if err != nil {
		t.Fatal(err)
	}
	report := res.GetReport()
	if len(report.GetArms()) != 1 || len(report.GetControls()) != 1 || report.GetFindings().GetLabeled() != 1 || !report.GetRank().GetRefused() || len(res.GetWarnings()) != 2 {
		t.Fatalf("%v", res)
	}
	if text, _ := json.Marshal(report.GetFindings().GetReviewers()); !strings.Contains(string(text), `"fixed":1`) {
		t.Fatalf("reviewers = %s", text)
	}
}

func TestTraceServiceFindingFailures(t *testing.T) {
	client, ctx, route := traceTransport(t, false), context.Background(), t.TempDir()
	if _, err := client.AddFinding(ctx, &tracev1.AddFindingRequest{Route: route, Finding: &tracev1.Finding{}}); err == nil || !strings.HasPrefix(err.Error(), string(rpc.InvalidArgument)+": ") {
		t.Fatal(err)
	}
	if _, err := client.SetOutcome(ctx, &tracev1.SetOutcomeRequest{Route: route, Id: "x", Outcome: "fixed"}); err == nil || !strings.HasPrefix(err.Error(), string(rpc.InvalidArgument)+": ") {
		t.Fatal(err)
	}
	if _, err := client.AddSpan(ctx, &tracev1.AddSpanRequest{Route: route, Span: &tracev1.Span{Start: "soon"}}); err == nil || !strings.HasPrefix(err.Error(), string(rpc.InvalidArgument)+": ") {
		t.Fatal(err)
	}
	if _, err := client.Get(ctx, &tracev1.GetRequest{Route: filepath.Join(route, "gone")}); err == nil || !strings.HasPrefix(err.Error(), string(rpc.NotFound)+": ") {
		t.Fatal(err)
	}
	broken := t.TempDir()
	if err := os.MkdirAll(filepath.Join(broken, trace.RunsFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Evaluate(ctx, &tracev1.EvaluateRequest{Suite: broken}); err == nil || !strings.HasPrefix(err.Error(), string(rpc.Internal)+": ") {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := traceFailure(cancelled, errors.New("x")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func appendTo(t *testing.T, path, text string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return append(data, text...)
}

func TestRecordRunWithNoRunIsRefusedInBothEncodings(t *testing.T) {
	suite := t.TempDir()
	if _, err := traceTransport(t, false).RecordRun(context.Background(), &tracev1.RecordRunRequest{Suite: suite}); err == nil || !strings.HasPrefix(err.Error(), string(rpc.InvalidArgument)+": ") {
		t.Fatalf("binary: %v", err)
	}
	server := httptest.NewServer((&Daemon{}).rpcHandler())
	defer server.Close()
	body := strings.NewReader(`{"suite":` + strconv.Quote(suite) + `}`)
	res, err := server.Client().Post(server.URL+"/rpc/herdsman.trace.v1.TraceService/RecordRun", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	text, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(text), string(rpc.InvalidArgument)) {
		t.Fatalf("json: %d %s", res.StatusCode, text)
	}
	if _, err := os.Stat(filepath.Join(suite, trace.RunsFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused run touched the file: %v", err)
	}
}

func TestRecordRunRefusesAnInfiniteDuration(t *testing.T) {
	suite := t.TempDir()
	run := &tracev1.Run{Task: "t", Arm: "a", Pass: true, DurationMs: math.Inf(1)}
	if _, err := traceTransport(t, false).RecordRun(context.Background(), &tracev1.RecordRunRequest{Suite: suite, Run: run}); err == nil || !strings.HasPrefix(err.Error(), string(rpc.InvalidArgument)+": ") {
		t.Fatalf("%v", err)
	}
}
