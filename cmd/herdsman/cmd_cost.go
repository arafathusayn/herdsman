package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const costHelp = `usage: herdsman cost [--project|--task|--route] [--json]

Reports what harnesses measured: tokens and cost per task, or per project or
route with the flag. Only usage a harness reported is counted, so a harness
that reports nothing adds nothing here.

exit codes:
  0  reported, including when nothing has been measured yet
  1  the usage log could not be read
  2  bad usage
`

// A line of the usage log can reach this size before the read fails.
const costMaxLine = 1 << 20

func init() {
	register(command{name: "cost", summary: "Report measured tokens and cost per task, project or route.", help: costHelp, run: runCost})
}

type costRecord struct {
	Project    string `json:"project"`
	Task       string `json:"task"`
	Route      string `json:"route"`
	Tokens     int64  `json:"tokens"`
	CostMicros int64  `json:"cost_micros"`
}

type costRow struct {
	Name       string `json:"name"`
	Tokens     int64  `json:"tokens"`
	CostMicros int64  `json:"cost_micros"`
}

type costSchema struct {
	Version string `json:"version"`
}

type costReport struct {
	Schema  costSchema `json:"schema"`
	Group   string     `json:"group"`
	Rows    []costRow  `json:"rows"`
	Total   costRow    `json:"total"`
	Skipped int        `json:"skipped"`
}

func runCost(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("cost", flag.ContinueOnError)
	flags.SetOutput(stderr)
	project := flags.Bool("project", false, "group by project")
	task := flags.Bool("task", false, "group by task (the default)")
	route := flags.Bool("route", false, "group by route")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "cost: unexpected argument %q\n", flags.Arg(0))
		return 2
	}
	picked := 0
	group := "task"
	for _, choice := range []struct {
		set  bool
		name string
	}{{*project, "project"}, {*task, "task"}, {*route, "route"}} {
		if choice.set {
			picked++
			group = choice.name
		}
	}
	if picked > 1 {
		fmt.Fprintln(stderr, "cost: use one of --project, --task, --route")
		return 2
	}
	file, err := usageLogPath()
	if err != nil {
		fmt.Fprintf(stderr, "cost: %v\n", err)
		return 1
	}
	report, measured, err := readCostReport(file, group)
	if err != nil {
		fmt.Fprintf(stderr, "cost: %v\n", err)
		return 1
	}
	if report.Skipped > 0 {
		fmt.Fprintf(stderr, "cost: skipped %d unreadable lines in %s; totals leave them out\n", report.Skipped, file)
	}
	if *asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		// The report holds only strings and integers.
		_ = encoder.Encode(report)
		return 0
	}
	if !measured {
		fmt.Fprintln(stdout, "no measured usage yet")
		return 0
	}
	printCost(stdout, report)
	return 0
}

func usageLogPath() (string, error) {
	if dir := os.Getenv("HERDSMAN_STATE_DIR"); dir != "" {
		return filepath.Join(dir, "usage.jsonl"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".herdsman", "usage.jsonl"), nil
}

func readCostReport(file, group string) (costReport, bool, error) {
	report := costReport{Schema: costSchema{Version: "1.0.0"}, Group: group, Rows: []costRow{}, Total: costRow{Name: "total"}}
	handle, err := os.Open(file)
	if errors.Is(err, fs.ErrNotExist) {
		return report, false, nil
	}
	if err != nil {
		return report, false, err
	}
	defer handle.Close()

	sums := map[string]*costRow{}
	scanner := bufio.NewScanner(handle)
	scanner.Buffer(nil, costMaxLine)
	measured := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record costRecord
		if json.Unmarshal([]byte(line), &record) != nil || record.Tokens < 0 || record.CostMicros < 0 {
			report.Skipped++
			continue
		}
		measured = true
		name := map[string]string{"project": record.Project, "task": record.Task, "route": record.Route}[group]
		if name == "" {
			name = "(none)"
		}
		row := sums[name]
		if row == nil {
			row = &costRow{Name: name}
			sums[name] = row
		}
		row.Tokens += record.Tokens
		row.CostMicros += record.CostMicros
		report.Total.Tokens += record.Tokens
		report.Total.CostMicros += record.CostMicros
	}
	if err := scanner.Err(); err != nil {
		return report, false, fmt.Errorf("read %s: %w", file, err)
	}
	for _, row := range sums {
		report.Rows = append(report.Rows, *row)
	}
	slices.SortFunc(report.Rows, func(a, b costRow) int { return strings.Compare(a.Name, b.Name) })
	return report, measured, nil
}

func printCost(out io.Writer, report costReport) {
	width := len("total")
	for _, row := range report.Rows {
		width = max(width, len(row.Name))
	}
	fmt.Fprintf(out, "%-*s  %12s  %s\n", width, report.Group, "tokens", "cost")
	for _, row := range append(slices.Clone(report.Rows), report.Total) {
		fmt.Fprintf(out, "%-*s  %12d  %s\n", width, row.Name, row.Tokens, costAmount(row.CostMicros))
	}
}

func costAmount(micros int64) string {
	text := fmt.Sprintf("%d.%06d", micros/1_000_000, micros%1_000_000)
	for strings.HasSuffix(text, "0") && len(text)-strings.IndexByte(text, '.') > 3 {
		text = text[:len(text)-1]
	}
	return "$" + text
}
