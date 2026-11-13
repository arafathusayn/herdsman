package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	tracev1 "github.com/arafathusayn/herdsman/sdk/trace/v1"
)

const evalExperimentalFlag = "--experimental-eval"

// evalVersion is the experimental surface's version, printed with every run.
const evalVersion = "0.1.0"

const evalHelp = `usage: herdsman eval --experimental-eval <suite folder> [--json] [--findings <route folder>]... [--rank]
       herdsman eval --experimental-eval run <suite folder> [--local] [--json]
       herdsman eval --experimental-eval finding add <route folder> --reviewer <name> --severity <level>
                     --title <text> [--id <id>] [--task <task>] [--head <commit>] [--file <path>] [--line <n>]
       herdsman eval --experimental-eval finding outcome <route folder> <id> dismissed|accepted|fixed

Experimental: the flag is required and the format can change between minor
releases. Reports a suite's recorded runs (runs.jsonl in the suite folder) per
arm: counts, and 95% confidence intervals of pass rate, duration and cost, with
an A/A control of two groups of the same arm. Every arm needs its own control;
a suite with an arm that has none is refused. Only ranges are reported, never a single value.

run executes the suite described by suite.json in the folder: {"tasks":
[{"id","prompt","check"}], "arms":[{"name","kind","command"}], "runs",
"control_runs", "timeout_seconds"}. Every run gets a new repository and its own
route, started as up starts one with the arm's command as the agent; ordinary
runs and the two halves of each arm's A/A control are interleaved, then the
report above is printed. The agent is told to create .eval-done when finished;
a run passes when it did and the task's check exits 0. It may write
{"tokens","cost_micros"} to .eval-usage.json. The route is stopped afterwards.
With --local a command runs directly, with no route or herdr: it gets the prompt
in HERDSMAN_EVAL_PROMPT and may report usage in the file HERDSMAN_EVAL_USAGE
names, and a run passes when the command and the check exit 0.

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
		subHelp: map[string]string{"finding": evalHelp, "run": evalHelp}, run: runEval})
}

func runEval(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if !slices.Contains(args, evalExperimentalFlag) {
		fmt.Fprintf(stderr, "eval: experimental; pass %s to use it\n", evalExperimentalFlag)
		return 2
	}
	args = slices.DeleteFunc(slices.Clone(args), func(arg string) bool { return arg == evalExperimentalFlag })
	fmt.Fprintf(stderr, "herdsman: eval is experimental (eval/%s) and can change between minor releases\n", evalVersion)
	if len(args) > 0 && args[0] == "finding" {
		return runEvalFinding(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "run" {
		return runEvalSuite(args[1:], stdout, stderr)
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
	printEvalReport(stdout, report, *asJSON)
	if report.Rank != nil {
		fmt.Fprintf(stderr, "eval: ranking refused: %s\n", report.Rank.Reason)
		return 3
	}
	return 0
}

func printEvalReport(stdout io.Writer, report evalView, asJSON bool) {
	if !asJSON {
		printEval(stdout, report)
		return
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	// The report holds only strings, numbers and booleans.
	_ = encoder.Encode(report)
}

func buildEvalReport(suite string, routes []string, rank bool, stderr io.Writer) (evalView, int) {
	paths, err := absolutePaths(append([]string{suite}, routes...))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return evalView{}, 1
	}
	service, closeService, err := dialTrace()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return evalView{}, 1
	}
	defer closeService()
	res, err := service.Evaluate(context.Background(), &tracev1.EvaluateRequest{Suite: paths[0], FindingRoutes: paths[1:], Rank: rank})
	if err != nil {
		text := rpcText(err)
		if strings.Contains(text.Error(), "A/A control") {
			fmt.Fprintf(stderr, "eval: no A/A control: %v\n", text)
		} else {
			fmt.Fprintln(stderr, text)
		}
		return evalView{}, 1
	}
	for _, warning := range res.GetWarnings() {
		fmt.Fprintf(stderr, "eval: %s\n", warning)
	}
	return evalFromProto(res.GetReport(), suite), 0
}

func intervalText(interval *intervalView, format string) string {
	if interval == nil {
		return "too few runs"
	}
	return fmt.Sprintf(format+".."+format, interval.Low, interval.High)
}

func printEval(out io.Writer, report evalView) {
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
		if code := setEvalOutcome(args[1], args[2], args[3], stderr); code != 0 {
			return code
		}
		return 0
	}
	fmt.Fprintln(stderr, usageLine)
	return 2
}

func setEvalOutcome(route, id, outcome string, stderr io.Writer) int {
	paths, err := absolutePaths([]string{route})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	service, closeService, err := dialTrace()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer closeService()
	if _, err := service.SetOutcome(context.Background(), &tracev1.SetOutcomeRequest{Route: paths[0], Id: id, Outcome: outcome}); err != nil {
		fmt.Fprintln(stderr, rpcText(err))
		return 1
	}
	return 0
}

func runFindingAdd(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("eval finding add", flag.ContinueOnError)
	flags.SetOutput(stderr)
	finding := &tracev1.Finding{}
	var line int
	idFlag := flags.String("id", "", "the finding's id (default: derived from its fields)")
	reviewerFlag := flags.String("reviewer", "", "who reported it")
	severityFlag := flags.String("severity", "", "its severity, such as P1")
	titleFlag := flags.String("title", "", "one line saying what is wrong")
	taskFlag := flags.String("task", "", "the task under review")
	headFlag := flags.String("head", "", "the commit under review")
	fileFlag := flags.String("file", "", "the file it is in")
	flags.IntVar(&line, "line", 0, "the line it is on")
	positional, err := parseInterspersed(flags, args)
	if err != nil {
		return 2
	}
	if len(positional) != 1 {
		fmt.Fprintln(stderr, "usage: herdsman eval --experimental-eval finding add <route folder> --reviewer <name> --severity <level> --title <text>")
		return 2
	}
	finding.Id, finding.Reviewer, finding.Severity, finding.Title = *idFlag, *reviewerFlag, *severityFlag, *titleFlag
	finding.Task, finding.Head, finding.File, finding.Line = *taskFlag, *headFlag, *fileFlag, int32(line)
	paths, err := absolutePaths(positional)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	service, closeService, err := dialTrace()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer closeService()
	res, err := service.AddFinding(context.Background(), &tracev1.AddFindingRequest{Route: paths[0], Finding: finding})
	if err != nil {
		fmt.Fprintln(stderr, rpcText(err))
		return 1
	}
	fmt.Fprintln(stdout, res.GetId())
	return 0
}
