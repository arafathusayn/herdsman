package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTraceFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runEvalArgs(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := runEval(args, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

const evalFlag = "--experimental-eval"

func evalSuite(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	lines := []string{
		`{"task":"t","arm":"opus","pass":true,"duration_ms":10,"cost_micros":5}`,
		`{"task":"t","arm":"opus","pass":false,"duration_ms":12,"cost_micros":6}`,
		`{"task":"t","arm":"opus","control":"a","pass":true,"duration_ms":10,"cost_micros":5}`,
		`{"task":"t","arm":"opus","control":"a","pass":true,"duration_ms":11,"cost_micros":6}`,
		`{"task":"t","arm":"opus","control":"b","pass":true,"duration_ms":10,"cost_micros":5}`,
		`{"task":"t","arm":"opus","control":"b","pass":true,"duration_ms":11,"cost_micros":6}`,
	}
	writeTraceFile(t, filepath.Join(dir, "runs.jsonl"), strings.Join(lines, "\n")+"\n")
	return dir
}

func TestEvalIsBehindItsFlagAndSaysSo(t *testing.T) {
	suite := evalSuite(t)
	if code, _, errOut := runEvalArgs(suite); code != 2 || !strings.Contains(errOut, evalFlag) {
		t.Fatalf("%d %q", code, errOut)
	}
	code, _, errOut := runEvalArgs(evalFlag, suite)
	if code != 0 || strings.Count(errOut, "experimental") != 1 || !strings.Contains(errOut, "eval/0.1.0") {
		t.Fatalf("the marker prints once: %d %q", code, errOut)
	}
}

func TestEvalTextShowsIntervalsAndControl(t *testing.T) {
	code, out, _ := runEvalArgs(evalFlag, evalSuite(t))
	if code != 0 || !strings.Contains(out, "opus") || !strings.Contains(out, "A/A") || !strings.Contains(out, "..") {
		t.Fatalf("%d %q", code, out)
	}
}

func TestEvalJSONCarriesItsVersion(t *testing.T) {
	code, out, _ := runEvalArgs(evalSuite(t), evalFlag, "--json")
	if code != 0 {
		t.Fatal(code)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["experimental"] != "eval/0.1.0" || parsed["arms"] == nil || parsed["controls"] == nil {
		t.Fatalf("%v", parsed)
	}
}

func TestEvalRefusesToRank(t *testing.T) {
	route := t.TempDir()
	code, _, errOut := runEvalArgs(evalFlag, evalSuite(t), "--findings", route, "--rank")
	if code != 3 || !strings.Contains(errOut, "no labeled set") {
		t.Fatalf("%d %q", code, errOut)
	}
	code, out, _ := runEvalArgs(evalFlag, evalSuite(t), "--rank", "--json")
	if code != 3 || !strings.Contains(out, `"refused": true`) {
		t.Fatalf("%d %q", code, out)
	}
}

func TestEvalFailsWithoutAControl(t *testing.T) {
	dir := t.TempDir()
	writeTraceFile(t, filepath.Join(dir, "runs.jsonl"), `{"task":"t","arm":"a","pass":true,"duration_ms":1}`+"\n")
	if code, _, errOut := runEvalArgs(evalFlag, dir); code != 1 || !strings.Contains(errOut, "A/A") {
		t.Fatalf("%d %q", code, errOut)
	}
	if code, _, _ := runEvalArgs(evalFlag, t.TempDir()); code != 1 {
		t.Fatalf("no runs exits %d", code)
	}
}

func TestEvalFindingsFlow(t *testing.T) {
	route := t.TempDir()
	code, id, errOut := runEvalArgs(evalFlag, "finding", "add", route, "--reviewer", "sol", "--severity", "P1", "--title", "bug", "--task", "t", "--head", "abc", "--file", "a.go", "--line", "3")
	if code != 0 {
		t.Fatalf("%d %q", code, errOut)
	}
	id = strings.TrimSpace(id)
	if code, _, errOut := runEvalArgs(evalFlag, "finding", "outcome", route, id, "fixed"); code != 0 {
		t.Fatalf("%d %q", code, errOut)
	}
	code, out, _ := runEvalArgs(evalFlag, evalSuite(t), "--findings", route)
	if code != 0 || !strings.Contains(out, "sol") || !strings.Contains(out, "fixed 1") {
		t.Fatalf("%d %q", code, out)
	}
	garbled := t.TempDir()
	writeTraceFile(t, filepath.Join(garbled, "trace", "findings.jsonl"), "garbage\n")
	if code, _, errOut := runEvalArgs(evalFlag, evalSuite(t), "--findings", garbled); code != 0 || !strings.Contains(errOut, "skipped 1") {
		t.Fatalf("%d %q", code, errOut)
	}
}

func TestEvalUsageAndFailures(t *testing.T) {
	route := t.TempDir()
	usage := [][]string{
		{evalFlag},
		{evalFlag, "--bogus"},
		{evalFlag, "a", "b"},
		{evalFlag, "finding"},
		{evalFlag, "finding", "other"},
		{evalFlag, "finding", "add"},
		{evalFlag, "finding", "add", route, "--bogus"},
		{evalFlag, "finding", "add", route, "extra"},
		{evalFlag, "finding", "outcome", route},
	}
	for _, args := range usage {
		if code, _, _ := runEvalArgs(args...); code != 2 {
			t.Errorf("%v exits %d", args, code)
		}
	}
	if code, _, _ := runEvalArgs(evalFlag, "finding", "add", route, "--reviewer", "r"); code != 1 {
		t.Error("an incomplete finding must exit 1")
	}
	if code, _, _ := runEvalArgs(evalFlag, "finding", "outcome", route, "nope", "fixed"); code != 1 {
		t.Error("an unknown finding must exit 1")
	}
	broken := t.TempDir()
	if err := os.MkdirAll(filepath.Join(broken, "trace", "findings.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runEvalArgs(evalFlag, evalSuite(t), "--findings", broken); code != 1 {
		t.Error("unreadable findings must exit 1")
	}
}

func TestEvalSaysWhatItCouldNotUse(t *testing.T) {
	suite := evalSuite(t)
	file := filepath.Join(suite, "runs.jsonl")
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	writeTraceFile(t, file, string(body)+"garbage\n"+`{"task":"t","arm":"solo","pass":true,"duration_ms":1}`+"\n")
	code, out, errOut := runEvalArgs(evalFlag, suite)
	if code != 0 || !strings.Contains(errOut, "skipped 1") || !strings.Contains(out, "too few runs") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
}
