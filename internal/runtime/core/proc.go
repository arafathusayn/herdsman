package core

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/internal/ledger"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/sys"
)

// Proc is one row of the process table.
type Proc struct {
	PID, PPID int
	// Argv is the command line split on blanks; enough to name a program.
	Argv []string
}

// Name is the program's base name.
func (p Proc) Name() string {
	if len(p.Argv) == 0 {
		return ""
	}
	return strings.TrimPrefix(filepath.Base(p.Argv[0]), "-")
}

// ProcessTable is what the provider reads of the machine's processes and the
// one way it signals one. Tests replace it.
type ProcessTable interface {
	// Identify returns a live process's start time; sys.ErrNoProcess when
	// no process has the pid.
	Identify(pid int) (time.Time, error)
	// Parent returns a process's parent pid.
	Parent(pid int) (int, error)
	// List reads every process.
	List(ctx context.Context) ([]Proc, error)
	// Signal sends sig to pid.
	Signal(pid int, sig syscall.Signal) error
}

// SystemProcs is the machine's process table.
type SystemProcs struct{}

// Identify implements ProcessTable.
func (SystemProcs) Identify(pid int) (time.Time, error) {
	p, err := sys.Identify(pid)
	return p.Start, err
}

// Parent implements ProcessTable.
func (SystemProcs) Parent(pid int) (int, error) {
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, sys.ErrNoProcess
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

// List implements ProcessTable.
func (SystemProcs) List(ctx context.Context) ([]Proc, error) {
	raw, err := ledger.ExecRunner(ctx, "ps", "-ax", "-ww", "-o", "pid=,ppid=,command=")
	if err != nil {
		return nil, err
	}
	return parseProcesses(raw)
}

func parseProcesses(raw []byte) ([]Proc, error) {
	// This command column contains no environment. The ledger's ParsePS
	// accepts ps -E output and conservatively cuts at KEY=value; using it
	// here would erase Codex flags after a -c key=value sandbox argument.
	rows, err := ledger.ParseCommands(raw)
	if err != nil {
		return nil, err
	}
	out := make([]Proc, 0, len(rows))
	for pid, command := range rows {
		words := strings.Fields(command)
		if len(words) == 0 {
			return nil, fmt.Errorf("core: process %d has no parent", pid)
		}
		parent, err := strconv.Atoi(words[0])
		if err != nil {
			return nil, fmt.Errorf("core: process %d has an invalid parent", pid)
		}
		out = append(out, Proc{PID: pid, PPID: parent, Argv: words[1:]})
	}
	return out, nil
}

// Signal implements ProcessTable.
func (SystemProcs) Signal(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }

// StartString is how a start time is kept in process evidence.
func StartString(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// Evidence reads a live process's evidence.
func Evidence(procs ProcessTable, pid int) (runtime.ProcessEvidence, error) {
	start, err := procs.Identify(pid)
	if err != nil {
		return runtime.ProcessEvidence{}, err
	}
	return runtime.ProcessEvidence{PID: pid, Start: StartString(start)}, nil
}

// Alive reports whether the process the evidence names still runs: the pid
// with the recorded start time. A reused pid is another process.
func Alive(procs ProcessTable, e runtime.ProcessEvidence) (bool, error) {
	if e.PID <= 0 || e.Start == "" {
		return false, nil
	}
	start, err := procs.Identify(e.PID)
	if errors.Is(err, sys.ErrNoProcess) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return StartString(start) == e.Start, nil
}

// descendants lists every process under pid in the table, deepest last.
func descendants(table []Proc, pid int) []Proc {
	children := map[int][]Proc{}
	for _, p := range table {
		children[p.PPID] = append(children[p.PPID], p)
	}
	var out []Proc
	queue := []int{pid}
	seen := map[int]bool{pid: true}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		for _, c := range children[next] {
			if seen[c.PID] {
				continue
			}
			seen[c.PID] = true
			out = append(out, c)
			queue = append(queue, c.PID)
		}
	}
	return out
}

// ancestorsOf walks from pid up its parents, pid first, at most 64 steps.
func ancestorsOf(procs ProcessTable, pid int) []int {
	var out []int
	for depth := 0; pid > 1 && depth < 64; depth++ {
		out = append(out, pid)
		parent, err := procs.Parent(pid)
		if err != nil || parent == pid {
			break
		}
		pid = parent
	}
	return out
}

// errEvidence is a process whose evidence does not match a record.
func errEvidence(e runtime.ProcessEvidence) error {
	return fmt.Errorf("process %d started at %s is not running", e.PID, e.Start)
}
