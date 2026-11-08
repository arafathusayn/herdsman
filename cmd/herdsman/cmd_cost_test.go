package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeUsage(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HERDSMAN_STATE_DIR", dir)
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, "usage.jsonl"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runCostArgs(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := runCost(args, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

const usageFixture = `{"schema":"1.0.0","project":"p1","task":"b","route":"r1","tokens":10,"cost_micros":1500000}
{"schema":"1.0.0","project":"p1","task":"a","route":"r1","tokens":5,"cost_micros":250000}
{"schema":"1.0.0","project":"p2","task":"a","route":"r2","tokens":1,"cost_micros":1}
`

func TestCostSumsByTaskByDefault(t *testing.T) {
	writeUsage(t, usageFixture)
	code, out, _ := runCostArgs()
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 {
		t.Fatalf("lines %q", lines)
	}
	if f := strings.Fields(lines[1]); strings.Join(f, " ") != "a 6 $0.250001" {
		t.Fatalf("row %q", lines[1])
	}
	if f := strings.Fields(lines[2]); strings.Join(f, " ") != "b 10 $1.50" {
		t.Fatalf("row %q", lines[2])
	}
	if f := strings.Fields(lines[3]); strings.Join(f, " ") != "total 16 $1.750001" {
		t.Fatalf("total %q", lines[3])
	}
}

func TestCostGroupsByTheFlagGiven(t *testing.T) {
	writeUsage(t, usageFixture)
	for flag, first := range map[string]string{"--project": "p1", "--route": "r1", "--task": "a"} {
		code, out, _ := runCostArgs(flag)
		lines := strings.Split(out, "\n")
		if code != 0 || !strings.HasPrefix(lines[1], first) {
			t.Fatalf("%s: exit %d out %q", flag, code, out)
		}
	}
}

func TestCostJSON(t *testing.T) {
	writeUsage(t, usageFixture)
	code, out, _ := runCostArgs("--route", "--json")
	var got costReport
	if err := json.Unmarshal([]byte(out), &got); err != nil || code != 0 {
		t.Fatalf("exit %d err %v: %s", code, err, out)
	}
	if got.Group != "route" || len(got.Rows) != 2 || got.Rows[0].Name != "r1" || got.Rows[0].Tokens != 15 || got.Total.CostMicros != 1750001 || got.Schema.Version == "" {
		t.Fatalf("%+v", got)
	}
}

func TestCostNamesRecordsWithoutTheGroupField(t *testing.T) {
	writeUsage(t, `{"tokens":3}`+"\n")
	_, out, _ := runCostArgs()
	if !strings.Contains(out, "(none)") {
		t.Fatalf("out %q", out)
	}
}

func TestCostSaysSoWhenNothingWasMeasured(t *testing.T) {
	writeUsage(t, "")
	code, out, _ := runCostArgs()
	if code != 0 || !strings.Contains(out, "no measured usage") {
		t.Fatalf("exit %d out %q", code, out)
	}
	code, out, _ = runCostArgs("--json")
	var got costReport
	if err := json.Unmarshal([]byte(out), &got); err != nil || code != 0 || len(got.Rows) != 0 || got.Rows == nil {
		t.Fatalf("exit %d err %v out %s", code, err, out)
	}
}

func TestCostCountsAndWarnsAboutBadLines(t *testing.T) {
	writeUsage(t, `{"task":"a","tokens":2}`+"\nnot json\n"+`{"task":"a","tokens":-4}`+"\n"+`{"task":"a","cost_micros":-1}`+"\n\n")
	code, out, errOut := runCostArgs()
	if code != 0 || !strings.Contains(errOut, "skipped 3") {
		t.Fatalf("exit %d stderr %q", code, errOut)
	}
	if !strings.Contains(out, "total") || !strings.Contains(out, " 2 ") {
		t.Fatalf("out %q", out)
	}
}

func TestCostRefusesBadUsage(t *testing.T) {
	writeUsage(t, "")
	for _, args := range [][]string{{"--project", "--route"}, {"--nope"}, {"stray"}} {
		if code, _, errOut := runCostArgs(args...); code != 2 || errOut == "" {
			t.Errorf("%v: exit %d stderr %q", args, code, errOut)
		}
	}
}

func TestCostFailsWhenTheLogCannotBeRead(t *testing.T) {
	dir := writeUsage(t, "")
	if err := os.Mkdir(filepath.Join(dir, "usage.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runCostArgs(); code != 1 || errOut == "" {
		t.Fatalf("exit %d stderr %q", code, errOut)
	}
}

func TestCostFindsTheLogUnderHomeByDefault(t *testing.T) {
	t.Setenv("HERDSMAN_STATE_DIR", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, ".herdsman"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".herdsman", "usage.jsonl"), []byte(`{"task":"z","tokens":1}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, out, _ := runCostArgs(); !strings.Contains(out, "z") {
		t.Fatalf("out %q", out)
	}
}

func TestCostFailsWithoutAHome(t *testing.T) {
	t.Setenv("HERDSMAN_STATE_DIR", "")
	t.Setenv("HOME", "")
	if code, _, _ := runCostArgs(); code != 1 {
		t.Fatalf("exit %d", code)
	}
}

func TestCostIsRegistered(t *testing.T) {
	if _, ok := commands["cost"]; !ok {
		t.Fatal("cost is not registered")
	}
}

func TestCostFailsWhenTheStateDirIsAFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDSMAN_STATE_DIR", file)
	if code, _, errOut := runCostArgs(); code != 1 || errOut == "" {
		t.Fatalf("exit %d stderr %q", code, errOut)
	}
}
