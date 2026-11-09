package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func awaitResult(t *testing.T, logs []string) Result {
	t.Helper()
	file := filepath.Join(filepath.Dir(logs[0]), "result.json")
	deadline := time.Now().Add(15 * time.Second)
	for {
		data, err := os.ReadFile(file)
		if err == nil {
			var result Result
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatalf("%v: %q", err, data)
			}
			return result
		}
		if time.Now().After(deadline) {
			t.Fatalf("no result file: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStartEnforcesTheCommandTimeout(t *testing.T) {
	r, dir := runner(t)
	out := filepath.Join(dir, "survived")
	logs, err := r.Start("pre-start", planOf(t, map[string]any{
		"slow": map[string]any{"command": "sleep 2; echo late > " + out, "timeout": "100ms"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	result := awaitResult(t, logs)
	if result.OK || len(result.Commands) != 1 || !result.Commands[0].TimedOut || result.Commands[0].Exit != -1 {
		t.Fatalf("%+v", result)
	}
	if result.File != filepath.Join(filepath.Dir(logs[0]), "result.json") || result.Hook != "pre-start" || result.Task != "t1" {
		t.Fatalf("%+v", result)
	}
	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(out); err == nil {
		t.Fatal("the command outlived its timeout")
	}
}

func TestStartRecordsAFailureAndStopsThePipeline(t *testing.T) {
	r, dir := runner(t)
	out := filepath.Join(dir, "second")
	logs, err := r.Start("pre-start", planOf(t, []any{"exit 3", "touch " + out}))
	if err != nil || len(logs) != 2 {
		t.Fatalf("%v %v", logs, err)
	}
	result := awaitResult(t, logs)
	if result.OK || len(result.Commands) != 1 || result.Commands[0].Exit != 3 {
		t.Fatalf("%+v", result)
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("the pipeline went on after a failure")
	}
}

func TestStartGivesTheCommandItsEnvironmentAndNotTheSupervisors(t *testing.T) {
	r, dir := runner(t)
	out := filepath.Join(dir, "env")
	logs, err := r.Start("pre-start", planOf(t, `echo "$HERDSMAN_TASK/$HERDSMAN_HOOK/${`+superviseEnv+`:-none}" > `+out))
	if err != nil {
		t.Fatal(err)
	}
	if result := awaitResult(t, logs); !result.OK {
		t.Fatalf("%+v", result)
	}
	if got, _ := os.ReadFile(out); string(got) != "t1/pre-start/none\n" {
		t.Fatalf("%q", got)
	}
}

func TestSuperviseRunsTheSavedPlan(t *testing.T) {
	r, dir := runner(t)
	folder, err := r.runFolder("pre-start")
	if err != nil {
		t.Fatal(err)
	}
	spec := filepath.Join(folder, planFile)
	if err := r.savePlan(spec, "pre-start", folder, planOf(t, "true")); err != nil {
		t.Fatal(err)
	}
	if code := supervise(spec); code != 0 {
		t.Fatal(code)
	}
	if _, err := os.Stat(filepath.Join(folder, "result.json")); err != nil {
		t.Fatal(err)
	}
	if code := supervise(filepath.Join(dir, "missing")); code == 0 {
		t.Fatal("a missing plan was accepted")
	}
	bad := filepath.Join(dir, "bad")
	os.WriteFile(bad, []byte("{"), 0o600)
	if code := supervise(bad); code == 0 {
		t.Fatal("a corrupt plan was accepted")
	}
}

func TestSuperviseReportsAFailedHook(t *testing.T) {
	r, _ := runner(t)
	folder, err := r.runFolder("pre-start")
	if err != nil {
		t.Fatal(err)
	}
	spec := filepath.Join(folder, planFile)
	if err := r.savePlan(spec, "pre-start", folder, planOf(t, "exit 4")); err != nil {
		t.Fatal(err)
	}
	if code := supervise(spec); code != 1 {
		t.Fatal(code)
	}
}
