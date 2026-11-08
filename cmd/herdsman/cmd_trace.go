package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/arafathusayn/herdsman/internal/trace"
)

const traceHelp = `usage: herdsman trace <route folder> [--json | --otlp <endpoint>] [--hook-logs <folder>]...
       herdsman trace add <route folder> --kind <kind> --name <name> [--task <task>]
                          [--start <RFC 3339>] [--duration-ms <n>] [--exit <n>]
                          [--tokens <n>] [--cost-micros <n>]

Shows one route as spans: git commands, hooks, checks, reviews and agent
turns, each with its timing and whatever exit or usage was reported. --json
prints them; --otlp posts them to an OpenTelemetry collector over HTTP (plain
http only to this machine). Each --hook-logs folder adds every hook run in it,
since a hook result does not name its route. Secrets are kept out: git
arguments are redacted and hook command text is left out.

trace add records a span (check, turn, review, hook or command) in the route.
Without --start the span ends now.

exit codes:
  0  reported or recorded, including when the route has no spans
  1  the route or a log could not be read, or the export failed
  2  bad usage
`

func init() {
	register(command{name: "trace", summary: "Show or export one route's spans as OpenTelemetry.", help: traceHelp,
		subHelp: map[string]string{"add": traceHelp}, run: runTrace})
}

// parseInterspersed parses flags that may come after positional arguments and
// returns the positional arguments.
func parseInterspersed(flags *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := flags.Parse(args); err != nil {
			return nil, err
		}
		if flags.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, flags.Arg(0))
		args = flags.Args()[1:]
	}
}

func runTrace(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "add" {
		return runTraceAdd(args[1:], stderr)
	}
	flags := flag.NewFlagSet("trace", flag.ContinueOnError)
	flags.SetOutput(stderr)
	asJSON := flags.Bool("json", false, "print JSON")
	endpoint := flags.String("otlp", "", "post to this OpenTelemetry collector")
	var hookLogs multi
	flags.Var(&hookLogs, "hook-logs", "another folder of hook results (repeatable)")
	positional, err := parseInterspersed(flags, args)
	if err != nil {
		return 2
	}
	if len(positional) != 1 || (*asJSON && *endpoint != "") {
		fmt.Fprint(stderr, "usage: herdsman trace <route folder> [--json | --otlp <endpoint>] [--hook-logs <folder>]...\n")
		return 2
	}
	usage, err := usageLogPath()
	if err != nil {
		fmt.Fprintf(stderr, "trace: %v\n", err)
		return 1
	}
	route, err := trace.Collect(trace.Sources{Route: positional[0], HookLogs: hookLogs, UsageFile: usage})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if route.Skipped > 0 {
		fmt.Fprintf(stderr, "trace: skipped %d unreadable records; the trace leaves them out\n", route.Skipped)
	}
	switch {
	case *endpoint != "":
		if err := trace.ExportOTLP(context.Background(), *endpoint, route); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "exported %d spans\n", len(route.Spans))
	case *asJSON:
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		// The trace holds only strings, numbers and times.
		_ = encoder.Encode(route)
	default:
		printTrace(stdout, route)
	}
	return 0
}

func printTrace(out io.Writer, route trace.Trace) {
	if len(route.Spans) == 0 {
		fmt.Fprintln(out, "no spans recorded")
		return
	}
	for _, span := range route.Spans {
		exit := "-"
		if span.Exit != nil {
			exit = fmt.Sprint(*span.Exit)
		}
		fmt.Fprintf(out, "%s  %-7s  %12.1f ms  exit %-4s  %s\n", span.Start.UTC().Format(time.RFC3339), span.Kind, span.DurationMS, exit, span.Name)
	}
}

func runTraceAdd(args []string, stderr io.Writer) int {
	const usageLine = "usage: herdsman trace add <route folder> --kind <kind> --name <name> [flags]"
	flags := flag.NewFlagSet("trace add", flag.ContinueOnError)
	flags.SetOutput(stderr)
	span := trace.Span{}
	flags.StringVar(&span.Kind, "kind", "", "check, turn, review, hook or command")
	flags.StringVar(&span.Name, "name", "", "the span's name")
	flags.StringVar(&span.Task, "task", "", "the task it belongs to")
	flags.Float64Var(&span.DurationMS, "duration-ms", 0, "how long it took")
	start := flags.String("start", "", "when it began, RFC 3339 (default: duration before now)")
	exit := flags.Int("exit", 0, "its exit code")
	tokens := flags.Int64("tokens", 0, "tokens the harness reported")
	cost := flags.Int64("cost-micros", 0, "cost the harness reported, in millionths of a dollar")
	positional, err := parseInterspersed(flags, args)
	if err != nil {
		return 2
	}
	if len(positional) != 1 {
		fmt.Fprintln(stderr, usageLine)
		return 2
	}
	span.Start = time.Now().UTC().Add(-time.Duration(span.DurationMS * float64(time.Millisecond)))
	if *start != "" {
		if span.Start, err = time.Parse(time.RFC3339Nano, *start); err != nil {
			fmt.Fprintf(stderr, "trace add: --start: %v\n", err)
			return 2
		}
	}
	// A flag left out stays unset: nothing reported differs from a reported zero.
	flags.Visit(func(set *flag.Flag) {
		switch set.Name {
		case "exit":
			span.Exit = exit
		case "tokens":
			span.Tokens = tokens
		case "cost-micros":
			span.CostMicros = cost
		}
	})
	if err := trace.Append(positional[0], span); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
