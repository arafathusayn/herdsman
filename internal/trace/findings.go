package trace

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"time"
)

// FindingsFile is the findings log inside a route's trace folder. It is append
// only: an outcome is a new line, and the latest outcome of a finding counts.
const FindingsFile = "findings.jsonl"

// What later happened to a finding.
const (
	OutcomeAccepted  = "accepted"
	OutcomeFixed     = "fixed"
	OutcomeDismissed = "dismissed"
)

// MinLabeledPerReviewer is how many findings with an outcome each reviewer
// needs, across at least two reviewers, before the findings are a labeled set.
const MinLabeledPerReviewer = 30

const (
	lineFinding = "finding"
	lineOutcome = "outcome"
)

// Finding is one thing a reviewer reported.
type Finding struct {
	ID       string    `json:"id"`
	Reviewer string    `json:"reviewer"`
	Severity string    `json:"severity"`
	Task     string    `json:"task,omitempty"`
	Head     string    `json:"head,omitempty"`
	File     string    `json:"file,omitempty"`
	Line     int       `json:"line,omitempty"`
	Title    string    `json:"title"`
	Time     time.Time `json:"time"`
	Outcome  string    `json:"outcome,omitempty"`
}

// findingLine is one line of the log, either a finding or an outcome.
type findingLine struct {
	Schema string `json:"schema"`
	Type   string `json:"type"`
	Finding
}

func validOutcome(outcome string) bool {
	return slices.Contains([]string{OutcomeAccepted, OutcomeFixed, OutcomeDismissed}, outcome)
}

// AddFinding records a finding and returns its id. Without an id one is
// derived from the finding, so that recording the same finding twice is one
// record.
func AddFinding(route string, finding Finding, at time.Time) (string, error) {
	if finding.Reviewer == "" || finding.Severity == "" || finding.Title == "" {
		return "", fmt.Errorf("%w: eval: a finding needs a reviewer, a severity and a title", ErrInvalid)
	}
	if finding.ID == "" {
		finding.ID = digest(finding.Reviewer, finding.Severity, finding.Task, finding.Head, finding.File, strconv.Itoa(finding.Line), finding.Title)[:16]
	}
	existing, _, err := LoadFindings(route)
	if err != nil {
		return "", err
	}
	if slices.ContainsFunc(existing, func(f Finding) bool { return f.ID == finding.ID }) {
		return finding.ID, nil
	}
	finding.Time, finding.Outcome = at, ""
	if err := appendLine(route, FindingsFile, findingLine{Schema: EvalVersion, Type: lineFinding, Finding: finding}); err != nil {
		return "", err
	}
	return finding.ID, nil
}

// SetOutcome records what happened to a finding.
func SetOutcome(route, id, outcome string, at time.Time) error {
	if !validOutcome(outcome) {
		return fmt.Errorf("%w: eval: outcome %q: use %s, %s or %s", ErrInvalid, outcome, OutcomeDismissed, OutcomeAccepted, OutcomeFixed)
	}
	existing, _, err := LoadFindings(route)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(existing, func(f Finding) bool { return f.ID == id }) {
		return fmt.Errorf("%w: eval: no finding %q in this route", ErrInvalid, id)
	}
	return appendLine(route, FindingsFile, findingLine{Schema: EvalVersion, Type: lineOutcome, Finding: Finding{ID: id, Outcome: outcome, Time: at}})
}

// LoadFindings reads a route's findings in the order they were recorded, each
// with its latest outcome, and how many lines it could not use.
func LoadFindings(route string) ([]Finding, int, error) {
	found := []Finding{}
	index := map[string]int{}
	skipped, err := eachLine(filepath.Join(route, traceDir, FindingsFile), func(data []byte) bool {
		var line findingLine
		if json.Unmarshal(data, &line) != nil || line.ID == "" {
			return false
		}
		position, known := index[line.ID]
		switch {
		case line.Type == lineFinding && !known:
			index[line.ID] = len(found)
			found = append(found, line.Finding)
			return true
		case line.Type == lineOutcome && known && validOutcome(line.Outcome):
			found[position].Outcome = line.Outcome
			return true
		}
		return false
	})
	if err != nil {
		return nil, 0, fmt.Errorf("eval: %w", err)
	}
	return found, skipped, nil
}

// ReviewerCounts is one reviewer's findings by outcome.
type ReviewerCounts struct {
	Reviewer  string `json:"reviewer"`
	Recorded  int    `json:"recorded"`
	Open      int    `json:"open"`
	Accepted  int    `json:"accepted"`
	Fixed     int    `json:"fixed"`
	Dismissed int    `json:"dismissed"`
}

// FindingCounts are the totals, with reviewers listed by name. No field orders
// them by quality.
type FindingCounts struct {
	Recorded  int              `json:"recorded"`
	Labeled   int              `json:"labeled"`
	Open      int              `json:"open"`
	Reviewers []ReviewerCounts `json:"reviewers"`
}

// CountFindings adds up findings per reviewer.
func CountFindings(found []Finding) FindingCounts {
	byReviewer := map[string]*ReviewerCounts{}
	counts := FindingCounts{Reviewers: []ReviewerCounts{}}
	for _, finding := range found {
		row := byReviewer[finding.Reviewer]
		if row == nil {
			row = &ReviewerCounts{Reviewer: finding.Reviewer}
			byReviewer[finding.Reviewer] = row
		}
		row.Recorded++
		counts.Recorded++
		switch finding.Outcome {
		case OutcomeAccepted:
			row.Accepted++
		case OutcomeFixed:
			row.Fixed++
		case OutcomeDismissed:
			row.Dismissed++
		default:
			row.Open++
			counts.Open++
			continue
		}
		counts.Labeled++
	}
	for _, name := range sortedKeys(byReviewer) {
		counts.Reviewers = append(counts.Reviewers, *byReviewer[name])
	}
	return counts
}

// RankDecision says why no ranking was produced.
type RankDecision struct {
	Refused    bool   `json:"refused"`
	LabeledSet bool   `json:"labeled_set"`
	Reason     string `json:"reason"`
}

// DecideRank always refuses. Without a labeled set there is nothing to rank
// on; with one, this version still ranks nothing and says so, because no
// ranking method has been validated against labeled data yet.
func DecideRank(counts FindingCounts) RankDecision {
	enough := 0
	for _, row := range counts.Reviewers {
		if row.Recorded-row.Open >= MinLabeledPerReviewer {
			enough++
		}
	}
	if enough < 2 {
		return RankDecision{Refused: true, Reason: fmt.Sprintf(
			"no labeled set: ranking needs at least two reviewers with %d findings each that have an outcome; %d reviewers have that many", MinLabeledPerReviewer, enough)}
	}
	return RankDecision{Refused: true, LabeledSet: true, Reason: "a labeled set exists, but this version has no validated ranking method and ranks nothing"}
}
