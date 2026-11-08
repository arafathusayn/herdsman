package main

import (
	"strings"
	"testing"
)

const safeCleanPlan = `{"plans":[{"task":"a","branch":"feat/a","branch_action":"delete"}]}`

func withTerminal(t *testing.T, value bool) {
	t.Helper()
	old := stdinIsTerminal
	t.Cleanup(func() { stdinIsTerminal = old })
	stdinIsTerminal = func(any) bool { return value }
}

func TestCleanWithYesCarriesEveryFlag(t *testing.T) {
	calls := stubWorktree(t, map[string]string{
		"CleanPlan": safeCleanPlan,
		"Clean":     `{"reports":[{"task":"a","snapshot":"S1","branch_deleted":true,"branch_reason":"ancestry","lost":["big.bin"],"submodules":["vendor/x"]}]}`,
	})
	code, out, errText := runVerb(runClean, "a", "-y", "--keep-branch", "--force-irreversible", "*.bin", "--force-irreversible", "x")
	if code != 0 {
		t.Fatalf("%d %s", code, errText)
	}
	if !strings.Contains(out, "herdsman undo S1") || !strings.Contains(out, "lost for good: big.bin") || !strings.Contains(out, "fetch again after a restore: vendor/x") {
		t.Fatalf("output %s", out)
	}
	body := (*calls)[1].body
	if len((*calls)) != 2 || body["keep_branch"] != true || len(body["named"].([]any)) != 2 || body["tasks"].([]any)[0] != "a" {
		t.Fatalf("calls %+v", *calls)
	}
}

func TestCleanRefusals(t *testing.T) {
	cases := []struct {
		name, plan, want string
		args             []string
		terminal         bool
		code             int
	}{
		{name: "running", plan: `{"plans":[{"task":"a","running":["process 5"]}]}`, args: []string{"a", "-y"}, want: "herdsman teardown", code: 1},
		{name: "unsafe", plan: `{"plans":[{"task":"a","needs_force":true,"reason":"dirty"}]}`, args: []string{"a", "-y"}, want: "--force", code: 1},
		{name: "blocked", plan: `{"plans":[{"task":"a","blocked":"locked"}]}`, args: []string{"a", "-y"}, want: "locked", code: 1},
		{name: "force without a terminal", plan: `{"plans":[{"task":"a","needs_force":true}]}`, args: []string{"a", "-y", "--force"}, want: "terminal", code: 2},
		{name: "no terminal", plan: safeCleanPlan, args: []string{"a"}, want: "-y", code: 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := stubWorktree(t, map[string]string{"CleanPlan": c.plan})
			withTerminal(t, c.terminal)
			code, _, errText := runVerb(runClean, c.args...)
			if code != c.code || !strings.Contains(errText, c.want) || len(*calls) != 1 {
				t.Fatalf("%d %q %+v", code, errText, *calls)
			}
		})
	}
}

func TestCleanUsage(t *testing.T) {
	for _, args := range [][]string{{"a", "--all"}, {"--merged", "--all"}, {"a", "b"}, {"--route", "r", "--merged"}, {"--nope"}} {
		stubWorktree(t, nil)
		if code, _, _ := runVerb(runClean, args...); code != 2 {
			t.Fatalf("%v: %d", args, code)
		}
	}
}

func TestCleanNothingToDoAndServiceErrors(t *testing.T) {
	stubWorktree(t, map[string]string{"CleanPlan": `{"plans":[]}`})
	if code, out, _ := runVerb(runClean, "--merged", "-y"); code != 0 || !strings.Contains(out, "nothing to clean") {
		t.Fatalf("%d %s", code, out)
	}
	stubWorktree(t, nil)
	if code, _, _ := runVerb(runClean, "a"); code != 1 {
		t.Fatalf("%d", code)
	}
	stubWorktree(t, map[string]string{"CleanPlan": `not json`})
	if code, _, _ := runVerb(runClean, "a"); code != 1 {
		t.Fatalf("%d", code)
	}
	stubWorktree(t, map[string]string{"CleanPlan": safeCleanPlan})
	if code, _, _ := runVerb(runClean, "a", "-y"); code != 1 {
		t.Fatalf("%d", code)
	}
	stubWorktree(t, map[string]string{"CleanPlan": safeCleanPlan, "Clean": `{"reports":[{"task":"a","error":"refused"}]}`})
	if code, _, errText := runVerb(runClean, "a", "-y"); code != 1 || !strings.Contains(errText, "refused") {
		t.Fatalf("%d %s", code, errText)
	}
}

func TestCleanAsksOnATerminal(t *testing.T) {
	withTerminal(t, true)
	calls := stubWorktree(t, map[string]string{"CleanPlan": safeCleanPlan, "Clean": `{"reports":[{"task":"a","warnings":["w"],"kept":["volume v"],"unmatched_names":["z"]}]}`})
	var out, errText strings.Builder
	if code := runClean([]string{"a"}, strings.NewReader("n\n"), &out, &errText); code != 0 || len(*calls) != 1 || !strings.Contains(out.String(), "nothing was removed") {
		t.Fatalf("%d %s %+v", code, out.String(), *calls)
	}
	out.Reset()
	if code := runClean([]string{"a"}, strings.NewReader("y\n"), &out, &errText); code != 0 || len(*calls) != 3 {
		t.Fatalf("%d %s", code, errText.String())
	}
	for _, want := range []string{"kept: volume v", "matched nothing: z", "warning: w"} {
		if !strings.Contains(out.String()+errText.String(), want) {
			t.Fatalf("missing %q in %s %s", want, out.String(), errText.String())
		}
	}
}

func TestCleanJSON(t *testing.T) {
	stubWorktree(t, map[string]string{"CleanPlan": safeCleanPlan, "Clean": `{"reports":[{"task":"a"}]}`})
	code, out, _ := runVerb(runClean, "a", "-y", "--json")
	if code != 0 || !strings.Contains(out, `"reports"`) || !strings.Contains(out, `"plans"`) {
		t.Fatalf("%d %s", code, out)
	}
}

func TestDownStopsThenCleansOnOneYes(t *testing.T) {
	calls := stubWorktree(t, map[string]string{
		"CleanPlan": `{"plans":[{"task":"a","running":["process 5"]}]}`, "TeardownPlan": stopPlan,
		"Teardown": `{"reports":[{"task":"a","outcomes":[{"kind":"process","id":"5","result":"stopped"}]}]}`,
		"Clean":    `{"reports":[{"task":"a","snapshot":"S1"}]}`,
	})
	code, out, errText := runVerb(runDown, "a", "-y", "--grace", "2")
	if code != 0 {
		t.Fatalf("%d %s", code, errText)
	}
	var order []string
	for _, call := range *calls {
		order = append(order, call.method)
	}
	if strings.Join(order, ",") != "CleanPlan,TeardownPlan,Teardown,Clean" {
		t.Fatalf("order %v", order)
	}
	if (*calls)[2].body["grace_seconds"].(float64) != 2 || !strings.Contains(out, "stopped") || !strings.Contains(out, "herdsman undo S1") {
		t.Fatalf("%+v %s", *calls, out)
	}
}

func TestDownFailedStopRemovesNothing(t *testing.T) {
	for _, reply := range []string{
		`{"reports":[{"task":"a","error":"changed"}]}`,
		`{"reports":[{"task":"a","outcomes":[{"kind":"process","id":"5","result":"failed: still alive"}]}]}`,
	} {
		calls := stubWorktree(t, map[string]string{"CleanPlan": safeCleanPlan, "TeardownPlan": stopPlan, "Teardown": reply})
		if code, _, _ := runVerb(runDown, "a", "-y"); code != 1 {
			t.Fatalf("%d", code)
		}
		for _, call := range *calls {
			if call.method == "Clean" {
				t.Fatal("clean ran after a failed stop")
			}
		}
	}
}

func TestDownMergedPlansTeardownPerTask(t *testing.T) {
	calls := stubWorktree(t, map[string]string{
		"CleanPlan":    `{"plans":[{"task":"a"},{"task":"b"}]}`,
		"TeardownPlan": `{"plans":[{"task":"x","lines":[]}]}`,
		"Clean":        `{"reports":[{"task":"a"},{"task":"b"}]}`,
	})
	if code, _, errText := runVerb(runDown, "--merged", "-y"); code != 0 {
		t.Fatalf("%d %s", code, errText)
	}
	selectors := []string{}
	for _, call := range *calls {
		if call.method == "TeardownPlan" {
			selectors = append(selectors, call.body["selector"].(string))
		}
	}
	if strings.Join(selectors, ",") != "a,b" {
		t.Fatalf("%v", selectors)
	}
}

func TestDownRefusesWhatCleanWouldRefuse(t *testing.T) {
	calls := stubWorktree(t, map[string]string{"CleanPlan": `{"plans":[{"task":"a","needs_force":true,"reason":"dirty"}]}`, "TeardownPlan": stopPlan})
	if code, _, _ := runVerb(runDown, "a", "-y"); code != 1 {
		t.Fatalf("%d", code)
	}
	for _, call := range *calls {
		if call.method == "Teardown" {
			t.Fatal("stopped a task that cannot be cleaned")
		}
	}
}

func TestDownUsageAndErrors(t *testing.T) {
	stubWorktree(t, nil)
	for _, args := range [][]string{{"a", "--all"}, {"--grace", "-1", "a"}, {"--include", "x", "a"}} {
		if code, _, _ := runVerb(runDown, args...); code != 2 {
			t.Fatalf("%v %d", args, code)
		}
	}
	if code, _, _ := runVerb(runDown, "a", "-y"); code != 1 {
		t.Fatal("plan error ignored")
	}
	stubWorktree(t, map[string]string{"CleanPlan": safeCleanPlan})
	if code, _, _ := runVerb(runDown, "a", "-y"); code != 1 {
		t.Fatal("teardown plan error ignored")
	}
	stubWorktree(t, map[string]string{"CleanPlan": safeCleanPlan, "TeardownPlan": stopPlan})
	if code, _, _ := runVerb(runDown, "a", "-y"); code != 1 {
		t.Fatal("teardown error ignored")
	}
	stubWorktree(t, map[string]string{"CleanPlan": `{"plans":[]}`})
	if code, out, _ := runVerb(runDown, "--merged", "-y"); code != 0 || !strings.Contains(out, "nothing") {
		t.Fatalf("%d %s", code, out)
	}
}

func TestDownAsksOnce(t *testing.T) {
	withTerminal(t, true)
	calls := stubWorktree(t, map[string]string{"CleanPlan": safeCleanPlan, "TeardownPlan": `{"plans":[{"task":"a","lines":[]}]}`})
	var out, errText strings.Builder
	if code := runDown([]string{"a"}, strings.NewReader("n\n"), &out, &errText); code != 0 || strings.Count(out.String(), "[y/N]") != 1 {
		t.Fatalf("%d %s", code, out.String())
	}
	for _, call := range *calls {
		if call.method == "Clean" || call.method == "Teardown" {
			t.Fatal("acted on a no")
		}
	}
}

func TestPruneSkipsWhatItMustNotTouch(t *testing.T) {
	calls := stubWorktree(t, map[string]string{
		"CleanPlan": `{"plans":[{"task":"a"},{"task":"b","needs_force":true},{"task":"c","running":["p"]},{"task":"d","blocked":"locked"}]}`,
		"Clean":     `{"reports":[{"task":"a","snapshot":"S"}]}`,
	})
	code, out, errText := runVerb(runPrune, "--min-age", "2h")
	if code != 0 {
		t.Fatalf("%d %s", code, errText)
	}
	if (*calls)[0].body["min_age_seconds"].(float64) != 7200 || (*calls)[0].body["merged"] != true {
		t.Fatalf("%+v", (*calls)[0])
	}
	tasks := (*calls)[1].body["tasks"].([]any)
	if len(tasks) != 1 || tasks[0] != "a" || strings.Count(out, "skipped") != 3 {
		t.Fatalf("%v %s", tasks, out)
	}
}

func TestPruneDryRunAndUsage(t *testing.T) {
	calls := stubWorktree(t, map[string]string{"CleanPlan": `{"plans":[{"task":"a"}]}`})
	if code, out, _ := runVerb(runPrune, "--dry-run"); code != 0 || len(*calls) != 1 || !strings.Contains(out, "would remove a") {
		t.Fatalf("%d %s", code, out)
	}
	if code, _, _ := runVerb(runPrune, "--min-age", "soon"); code != 2 {
		t.Fatalf("%d", code)
	}
	if code, _, _ := runVerb(runPrune, "extra"); code != 2 {
		t.Fatalf("%d", code)
	}
	stubWorktree(t, nil)
	if code, _, _ := runVerb(runPrune); code != 1 {
		t.Fatalf("%d", code)
	}
	stubWorktree(t, map[string]string{"CleanPlan": `{"plans":[{"task":"a","blocked":"x"}]}`})
	if code, out, _ := runVerb(runPrune); code != 0 || !strings.Contains(out, "nothing to prune") {
		t.Fatalf("%d %s", code, out)
	}
	stubWorktree(t, map[string]string{"CleanPlan": `{"plans":[{"task":"a"}]}`})
	if code, _, _ := runVerb(runPrune); code != 1 {
		t.Fatalf("%d", code)
	}
	stubWorktree(t, map[string]string{"CleanPlan": `{"plans":[{"task":"a"}]}`, "Clean": `{"reports":[{"task":"a","error":"e"}]}`})
	if code, _, _ := runVerb(runPrune); code != 1 {
		t.Fatalf("%d", code)
	}
}
