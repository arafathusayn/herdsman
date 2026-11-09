package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type failingStream struct{}

func (failingStream) Read([]byte) (int, error)  { return 0, errors.New("broken") }
func (failingStream) Write([]byte) (int, error) { return 0, errors.New("broken") }

func onATerminal(t *testing.T) {
	t.Helper()
	old := stdinIsTerminal
	t.Cleanup(func() { stdinIsTerminal = old })
	stdinIsTerminal = func(any) bool { return true }
}

func TestAHookOnlyPlanAsksAboutTheHooks(t *testing.T) {
	onATerminal(t)
	calls := stubWorktree(t, map[string]string{"TeardownPlan": hookOnlyPlan})
	var stdout, stderr bytes.Buffer
	if code := runTeardown([]string{"a"}, strings.NewReader("n\n"), &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout.String(), "run the teardown hooks?") || len(*calls) != 1 {
		t.Fatalf("out = %q, calls = %+v", stdout.String(), *calls)
	}
}

func TestAHookOnlyPlanWithoutApprovalStaysOneJSONDocument(t *testing.T) {
	stubWorktree(t, map[string]string{"TeardownPlan": hookOnlyPlan})
	code, out, _ := runVerb(runTeardown, "a", "--json")
	if code != 0 || !strings.HasPrefix(out, "{") || strings.Contains(out, "nothing to stop") {
		t.Fatalf("code = %d, out = %q", code, out)
	}
}

func TestATeardownQuestionThatCannotBeReadFails(t *testing.T) {
	onATerminal(t)
	stubWorktree(t, map[string]string{"TeardownPlan": stopPlan})
	var stdout, stderr bytes.Buffer
	if code := runTeardown([]string{"a"}, failingStream{}, &stdout, &stderr); code != 1 {
		t.Fatalf("code = %d", code)
	}
}

func TestAPlanItCannotDecodeIsSkipped(t *testing.T) {
	stubWorktree(t, map[string]string{"TeardownPlan": `{"plans":["not a plan"]}`})
	code, out, _ := runVerb(runTeardown, "a", "-y")
	if code != 0 || !strings.Contains(out, "nothing to stop") {
		t.Fatalf("code = %d, out = %q", code, out)
	}
}

func TestTeardownJSONThatCannotBeWrittenFails(t *testing.T) {
	var stderr bytes.Buffer
	if code := writeTeardownJSON(failingStream{}, nil, nil, &stderr); code != 1 {
		t.Fatalf("code = %d", code)
	}
}

func TestTeardownJSONReportsAFailedTask(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := writeTeardownJSON(&stdout, nil, []teardownReport{{Task: "a", Error: "x"}}, &stderr); code != 1 {
		t.Fatalf("code = %d", code)
	}
}
