// Package machine reads the machine's load and free memory the way the
// skill's scripts do, without running a program: /proc on Linux, sysctl
// values through the kernel on macOS (not the sysctl program).
package machine

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
)

// Load is the 1-, 5- and 15-minute load averages.
type Load [3]float64

// CPUs is the number of usable cores.
func CPUs() int { return runtime.NumCPU() }

// Reading is "<1-minute load> <free memory %>", as run-check.sh's probe
// prints it, or "" when either is unknown.
// The readers of Reading; tests replace them.
var (
	readLoad    = ReadLoad
	freePercent = FreePercent
)

func Reading() string {
	l, err := readLoad()
	if err != nil {
		return ""
	}
	free, err := freePercent()
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s %d", FormatLoad(l[0]), free)
}

// FormatLoad prints a load as /proc/loadavg and sysctl do: two decimals.
func FormatLoad(v float64) string { return fmt.Sprintf("%.2f", v) }

// Denied reports whether err is a privilege the environment refused
// (EPERM or EACCES), as a sandbox refuses sysctl or setpriority; tests
// skip on it, by the error itself (A25).
func Denied(err error) bool {
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES)
}

// ProbeError is the first error of the machine's probes, nil when both
// read.
func ProbeError() error {
	if _, err := readLoad(); err != nil {
		return err
	}
	_, err := freePercent()
	return err
}
