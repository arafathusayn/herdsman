package main

import (
	"fmt"
	"time"
)

// The three slots of a round: herdsman twice for the A/A control, wt once.
const (
	slotFirst  = "herdsman-a"
	slotSecond = "herdsman-b"
	slotWt     = "wt"
)

// measureFunc times one sample of a case for a slot.
type measureFunc func(slot string, c Case, round int) (time.Duration, error)

// rotation orders a round's slots. Every slot takes every position in turn, so
// a slow drift of the machine does not favour one of them.
func rotation(round int) []string {
	slots := []string{slotFirst, slotSecond, slotWt}
	start := round % len(slots)
	return append(append([]string{}, slots[start:]...), slots[:start]...)
}

func unavailable(c Case, reason string) CaseResult {
	return CaseResult{ID: c.ID, Kind: c.Kind, Verdict: VerdictUnavailable, BudgetMS: c.BudgetMS, Reason: reason}
}

func milliseconds(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// runCase measures a case and judges it. Any failed sample makes the whole
// case unavailable: an interval over the samples that happened to work would
// report a tool as faster for failing.
func runCase(c Case, rounds int, measure measureFunc) CaseResult {
	if rounds < 2 {
		return unavailable(c, "an interval needs at least two rounds")
	}
	samples := map[string][]float64{}
	for round := range rounds {
		for _, slot := range rotation(round) {
			if slot == slotWt && c.Kind == KindBudget {
				continue
			}
			elapsed, err := measure(slot, c, round)
			if err != nil {
				return unavailable(c, fmt.Sprintf("%s: %v", slot, err))
			}
			samples[slot] = append(samples[slot], milliseconds(elapsed))
		}
	}
	return judge(c, samples)
}

func judge(c Case, samples map[string][]float64) CaseResult {
	first, second := samples[slotFirst], samples[slotSecond]
	differences := make([]float64, len(first))
	for i := range first {
		differences[i] = first[i] - second[i]
	}
	herdsman, err := meanInterval(append(append([]float64{}, first...), second...))
	if err != nil {
		return unavailable(c, err.Error())
	}
	aa, err := meanInterval(differences)
	if err != nil {
		return unavailable(c, err.Error())
	}
	result := CaseResult{ID: c.ID, Kind: c.Kind, Herdsman: &herdsman, AA: &aa, BudgetMS: c.BudgetMS}
	if c.Kind == KindBudget {
		result.Verdict = judgeBudget(herdsman, aa, c.BudgetMS)
		return result
	}
	other, err := meanInterval(samples[slotWt])
	if err != nil {
		return unavailable(c, err.Error())
	}
	result.Wt = &other
	result.Verdict = judgeVersus(herdsman, other, aa)
	return result
}
