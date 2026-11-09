package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

const stopPlan = `{"plans":[{"task":"a","lines":[{"kind":"process","id":"5","level":"exclusive","class":"compensatable","action":"stop"}]}]}`

type verbCall struct {
	method string
	body   map[string]any
}

// stubWorktree answers each method with the given JSON.
func stubWorktree(t *testing.T, replies map[string]string) *[]verbCall {
	t.Helper()
	calls := &[]verbCall{}
	old := worktreeCall
	t.Cleanup(func() { worktreeCall = old })
	oldDial := dialLedger
	t.Cleanup(func() { dialLedger = oldDial })
	dialLedger = func() (daemonv1.LedgerService, func(), error) { return ledgerStub{}, func() {}, nil }
	worktreeCall = func(_ context.Context, method string, request []byte) ([]byte, error) {
		var body map[string]any
		_ = json.Unmarshal(request, &body)
		*calls = append(*calls, verbCall{method, body})
		reply, ok := replies[method]
		if !ok {
			return nil, errors.New("no reply for " + method)
		}
		return []byte(reply), nil
	}
	return calls
}

func runVerb(run func([]string, io.Reader, io.Writer, io.Writer) int, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestTeardownStopsWithYes(t *testing.T) {
	calls := stubWorktree(t, map[string]string{
		"TeardownPlan": stopPlan,
		"Teardown":     `{"reports":[{"task":"a","outcomes":[{"kind":"process","id":"5","result":"stopped"}]}]}`,
	})
	code, out, _ := runVerb(runTeardown, "a", "-y", "--grace", "3")
	if code != 0 || !strings.Contains(out, "exclusive") || !strings.Contains(out, "stopped") {
		t.Fatalf("code = %d, out = %q", code, out)
	}
	if len(*calls) != 2 || (*calls)[1].body["grace_seconds"] != float64(3) {
		t.Fatalf("calls = %+v", *calls)
	}
}

func TestTeardownRefusesWithoutATerminalOrYes(t *testing.T) {
	calls := stubWorktree(t, map[string]string{"TeardownPlan": stopPlan})
	code, _, stderr := runVerb(runTeardown, "a")
	if code != 2 || !strings.Contains(stderr, "-y") || len(*calls) != 1 {
		t.Fatalf("code = %d, stderr = %q, calls = %+v", code, stderr, *calls)
	}
}

func TestTeardownAsksOnATerminal(t *testing.T) {
	old := stdinIsTerminal
	t.Cleanup(func() { stdinIsTerminal = old })
	stdinIsTerminal = func(any) bool { return true }
	stubWorktree(t, map[string]string{
		"TeardownPlan": stopPlan,
		"Teardown":     `{"reports":[{"task":"a"}]}`,
	})
	var stdout, stderr bytes.Buffer
	if code := runTeardown([]string{"a"}, strings.NewReader("y\n"), &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d", code)
	}
	stdout.Reset()
	if code := runTeardown([]string{"a"}, strings.NewReader("n\n"), &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "nothing was stopped") {
		t.Fatalf("code = %d, out = %q", code, stdout.String())
	}
}

func TestTeardownNothingToStop(t *testing.T) {
	stubWorktree(t, map[string]string{"TeardownPlan": `{"plans":[{"task":"a","lines":[{"kind":"port","id":"1/2","action":"report"}]}]}`})
	code, out, _ := runVerb(runTeardown, "a")
	if code != 0 || !strings.Contains(out, "nothing to stop") {
		t.Fatalf("code = %d, out = %q", code, out)
	}
	code, out, _ = runVerb(runTeardown, "a", "--json")
	if code != 0 || !strings.Contains(out, `"plans"`) {
		t.Fatalf("code = %d, out = %q", code, out)
	}
}

func TestTeardownReportsAStoppedPlan(t *testing.T) {
	stubWorktree(t, map[string]string{
		"TeardownPlan": stopPlan,
		"Teardown":     `{"reports":[{"task":"a","error":"ledger: plan stopped: process 5"}]}`,
	})
	code, _, stderr := runVerb(runTeardown, "a", "-y")
	if code != 1 || !strings.Contains(stderr, "plan stopped") {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	code, out, _ := runVerb(runTeardown, "a", "-y", "--json")
	if code != 1 || !strings.Contains(out, "plan stopped") {
		t.Fatalf("code = %d, out = %q", code, out)
	}
}

func TestTeardownJSONDoesNotPrintATable(t *testing.T) {
	stubWorktree(t, map[string]string{
		"TeardownPlan": stopPlan,
		"Teardown":     `{"reports":[{"task":"a"}]}`,
	})
	code, out, _ := runVerb(runTeardown, "a", "-y", "--json")
	if code != 0 || strings.Contains(out, "KIND") {
		t.Fatalf("code = %d, out = %q", code, out)
	}
}

func TestTeardownBadUsage(t *testing.T) {
	stubWorktree(t, nil)
	for _, args := range [][]string{
		{"a", "b"}, {"a", "--all"}, {"--route", "r", "--all"}, {"--grace", "-1"},
		{"--include", "everything"}, {"--nope"},
	} {
		if code, _, _ := runVerb(runTeardown, args...); code != 2 {
			t.Errorf("%v: code = %d", args, code)
		}
	}
}

func TestTeardownBackendFailures(t *testing.T) {
	stubWorktree(t, nil)
	if code, _, _ := runVerb(runTeardown, "a"); code != 1 {
		t.Fatalf("plan failure: code = %d", code)
	}
	stubWorktree(t, map[string]string{"TeardownPlan": stopPlan})
	if code, _, _ := runVerb(runTeardown, "a", "-y"); code != 1 {
		t.Fatalf("teardown failure: code = %d", code)
	}
	stubWorktree(t, map[string]string{"TeardownPlan": `not json`})
	if code, _, _ := runVerb(runTeardown, "a"); code != 1 {
		t.Fatalf("bad plan json: code = %d", code)
	}
	stubWorktree(t, map[string]string{"TeardownPlan": stopPlan, "Teardown": `not json`})
	if code, _, _ := runVerb(runTeardown, "a", "-y"); code != 1 {
		t.Fatalf("bad report json: code = %d", code)
	}
}

func TestResourceAdd(t *testing.T) {
	t.Setenv("HERDSMAN_EXEC", "env-exec")
	calls := stubWorktree(t, map[string]string{"ResourceAdd": `{}`})
	code, out, _ := runVerb(runResource, "add", "--kind", "volume", "--id", "v1", "--task", "a")
	if code != 0 || !strings.Contains(out, "recorded volume v1") {
		t.Fatalf("code = %d, out = %q", code, out)
	}
	body := (*calls)[0].body
	if body["execution"] != "env-exec" || body["task"] != "a" || body["kind"] != "volume" {
		t.Fatalf("body = %+v", body)
	}
}

func TestResourceAddBadUsageAndFailure(t *testing.T) {
	stubWorktree(t, nil)
	for _, args := range [][]string{{}, {"remove"}, {"add"}, {"add", "--kind", "x"}, {"add", "--nope"}} {
		if code, _, _ := runVerb(runResource, args...); code != 2 {
			t.Errorf("%v: code = %d", args, code)
		}
	}
	if code, _, _ := runVerb(runResource, "add", "--kind", "x", "--id", "y"); code != 1 {
		t.Fatalf("backend failure: code = %d", code)
	}
}

const hookOnlyPlan = `{"plans":[{"task":"a","lines":[]}],"hooks":true}`

func TestAnEmptyPlanWithHooksRunsThemOnYes(t *testing.T) {
	calls := stubWorktree(t, map[string]string{"TeardownPlan": hookOnlyPlan, "Teardown": `{"reports":[{"task":"a","outcomes":[]}]}`})
	code, out, _ := runVerb(runTeardown, "a", "-y")
	if code != 0 || len(*calls) != 2 || (*calls)[1].method != "Teardown" || !strings.Contains(out, "hooks ran") {
		t.Fatalf("code = %d, out = %q, calls = %+v", code, out, *calls)
	}
}

func TestAnEmptyPlanWithHooksStaysQuietWithoutApproval(t *testing.T) {
	calls := stubWorktree(t, map[string]string{"TeardownPlan": hookOnlyPlan})
	code, out, stderr := runVerb(runTeardown, "a")
	if code != 0 || len(*calls) != 1 || !strings.Contains(out, "nothing to stop") || !strings.Contains(stderr, "-y") {
		t.Fatalf("code = %d, out = %q, stderr = %q, calls = %+v", code, out, stderr, *calls)
	}
}

func TestAnEmptyPlanWithoutHooksCallsNothing(t *testing.T) {
	calls := stubWorktree(t, map[string]string{"TeardownPlan": `{"plans":[{"task":"a","lines":[]}]}`})
	code, out, _ := runVerb(runTeardown, "a", "-y")
	if code != 0 || len(*calls) != 1 || !strings.Contains(out, "nothing to stop") {
		t.Fatalf("code = %d, out = %q, calls = %+v", code, out, *calls)
	}
}
