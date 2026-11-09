package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeSuite(t *testing.T, suite suiteSpec) string {
	t.Helper()
	folder := t.TempDir()
	data, err := json.Marshal(suite)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, evalSuiteFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return folder
}

// A scripted harness: it does the task's work, reports usage, and the check
// looks at what it left. The second arm never does the work.
func scriptedSuite(t *testing.T) string {
	return writeSuite(t, suiteSpec{
		Tasks: []evalTask{{ID: "make-file", Prompt: "create done.txt", Check: "test -f done.txt"}},
		Arms: []evalArm{
			{Name: "works", Command: `printf '%s' "$HERDSMAN_EVAL_PROMPT" > done.txt; echo '{"tokens":7,"cost_micros":11}' > "$HERDSMAN_EVAL_USAGE"`},
			{Name: "idles", Command: "true"},
		},
		Runs: 2, ControlRuns: 2,
	})
}

func TestEvalRunExecutesTheSuiteThroughRoutesWithControls(t *testing.T) {
	suite := scriptedSuite(t)
	code, out, errOut := runEvalArgs(evalFlag, "run", "--local", suite, "--json")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	var report evalView
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if len(report.Arms) != 2 || len(report.Controls) != 2 {
		t.Fatalf("%+v", report)
	}
	for _, arm := range report.Arms {
		want := int32(0)
		if arm.Arm == "works" {
			want = 2
		}
		if arm.Runs != 2 || arm.Passed != want {
			t.Errorf("%+v", arm)
		}
	}
	for _, control := range report.Controls {
		if control.RunsA != 2 || control.RunsB != 2 {
			t.Errorf("%+v", control)
		}
	}
	routes, err := filepath.Glob(filepath.Join(suite, "routes", "*-works-make-file-a"))
	if err != nil || len(routes) != 2 {
		t.Fatalf("%v %v", routes, err)
	}
	code, out, errOut = runTraceArgs(routes[0], "--json")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	for _, want := range []string{`"kind": "turn"`, `"kind": "check"`, `"tokens": 7`, `"cost_micros": 11`} {
		if !strings.Contains(out, want) {
			t.Errorf("no %s in %s", want, out)
		}
	}
}

func TestEvalRunRefusesToPoolWithEarlierRuns(t *testing.T) {
	suite := scriptedSuite(t)
	if err := os.WriteFile(filepath.Join(suite, "runs.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runEvalArgs(evalFlag, "run", "--local", suite); code != 1 || !strings.Contains(errOut, "already holds runs") {
		t.Fatalf("%d %s", code, errOut)
	}
}

func TestEvalRunRefusesABadSuite(t *testing.T) {
	task := []evalTask{{ID: "t", Check: "true"}}
	arm := []evalArm{{Name: "a", Command: "true"}}
	cases := map[string]suiteSpec{
		"no task":     {Arms: arm, Runs: 1, ControlRuns: 2},
		"no arm":      {Tasks: task, Runs: 1, ControlRuns: 2},
		"no runs":     {Tasks: task, Arms: arm, ControlRuns: 2},
		"one control": {Tasks: task, Arms: arm, Runs: 1, ControlRuns: 1},
		"bad task":    {Tasks: []evalTask{{ID: "../x", Check: "true"}}, Arms: arm, Runs: 1, ControlRuns: 2},
		"no check":    {Tasks: []evalTask{{ID: "t"}}, Arms: arm, Runs: 1, ControlRuns: 2},
		"bad arm":     {Tasks: task, Arms: []evalArm{{Name: "a/b", Command: "true"}}, Runs: 1, ControlRuns: 2},
		"no command":  {Tasks: task, Arms: []evalArm{{Name: "a"}}, Runs: 1, ControlRuns: 2},
	}
	for name, suite := range cases {
		if code, _, errOut := runEvalArgs(evalFlag, "run", "--local", writeSuite(t, suite)); code != 1 || !strings.Contains(errOut, "eval:") {
			t.Errorf("%s: %d %s", name, code, errOut)
		}
	}
	unknown := t.TempDir()
	if err := os.WriteFile(filepath.Join(unknown, evalSuiteFile), []byte(`{"bogus":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runEvalArgs(evalFlag, "run", "--local", unknown); code != 1 {
		t.Error("an unknown key must be refused")
	}
	if code, _, _ := runEvalArgs(evalFlag, "run", "--local", t.TempDir()); code != 1 {
		t.Error("a folder with no suite must be refused")
	}
	if code, _, _ := runEvalArgs(evalFlag, "run"); code != 2 {
		t.Error("no folder is a usage error")
	}
	if code, _, _ := runEvalArgs(evalFlag, "run", "--bogus"); code != 2 {
		t.Error("an unknown flag is a usage error")
	}
}

func TestEvalRunATimeoutFailsTheRunAndStopsItsChildren(t *testing.T) {
	suite := writeSuite(t, suiteSpec{
		Tasks:          []evalTask{{ID: "t", Check: "true"}},
		Arms:           []evalArm{{Name: "slow", Command: "sleep 30 & wait"}},
		Runs:           1,
		ControlRuns:    2,
		TimeoutSeconds: 1,
	})
	began := time.Now()
	code, out, errOut := runEvalArgs(evalFlag, "run", "--local", suite, "--json")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	if elapsed := time.Since(began); elapsed > 15*time.Second {
		t.Fatalf("the timeout did not stop the command: %v", elapsed)
	}
	var report evalView
	if err := json.Unmarshal([]byte(out), &report); err != nil || report.Arms[0].Passed != 0 {
		t.Fatalf("%v %s", err, out)
	}
}

func TestEvalRunIgnoresUnreadableUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if usage := readEvalUsage(path); usage.Tokens != nil || usage.CostMicros != nil {
		t.Fatalf("%+v", usage)
	}
}
