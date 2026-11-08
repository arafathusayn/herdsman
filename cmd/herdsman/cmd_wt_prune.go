package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const pruneHelp = `usage: herdsman prune [--min-age <duration>] [--dry-run]

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
	plans, _, err := callCleanPlan(map[string]any{"dir": dir, "merged": true, "min_age_seconds": int64(age.Seconds())})
	if err != nil {
		return fail(stderr, err)
	}
	var tasks []string
	for _, plan := range plans {
		if reason := pruneSkip(plan); reason != "" {
			fmt.Fprintf(stdout, "%s: skipped: %s\n", plan.Task, reason)
			continue
		}
		tasks = append(tasks, plan.Task)
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
	reports, _, err := callClean(dir, tasks, &cleanOptions{force: &force, keepBranch: &keep, named: &stringList{}})
	if err != nil {
		return fail(stderr, err)
	}
	return printCleanReports(stdout, stderr, reports)
}
