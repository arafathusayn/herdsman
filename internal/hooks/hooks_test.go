package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/wtree"
)

func TestTypesAreTheTwelvePoints(t *testing.T) {
	if len(Types) != 12 {
		t.Fatalf("%d types", len(Types))
	}
	for _, stage := range []string{"switch", "start", "commit", "merge", "teardown", "clean"} {
		for _, side := range []string{"pre-", "post-"} {
			if !slices.Contains(Types, side+stage) {
				t.Errorf("missing %s%s", side, stage)
			}
		}
	}
}

func TestParseStringForm(t *testing.T) {
	config, err := Parse(map[string]any{"pre-start": "make deps"})
	if err != nil {
		t.Fatal(err)
	}
	steps := config["pre-start"]
	if len(steps) != 1 || len(steps[0]) != 1 || steps[0][0].Template != "make deps" || steps[0][0].Name != "pre-start" {
		t.Fatalf("%+v", steps)
	}
}

func TestParseMapFormRunsTogetherSortedByName(t *testing.T) {
	config, err := Parse(map[string]any{"post-start": map[string]any{"web": "a", "db": "b"}})
	if err != nil {
		t.Fatal(err)
	}
	steps := config["post-start"]
	if len(steps) != 1 || len(steps[0]) != 2 || steps[0][0].Name != "db" || steps[0][1].Name != "web" {
		t.Fatalf("%+v", steps)
	}
}

func TestParsePipelineFormKeepsOrder(t *testing.T) {
	config, err := Parse(map[string]any{"pre-merge": []any{"first", map[string]any{"x": "second", "y": "third"}, "fourth"}})
	if err != nil {
		t.Fatal(err)
	}
	steps := config["pre-merge"]
	if len(steps) != 3 || steps[0][0].Template != "first" || len(steps[1]) != 2 || steps[2][0].Template != "fourth" {
		t.Fatalf("%+v", steps)
	}
}

func TestParseTimeoutOnACommand(t *testing.T) {
	config, err := Parse(map[string]any{"pre-clean": map[string]any{"slow": map[string]any{"command": "x", "timeout": "90s"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := config["pre-clean"][0][0]; got.Template != "x" || got.Timeout != 90*time.Second {
		t.Fatalf("%+v", got)
	}
}

func TestParseAcceptsUnderscoreKeys(t *testing.T) {
	config, err := Parse(map[string]any{"pre_merge": "x"})
	if err != nil || len(config["pre-merge"]) != 1 {
		t.Fatalf("%+v %v", config, err)
	}
}

func TestParseRefusesBadInput(t *testing.T) {
	cases := map[string]map[string]any{
		"unknown hook":   {"pre-launch": "x"},
		"number":         {"pre-start": 3},
		"empty":          {"pre-start": ""},
		"empty pipeline": {"pre-start": []any{}},
		"bad step":       {"pre-start": []any{3}},
		"nested list":    {"pre-start": []any{[]any{"x"}}},
		"empty map":      {"pre-start": map[string]any{}},
		"bad value":      {"pre-start": map[string]any{"a": 3}},
		"no command":     {"pre-start": map[string]any{"a": map[string]any{"timeout": "1s"}}},
		"bad command":    {"pre-start": map[string]any{"a": map[string]any{"command": 3}}},
		"bad timeout":    {"pre-start": map[string]any{"a": map[string]any{"command": "x", "timeout": "soon"}}},
		"timeout type":   {"pre-start": map[string]any{"a": map[string]any{"command": "x", "timeout": 3}}},
		"negative":       {"pre-start": map[string]any{"a": map[string]any{"command": "x", "timeout": "-1s"}}},
		"unknown field":  {"pre-start": map[string]any{"a": map[string]any{"command": "x", "retry": true}}},
	}
	for name, input := range cases {
		if _, err := Parse(input); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func vars(dir string) wtree.Vars {
	return wtree.Vars{Values: map[string]string{
		"task": "t1", "branch": "feat/t1", "worktree_path": dir, "route": "r1",
	}}
}

func TestPlanRendersEveryCommandWithItsHookName(t *testing.T) {
	config, _ := Parse(map[string]any{"pre-start": map[string]any{"a": "echo {{ task }} {{ hook_type }} {{ hook_name }}"}})
	plan, err := config.Plan("pre-start", vars("/w"))
	if err != nil {
		t.Fatal(err)
	}
	if got := plan[0][0].Rendered; got != "echo t1 pre-start a" {
		t.Fatal(got)
	}
}

func TestPlanReportsABadTemplate(t *testing.T) {
	config, _ := Parse(map[string]any{"pre-start": "echo {{ nothing }}"})
	if _, err := config.Plan("pre-start", vars("/w")); err == nil {
		t.Fatal("accepted")
	}
}

func TestPlanOfAnUnsetHookIsEmpty(t *testing.T) {
	plan, err := Config{}.Plan("pre-start", vars("/w"))
	if err != nil || len(plan) != 0 {
		t.Fatalf("%+v %v", plan, err)
	}
}

func TestApprovalIsPerExactTemplateAndProject(t *testing.T) {
	store := &Approvals{Path: filepath.Join(t.TempDir(), "sub", "approvals.json")}
	if ok, err := store.Approved("p", "echo a"); err != nil || ok {
		t.Fatalf("%v %v", ok, err)
	}
	if err := store.Approve("p", []string{"echo a", "echo a"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		project, template string
		want              bool
	}{{"p", "echo a", true}, {"p", "echo a ", false}, {"q", "echo a", false}} {
		if ok, _ := store.Approved(c.project, c.template); ok != c.want {
			t.Errorf("%+v: %v", c, ok)
		}
	}
	data, _ := os.ReadFile(store.Path)
	if strings.Count(string(data), "echo a") != 1 {
		t.Fatalf("duplicate stored: %s", data)
	}
	info, _ := os.Stat(store.Path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("%v", info.Mode())
	}
}

func TestApprovalsRefuseACorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.json")
	os.WriteFile(path, []byte("{"), 0o600)
	store := &Approvals{Path: path}
	if _, err := store.Approved("p", "x"); err == nil {
		t.Fatal("read a corrupt file")
	}
	if err := store.Approve("p", []string{"x"}); err == nil {
		t.Fatal("overwrote a corrupt file")
	}
}

func TestUnapprovedListsOnlyMissingTemplates(t *testing.T) {
	store := &Approvals{Path: filepath.Join(t.TempDir(), "a.json")}
	store.Approve("p", []string{"one"})
	config, _ := Parse(map[string]any{"pre-start": []any{"one", "two"}})
	plan, _ := config.Plan("pre-start", vars("/w"))
	missing, err := store.Unapproved("p", plan)
	if err != nil || !slices.Equal(missing, []string{"two"}) {
		t.Fatalf("%v %v", missing, err)
	}
}

func runner(t *testing.T) (*Runner, string) {
	t.Helper()
	dir := t.TempDir()
	return &Runner{LogDir: filepath.Join(dir, "logs"), Dir: dir, Env: Env{Task: "t1", Branch: "feat/t1", Worktree: dir, Route: "r1"}}, dir
}

func planOf(t *testing.T, input any) Plan {
	t.Helper()
	config, err := Parse(map[string]any{"pre-start": input})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := config.Plan("pre-start", vars("/w"))
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestRunGivesTheHookItsEnvironment(t *testing.T) {
	r, dir := runner(t)
	out := filepath.Join(dir, "env")
	plan := planOf(t, `printf '%s|%s|%s|%s|%s' "$HERDSMAN_TASK" "$HERDSMAN_BRANCH" "$HERDSMAN_WORKTREE_PATH" "$HERDSMAN_ROUTE" "$HERDSMAN_HOOK" > `+out)
	result, err := r.Run(context.Background(), "pre-start", plan)
	if err != nil {
		t.Fatalf("%v %+v", err, result)
	}
	data, _ := os.ReadFile(out)
	if want := "t1|feat/t1|" + dir + "|r1|pre-start"; string(data) != want {
		t.Fatalf("%s, want %s", data, want)
	}
}

func TestRunWritesResultAndLog(t *testing.T) {
	r, _ := runner(t)
	result, err := r.Run(context.Background(), "pre-start", planOf(t, "echo hello"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Commands) != 1 || result.Commands[0].Exit != 0 || !result.OK {
		t.Fatalf("%+v", result)
	}
	data, err := os.ReadFile(result.Commands[0].Log)
	if err != nil || strings.TrimSpace(string(data)) != "hello" {
		t.Fatalf("%q %v", data, err)
	}
	raw, err := os.ReadFile(result.File)
	if err != nil {
		t.Fatal(err)
	}
	var back Result
	if err := json.Unmarshal(raw, &back); err != nil || back.Hook != "pre-start" || back.Task != "t1" {
		t.Fatalf("%+v %v", back, err)
	}
}

func TestRunStopsThePipelineAtTheFirstFailure(t *testing.T) {
	r, dir := runner(t)
	marker := filepath.Join(dir, "never")
	result, err := r.Run(context.Background(), "pre-start", planOf(t, []any{"exit 7", "touch " + marker}))
	var failed *FailedError
	if !errors.As(err, &failed) || failed.Exit != 7 {
		t.Fatalf("%v", err)
	}
	if result.OK || len(result.Commands) != 1 || result.Commands[0].Exit != 7 {
		t.Fatalf("%+v", result)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("ran past the failure")
	}
}

func TestRunMapFormIsConcurrent(t *testing.T) {
	r, dir := runner(t)
	// Each command waits for the other's file, so running them one after the
	// other would end in the timeout.
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	wait := func(mine, other string) string {
		return "touch " + mine + "; i=0; while [ ! -e " + other + " ] && [ $i -lt 100 ]; do sleep 0.05; i=$((i+1)); done; [ -e " + other + " ]"
	}
	result, err := r.Run(context.Background(), "pre-start", planOf(t, map[string]any{"a": wait(a, b), "b": wait(b, a)}))
	if err != nil {
		t.Fatalf("%v %+v", err, result)
	}
}

func TestRunTimeoutKillsTheProcessGroup(t *testing.T) {
	r, dir := runner(t)
	pidFile := filepath.Join(dir, "pid")
	config, _ := Parse(map[string]any{"pre-start": map[string]any{"slow": map[string]any{
		"command": "sleep 30 & echo $! > " + pidFile + "; wait", "timeout": "300ms",
	}}})
	plan, _ := config.Plan("pre-start", vars("/w"))
	start := time.Now()
	result, err := r.Run(context.Background(), "pre-start", plan)
	if err == nil || !result.Commands[0].TimedOut {
		t.Fatalf("%v %+v", err, result)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("took %v", elapsed)
	}
	data, _ := os.ReadFile(pidFile)
	pid := strings.TrimSpace(string(data))
	if pid == "" {
		t.Fatal("no pid")
	}
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("child %s survived", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunRefusesACancelledContext(t *testing.T) {
	r, _ := runner(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Run(ctx, "pre-start", planOf(t, "true")); err == nil {
		t.Fatal("ran")
	}
}

func TestStartLeavesTheCommandRunningAndLogs(t *testing.T) {
	r, dir := runner(t)
	out := filepath.Join(dir, "bg")
	logs, err := r.Start("pre-start", planOf(t, "echo started; echo done > "+out))
	if err != nil || len(logs) != 1 {
		t.Fatalf("%v %v", logs, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, _ := os.ReadFile(logs[0])
		if _, err := os.Stat(out); err == nil && strings.Contains(string(data), "started") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("background command never finished: %q", data)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLogNamesCannotEscapeTheLogFolder(t *testing.T) {
	r, _ := runner(t)
	r.Env.Task = "../../evil"
	config, _ := Parse(map[string]any{"pre-start": map[string]any{"../x": "true"}})
	plan, _ := config.Plan("pre-start", wtree.Vars{Values: map[string]string{}})
	result, err := r.Run(context.Background(), "pre-start", plan)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(r.LogDir, result.Commands[0].Log)
	if err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("%s", result.Commands[0].Log)
	}
}

func processAlive(pid string) bool {
	n, err := strconv.Atoi(pid)
	if err != nil {
		return false
	}
	return syscall.Kill(n, 0) == nil
}
