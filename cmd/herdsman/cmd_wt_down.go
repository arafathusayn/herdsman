package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const downHelp = `usage: herdsman down [<task>|--merged|--route <folder>|--all] [-y] [--force]
                     [--grace <seconds>] [--keep-branch] [--delete-branch] [--archive-only]
                     [--include associated]
                     [--force-irreversible <path>]... [--json]

Stops what a task runs, then frees what it holds, as one plan with one question.
A stop that fails ends the command before anything is removed.

exit codes:
  0  every selected task was stopped and freed
  1  a stop or a clean failed
  2  bad usage, or no terminal to ask on
`

func init() {
	register(command{name: "down", summary: "Stop a task, then free what it holds.", help: downHelp, run: runDown})
}

func teardownPlansFor(dir string, tasks []string) ([]json.RawMessage, error) {
	var plans []json.RawMessage
	for _, task := range tasks {
		raw, err := callWorktree("TeardownPlan", map[string]any{"dir": dir, "selector": task})
		if err != nil {
			return nil, err
		}
		var planned struct {
			Plans []json.RawMessage `json:"plans"`
		}
		if err := json.Unmarshal(raw, &planned); err != nil {
			return nil, err
		}
		plans = append(plans, planned.Plans...)
	}
	return plans, nil
}

// stopHalf runs the teardown half and says whether the clean half may start.
// Its outcomes are printed only in text mode; JSON carries them in the report.
func stopHalf(dir string, plans []json.RawMessage, grace int, include, asJSON bool, stdout, stderr io.Writer) ([]teardownReport, bool) {
	raw, err := callWorktree("Teardown", map[string]any{"dir": dir, "plans": plans, "grace_seconds": grace, "include_associated": include})
	if err != nil {
		fail(stderr, err)
		return nil, false
	}
	var done struct {
		Reports []teardownReport `json:"reports"`
	}
	if err := json.Unmarshal(raw, &done); err != nil {
		fail(stderr, err)
		return nil, false
	}
	ok := true
	for _, report := range done.Reports {
		for _, outcome := range report.Outcomes {
			if !asJSON {
				fmt.Fprintf(stdout, "%s %s %s: %s\n", report.Task, outcome.Kind, outcome.ID, outcome.Result)
			}
			if strings.HasPrefix(outcome.Result, "failed") {
				ok = false
			}
		}
		if report.Error != "" {
			fmt.Fprintf(stderr, "herdsman: %s: %s\n", report.Task, report.Error)
			ok = false
		}
	}
	return done.Reports, ok
}

// writeDownJSON emits one document holding both halves.
func writeDownJSON(stdout, stderr io.Writer, stopPlans []json.RawMessage, stopped []teardownReport, plans, rawReports []json.RawMessage, code int) int {
	if stopped == nil {
		stopped = []teardownReport{}
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	err := encoder.Encode(map[string]any{
		"teardown": map[string]any{"plans": orEmpty(stopPlans), "reports": stopped},
		"plans":    orEmpty(plans), "reports": orEmpty(rawReports),
	})
	if err != nil {
		return fail(stderr, err)
	}
	return code
}

func runDown(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := verbFlags("down", stderr)
	options := addCleanFlags(fs)
	grace := fs.Int("grace", 0, "seconds between the polite and the forced stop")
	include := fs.String("include", "", `also stop what is only "associated"`)
	positional, _, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	selector, err := options.selector(positional)
	if err != nil {
		return usageError(stderr, err.Error())
	}
	if *grace < 0 {
		return usageError(stderr, "--grace is a number of seconds")
	}
	if *include != "" && *include != "associated" {
		return usageError(stderr, `--include takes only "associated"`)
	}
	dir, err := workingFolder()
	if err != nil {
		return fail(stderr, err)
	}
	plans, rawPlans, err := callCleanPlan(cleanPlanRequest(dir, selector, options), stderr)
	if err != nil {
		return fail(stderr, err)
	}
	tasks, stop := options.checkPlans(plans, true)
	if stop != "" {
		fmt.Fprintf(stderr, "herdsman: %s\n", stop)
		return 1
	}
	if len(tasks) == 0 {
		if *options.asJSON {
			return writeDownJSON(stdout, stderr, nil, nil, rawPlans, nil, 0)
		}
		fmt.Fprintln(stdout, "nothing to take down")
		return 0
	}
	stopping, err := teardownPlansFor(dir, tasks)
	if err != nil {
		return fail(stderr, err)
	}
	if !*options.asJSON {
		printTeardownPlans(stdout, stopping)
		printCleanPlans(stdout, plans)
	}
	if ok, code := options.approve(stdin, stdout, stderr, "stop and free these? [y/N] "); !ok {
		return code
	}
	includeAssociated := *include == "associated"
	var stopped []teardownReport
	if hasStops(stopping, includeAssociated) {
		var ok bool
		if stopped, ok = stopHalf(dir, stopping, *grace, includeAssociated, *options.asJSON, stdout, stderr); !ok {
			if *options.asJSON {
				return writeDownJSON(stdout, stderr, stopping, stopped, rawPlans, nil, 1)
			}
			return 1
		}
	}
	reports, rawReports, err := callClean(dir, tasks, plans, options)
	if err != nil {
		return fail(stderr, err)
	}
	if *options.asJSON {
		code := 0
		for _, report := range reports {
			if report.Error != "" {
				code = 1
			}
		}
		return writeDownJSON(stdout, stderr, stopping, stopped, rawPlans, rawReports, code)
	}
	return printCleanReports(stdout, stderr, reports)
}
