package main

import (
	"strings"
	"testing"
)

func TestReportNamesEveryGateAndCounts(t *testing.T) {
	var b strings.Builder
	WriteReport(&b, "linux", []Result{
		{Gate: "first", Status: StatusPass},
		{Gate: "second", Status: StatusSkip, Reason: "docker declared absent on linux"},
		{Gate: "third", Status: StatusFail, Reason: "lsof declared provided on linux, probe failed\nsecond line"},
	})
	out := b.String()
	for _, want := range []string{
		"PASS first", "SKIP second: docker declared absent on linux",
		"FAIL third: lsof declared provided on linux, probe failed", "    second line",
		"linux: 1 passed, 1 skipped, 1 failed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
}

func TestPassedAllowsSkipsButNoFailureAndNeedsAPass(t *testing.T) {
	pass := Result{Status: StatusPass}
	skip := Result{Status: StatusSkip}
	fail := Result{Status: StatusFail}
	for _, c := range []struct {
		in   []Result
		want bool
	}{
		{[]Result{pass, skip}, true},
		{[]Result{pass, fail}, false},
		{[]Result{skip}, false},
		{nil, false},
	} {
		if got := Passed(c.in); got != c.want {
			t.Errorf("Passed(%+v) = %v", c.in, got)
		}
	}
}
