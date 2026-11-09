package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakeClock returns a duration per tool and records the order of the calls.
type fakeClock struct {
	calls []string
	ms    map[string]float64
	fail  map[string]error
}

func (f *fakeClock) measure(tool string, _ Case, _ int) (time.Duration, error) {
	f.calls = append(f.calls, tool)
	if err := f.fail[tool]; err != nil {
		return 0, err
	}
	base := f.ms[tool]
	// A little spread so the interval has a width.
	spread := float64(len(f.calls)%3) / 10
	return time.Duration((base + spread) * float64(time.Millisecond)), nil
}

func TestRotationVisitsEveryPosition(t *testing.T) {
	got := [][]string{rotation(0), rotation(1), rotation(2)}
	want := [][]string{
		{slotFirst, slotSecond, slotWt},
		{slotSecond, slotWt, slotFirst},
		{slotWt, slotFirst, slotSecond},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestRunCaseVersusWins(t *testing.T) {
	clock := &fakeClock{ms: map[string]float64{slotFirst: 2, slotSecond: 2, slotWt: 20}}
	got := runCase(Case{ID: "a", Kind: KindVersus}, 6, clock.measure)
	if got.Verdict != VerdictWin {
		t.Fatalf("got %+v", got)
	}
	if got.Herdsman == nil || got.Wt == nil || got.AA == nil {
		t.Fatalf("got %+v", got)
	}
	if len(clock.calls) != 18 {
		t.Fatalf("three slots a round, got %d calls", len(clock.calls))
	}
}

func TestRunCaseVersusLoses(t *testing.T) {
	clock := &fakeClock{ms: map[string]float64{slotFirst: 30, slotSecond: 30, slotWt: 5}}
	if got := runCase(Case{ID: "a", Kind: KindVersus}, 6, clock.measure); got.Verdict != VerdictLost {
		t.Fatalf("got %+v", got)
	}
}

func TestRunCaseBudgetSkipsWt(t *testing.T) {
	clock := &fakeClock{ms: map[string]float64{slotFirst: 10, slotSecond: 10}}
	got := runCase(Case{ID: "b", Kind: KindBudget, BudgetMS: 200}, 4, clock.measure)
	if got.Verdict != VerdictWithin || got.Wt != nil {
		t.Fatalf("got %+v", got)
	}
	for _, call := range clock.calls {
		if call == slotWt {
			t.Fatal("a budget case has no other tool to run")
		}
	}
}

func TestRunCaseOverBudget(t *testing.T) {
	clock := &fakeClock{ms: map[string]float64{slotFirst: 500, slotSecond: 500}}
	if got := runCase(Case{ID: "b", Kind: KindBudget, BudgetMS: 200}, 4, clock.measure); got.Verdict != VerdictOver {
		t.Fatalf("got %+v", got)
	}
}

func TestRunCaseReportsAFailedSampleAsUnavailable(t *testing.T) {
	clock := &fakeClock{ms: map[string]float64{slotFirst: 1, slotSecond: 1}, fail: map[string]error{slotWt: errors.New("exit 2")}}
	got := runCase(Case{ID: "a", Kind: KindVersus}, 3, clock.measure)
	if got.Verdict != VerdictUnavailable || !strings.Contains(got.Reason, "exit 2") {
		t.Fatalf("got %+v", got)
	}
	if got.Herdsman != nil {
		t.Fatal("no interval may be reported from a partial run")
	}
}

func TestRunCaseNeedsAtLeastTwoRounds(t *testing.T) {
	clock := &fakeClock{ms: map[string]float64{slotFirst: 1, slotSecond: 1, slotWt: 1}}
	if got := runCase(Case{ID: "a", Kind: KindVersus}, 1, clock.measure); got.Verdict != VerdictUnavailable {
		t.Fatalf("got %+v", got)
	}
}
