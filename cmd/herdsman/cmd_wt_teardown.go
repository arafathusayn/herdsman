package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/arafathusayn/herdsman/sdk/client"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

const teardownHelp = `usage: herdsman teardown [<task>|<selector>] [--route <folder>|--all] [-y]
                         [--grace <seconds>] [--include associated] [--json]

Stops what a task runs and keeps everything it holds: it prints what it would do, asks,
then signals only what the task exclusively owns, checking each one again just
before its signal. Nothing is removed.

exit codes:
  0  everything approved was stopped
  1  a plan stopped because the machine changed, or a stop failed
  2  bad usage, or no terminal to ask on and no -y
`

type teardownLine struct {
	Kind       string   `json:"kind"`
	ID         string   `json:"id"`
	Identity   string   `json:"identity"`
	Level      string   `json:"level"`
	Class      string   `json:"class"`
	Action     string   `json:"action"`
	SharedWith []string `json:"shared_with,omitempty"`
	Name       string   `json:"name,omitempty"`
}

type teardownPlan struct {
	Task  string         `json:"task"`
	Lines []teardownLine `json:"lines"`
}

type teardownOutcome struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Result string `json:"result"`
}

type teardownReport struct {
	Task     string            `json:"task"`
	Outcomes []teardownOutcome `json:"outcomes"`
	Error    string            `json:"error"`
}

// dialLedger reaches the daemon through the SDK; tests replace it.
var dialLedger = func() (daemonv1.LedgerService, func(), error) {
	sdk, err := client.Dial(client.Options{})
	if err != nil {
		return nil, nil, err
	}
	return sdk.Ledger, sdk.Close, nil
}

type teardownPlans struct {
	Plans []json.RawMessage
	Hooks bool
}

func fetchTeardownPlans(request *daemonv1.TeardownPlansRequest) (teardownPlans, error) {
	service, closeService, err := dialLedger()
	if err != nil {
		return teardownPlans{}, err
	}
	defer closeService()
	res, err := service.TeardownPlans(context.Background(), request)
	if err != nil {
		return teardownPlans{}, err
	}
	planned := teardownPlans{Hooks: res.GetHooks()}
	for _, document := range res.GetPlans() {
		planned.Plans = append(planned.Plans, json.RawMessage(document))
	}
	return planned, nil
}

func runTeardownPlans(dir string, plans []json.RawMessage, grace int, includeAssociated bool) ([]teardownReport, error) {
	service, closeService, err := dialLedger()
	if err != nil {
		return nil, err
	}
	defer closeService()
	request := &daemonv1.TeardownRequest{Dir: dir, GraceSeconds: int32(grace), IncludeAssociated: includeAssociated}
	for _, plan := range plans {
		request.Plans = append(request.Plans, []byte(plan))
	}
	res, err := service.Teardown(context.Background(), request)
	if err != nil {
		return nil, err
	}
	var reports []teardownReport
	for _, wire := range res.GetReports() {
		report := teardownReport{Task: wire.GetTask(), Error: wire.GetError()}
		for _, outcome := range wire.GetOutcomes() {
			report.Outcomes = append(report.Outcomes, teardownOutcome{Kind: outcome.GetKind(), ID: outcome.GetId(), Result: outcome.GetResult()})
		}
		reports = append(reports, report)
	}
	return reports, nil
}

func init() {
	register(command{name: "teardown", summary: "Stop what a task runs, keeping what it holds.", help: teardownHelp, run: runTeardown})
}

func printTeardownPlans(w io.Writer, plans []json.RawMessage) {
	for _, raw := range plans {
		var plan teardownPlan
		if json.Unmarshal(raw, &plan) != nil {
			continue
		}
		fmt.Fprintf(w, "task %s\n", plan.Task)
		table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(table, "KIND\tID\tEVIDENCE\tACTION\tCLASS\tSHARED WITH")
		for _, line := range plan.Lines {
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n", line.Kind, line.ID, line.Level, line.Action, line.Class, strings.Join(line.SharedWith, ","))
		}
		table.Flush()
	}
}

func hasStops(plans []json.RawMessage, includeAssociated bool) bool {
	for _, raw := range plans {
		var plan teardownPlan
		if json.Unmarshal(raw, &plan) != nil {
			continue
		}
		for _, line := range plan.Lines {
			if line.Action == "stop" || line.Action == "ask" && includeAssociated {
				return true
			}
		}
	}
	return false
}

func confirmTeardown(stdin io.Reader, stdout io.Writer, question string) (bool, error) {
	fmt.Fprint(stdout, question+" [y/N] ")
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(line), "y"), nil
}

func runTeardown(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if code, opened := openTeardownForm(&args, stdin, stdout, stderr); opened {
		return code
	}
	fs := verbFlags("teardown", stderr)
	yes := fs.Bool("y", false, "do not ask")
	route := fs.String("route", "", "every task of a route folder")
	all := fs.Bool("all", false, "every task")
	grace := fs.Int("grace", 0, "seconds between the polite and the forced stop")
	include := fs.String("include", "", `also stop what is only "associated"`)
	asJSON := fs.Bool("json", false, "print each plan and report as JSON")
	showID := fs.Bool("show-plan-id", false, "print the plan and its ID, and stop")
	approved := fs.String("approve-plan", "", "run only if the plan still has this ID")
	addFormFlags(fs)
	positional, _, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	selector := ""
	if len(positional) > 1 || len(positional) == 1 && (*route != "" || *all) || *route != "" && *all {
		return usageError(stderr, "name one task, or --route, or --all")
	}
	if len(positional) == 1 {
		selector = positional[0]
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
	planned, err := fetchTeardownPlans(&daemonv1.TeardownPlansRequest{Dir: dir, Selector: selector, Route: *route, All: *all})
	if err != nil {
		return fail(stderr, err)
	}
	if !*asJSON {
		printTeardownPlans(stdout, planned.Plans)
	}
	if *showID {
		fmt.Fprintln(stdout, planIDPrefix+teardownPlanID(planned.Plans))
		return 0
	}
	if *approved != "" && teardownPlanID(planned.Plans) != *approved {
		fmt.Fprintln(stderr, "herdsman: the plan changed after it was confirmed; nothing was stopped")
		return 1
	}
	includeAssociated := *include == "associated"
	hooksOnly := !hasStops(planned.Plans, includeAssociated)
	unapproved := !*yes && *approved == ""
	if hooksOnly && unapproved && planned.Hooks && !stdinIsTerminal(stdin) {
		fmt.Fprintln(stderr, "herdsman: nothing to stop, and the teardown hooks did not run: pass -y to run them")
	}
	if hooksOnly && (!planned.Hooks || unapproved && !stdinIsTerminal(stdin)) {
		if *asJSON {
			return writeTeardownJSON(stdout, planned.Plans, nil, stderr)
		}
		fmt.Fprintln(stdout, "nothing to stop")
		return 0
	}
	if unapproved {
		if !stdinIsTerminal(stdin) {
			return usageError(stderr, "no terminal to ask on: read this plan and pass -y")
		}
		question := "stop these?"
		if hooksOnly {
			question = "nothing to stop; run the teardown hooks?"
		}
		proceed, err := confirmTeardown(stdin, stdout, question)
		if err != nil {
			return fail(stderr, err)
		}
		if !proceed {
			fmt.Fprintln(stdout, "nothing was stopped")
			return 0
		}
	}
	reports, err := runTeardownPlans(dir, planned.Plans, *grace, includeAssociated)
	if err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		return writeTeardownJSON(stdout, planned.Plans, reports, stderr)
	}
	code := 0
	for _, report := range reports {
		for _, outcome := range report.Outcomes {
			fmt.Fprintf(stdout, "%s %s %s: %s\n", report.Task, outcome.Kind, outcome.ID, outcome.Result)
		}
		if report.Error != "" {
			fmt.Fprintf(stderr, "herdsman: %s: %s\n", report.Task, report.Error)
			code = 1
		}
	}
	if hooksOnly && code == 0 {
		fmt.Fprintln(stdout, "nothing to stop; the teardown hooks ran")
	}
	return code
}

func writeTeardownJSON(stdout io.Writer, plans []json.RawMessage, reports []teardownReport, stderr io.Writer) int {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(map[string]any{"plans": plans, "reports": reports}); err != nil {
		return fail(stderr, err)
	}
	for _, report := range reports {
		if report.Error != "" {
			return 1
		}
	}
	return 0
}
