package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const pruneHelp = `usage: herdsman prune [--min-age <duration>] [--dry-run] [--json]

Frees integrated trees older than --min-age (default 1d; 12h and 3d both work).
Locked, dirty and current trees, and tasks still running, are skipped.

exit codes:
  0  every eligible tree was freed
  1  a tree could not be freed
  2  bad usage
`

func init() {
	register(command{name: "prune", summary: "Free integrated worktrees past a minimum age.", help: pruneHelp, run: runPrune})
}

// parseAge reads a Go duration, plus whole days.
func parseAge(text string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(text, "d"); ok {
		count, err := strconv.Atoi(days)
		if err != nil || count < 0 {
			return 0, fmt.Errorf("bad age %q", text)
		}
		return time.Duration(count) * 24 * time.Hour, nil
	}
	return time.ParseDuration(text)
}

type pruneSkipped struct {
	Task   string `json:"task"`
	Reason string `json:"reason"`
}

func pruneSkip(plan cleanPlan) string {
	switch {
	case plan.Blocked != "":
		return plan.Blocked
	case plan.NeedsForce:
		return "unsaved work: " + plan.Reason
	case len(plan.Running) > 0:
		return "it still runs " + strings.Join(plan.Running, ", ")
	}
	return ""
}

func runPrune(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := verbFlags("prune", stderr)
	minAge := fs.String("min-age", "1d", "only trees older than this")
	dryRun := fs.Bool("dry-run", false, "print what would go and change nothing")
	asJSON := fs.Bool("json", false, "print the outcome as JSON")
	positional, _, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) != 0 {
		return usageError(stderr, "prune takes no task; it selects integrated trees")
	}
	age, err := parseAge(*minAge)
	if err != nil {
		return usageError(stderr, "--min-age is a duration such as 12h or 3d")
	}
	dir, err := workingFolder()
	if err != nil {
		return fail(stderr, err)
	}
	plans, rawPlans, err := callCleanPlan(map[string]any{"dir": dir, "merged": true, "min_age_seconds": int64(age.Seconds())}, stderr)
	if err != nil {
		return fail(stderr, err)
	}
	tasks := []string{}
	skipped := []pruneSkipped{}
	for _, plan := range plans {
		if reason := pruneSkip(plan); reason != "" {
			skipped = append(skipped, pruneSkipped{Task: plan.Task, Reason: reason})
			if !*asJSON {
				fmt.Fprintf(stdout, "%s: skipped: %s\n", plan.Task, reason)
			}
			continue
		}
		tasks = append(tasks, plan.Task)
	}
	document := map[string]any{"plans": orEmpty(rawPlans), "tasks": tasks, "skipped": skipped, "dry_run": *dryRun, "reports": []json.RawMessage{}}
	if *asJSON && (len(tasks) == 0 || *dryRun) {
		return writePruneJSON(stdout, stderr, document, 0)
	}
	if len(tasks) == 0 {
		fmt.Fprintln(stdout, "nothing to prune")
		return 0
	}
	if *dryRun {
		fmt.Fprintf(stdout, "would remove %s\n", strings.Join(tasks, ", "))
		return 0
	}
	force, keep := false, false
	reports, rawReports, err := callClean(dir, tasks, plans, &cleanOptions{force: &force, keepBranch: &keep, deleteBranch: new(bool), archiveOnly: new(bool), named: &stringList{}})
	if err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		document["reports"] = orEmpty(rawReports)
		code := 0
		for _, report := range reports {
			if report.Error != "" {
				code = 1
			}
		}
		return writePruneJSON(stdout, stderr, document, code)
	}
	return printCleanReports(stdout, stderr, reports)
}

func writePruneJSON(stdout, stderr io.Writer, document map[string]any, code int) int {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return fail(stderr, err)
	}
	return code
}
