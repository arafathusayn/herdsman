package main

import (
	"fmt"
	"io"
	"strings"
)

// Status is the state of one gate.
type Status string

const (
	// StatusRun means every need is met and the gate may start.
	StatusRun  Status = "run"
	StatusPass Status = "pass"
	StatusSkip Status = "skip"
	StatusFail Status = "fail"
)

// Result is the outcome of one gate.
type Result struct {
	Gate   string
	Status Status
	Reason string
}

// Passed reports whether the run may count: nothing failed and at least one
// gate really ran. A run of only skips proved nothing.
func Passed(results []Result) bool {
	ran := false
	for _, r := range results {
		switch r.Status {
		case StatusPass:
			ran = true
		case StatusSkip:
		default:
			return false
		}
	}
	return ran
}

// WriteReport prints one line per gate, by name, then the totals.
func WriteReport(w io.Writer, goos string, results []Result) {
	counts := map[Status]int{}
	for _, r := range results {
		counts[r.Status]++
		label := strings.ToUpper(string(r.Status))
		if r.Reason == "" {
			fmt.Fprintf(w, "%s %s\n", label, r.Gate)
			continue
		}
		first, rest, _ := strings.Cut(r.Reason, "\n")
		fmt.Fprintf(w, "%s %s: %s\n", label, r.Gate, first)
		if rest != "" {
			for _, line := range strings.Split(rest, "\n") {
				fmt.Fprintf(w, "    %s\n", line)
			}
		}
	}
	fmt.Fprintf(w, "%s: %d passed, %d skipped, %d failed\n", goos, counts[StatusPass], counts[StatusSkip], counts[StatusFail])
}
