package trace

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

// EvalVersion is the version of the experimental evaluation surface: its
// reports, its run records and its findings log.
const EvalVersion = "0.1.0"

// RunsFile holds a suite's recorded runs, one JSON object per line.
const RunsFile = "runs.jsonl"

// The two halves of an A/A control: the same arm, run as two separate groups.
const (
	controlA = "a"
	controlB = "b"
)

// ErrNoControl refuses a report that has no noise measurement to read it by.
var ErrNoControl = errors.New("every reported arm needs its own A/A control of at least two runs on each side, and a suite needs at least one arm of ordinary runs")

// Run is one recorded run of a suite's task.
type Run struct {
	Schema     string   `json:"schema,omitempty"`
	Task       string   `json:"task"`
	Arm        string   `json:"arm"`
	Control    string   `json:"control,omitempty"`
	Pass       *bool    `json:"pass"`
	DurationMS *float64 `json:"duration_ms"`
	CostMicros *int64   `json:"cost_micros,omitempty"`
}

// finiteDuration is a duration JSON can carry: zero or more and a real number.
func finiteDuration(ms float64) bool {
	return ms >= 0 && !math.IsInf(ms, 1) && !math.IsNaN(ms)
}

func (r Run) valid() bool {
	return r.Task != "" && r.Arm != "" && r.Pass != nil && r.DurationMS != nil && finiteDuration(*r.DurationMS) &&
		(r.CostMicros == nil || *r.CostMicros >= 0) && slices.Contains([]string{"", controlA, controlB}, r.Control)
}

// encodeRun is replaced only by the test of a run that cannot be encoded.
var encodeRun = func(run Run) ([]byte, error) { return json.Marshal(run) }

// AppendRun records one run in the suite folder, in one write call so that
// lines from concurrent processes do not interleave.
func AppendRun(suite string, run Run) error {
	if !run.valid() {
		return fmt.Errorf("%w: eval: a run needs a task, an arm, a result and a duration of zero or more", ErrInvalid)
	}
	run.Schema = EvalVersion
	line, err := encodeRun(run)
	if err != nil {
		return fmt.Errorf("eval: encode: %w", err)
	}
	handle, err := os.OpenFile(filepath.Join(suite, RunsFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("eval: %w", err)
	}
	_, err = handle.Write(append(line, '\n'))
	return errors.Join(err, handle.Close())
}

// LoadRuns reads a suite folder. It returns the runs and how many lines it
// could not use.
func LoadRuns(suite string) ([]Run, int, error) {
	path := filepath.Join(suite, RunsFile)
	if _, err := os.Stat(path); err != nil {
		return nil, 0, fmt.Errorf("eval: %w", err)
	}
	var runs []Run
	skipped, err := eachLine(path, func(line []byte) bool {
		var run Run
		if json.Unmarshal(line, &run) != nil || !run.valid() {
			return false
		}
		runs = append(runs, run)
		return true
	})
	if err != nil {
		return nil, 0, fmt.Errorf("eval: %w", err)
	}
	return runs, skipped, nil
}

// ArmReport is one arm's counts and intervals. The report states ranges only:
// no mean, median or other single value. An interval is absent when too few
// runs reported the measure.
type ArmReport struct {
	Arm        string    `json:"arm"`
	Runs       int       `json:"runs"`
	Passed     int       `json:"passed"`
	PassRate   *Interval `json:"pass_rate"`
	DurationMS *Interval `json:"duration_ms"`
	CostRuns   int       `json:"cost_runs"`
	CostMicros *Interval `json:"cost_micros"`
}

// ControlReport is the A/A band of one arm: the interval of the difference
// between two groups that differ in nothing. A band that is wide says the arms
// cannot be told apart at that size.
type ControlReport struct {
	Arm                  string    `json:"arm"`
	RunsA                int       `json:"runs_a"`
	RunsB                int       `json:"runs_b"`
	PassRateDifference   *Interval `json:"pass_rate_difference"`
	DurationMSDifference *Interval `json:"duration_ms_difference"`
	CostMicrosDifference *Interval `json:"cost_micros_difference"`
}

// Report is what eval prints.
type Report struct {
	Schema       Schema          `json:"schema"`
	Experimental string          `json:"experimental"`
	Suite        string          `json:"suite"`
	Arms         []ArmReport     `json:"arms"`
	Controls     []ControlReport `json:"controls"`
	Findings     *FindingCounts  `json:"findings,omitempty"`
	Rank         *RankDecision   `json:"rank,omitempty"`
	Skipped      int             `json:"skipped"`
}

// group is a set of runs with their measures pulled out.
type group struct {
	runs      int
	passed    int
	durations []float64
	costs     []float64
}

func (g *group) add(run Run) {
	g.runs++
	if *run.Pass {
		g.passed++
	}
	g.durations = append(g.durations, *run.DurationMS)
	if run.CostMicros != nil {
		g.costs = append(g.costs, float64(*run.CostMicros))
	}
}

func optional(interval Interval, ok bool) *Interval {
	if !ok {
		return nil
	}
	return &interval
}

// Evaluate reports every arm with its intervals and its own A/A control. Arms
// are listed by name: nothing here orders them by result. It fails with
// ErrNoControl when any reported arm lacks a complete control, because another
// arm's control says nothing about this arm's noise.
func Evaluate(suite string, runs []Run, skipped int) (Report, error) {
	arms := map[string]*group{}
	controls := map[string]map[string]*group{}
	for _, run := range runs {
		if run.Control == "" {
			if arms[run.Arm] == nil {
				arms[run.Arm] = &group{}
			}
			arms[run.Arm].add(run)
			continue
		}
		if controls[run.Arm] == nil {
			controls[run.Arm] = map[string]*group{controlA: {}, controlB: {}}
		}
		controls[run.Arm][run.Control].add(run)
	}
	if len(arms) == 0 {
		return Report{}, ErrNoControl
	}
	for _, name := range sortedKeys(arms) {
		if controls[name] == nil {
			return Report{}, fmt.Errorf("%w: arm %q has none", ErrNoControl, name)
		}
	}
	report := Report{
		Schema: Schema{Version: EvalVersion}, Experimental: "eval/" + EvalVersion, Suite: suite,
		Arms: []ArmReport{}, Controls: []ControlReport{}, Skipped: skipped,
	}
	for _, name := range sortedKeys(arms) {
		g := arms[name]
		report.Arms = append(report.Arms, ArmReport{
			Arm: name, Runs: g.runs, Passed: g.passed,
			PassRate:   optional(wilson(g.passed, g.runs)),
			DurationMS: optional(meanInterval(g.durations)),
			CostRuns:   len(g.costs),
			CostMicros: optional(meanInterval(g.costs)),
		})
	}
	for _, name := range sortedKeys(controls) {
		a, b := controls[name][controlA], controls[name][controlB]
		if a.runs < 2 || b.runs < 2 {
			return Report{}, ErrNoControl
		}
		report.Controls = append(report.Controls, ControlReport{
			Arm: name, RunsA: a.runs, RunsB: b.runs,
			PassRateDifference:   optional(rateDifference(a.passed, a.runs, b.passed, b.runs)),
			DurationMSDifference: optional(meanDifference(a.durations, b.durations)),
			CostMicrosDifference: optional(meanDifference(a.costs, b.costs)),
		})
	}
	return report, nil
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
