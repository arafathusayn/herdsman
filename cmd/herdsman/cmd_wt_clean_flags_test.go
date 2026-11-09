package main

import (
	"strings"
	"testing"
)

func TestArchiveOnlyNeedsNoForceAndNoTerminal(t *testing.T) {
	withCleanTerminal(t, false)
	calls := stubWorktree(t, map[string]string{
		"CleanPlan": `{"plans":[{"task":"a","needs_force":true,"reason":"dirty"}]}`,
		"Clean":     `{"reports":[{"task":"a","snapshot":"S1","archive_only":true}]}`,
	})
	code, out, errText := runVerb(runClean, "a", "--archive-only", "-y")
	if code != 0 || !strings.Contains(out, "archived") || !strings.Contains(out, "S1") {
		t.Fatalf("%d %q %q", code, out, errText)
	}
	if (*calls)[1].body["archive_only"] != true {
		t.Fatalf("calls %+v", *calls)
	}
}

func TestForceRunsUnattendedOnlyWithArchiveOnly(t *testing.T) {
	withCleanTerminal(t, false)
	stubWorktree(t, map[string]string{"CleanPlan": safeCleanPlan, "Clean": `{"reports":[{"task":"a","snapshot":"S1","archive_only":true}]}`})
	if code, _, _ := runVerb(runClean, "a", "--force", "--archive-only", "-y"); code != 0 {
		t.Fatalf("with archive-only: %d", code)
	}
	if code, _, errText := runVerb(runClean, "a", "--force", "-y"); code != 2 || !strings.Contains(errText, "terminal") {
		t.Fatalf("alone: %d %q", code, errText)
	}
}

func TestDeleteBranchReachesPlanAndClean(t *testing.T) {
	calls := stubWorktree(t, map[string]string{"CleanPlan": safeCleanPlan, "Clean": `{"reports":[{"task":"a","snapshot":"S1"}]}`})
	if code, _, errText := runVerb(runClean, "a", "--delete-branch", "-y"); code != 0 {
		t.Fatalf("%d %q", code, errText)
	}
	if (*calls)[0].body["delete_branch"] != true || (*calls)[1].body["delete_branch"] != true {
		t.Fatalf("calls %+v", *calls)
	}
}

func TestContradictoryBranchFlagsAreBadUsage(t *testing.T) {
	stubWorktree(t, nil)
	for _, args := range [][]string{{"a", "--delete-branch", "--keep-branch"}, {"a", "--archive-only", "--delete-branch"}} {
		if code, _, _ := runVerb(runClean, args...); code != 2 {
			t.Errorf("%v: %d", args, code)
		}
		if code, _, _ := runVerb(runDown, args...); code != 2 {
			t.Errorf("down %v: %d", args, code)
		}
	}
}

func TestTheFirstRebuildableSetIsPrintedApartFromTheDocument(t *testing.T) {
	stubWorktree(t, map[string]string{
		"CleanPlan": `{"plans":[{"task":"a"}],"first_rebuildable":["node_modules","dist"]}`,
		"Clean":     `{"reports":[{"task":"a","snapshot":"S1"}]}`,
	})
	code, out, errText := runVerb(runClean, "a", "-y", "--json")
	if code != 0 || !strings.Contains(errText, "node_modules, dist") || strings.Contains(out, "treated as rebuildable") {
		t.Fatalf("%d %q %q", code, out, errText)
	}
}
