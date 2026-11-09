package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon"
	suggestv1 "github.com/arafathusayn/herdsman/sdk/suggest/v1"
	tracev1 "github.com/arafathusayn/herdsman/sdk/trace/v1"
)

var (
	defaultDialTrace   = dialTrace
	defaultDialSuggest = dialSuggest
)

// The variables above are read before any init function runs, so they hold the
// real dialers while the tests' init replaces the ones the verbs use.
func TestDefaultDialersBuildAClientWithoutConnecting(t *testing.T) {
	t.Setenv("HERDSMAN_STATE_DIR", t.TempDir())
	service, done, err := defaultDialTrace()
	if err != nil || service == nil {
		t.Fatal(err)
	}
	done()
	catalog, done, err := defaultDialSuggest()
	if err != nil || catalog == nil {
		t.Fatal(err)
	}
	done()
}

func TestDefaultDialersReportNoHomeFolder(t *testing.T) {
	t.Setenv("HERDSMAN_STATE_DIR", "")
	t.Setenv("HERDSMAN_PRIVATE_HOME", "")
	t.Setenv("HOME", "")
	if _, _, err := defaultDialTrace(); err == nil {
		t.Fatal("no home folder must be an error")
	}
	if _, _, err := defaultDialSuggest(); err == nil {
		t.Fatal("no home folder must be an error")
	}
}

func withBrokenDial(t *testing.T) {
	t.Helper()
	savedTrace, savedSuggest := dialTrace, dialSuggest
	t.Cleanup(func() { dialTrace, dialSuggest = savedTrace, savedSuggest })
	dialTrace = func() (tracev1.TraceService, func(), error) { return nil, nil, errors.New("down") }
	dialSuggest = func() (suggestv1.SuggestService, func(), error) { return nil, nil, errors.New("down") }
}

// inDeletedFolder leaves the process with no working folder, so a relative
// path cannot be resolved.
func inDeletedFolder(t *testing.T) {
	t.Helper()
	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(gone, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(gone)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
}

func traceAndEvalCalls(route, suite string) map[string]func() int {
	code := func(run func(...string) (int, string, string), args ...string) func() int {
		return func() int {
			got, _, _ := run(args...)
			return got
		}
	}
	return map[string]func() int{
		"trace":       code(runTraceArgs, route),
		"trace add":   code(runTraceArgs, "add", route, "--kind", "check", "--name", "c"),
		"eval":        code(runEvalArgs, evalFlag, suite),
		"outcome":     code(runEvalArgs, evalFlag, "finding", "outcome", route, "id", "fixed"),
		"finding add": code(runEvalArgs, evalFlag, "finding", "add", route, "--reviewer", "r", "--severity", "P1", "--title", "t"),
		"eval run":    code(runEvalArgs, evalFlag, "run", suite),
	}
}

func TestVerbsReportADaemonThatCannotBeReached(t *testing.T) {
	suite := writeSuite(t, suiteSpec{Tasks: []evalTask{{ID: "t", Check: "true"}}, Arms: []evalArm{{Name: "a", Command: "true"}}, Runs: 1, ControlRuns: 2})
	withBrokenDial(t)
	for name, call := range traceAndEvalCalls(t.TempDir(), suite) {
		if got := call(); got != 1 {
			t.Errorf("%s exits %d", name, got)
		}
	}
	if _, errOut, code := runSuggest(t, "", "source", "list"); code != 1 {
		t.Errorf("source list exits %d: %s", code, errOut)
	}
}

func TestVerbsReportARelativePathWithNoWorkingFolder(t *testing.T) {
	saved := absolutePath
	t.Cleanup(func() { absolutePath = saved })
	absolutePath = func(string) (string, error) { return "", errors.New("no working folder") }
	for name, call := range traceAndEvalCalls("route", "suite") {
		if got := call(); got != 1 {
			t.Errorf("%s exits %d", name, got)
		}
	}
	inDeletedFolder(t)
	if _, _, code := runSuggest(t, "", "source", "list"); code != 1 {
		t.Errorf("source list exits %d", code)
	}
}

func TestTraceExportReportsAFailure(t *testing.T) {
	withBrokenExport := failingExport{TraceService: daemon.NewTraceService(false)}
	withTrace(t, withBrokenExport)
	if code, _, errOut := runTraceArgs(t.TempDir(), "--otlp", "http://127.0.0.1:1/v1/traces"); code != 1 || !strings.Contains(errOut, "collector is down") {
		t.Fatalf("%d %s", code, errOut)
	}
}

type failingExport struct{ tracev1.TraceService }

func (failingExport) Export(context.Context, *tracev1.ExportRequest) (*tracev1.ExportResponse, error) {
	return nil, errors.New("collector is down")
}

func TestTraceFromProtoRefusesABadStart(t *testing.T) {
	if _, err := traceFromProto(&tracev1.RouteTrace{Spans: []*tracev1.Span{{Start: "soon"}}}); err == nil {
		t.Fatal("a bad start must be an error")
	}
}

func TestRPCTextDropsTheCodeFromAPlainError(t *testing.T) {
	if got := rpcText(errors.New("not_found: gone")).Error(); got != "gone" {
		t.Fatalf("%q", got)
	}
	if got := rpcText(errors.New("plain")).Error(); got != "plain" {
		t.Fatalf("%q", got)
	}
}

func TestSDKSuggestCallReportsWhatItCannotCarry(t *testing.T) {
	ctx := context.Background()
	if _, err := sdkSuggestCall(ctx, "Nope", []byte(`{}`)); err == nil || !strings.Contains(err.Error(), "no method") {
		t.Fatalf("%v", err)
	}
	if _, err := sdkSuggestCall(ctx, "Search", []byte(`{`)); err == nil {
		t.Fatal("a bad request must be an error")
	}
	suggestHome(t)
	if _, err := sdkSuggestCall(ctx, "Get", []byte(`{"id":"absent"}`)); err == nil || strings.HasPrefix(err.Error(), "not_found:") {
		t.Fatalf("a refusal must read as the service's words: %v", err)
	}
	saved := dialSuggest
	t.Cleanup(func() { dialSuggest = saved })
	dialSuggest = func() (suggestv1.SuggestService, func(), error) { return invalidUTF8Service{}, func() {}, nil }
	if _, err := sdkSuggestCall(ctx, "Sources", []byte(`{}`)); err == nil {
		t.Fatal("a response that cannot be encoded must be an error")
	}
}

// invalidUTF8Service answers with text JSON cannot carry.
type invalidUTF8Service struct{ suggestv1.SuggestService }

func (invalidUTF8Service) Sources(context.Context, *suggestv1.SourcesRequest) (*suggestv1.SourcesResponse, error) {
	return &suggestv1.SourcesResponse{Sources: []*suggestv1.Pin{{Url: "\xff"}}}, nil
}

// failingTrace is the real service with chosen methods failing.
type failingTrace struct {
	tracev1.TraceService
	span, record, evaluate error
}

func (f failingTrace) AddSpan(ctx context.Context, req *tracev1.AddSpanRequest) (*tracev1.AddSpanResponse, error) {
	if f.span != nil {
		return nil, f.span
	}
	return f.TraceService.AddSpan(ctx, req)
}

func (f failingTrace) RecordRun(ctx context.Context, req *tracev1.RecordRunRequest) (*tracev1.RecordRunResponse, error) {
	if f.record != nil {
		return nil, f.record
	}
	return f.TraceService.RecordRun(ctx, req)
}

func (f failingTrace) Evaluate(ctx context.Context, req *tracev1.EvaluateRequest) (*tracev1.EvaluateResponse, error) {
	if f.evaluate != nil {
		return nil, f.evaluate
	}
	return f.TraceService.Evaluate(ctx, req)
}

func withTrace(t *testing.T, service tracev1.TraceService) {
	t.Helper()
	saved := dialTrace
	t.Cleanup(func() { dialTrace = saved })
	dialTrace = func() (tracev1.TraceService, func(), error) { return service, func() {}, nil }
}

func oneTaskSuite(t *testing.T) string {
	return writeSuite(t, suiteSpec{Tasks: []evalTask{{ID: "t", Check: "true"}}, Arms: []evalArm{{Name: "a", Command: "true"}}, Runs: 1, ControlRuns: 2})
}

func TestEvalRunStopsWhenItCannotRecord(t *testing.T) {
	real := daemon.NewTraceService(false)
	broken := errors.New("broken")
	for name, service := range map[string]failingTrace{
		"span":     {TraceService: real, span: broken},
		"record":   {TraceService: real, record: broken},
		"evaluate": {TraceService: real, evaluate: broken},
	} {
		t.Run(name, func(t *testing.T) {
			withTrace(t, service)
			if code, _, errOut := runEvalArgs(evalFlag, "run", oneTaskSuite(t)); code != 1 || !strings.Contains(errOut, "broken") {
				t.Fatalf("%d %s", code, errOut)
			}
		})
	}
}

func TestEvalRunStopsWhenTheCheckSpanCannotBeRecorded(t *testing.T) {
	withTrace(t, &checkSpanFails{TraceService: daemon.NewTraceService(false)})
	if code, _, errOut := runEvalArgs(evalFlag, "run", oneTaskSuite(t)); code != 1 || !strings.Contains(errOut, "broken") {
		t.Fatalf("%d %s", code, errOut)
	}
}

type checkSpanFails struct{ tracev1.TraceService }

func (c *checkSpanFails) AddSpan(ctx context.Context, req *tracev1.AddSpanRequest) (*tracev1.AddSpanResponse, error) {
	if req.GetSpan().GetKind() == "check" {
		return nil, errors.New("broken")
	}
	return c.TraceService.AddSpan(ctx, req)
}

func TestEvalRunStopsWhenItsFoldersCannotBeMade(t *testing.T) {
	first := filepath.Join("routes", "00001-a-t-run")
	for name, block := range map[string]string{
		"route folder": "routes",
		"marker":       filepath.Join(first, "00-route.md", "x"),
		"log":          filepath.Join(first, "harness.log", "x"),
	} {
		t.Run(name, func(t *testing.T) {
			suite := oneTaskSuite(t)
			path := filepath.Join(suite, block)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if code, _, errOut := runEvalArgs(evalFlag, "run", suite); code != 1 || errOut == "" {
				t.Fatalf("%d %s", code, errOut)
			}
		})
	}
}

type badStartTrace struct{ tracev1.TraceService }

func (badStartTrace) Get(context.Context, *tracev1.GetRequest) (*tracev1.GetResponse, error) {
	return &tracev1.GetResponse{Trace: &tracev1.RouteTrace{Spans: []*tracev1.Span{{Start: "soon"}}}}, nil
}

func TestTraceReportsASpanItCannotRead(t *testing.T) {
	withTrace(t, badStartTrace{})
	if code, _, errOut := runTraceArgs(t.TempDir()); code != 1 || !strings.Contains(errOut, "trace:") {
		t.Fatalf("%d %s", code, errOut)
	}
}
