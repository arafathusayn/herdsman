package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/arafathusayn/herdsman/internal/trace"
)

const evalExperimentalFlag = "--experimental-eval"

const evalHelp = `usage: herdsman eval --experimental-eval <suite folder> [--json] [--findings <route folder>]... [--rank]
       herdsman eval --experimental-eval finding add <route folder> --reviewer <name> --severity <level>
                     --title <text> [--id <id>] [--task <task>] [--head <commit>] [--file <path>] [--line <n>]
       herdsman eval --experimental-eval finding outcome <route folder> <id> dismissed|accepted|fixed

Experimental: the flag is required and the format can change between minor
releases. Reports a suite's recorded runs (runs.jsonl in the suite folder) per
arm: counts, and 95% confidence intervals of pass rate, duration and cost, with
an A/A control of two groups of the same arm. A suite without a control is
refused. Only ranges are reported, never a single value.

Each --findings route adds its reviewer findings, counted by outcome. --rank
never ranks: it reports why not, and exits 3.

exit codes:
  0  reported or recorded
  1  a file could not be read or written, or the suite has no A/A control
  2  bad usage, or the experimental flag is missing
  3  a ranking was asked for and refused
`

func init() {
	register(command{name: "eval", summary: "Report a suite's runs with intervals and an A/A control (experimental).", help: evalHelp,
		subHelp: map[string]string{"finding": evalHelp}, run: runEval})
}

func runEval(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if !slices.Contains(args, evalExperimentalFlag) {
		fmt.Fprintf(stderr, "eval: experimental; pass %s to use it\n", evalExperimentalFlag)
		return 2
	}
	args = slices.DeleteFunc(slices.Clone(args), func(arg string) bool { return arg == evalExperimentalFlag })
	fmt.Fprintf(stderr, "herdsman: eval is experimental (eval/%s) and can change between minor releases\n", trace.EvalVersion)
	if len(args) > 0 && args[0] == "finding" {
		return runEvalFinding(args[1:], stdout, stderr)
	}
	return runEvalReport(args, stdout, stderr)
}

func runEvalReport(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("eval", flag.ContinueOnError)
	flags.SetOutput(stderr)
	asJSON := flags.Bool("json", false, "print JSON")
	rank := flags.Bool("rank", false, "ask for a ranking (always refused for now)")
	var routes multi
	flags.Var(&routes, "findings", "a route folder whose findings to count (repeatable)")
	positional, err := parseInterspersed(flags, args)
	if err != nil {
		return 2
	}
	if len(positional) != 1 {
		fmt.Fprintln(stderr, "usage: herdsman eval --experimental-eval <suite folder> [--json] [--findings <route folder>]... [--rank]")
		return 2
	}
	report, code := buildEvalReport(positional[0], routes, *rank, stderr)
	if code != 0 {
		return code
	}
	if *asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		// The report holds only strings, numbers and booleans.
		_ = encoder.Encode(report)
	} else {
		printEval(stdout, report)
	}
	if report.Rank != nil {
		fmt.Fprintf(stderr, "eval: ranking refused: %s\n", report.Rank.Reason)
		return 3
	}
	return 0
}

func buildEvalReport(suite string, routes []string, rank bool, stderr io.Writer) (trace.Report, int) {
	runs, skipped, err := trace.LoadRuns(suite)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return trace.Report{}, 1
	}
	report, err := trace.Evaluate(suite, runs, skipped)
	if err != nil {
		fmt.Fprintf(stderr, "eval: no A/A control: %v\n", err)
		return trace.Report{}, 1
	}
	var found []trace.Finding
	for _, route := range routes {
		some, unreadable, err := trace.LoadFindings(route)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return trace.Report{}, 1
		}
		if unreadable > 0 {
			fmt.Fprintf(stderr, "eval: skipped %d unreadable finding lines in %s\n", unreadable, route)
		}
		found = append(found, some...)
	}
	counts := trace.CountFindings(found)
	if len(routes) > 0 {
		report.Findings = &counts
	}
	if rank {
		decision := trace.DecideRank(counts)
		report.Rank = &decision
	}
	if report.Skipped > 0 {
		fmt.Fprintf(stderr, "eval: skipped %d unreadable run lines\n", report.Skipped)
	}
	return report, 0
}

func intervalText(interval *trace.Interval, format string) string {
	if interval == nil {
		return "too few runs"
	}
	return fmt.Sprintf(format+".."+format, interval.Low, interval.High)
}

func printEval(out io.Writer, report trace.Report) {
	fmt.Fprintf(out, "suite %s (95%% confidence intervals)\n", report.Suite)
	for _, arm := range report.Arms {
		fmt.Fprintf(out, "%s: %d runs, %d passed, pass rate %s, duration ms %s, cost micros %s (%d runs reported cost)\n",
			arm.Arm, arm.Runs, arm.Passed, intervalText(arm.PassRate, "%.3f"), intervalText(arm.DurationMS, "%.1f"), intervalText(arm.CostMicros, "%.1f"), arm.CostRuns)
	}
	for _, control := range report.Controls {
		fmt.Fprintf(out, "A/A %s: %d and %d runs, pass rate difference %s, duration ms difference %s, cost micros difference %s\n",
			control.Arm, control.RunsA, control.RunsB, intervalText(control.PassRateDifference, "%.3f"), intervalText(control.DurationMSDifference, "%.1f"), intervalText(control.CostMicrosDifference, "%.1f"))
	}
	if report.Findings != nil {
		fmt.Fprintf(out, "findings: %d recorded, %d with an outcome, %d open\n", report.Findings.Recorded, report.Findings.Labeled, report.Findings.Open)
		for _, row := range report.Findings.Reviewers {
			fmt.Fprintf(out, "  %s: recorded %d, open %d, accepted %d, fixed %d, dismissed %d\n", row.Reviewer, row.Recorded, row.Open, row.Accepted, row.Fixed, row.Dismissed)
		}
	}
}

func runEvalFinding(args []string, stdout, stderr io.Writer) int {
	const usageLine = "usage: herdsman eval --experimental-eval finding add|outcome <route folder> ..."
	if len(args) == 0 {
		fmt.Fprintln(stderr, usageLine)
		return 2
	}
	switch args[0] {
	case "add":
		return runFindingAdd(args[1:], stdout, stderr)
	case "outcome":
		if len(args) != 4 {
			fmt.Fprintln(stderr, usageLine)
			return 2
		}
		if err := trace.SetOutcome(args[1], args[2], args[3], time.Now().UTC()); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	fmt.Fprintln(stderr, usageLine)
	return 2
}

func runFindingAdd(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("eval finding add", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var finding trace.Finding
	flags.StringVar(&finding.ID, "id", "", "the finding's id (default: derived from its fields)")
	flags.StringVar(&finding.Reviewer, "reviewer", "", "who reported it")
	flags.StringVar(&finding.Severity, "severity", "", "its severity, such as P1")
	flags.StringVar(&finding.Title, "title", "", "one line saying what is wrong")
	flags.StringVar(&finding.Task, "task", "", "the task under review")
	flags.StringVar(&finding.Head, "head", "", "the commit under review")
	flags.StringVar(&finding.File, "file", "", "the file it is in")
	flags.IntVar(&finding.Line, "line", 0, "the line it is on")
	positional, err := parseInterspersed(flags, args)
	if err != nil {
		return 2
	}
	if len(positional) != 1 {
		fmt.Fprintln(stderr, "usage: herdsman eval --experimental-eval finding add <route folder> --reviewer <name> --severity <level> --title <text>")
		return 2
	}
	id, err := trace.AddFinding(positional[0], finding, time.Now().UTC())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, id)
	return 0
}
