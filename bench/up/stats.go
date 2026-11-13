package main

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// The verdicts of the case, as the worktree benchmark writes them.
const (
	verdictWithin      = "within"
	verdictOver        = "over"
	verdictUnavailable = "unavailable"
)

// Interval is a 95% confidence interval in milliseconds.
type Interval struct {
	Low  float64
	High float64
}

// band is the widest distance of the A/A interval from zero.
func (i Interval) band() float64 { return math.Max(math.Abs(i.Low), math.Abs(i.High)) }

// studentTTable holds the two-sided 95% critical values by degrees of freedom.
var studentTTable = []float64{
	12.706, 4.303, 3.182, 2.776, 2.571, 2.447, 2.365, 2.306, 2.262, 2.228,
	2.201, 2.179, 2.160, 2.145, 2.131, 2.120, 2.110, 2.101, 2.093, 2.086,
	2.080, 2.074, 2.069, 2.064, 2.060, 2.056, 2.052, 2.048, 2.045, 2.042,
}

func studentT(df int) float64 {
	switch {
	case df <= len(studentTTable):
		return studentTTable[df-1]
	case df < 40:
		return studentTTable[len(studentTTable)-1]
	case df < 60:
		return 2.0211
	case df < 120:
		return 2.0003
	}
	return 1.980
}

// meanInterval is the 95% interval of the mean of the samples.
func meanInterval(samples []float64) (Interval, error) {
	n := len(samples)
	if n < 2 {
		return Interval{}, errors.New("an interval needs at least two samples")
	}
	var sum float64
	for _, s := range samples {
		sum += s
	}
	mean := sum / float64(n)
	var squares float64
	for _, s := range samples {
		squares += (s - mean) * (s - mean)
	}
	half := studentT(n-1) * math.Sqrt(squares/float64(n-1)) / math.Sqrt(float64(n))
	return Interval{Low: mean - half, High: mean + half}, nil
}

// Result is the measured case.
type Result struct {
	Rounds   int
	BudgetMS float64
	Herdsman *Interval
	AA       *Interval
	Verdict  string
	Reason   string
}

// String is the report line: intervals only.
func (r Result) String() string {
	if r.Herdsman == nil || r.AA == nil {
		return fmt.Sprintf("%s budget %.0fms: %s: %s", CaseUpCore, r.BudgetMS, r.Verdict, r.Reason)
	}
	return fmt.Sprintf("%s budget %.0fms rounds %d: herdsman 95%% CI [%.2f, %.2f] ms, A/A 95%% CI [%.2f, %.2f] ms: %s",
		CaseUpCore, r.BudgetMS, r.Rounds, r.Herdsman.Low, r.Herdsman.High, r.AA.Low, r.AA.High, r.Verdict)
}

// measureFunc times one sample for a slot.
type measureFunc func(slot string, round int) (time.Duration, error)

// runCase measures both slots in rotating order and judges the budget. Any
// failed sample makes the case unavailable: an interval over the samples that
// happened to work would hide the failures.
func runCase(rounds int, budgetMS float64, measure measureFunc) Result {
	result := Result{Rounds: rounds, BudgetMS: budgetMS}
	if rounds < 2 {
		result.Verdict, result.Reason = verdictUnavailable, "an interval needs at least two rounds"
		return result
	}
	samples := map[string][]float64{}
	for round := range rounds {
		slots := []string{slotFirst, slotSecond}
		if round%2 == 1 {
			slots[0], slots[1] = slots[1], slots[0]
		}
		for _, slot := range slots {
			elapsed, err := measure(slot, round)
			if err != nil {
				result.Verdict, result.Reason = verdictUnavailable, fmt.Sprintf("%s round %d: %v", slot, round, err)
				return result
			}
			samples[slot] = append(samples[slot], float64(elapsed)/float64(time.Millisecond))
		}
	}
	first, second := samples[slotFirst], samples[slotSecond]
	differences := make([]float64, len(first))
	for i := range first {
		differences[i] = first[i] - second[i]
	}
	herdsman, err := meanInterval(append(append([]float64{}, first...), second...))
	if err != nil {
		result.Verdict, result.Reason = verdictUnavailable, err.Error()
		return result
	}
	aa, err := meanInterval(differences)
	if err != nil {
		result.Verdict, result.Reason = verdictUnavailable, err.Error()
		return result
	}
	result.Herdsman, result.AA = &herdsman, &aa
	result.Verdict = verdictOver
	if herdsman.High+aa.band() < budgetMS {
		result.Verdict = verdictWithin
	}
	return result
}
