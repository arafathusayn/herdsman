// Package machine reads the machine's load and free memory the way the
// skill's scripts do, without running a program: /proc on Linux, sysctl
// values through the kernel on macOS (not the sysctl program).
package machine

import (
	"fmt"
	"runtime"
)

// Load is the 1-, 5- and 15-minute load averages.
type Load [3]float64

// CPUs is the number of usable cores.
func CPUs() int { return runtime.NumCPU() }

// Reading is "<1-minute load> <free memory %>", as run-check.sh's probe
// prints it, or "" when either is unknown.
func Reading() string {
	l, err := ReadLoad()
	if err != nil {
		return ""
	}
	free, err := FreePercent()
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s %d", FormatLoad(l[0]), free)
}

// FormatLoad prints a load as /proc/loadavg and sysctl do: two decimals.
func FormatLoad(v float64) string { return fmt.Sprintf("%.2f", v) }
