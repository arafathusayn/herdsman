package main

import (
	"encoding/json"
	"io"
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

func TestCleanCarriesThePlansPullRequestHeads(t *testing.T) {
	calls := stubWorktree(t, map[string]string{
		"CleanPlan": `{"plans":[{"task":"a","branch":"feat/a","pull_request_head":"abc"}],"warnings":["gh is down"]}`,
		"Clean":     `{"reports":[{"task":"a"}]}`,
	})
	code, _, errText := runVerb(runClean, "a", "-y")
	if code != 0 || !strings.Contains(errText, "gh is down") {
		t.Fatalf("%d %s", code, errText)
	}
	heads := (*calls)[1].body["pull_request_heads"].(map[string]any)
	if heads["feat/a"] != "abc" {
		t.Fatalf("%+v", heads)
	}
}

func TestCleanShowsNamedPathsBeforeAsking(t *testing.T) {
	withTerminal(t, true)
	stubWorktree(t, map[string]string{"CleanPlan": safeCleanPlan})
	var out, errText strings.Builder
	if code := runClean([]string{"a", "--force-irreversible", "big.bin"}, strings.NewReader("n\n"), &out, &errText); code != 0 {
		t.Fatalf("%d %s", code, errText.String())
	}
	text := out.String()
	named, asked := strings.Index(text, "big.bin"), strings.Index(text, "[y/N]")
	if named < 0 || asked < 0 || named > asked {
		t.Fatalf("%s", text)
	}
}

func decodeOne(t *testing.T, out string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(out))
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("%v in %q", err, out)
	}
	if decoder.More() {
		t.Fatalf("more than one document in %q", out)
	}
	return document
}

func TestJSONOutputIsOneDocumentWhenThereIsNothingToDo(t *testing.T) {
	stubWorktree(t, map[string]string{"CleanPlan": `{"plans":[]}`})
	for _, run := range []func([]string, io.Reader, io.Writer, io.Writer) int{runClean, runDown, runPrune} {
		code, out, _ := runVerb(run, "--json")
		if code != 0 {
			t.Fatalf("%d", code)
		}
		decodeOne(t, out)
	}
}

func TestDownJSONIsOneDocumentWithBothHalves(t *testing.T) {
	stubWorktree(t, map[string]string{
		"CleanPlan": safeCleanPlan, "TeardownPlan": stopPlan,
		"Teardown": `{"reports":[{"task":"a","outcomes":[{"kind":"process","id":"5","result":"stopped"}]}]}`,
		"Clean":    `{"reports":[{"task":"a","snapshot":"S1"}]}`,
	})
	code, out, errText := runVerb(runDown, "a", "-y", "--json")
	if code != 0 {
		t.Fatalf("%d %s", code, errText)
	}
	document := decodeOne(t, out)
	teardown := document["teardown"].(map[string]any)
	if len(teardown["plans"].([]any)) != 1 || len(teardown["reports"].([]any)) != 1 || len(document["reports"].([]any)) != 1 {
		t.Fatalf("%+v", document)
	}
}

func TestDownJSONReportsAFailedStop(t *testing.T) {
	calls := stubWorktree(t, map[string]string{
		"CleanPlan": safeCleanPlan, "TeardownPlan": stopPlan,
		"Teardown": `{"reports":[{"task":"a","error":"changed"}]}`,
	})
	code, out, _ := runVerb(runDown, "a", "-y", "--json")
	if code != 1 || len(decodeOne(t, out)["teardown"].(map[string]any)["reports"].([]any)) != 1 {
		t.Fatalf("%d %s", code, out)
	}
	for _, call := range *calls {
		if call.method == "Clean" {
			t.Fatal("clean ran after a failed stop")
		}
	}
}

func TestPruneJSON(t *testing.T) {
	plan := `{"plans":[{"task":"a"},{"task":"b","blocked":"locked"}]}`
	stubWorktree(t, map[string]string{"CleanPlan": plan})
	code, out, _ := runVerb(runPrune, "--dry-run", "--json")
	document := decodeOne(t, out)
	if code != 0 || document["dry_run"] != true || len(document["skipped"].([]any)) != 1 || len(document["tasks"].([]any)) != 1 {
		t.Fatalf("%d %+v", code, document)
	}
	stubWorktree(t, map[string]string{"CleanPlan": plan, "Clean": `{"reports":[{"task":"a","error":"e"}]}`})
	code, out, _ = runVerb(runPrune, "--json")
	if code != 1 || len(decodeOne(t, out)["reports"].([]any)) != 1 {
		t.Fatalf("%d %s", code, out)
	}
}
