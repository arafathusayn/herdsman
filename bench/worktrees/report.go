package main

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// SchemaVersion is the version of the results file format. Readers keep the
// keys they do not know, so a later version can add keys without a major bump.
const SchemaVersion = "1.0.0"

// Case kinds and verdicts, as written in a results file.
const (
	KindVersus = "versus"
	KindBudget = "budget"

	VerdictWin         = "win"
	VerdictLost        = "lost"
	VerdictWithin      = "within"
	VerdictOver        = "over"
	VerdictUnavailable = "unavailable"
)

// measuredPackages are the folders whose commits make a results file stale.
var measuredPackages = []string{"internal/wtree", "internal/cow", "internal/ledger"}

// Interval is a 95% confidence interval in milliseconds.
type Interval struct {
	Low  float64
	High float64
}

func (i Interval) String() string { return fmt.Sprintf("%.3f..%.3f", i.Low, i.High) }

// band is the largest difference the A/A control could not tell from noise.
func (i Interval) band() float64 { return math.Max(math.Abs(i.Low), math.Abs(i.High)) }

// studentTTable holds the two-sided 95% critical values by degrees of freedom.
var studentTTable = []float64{
	12.706, 4.303, 3.182, 2.776, 2.571, 2.447, 2.365, 2.306, 2.262, 2.228,
	2.201, 2.179, 2.160, 2.145, 2.131, 2.120, 2.110, 2.101, 2.093, 2.086,
	2.080, 2.074, 2.069, 2.064, 2.060, 2.056, 2.052, 2.048, 2.045, 2.042,
}

// studentT rounds down to the nearest tabulated degrees of freedom past the
// exact table. That value is the larger one, so an interval is never narrower
// than 95% promises.
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

// judgeVersus lets herdsman win only when its upper bound, widened by the A/A
// band, stays below the other tool's lower bound. Widening, rather than
// rejecting a case whose A/A misses zero, keeps a 5% chance per case from
// failing a run that is otherwise clean.
func judgeVersus(herdsman, other, aa Interval) string {
	if herdsman.High+aa.band() < other.Low {
		return VerdictWin
	}
	return VerdictLost
}

func judgeBudget(herdsman, aa Interval, budgetMS float64) string {
	if herdsman.High+aa.band() < budgetMS {
		return VerdictWithin
	}
	return VerdictOver
}

// CaseResult is one measured case. Intervals are absent when a case could not run.
type CaseResult struct {
	ID       string
	Kind     string
	Verdict  string
	Herdsman *Interval
	Wt       *Interval
	AA       *Interval
	BudgetMS float64
	Reason   string
	Extra    []string // Tokens of a later schema, written back unchanged.
}

// Results is one results file.
type Results struct {
	Schema    string
	Date      string
	Commit    string
	Machine   string
	Git       string
	Go        string
	Wt        string
	Load      string
	Rounds    int
	Small     int
	Large     int
	IgnoredMB int
	Cases     []CaseResult
	Extra     map[string]string
}

// FileName is the name the results of this run are committed under.
func (r Results) FileName() string {
	day, _, _ := strings.Cut(r.Date, "T")
	return day + "-" + r.Wt + ".md"
}

func cell(i *Interval) string {
	if i == nil {
		return "-"
	}
	return i.String()
}

func (c CaseResult) frontLine() string {
	parts := []string{"id=" + c.ID, "kind=" + c.Kind, "verdict=" + c.Verdict}
	for _, field := range []struct {
		name  string
		value *Interval
	}{{"herdsman", c.Herdsman}, {"wt", c.Wt}, {"aa", c.AA}} {
		if field.value != nil {
			parts = append(parts, field.name+"="+field.value.String())
		}
	}
	if c.BudgetMS > 0 {
		parts = append(parts, "budget="+strconv.FormatFloat(c.BudgetMS, 'f', -1, 64))
	}
	parts = append(parts, c.Extra...)
	if c.Reason != "" {
		parts = append(parts, "reason="+strconv.Quote(c.Reason))
	}
	return "case: " + strings.Join(parts, " ")
}

// Render writes the file: front matter for the check, a table for people.
func (r Results) Render() string {
	var b strings.Builder
	b.WriteString("---\n")
	for _, kv := range [][2]string{
		{"schema", r.Schema}, {"date", r.Date}, {"commit", r.Commit}, {"machine", r.Machine},
		{"git", r.Git}, {"go", r.Go}, {"wt", r.Wt}, {"load", r.Load}, {"rounds", strconv.Itoa(r.Rounds)},
		{"worktrees_small", strconv.Itoa(r.Small)}, {"worktrees_large", strconv.Itoa(r.Large)}, {"ignored_mb", strconv.Itoa(r.IgnoredMB)},
	} {
		fmt.Fprintf(&b, "%s: %s\n", kv[0], kv[1])
	}
	keys := make([]string, 0, len(r.Extra))
	for key := range r.Extra {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&b, "%s: %s\n", key, r.Extra[key])
	}
	for _, c := range r.Cases {
		b.WriteString(c.frontLine() + "\n")
	}
	b.WriteString("---\n\n")
	b.WriteString("# Worktree benchmark results\n\n")
	b.WriteString("95% confidence intervals of the mean, in milliseconds. The A/A row is the same build against itself.\n\n")
	b.WriteString("| Case | herdsman | wt | A/A | Budget | Verdict |\n| --- | --- | --- | --- | --- | --- |\n")
	for _, c := range r.Cases {
		budget := "-"
		if c.BudgetMS > 0 {
			budget = "under " + strconv.FormatFloat(c.BudgetMS, 'f', -1, 64)
		}
		verdict := c.Verdict
		if c.Reason != "" {
			verdict += ": " + c.Reason
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", c.ID, cell(c.Herdsman), cell(c.Wt), cell(c.AA), budget, verdict)
	}
	return b.String()
}

func parseInterval(text string) (*Interval, error) {
	low, high, ok := strings.Cut(text, "..")
	if !ok {
		return nil, fmt.Errorf("bad interval %q", text)
	}
	l, err := strconv.ParseFloat(low, 64)
	if err != nil {
		return nil, fmt.Errorf("bad interval %q", text)
	}
	h, err := strconv.ParseFloat(high, 64)
	if err != nil {
		return nil, fmt.Errorf("bad interval %q", text)
	}
	return &Interval{Low: l, High: h}, nil
}

func parseCase(text string) (CaseResult, error) {
	var c CaseResult
	if head, quoted, ok := strings.Cut(text, " reason="); ok {
		reason, err := strconv.Unquote(quoted)
		if err != nil {
			return c, fmt.Errorf("bad reason %s", quoted)
		}
		c.Reason, text = reason, head
	}
	for _, token := range strings.Fields(text) {
		key, value, ok := strings.Cut(token, "=")
		if !ok {
			return c, fmt.Errorf("bad case token %q", token)
		}
		var err error
		switch key {
		case "id":
			c.ID = value
		case "kind":
			c.Kind = value
		case "verdict":
			c.Verdict = value
		case "herdsman":
			c.Herdsman, err = parseInterval(value)
		case "wt":
			c.Wt, err = parseInterval(value)
		case "aa":
			c.AA, err = parseInterval(value)
		case "budget":
			c.BudgetMS, err = strconv.ParseFloat(value, 64)
		default:
			c.Extra = append(c.Extra, token)
		}
		if err != nil {
			return c, err
		}
	}
	return c, nil
}

// ParseResults reads a results file of this or any earlier schema.
func ParseResults(text string) (Results, error) {
	var r Results
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return r, errors.New("no front matter")
	}
	front, _, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return r, errors.New("front matter is not closed")
	}
	for _, line := range strings.Split(front, "\n") {
		key, value, ok := strings.Cut(line, ": ")
		if !ok {
			key, ok = strings.CutSuffix(line, ":")
			if !ok {
				return r, fmt.Errorf("bad line %q", line)
			}
		}
		switch key {
		case "schema":
			r.Schema = value
		case "date":
			r.Date = value
		case "commit":
			r.Commit = value
		case "machine":
			r.Machine = value
		case "git":
			r.Git = value
		case "go":
			r.Go = value
		case "wt":
			r.Wt = value
		case "load":
			r.Load = value
		case "rounds", "worktrees_small", "worktrees_large", "ignored_mb":
			n, err := strconv.Atoi(value)
			if err != nil {
				return r, fmt.Errorf("bad %s %q", key, value)
			}
			*map[string]*int{"rounds": &r.Rounds, "worktrees_small": &r.Small, "worktrees_large": &r.Large, "ignored_mb": &r.IgnoredMB}[key] = n
		case "case":
			c, err := parseCase(value)
			if err != nil {
				return r, err
			}
			r.Cases = append(r.Cases, c)
		default:
			if r.Extra == nil {
				r.Extra = map[string]string{}
			}
			r.Extra[key] = value
		}
	}
	if r.Commit == "" {
		return r, errors.New("no commit recorded")
	}
	return r, nil
}

// cleanEnv drops what would point a child at another repository or at the
// user's Herdr session.
func cleanEnv(env []string) []string {
	kept := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, "GIT_") || strings.HasPrefix(entry, "HERDR_") || strings.HasPrefix(entry, "HERDSMAN_") {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = cleanEnv(os.Environ())
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func newestResults(dir string) (Results, string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return Results{}, "", err
	}
	var newest Results
	var name string
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return Results{}, "", err
		}
		r, err := ParseResults(string(data))
		if err != nil {
			return Results{}, "", fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		if name == "" || r.Date > newest.Date {
			newest, name = r, filepath.Base(path)
		}
	}
	if name == "" {
		return Results{}, "", fmt.Errorf("no results file in %s", dir)
	}
	return newest, name, nil
}

func usable(i *Interval) bool {
	if i == nil || math.IsNaN(i.Low) || math.IsNaN(i.High) || math.IsInf(i.Low, 0) || math.IsInf(i.High, 0) {
		return false
	}
	return i.Low <= i.High
}

// checkEvidence judges a case again from its intervals, so that a verdict in
// the file counts only when the numbers beside it support it.
func checkEvidence(c CaseResult) error {
	if !usable(c.Herdsman) || !usable(c.AA) {
		return errors.New("has no usable interval")
	}
	var verdict string
	switch c.Kind {
	case KindVersus:
		if slices.Contains(requiredBudgets, c.ID) {
			return errors.New("must be a budget case")
		}
		if !usable(c.Wt) {
			return errors.New("has no usable wt interval")
		}
		verdict = judgeVersus(*c.Herdsman, *c.Wt, *c.AA)
	case KindBudget:
		if slices.Contains(requiredVersus, c.ID) {
			return errors.New("must be compared with wt")
		}
		budget, fixed := requiredBudgetMS[c.ID]
		if fixed && c.BudgetMS != budget {
			return fmt.Errorf("records budget %v, the gate's is %v", c.BudgetMS, budget)
		}
		if c.BudgetMS <= 0 {
			return errors.New("has no budget")
		}
		verdict = judgeBudget(*c.Herdsman, *c.AA, c.BudgetMS)
	default:
		return fmt.Errorf("has unknown kind %q", c.Kind)
	}
	if verdict != c.Verdict {
		return fmt.Errorf("says %s but its intervals say %s", c.Verdict, verdict)
	}
	return nil
}

// checkRun refuses a file that does not say what was measured, or that
// measured less than the gate's workload.
func checkRun(r Results) error {
	for key, value := range map[string]string{"schema": r.Schema, "date": r.Date, "machine": r.Machine, "git": r.Git, "go": r.Go, "wt": r.Wt, "load": r.Load} {
		if value == "" {
			return fmt.Errorf("records no %s", key)
		}
	}
	if r.Rounds < gateRounds {
		return fmt.Errorf("ran %d rounds, the gate needs %d", r.Rounds, gateRounds)
	}
	if r.Small != gateSmall || r.Large != gateLarge || r.IgnoredMB != gateIgnoredMB {
		return fmt.Errorf("measured %d and %d worktrees with %d MB ignored, the gate needs %d and %d with %d MB",
			r.Small, r.Large, r.IgnoredMB, gateSmall, gateLarge, gateIgnoredMB)
	}
	return nil
}

func checkCases(name string, r Results) error {
	if err := checkRun(r); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	byID := map[string]CaseResult{}
	for _, c := range r.Cases {
		byID[c.ID] = c
	}
	required := append(append([]string{}, requiredVersus...), requiredBudgets...)
	for _, id := range required {
		if _, ok := byID[id]; !ok {
			return fmt.Errorf("%s: case %s is missing", name, id)
		}
	}
	for _, c := range r.Cases {
		if c.Verdict != VerdictWin && c.Verdict != VerdictWithin {
			return fmt.Errorf("%s: case %s is %s", name, c.ID, c.Verdict)
		}
		if err := checkEvidence(c); err != nil {
			return fmt.Errorf("%s: case %s %w", name, c.ID, err)
		}
	}
	return nil
}

func checkFreshness(repo, name string, r Results) error {
	if _, err := gitOutput(repo, "merge-base", "--is-ancestor", r.Commit, "HEAD"); err != nil {
		return fmt.Errorf("%s: commit %s is not in this history; run the benchmark again", name, r.Commit)
	}
	last, err := gitOutput(repo, append([]string{"log", "-1", "--format=%H", "--"}, measuredPackages...)...)
	if err != nil {
		return fmt.Errorf("read the last measured commit: %w", err)
	}
	if last == "" {
		return nil
	}
	if _, err := gitOutput(repo, "merge-base", "--is-ancestor", last, r.Commit); err != nil {
		return fmt.Errorf("%s: commit %s is older than %s, the last commit to the measured packages; run the benchmark again", name, r.Commit, last)
	}
	return nil
}

// Check is the bench-results gate: every committed file must parse, and the
// newest must be current and hold no lost or missing case.
func Check(repo, resultsDir string) error {
	r, name, err := newestResults(resultsDir)
	if err != nil {
		return err
	}
	if err := checkFreshness(repo, name, r); err != nil {
		return err
	}
	return checkCases(name, r)
}
