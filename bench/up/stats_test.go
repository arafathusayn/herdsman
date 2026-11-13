package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The two slots alternate their order, the budget is judged on the upper
// bound widened by the A/A band, and the report has intervals only.
func TestRunCaseJudgesTheBudgetWithTheAAControl(t *testing.T) {
	var order []string
	measure := func(slot string, round int) (time.Duration, error) {
		order = append(order, slot)
		return time.Duration(100+round%3) * time.Millisecond, nil
	}
	r := runCase(6, 200, measure)
	if r.Verdict != verdictWithin || r.Herdsman == nil || r.AA == nil {
		t.Fatalf("%+v", r)
	}
	if order[0] != slotFirst || order[2] != slotSecond {
		t.Errorf("the slots do not rotate: %v", order)
	}
	if r.AA.Low > 0 || r.AA.High < 0 {
		t.Errorf("A/A of the same build misses zero: %+v", r.AA)
	}
	line := r.String()
	if !strings.Contains(line, "95% CI") || strings.Contains(line, "median") || strings.Contains(line, "p50") {
		t.Errorf("report %q", line)
	}
	if over := runCase(6, 101, measure); over.Verdict != verdictOver {
		t.Errorf("a budget below the interval: %+v", over)
	}
}

func TestAFailedSampleMakesTheCaseUnavailable(t *testing.T) {
	r := runCase(4, 100, func(slot string, round int) (time.Duration, error) {
		if round == 2 {
			return 0, errors.New("boom")
		}
		return time.Millisecond, nil
	})
	if r.Verdict != verdictUnavailable || r.Herdsman != nil || !strings.Contains(r.Reason, "boom") {
		t.Fatalf("%+v", r)
	}
	if one := runCase(1, 100, nil); one.Verdict != verdictUnavailable {
		t.Fatalf("%+v", one)
	}
}
