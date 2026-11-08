package sys

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// ErrNoProcess reports that no process has the given pid.
var ErrNoProcess error = syscall.ESRCH

// Process identifies one process over time: a pid alone can be reused once the
// process ends, a pid with its start time cannot.
type Process struct {
	PID   int
	Start time.Time
}

// Identify reads the identity of the running process with this pid. A zombie
// still has one.
func Identify(pid int) (Process, error) {
	if pid <= 0 {
		return Process{}, ErrNoProcess
	}
	start, err := processStart(pid)
	if err != nil {
		return Process{}, err
	}
	return Process{PID: pid, Start: start}, nil
}

// Same reports whether two identities name one process. On Linux the start time
// is derived from the boot time, which moves when the clock is stepped, so a
// difference means "not proven to be the same process", the safe reading for a
// caller about to act on it.
func (p Process) Same(other Process) bool {
	return p.PID == other.PID && p.Start.Equal(other.Start)
}

// mapGone turns any of the gone errors into ErrNoProcess.
func mapGone(err error, gone ...error) error {
	for _, g := range gone {
		if errors.Is(err, g) {
			return ErrNoProcess
		}
	}
	return err
}

// userHZ is the unit of the start time in /proc/<pid>/stat on the Linux
// architectures this program is built for.
const userHZ = 100

// startFromProc reads a process's start time from a /proc tree: field 22 of
// <pid>/stat counts clock ticks since boot, and the btime line of stat is the
// boot time in seconds.
func startFromProc(root string, pid int) (time.Time, error) {
	stat, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "stat"))
	if err != nil {
		return time.Time{}, mapGone(err, os.ErrNotExist, syscall.ESRCH)
	}
	ticks, err := parseStartTicks(stat)
	if err != nil {
		return time.Time{}, err
	}
	system, err := os.ReadFile(filepath.Join(root, "stat"))
	if err != nil {
		return time.Time{}, err
	}
	boot, err := parseBootTime(system)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(boot+ticks/userHZ, (ticks%userHZ)*int64(time.Second/userHZ)), nil
}

// parseStartTicks finds the start time in a /proc/<pid>/stat line. The command
// name may hold spaces and parentheses, so fields count from the last ')'.
func parseStartTicks(stat []byte) (int64, error) {
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 {
		return 0, errors.New("process stat has no command name")
	}
	fields := bytes.Fields(stat[end+1:])
	const startField = 19 // field 22 of the line, the state being field 3
	if len(fields) <= startField {
		return 0, fmt.Errorf("process stat has %d fields after the command name", len(fields))
	}
	return strconv.ParseInt(string(fields[startField]), 10, 64)
}

// parseBootTime finds the btime line of /proc/stat.
func parseBootTime(system []byte) (int64, error) {
	for line := range bytes.Lines(system) {
		if value, ok := bytes.CutPrefix(line, []byte("btime ")); ok {
			return strconv.ParseInt(string(bytes.TrimSpace(value)), 10, 64)
		}
	}
	return 0, errors.New("system stat has no boot time")
}
