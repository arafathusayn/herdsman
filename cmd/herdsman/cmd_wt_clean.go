package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

const cleanHelp = `usage: herdsman clean [<task>|--merged|--route <folder>|--all] [-y] [--force]
                      [--keep-branch] [--force-irreversible <path>]... [--json]

Frees what a task holds. A verified snapshot comes first, including ignored files;
every path must then be captured, rebuildable or named, or nothing is removed.
The branch is deleted only with proof that it was integrated.

exit codes:
  0  every selected tree was freed
  1  a tree was refused, or a step failed
  2  bad usage, or no terminal to ask on
`

type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

type cleanPlan struct {
	Task         string   `json:"task"`
	Branch       string   `json:"branch"`
	NeedsForce   bool     `json:"needs_force"`
	Reason       string   `json:"reason"`
	Blocked      string   `json:"blocked"`
	Running      []string `json:"running"`
	BranchAction string   `json:"branch_action"`
	PRHead       string   `json:"pull_request_head"`
}

type cleanReport struct {
	Task           string   `json:"task"`
	Snapshot       string   `json:"snapshot"`
	Lost           []string `json:"lost"`
	Submodules     []string `json:"submodules"`
	UnmatchedNames []string `json:"unmatched_names"`
	BranchDeleted  bool     `json:"branch_deleted"`
	BranchReason   string   `json:"branch_reason"`
	Kept           []string `json:"kept"`
	Warnings       []string `json:"warnings"`
	Error          string   `json:"error"`
}

// cleanOptions are the flags clean and down share.
type cleanOptions struct {
	yes, all, merged, force, keepBranch, asJSON *bool
	route                                       *string
	named                                       *stringList
}

func addCleanFlags(fs *flag.FlagSet) *cleanOptions {
	named := &stringList{}
	fs.Var(named, "force-irreversible", "lose this path or glob, which no snapshot holds")
	return &cleanOptions{
		yes: fs.Bool("y", false, "do not ask"), all: fs.Bool("all", false, "every task"),
		merged: fs.Bool("merged", false, "every integrated task"), force: fs.Bool("force", false, "go on with unsaved work, which the snapshot holds"),
		keepBranch: fs.Bool("keep-branch", false, "keep the branch"), asJSON: fs.Bool("json", false, "print the plans and reports as JSON"),
		route: fs.String("route", "", "every task of a route folder"), named: named,
	}
}

// selector validates the choice of trees and returns the named task, if any.
func (o *cleanOptions) selector(positional []string) (string, error) {
	chosen := len(positional)
	for _, set := range []bool{*o.route != "", *o.all, *o.merged} {
		if set {
			chosen++
		}
	}
	if chosen > 1 {
		return "", fmt.Errorf("name one task, or --merged, or --route, or --all")
	}
	if len(positional) == 1 {
		return positional[0], nil
	}
	return "", nil
}

func (o *cleanOptions) bulk() bool { return *o.route != "" || *o.all || *o.merged }

func callCleanPlan(request map[string]any, stderr io.Writer) ([]cleanPlan, []json.RawMessage, error) {
	raw, err := callWorktree("CleanPlan", request)
	if err != nil {
		return nil, nil, err
	}
	var typed struct {
		Plans    []cleanPlan `json:"plans"`
		Warnings []string    `json:"warnings"`
	}
	if err := json.Unmarshal(raw, &typed); err != nil {
		return nil, nil, err
	}
	for _, warning := range typed.Warnings {
		fmt.Fprintf(stderr, "herdsman: warning: %s\n", warning)
	}
	var plain struct {
		Plans []json.RawMessage `json:"plans"`
	}
	if err := json.Unmarshal(raw, &plain); err != nil {
		return nil, nil, err
	}
	return typed.Plans, plain.Plans, nil
}

func printCleanPlans(w io.Writer, plans []cleanPlan) {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "TASK\tBRANCH\tBRANCH ACTION\tNOTE")
	for _, plan := range plans {
		note := plan.Reason
		if plan.Blocked != "" {
			note = "skipped: " + plan.Blocked
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", plan.Task, plan.Branch, plan.BranchAction, note)
	}
	table.Flush()
}

// checkPlans returns the tasks to free or the message that stops them all.
func (o *cleanOptions) checkPlans(plans []cleanPlan, allowRunning bool) ([]string, string) {
	tasks := make([]string, 0, len(plans))
	for _, plan := range plans {
		switch {
		case plan.Blocked != "" && o.bulk():
			continue
		case plan.Blocked != "":
			return nil, fmt.Sprintf("%s: %s", plan.Task, plan.Blocked)
		case len(plan.Running) > 0 && !allowRunning:
			return nil, fmt.Sprintf("%s still runs %s; run herdsman teardown or herdsman down first", plan.Task, strings.Join(plan.Running, ", "))
		case plan.NeedsForce && !*o.force:
			return nil, fmt.Sprintf("%s: %s; --force goes on, because the snapshot holds it", plan.Task, plan.Reason)
		}
		tasks = append(tasks, plan.Task)
	}
	return tasks, ""
}

// approve asks when it must. --force always asks: losing a tree with unsaved
// work in it is not something a script should do unattended.
func (o *cleanOptions) approve(stdin io.Reader, stdout, stderr io.Writer, question string) (bool, int) {
	if *o.asJSON {
		stdout = stderr // Standard output carries only the document.
	}
	if len(*o.named) > 0 {
		fmt.Fprintf(stdout, "what these match and no snapshot holds is lost for good: %s\n", strings.Join(*o.named, ", "))
	}
	if *o.yes && !*o.force {
		return true, 0
	}
	if !stdinIsTerminal(stdin) {
		if *o.force {
			return false, usageError(stderr, "--force needs a terminal to ask on")
		}
		return false, usageError(stderr, "no terminal to ask on: read this plan and pass -y")
	}
	ok, err := confirmQuestion(stdin, stdout, question)
	if err != nil {
		return false, fail(stderr, err)
	}
	if !ok {
		fmt.Fprintln(stdout, "nothing was removed")
	}
	return ok, 0
}

func printCleanReports(stdout, stderr io.Writer, reports []cleanReport) int {
	code := 0
	for _, report := range reports {
		if report.Error != "" {
			fmt.Fprintf(stderr, "herdsman: %s: %s\n", report.Task, report.Error)
			code = 1
			continue
		}
		branch := "branch kept"
		if report.BranchDeleted {
			branch = "branch deleted (" + report.BranchReason + ")"
		}
		fmt.Fprintf(stdout, "%s: freed, %s\n", report.Task, branch)
		if len(report.Submodules) > 0 {
			fmt.Fprintf(stdout, "  submodules to fetch again after a restore: %s\n", strings.Join(report.Submodules, ", "))
		}
		if len(report.Lost) > 0 {
			fmt.Fprintf(stdout, "  lost for good: %s\n", strings.Join(report.Lost, ", "))
		}
		if len(report.UnmatchedNames) > 0 {
			fmt.Fprintf(stdout, "  matched nothing: %s\n", strings.Join(report.UnmatchedNames, ", "))
		}
		if len(report.Kept) > 0 {
			fmt.Fprintf(stdout, "  kept: %s\n", strings.Join(report.Kept, ", "))
		}
		for _, warning := range report.Warnings {
			fmt.Fprintf(stderr, "herdsman: %s: warning: %s\n", report.Task, warning)
		}
		if report.Snapshot != "" {
			fmt.Fprintf(stdout, "  restore with: herdsman undo %s\n", report.Snapshot)
		}
	}
	return code
}

// callClean hands the plan's pull request heads back, so that one pass reads
// GitHub once. The map is never nil: nil would ask the service to read again.
func callClean(dir string, tasks []string, plans []cleanPlan, o *cleanOptions) ([]cleanReport, []json.RawMessage, error) {
	heads := make(map[string]string, len(plans))
	for _, plan := range plans {
		if plan.PRHead != "" {
			heads[plan.Branch] = plan.PRHead
		}
	}
	raw, err := callWorktree("Clean", map[string]any{"dir": dir, "tasks": tasks, "force": *o.force, "keep_branch": *o.keepBranch, "named": []string(*o.named), "pull_request_heads": heads})
	if err != nil {
		return nil, nil, err
	}
	var typed struct {
		Reports []cleanReport `json:"reports"`
	}
	if err := json.Unmarshal(raw, &typed); err != nil {
		return nil, nil, err
	}
	var plain struct {
		Reports []json.RawMessage `json:"reports"`
	}
	if err := json.Unmarshal(raw, &plain); err != nil {
		return nil, nil, err
	}
	return typed.Reports, plain.Reports, nil
}

func cleanPlanRequest(dir, selector string, o *cleanOptions) map[string]any {
	return map[string]any{"dir": dir, "selector": selector, "route": *o.route, "all": *o.all, "merged": *o.merged, "keep_branch": *o.keepBranch}
}

func init() {
	register(command{name: "clean", summary: "Free what a task holds, after a verified snapshot.", help: cleanHelp, run: runClean})
}

func runClean(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := verbFlags("clean", stderr)
	options := addCleanFlags(fs)
	positional, _, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	selector, err := options.selector(positional)
	if err != nil || len(positional) > 1 {
		return usageError(stderr, "name one task, or --merged, or --route, or --all")
	}
	dir, err := workingFolder()
	if err != nil {
		return fail(stderr, err)
	}
	plans, rawPlans, err := callCleanPlan(cleanPlanRequest(dir, selector, options), stderr)
	if err != nil {
		return fail(stderr, err)
	}
	if !*options.asJSON {
		printCleanPlans(stdout, plans)
	}
	tasks, stop := options.checkPlans(plans, false)
	if stop != "" {
		fmt.Fprintf(stderr, "herdsman: %s\n", stop)
		return 1
	}
	if len(tasks) == 0 {
		if *options.asJSON {
			return writeCleanJSON(stdout, stderr, rawPlans, nil, nil)
		}
		fmt.Fprintln(stdout, "nothing to clean")
		return 0
	}
	if ok, code := options.approve(stdin, stdout, stderr, "free these? [y/N] "); !ok {
		return code
	}
	reports, rawReports, err := callClean(dir, tasks, plans, options)
	if err != nil {
		return fail(stderr, err)
	}
	if *options.asJSON {
		return writeCleanJSON(stdout, stderr, rawPlans, rawReports, reports)
	}
	return printCleanReports(stdout, stderr, reports)
}

func orEmpty(list []json.RawMessage) []json.RawMessage {
	if list == nil {
		return []json.RawMessage{}
	}
	return list
}

func writeCleanJSON(stdout, stderr io.Writer, plans, rawReports []json.RawMessage, reports []cleanReport) int {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(map[string]any{"plans": orEmpty(plans), "reports": orEmpty(rawReports)}); err != nil {
		return fail(stderr, err)
	}
	for _, report := range reports {
		if report.Error != "" {
			return 1
		}
	}
	return 0
}
