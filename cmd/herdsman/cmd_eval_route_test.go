package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	tracev1 "github.com/arafathusayn/herdsman/sdk/trace/v1"
)

// scriptedRoutes is the daemon's route service with a scripted harness behind
// it: a started route gets a folder, and the agent acts on its brief.
type scriptedRoutes struct {
	daemonv1.RouteService
	agent   func(repo, prompt, command, kind string)
	state   string
	upErr   error
	getErr  error
	stopErr error
	started atomic.Int32
	stopped atomic.Int32
	folders map[string]string
}

func (s *scriptedRoutes) Up(_ context.Context, req *daemonv1.UpRequest) (*daemonv1.UpResponse, error) {
	if s.upErr != nil {
		return nil, s.upErr
	}
	s.started.Add(1)
	if !req.GetNoAdjutant() || req.GetCaller().GetPid() == 0 || req.GetDate() == "" {
		return nil, errors.New("not the request up sends")
	}
	folder := filepath.Join(req.GetRepo(), ".herdsman", req.GetRoute().GetValue()+"-"+req.GetDate())
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return nil, err
	}
	s.folders[req.GetRepo()] = folder
	go s.agent(req.GetRepo(), req.GetPrompt(), req.GetOrchestratorCommand(), req.GetOrchestratorKind())
	return &daemonv1.UpResponse{}, nil
}

func (s *scriptedRoutes) Get(_ context.Context, req *daemonv1.RouteServiceGetRequest) (*daemonv1.RouteServiceGetResponse, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	state := s.state
	if state == "" {
		state = "done"
	}
	return &daemonv1.RouteServiceGetResponse{Start: &daemonv1.RouteStart{State: state, Reason: "scripted", Folder: s.folders[req.GetRepo()]}}, nil
}

func (s *scriptedRoutes) Stop(context.Context, *daemonv1.RouteServiceStopRequest) (*daemonv1.RouteServiceStopResponse, error) {
	s.stopped.Add(1)
	return &daemonv1.RouteServiceStopResponse{}, s.stopErr
}

func withScriptedRoutes(t *testing.T, s *scriptedRoutes) *scriptedRoutes {
	t.Helper()
	s.folders = map[string]string{}
	savedDial, savedPoll := dialEvalRoutes, evalPollEvery
	t.Cleanup(func() { dialEvalRoutes, evalPollEvery = savedDial, savedPoll })
	evalPollEvery = time.Millisecond
	dialEvalRoutes = func(context.Context) (*evalRoutes, func(), error) {
		return &evalRoutes{service: s, socket: "socket", kinds: []string{"codex"}}, func() {}, nil
	}
	return s
}

func routeSuite(t *testing.T, timeout int) string {
	return writeSuite(t, suiteSpec{
		Tasks:          []evalTask{{ID: "make-file", Prompt: "create done.txt", Check: "test -f done.txt"}},
		Arms:           []evalArm{{Name: "works", Kind: "codex", Command: "codex"}, {Name: "skips", Kind: "codex", Command: "codex --skip"}},
		Runs:           1,
		ControlRuns:    2,
		TimeoutSeconds: timeout,
	})
}

func finishingAgent(repo, prompt, command, _ string) {
	if !strings.Contains(prompt, evalDoneFile) {
		return
	}
	if !strings.Contains(command, "--skip") {
		_ = os.WriteFile(filepath.Join(repo, "done.txt"), nil, 0o644)
		_ = os.WriteFile(filepath.Join(repo, evalUsageFile), []byte(`{"cost_micros":9}`), 0o644)
	}
	_ = os.WriteFile(filepath.Join(repo, evalDoneFile), nil, 0o644)
}

func TestEvalRunGivesEveryRunItsOwnRouteAndControls(t *testing.T) {
	routes := withScriptedRoutes(t, &scriptedRoutes{agent: finishingAgent})
	suite := routeSuite(t, 30)
	code, out, errOut := runEvalArgs(evalFlag, "run", suite, "--json")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	var report evalView
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	// Two arms, each one ordinary run and two per control half.
	if routes.started.Load() != 10 || routes.stopped.Load() != 10 {
		t.Fatalf("started %d, stopped %d", routes.started.Load(), routes.stopped.Load())
	}
	if len(report.Controls) != 2 || report.Controls[0].RunsA != 2 || report.Controls[0].RunsB != 2 {
		t.Fatalf("%+v", report.Controls)
	}
	for _, arm := range report.Arms {
		want := int32(0)
		if arm.Arm == "works" {
			want = 1
		}
		if arm.Runs != 1 || arm.Passed != want {
			t.Errorf("%+v", arm)
		}
	}
	checks, err := filepath.Glob(filepath.Join(suite, "routes", "*", "work", ".herdsman", "*", "trace", "spans.jsonl"))
	if err != nil || len(checks) != 10 {
		t.Fatalf("%d span logs: %v", len(checks), err)
	}
	runs, err := os.ReadFile(filepath.Join(suite, "runs.jsonl"))
	if err != nil || !strings.Contains(string(runs), `"cost_micros":9`) {
		t.Fatalf("%s %v", runs, err)
	}
}

func TestEvalRunAnAgentThatNeverFinishesFailsAndIsStopped(t *testing.T) {
	routes := withScriptedRoutes(t, &scriptedRoutes{agent: func(string, string, string, string) {}})
	suite := writeSuite(t, suiteSpec{Tasks: []evalTask{{ID: "t", Check: "true"}}, Arms: []evalArm{{Name: "a", Kind: "codex", Command: "codex"}}, Runs: 1, ControlRuns: 2, TimeoutSeconds: 1})
	saved := evalTimeoutUnit
	t.Cleanup(func() { evalTimeoutUnit = saved })
	evalTimeoutUnit = 10 * time.Millisecond
	code, out, errOut := runEvalArgs(evalFlag, "run", suite, "--json")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	var report evalView
	if err := json.Unmarshal([]byte(out), &report); err != nil || report.Arms[0].Passed != 0 {
		t.Fatalf("%v %s", err, out)
	}
	if routes.stopped.Load() != routes.started.Load() {
		t.Fatalf("started %d, stopped %d", routes.started.Load(), routes.stopped.Load())
	}
}

func TestEvalRunNeedsAKindForARoute(t *testing.T) {
	suite := writeSuite(t, suiteSpec{Tasks: []evalTask{{ID: "t", Check: "true"}}, Arms: []evalArm{{Name: "a", Command: "x"}}, Runs: 1, ControlRuns: 2})
	if code, _, errOut := runEvalArgs(evalFlag, "run", suite); code != 1 || !strings.Contains(errOut, "kind") {
		t.Fatalf("%d %s", code, errOut)
	}
}

func TestEvalRunStopsAtARouteFailure(t *testing.T) {
	broken := errors.New("broken")
	cases := map[string]*scriptedRoutes{
		"up":     {upErr: broken, agent: finishingAgent},
		"get":    {getErr: broken, agent: finishingAgent},
		"stop":   {stopErr: broken, agent: finishingAgent},
		"halted": {state: "failed", agent: finishingAgent},
	}
	for name, routes := range cases {
		t.Run(name, func(t *testing.T) {
			withScriptedRoutes(t, routes)
			code, _, errOut := runEvalArgs(evalFlag, "run", routeSuite(t, 30))
			if code != 1 || errOut == "" {
				t.Fatalf("%d %s", code, errOut)
			}
		})
	}
}

func TestEvalRunStopsARouteThatFailedToStart(t *testing.T) {
	routes := withScriptedRoutes(t, &scriptedRoutes{state: "failed", agent: func(string, string, string, string) {}})
	if code, _, _ := runEvalArgs(evalFlag, "run", routeSuite(t, 30)); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if routes.stopped.Load() != 1 {
		t.Fatalf("stopped %d", routes.stopped.Load())
	}
}

func TestEvalRunReportsRoutesThatCannotBeReached(t *testing.T) {
	withScriptedRoutes(t, &scriptedRoutes{})
	dialEvalRoutes = func(context.Context) (*evalRoutes, func(), error) { return nil, nil, errors.New("no herdr") }
	if code, _, errOut := runEvalArgs(evalFlag, "run", routeSuite(t, 30)); code != 1 || !strings.Contains(errOut, "no herdr") {
		t.Fatalf("%d %s", code, errOut)
	}
}

func TestEvalRunStopsWhenItsRouteFoldersCannotBeMade(t *testing.T) {
	first := filepath.Join("routes", "00001-works-make-file-run")
	for name, block := range map[string]string{
		"route folder": "routes",
		"git":          filepath.Join(first, "work", ".git"),
	} {
		t.Run(name, func(t *testing.T) {
			withScriptedRoutes(t, &scriptedRoutes{agent: finishingAgent})
			suite := routeSuite(t, 30)
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

func TestEvalRunStopsWhenTheCheckSpanOfARouteFails(t *testing.T) {
	withScriptedRoutes(t, &scriptedRoutes{agent: finishingAgent})
	withTrace(t, &checkSpanFails{TraceService: daemon.NewTraceService(false)})
	if code, _, errOut := runEvalArgs(evalFlag, "run", routeSuite(t, 30)); code != 1 || !strings.Contains(errOut, "broken") {
		t.Fatalf("%d %s", code, errOut)
	}
}

func TestRouteCostAddsUpTheSpansWithoutAUsageFile(t *testing.T) {
	route := t.TempDir()
	start := time.Now().Format(time.RFC3339Nano)
	service := daemon.NewTraceService(false)
	runner := &evalRunner{service: service}
	ctx := context.Background()
	if got := runner.routeCost(ctx, route, t.TempDir()); got != nil {
		t.Fatalf("no spans: %v", *got)
	}
	five, seven := int64(5), int64(7)
	spans := []*tracev1.Span{
		{Kind: "turn", Name: "a", Start: start, CostMicros: &five},
		{Kind: "turn", Name: "b", Start: start},
		{Kind: "turn", Name: "c", Start: start, CostMicros: &seven},
	}
	for _, span := range spans {
		if _, err := service.AddSpan(ctx, &tracev1.AddSpanRequest{Route: route, Span: span}); err != nil {
			t.Fatal(err)
		}
	}
	if got := runner.routeCost(ctx, route, t.TempDir()); got == nil || *got != 12 {
		t.Fatalf("%v", got)
	}
}

func TestEvalPauseEndsWithItsContext(t *testing.T) {
	saved := evalPollEvery
	t.Cleanup(func() { evalPollEvery = saved })
	evalPollEvery = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	evalPause(ctx)
}
