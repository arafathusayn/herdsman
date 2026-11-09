package ledger

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Runner runs a command and returns its standard output. A failed command
// returns an *exec.ExitError carrying its standard error.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner runs the command with the C locale, so that ps prints start times
// in the one form the parser reads.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	return cmd.Output()
}

// Reader collects the live half of a Snapshot from the machine.
type Reader struct {
	Run      Runner
	Location *time.Location
	GOOS     string
	// Self is this process. It and its ancestors are never signaled, because a
	// task's teardown may be run from inside the task's own pane. Zero is none.
	Self int
	// Engine is the container tool; empty picks docker, then podman.
	Engine string
}

// NewReader reads this machine.
func NewReader() *Reader {
	return &Reader{Run: ExecRunner, Location: time.Local, GOOS: runtime.GOOS, Self: os.Getpid()}
}

const psFormat = "pid=,ppid=,pgid=,lstart=,command="

// environmentArgs is the ps call that joins each row's environment to it.
func (r *Reader) environmentArgs() []string {
	if r.GOOS == "darwin" {
		return []string{"-E", "-ax", "-ww", "-o", psFormat}
	}
	return []string{"axeww", "-o", psFormat}
}

// parseStates reads `ps -ax -o pid=,stat=` into the pids still running, which
// leaves out zombies: a zombie has exited and shows no argv to prove.
func parseStates(raw []byte) (map[int]bool, error) {
	if err := checkSize(raw); err != nil {
		return nil, err
	}
	running := make(map[int]bool)
	for _, line := range outputLines(raw) {
		field, rest := nextField(line)
		pid, err := strconv.Atoi(field)
		if err != nil {
			return nil, fmt.Errorf("ledger: ps state line %q: %w", line, err)
		}
		running[pid] = !strings.HasPrefix(strings.TrimSpace(rest), "Z")
	}
	return running, nil
}

// readProcesses reads argv first, the environment second and the state last,
// so that a process leaving between two reads can only drop out: a row the
// later read lacks loses nothing, while the opposite order would show a live
// process without its proof, and end a teardown for no reason.
func (r *Reader) readProcesses(ctx context.Context) ([]Process, map[int]string, error) {
	raw, err := r.Run(ctx, "ps", "-ax", "-ww", "-o", "pid=,command=")
	if err != nil {
		return nil, nil, fmt.Errorf("ledger: ps commands: %w", err)
	}
	commands, err := ParseCommands(raw)
	if err != nil {
		return nil, nil, err
	}
	raw, err = r.Run(ctx, "ps", r.environmentArgs()...)
	if err != nil {
		return nil, nil, fmt.Errorf("ledger: ps environment: %w", err)
	}
	rows, err := ParsePS(raw, r.Location)
	if err != nil {
		return nil, nil, err
	}
	raw, err = r.Run(ctx, "ps", "-ax", "-o", "pid=,stat=")
	if err != nil {
		return nil, nil, fmt.Errorf("ledger: ps states: %w", err)
	}
	running, err := parseStates(raw)
	if err != nil {
		return nil, nil, err
	}
	if len(rows) == 0 {
		return nil, nil, errors.New("ledger: ps listed no processes")
	}
	processes := make([]Process, 0, len(rows))
	for _, row := range rows {
		if running[row.Pid] {
			processes = append(processes, row)
		}
	}
	for pid := range commands {
		if !running[pid] {
			delete(commands, pid)
		}
	}
	return processes, commands, nil
}

// lsof treats exit status 1 with nothing on standard error as an empty result,
// which is how lsof reports no match. Any other failure is a failed read: a
// plan built without these rows could call a shared server exclusive.
func (r *Reader) lsof(ctx context.Context, args ...string) ([]byte, error) {
	out, err := r.Run(ctx, "lsof", append([]string{"-w"}, args...)...)
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1 && len(exit.Stderr) == 0) {
		return nil, fmt.Errorf("ledger: lsof %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// cwdArgs reads every process's working folder. This read walks every process
// and is the slowest of a scan. On macOS -b and -l drop lookups it does not
// need; on Linux -b would lose the folder, which comes from a readlink.
func (r *Reader) cwdArgs() []string {
	if r.GOOS == "darwin" {
		return []string{"-b", "-l", "-d", "cwd", "-F", "pn"}
	}
	return []string{"-d", "cwd", "-F", "pn"}
}

// readSockets runs its three reads together: nothing depends on their order.
func (r *Reader) readSockets(ctx context.Context, into *Snapshot) error {
	var group sync.WaitGroup
	var errs [3]error
	group.Go(func() {
		raw, err := r.lsof(ctx, r.cwdArgs()...)
		if err != nil {
			errs[0] = err
			return
		}
		if into.Cwds, errs[0] = ParseLsofCwd(raw); errs[0] == nil && len(into.Cwds) == 0 {
			errs[0] = errors.New("ledger: lsof listed no working folders")
		}
	})
	group.Go(func() {
		raw, err := r.lsof(ctx, "-iTCP", "-sTCP:LISTEN", "-P", "-n", "-F", "pn")
		if err != nil {
			errs[1] = err
			return
		}
		into.Listeners, errs[1] = ParseLsofListeners(raw)
	})
	group.Go(func() {
		raw, err := r.lsof(ctx, "-iTCP", "-sTCP:ESTABLISHED", "-P", "-n", "-F", "pn")
		if err != nil {
			errs[2] = err
			return
		}
		into.Connections, errs[2] = ParseLsofConnections(raw)
	})
	group.Wait()
	return errors.Join(errs[:]...)
}

// ancestors marks this process and every process above it as the pane's.
func (r *Reader) ancestors(processes []Process) []Excluded {
	byPid := make(map[int]Process, len(processes))
	for _, process := range processes {
		byPid[process.Pid] = process
	}
	var excluded []Excluded
	for pid := r.Self; pid > 1; {
		process, ok := byPid[pid]
		if !ok {
			break
		}
		excluded = append(excluded, Excluded{Identity: ProcessIdentity(pid, process.Start), Role: RolePaneShell})
		pid = process.Parent
	}
	return excluded
}

// Read fills base with what the machine shows now. Processes are read before
// sockets, so that a pid leaving mid-read loses its socket rows and never
// appears with an open socket and no process.
func (r *Reader) Read(ctx context.Context, base Snapshot) (Snapshot, error) {
	processes, commands, err := r.readProcesses(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	base.Processes, base.Commands = processes, commands
	base.Excluded = append(append([]Excluded(nil), base.Excluded...), r.ancestors(processes)...)
	if err := r.readSockets(ctx, &base); err != nil {
		return Snapshot{}, err
	}
	if err := r.readContainers(ctx, &base); err != nil {
		return Snapshot{}, err
	}
	return base, nil
}

// Survey fills base as Read does, with the process, socket and container reads
// running together, for building a plan a person will look at. It gives up the
// order Read keeps, so a stop must revalidate with Read. A row of a process
// that left between the reads is dropped, which is what that order guarantees.
func (r *Reader) Survey(ctx context.Context, base Snapshot) (Snapshot, error) {
	var group sync.WaitGroup
	var processes []Process
	var commands map[int]string
	var errs [3]error
	group.Go(func() { processes, commands, errs[0] = r.readProcesses(ctx) })
	group.Go(func() { errs[1] = r.readSockets(ctx, &base) })
	group.Go(func() { errs[2] = r.readContainers(ctx, &base) })
	group.Wait()
	if err := errors.Join(errs[:]...); err != nil {
		return Snapshot{}, err
	}
	base.Processes, base.Commands = processes, commands
	base.Excluded = append(append([]Excluded(nil), base.Excluded...), r.ancestors(processes)...)
	listed := make(map[int]bool, len(processes))
	for _, process := range processes {
		listed[process.Pid] = true
	}
	base.Cwds = slices.DeleteFunc(base.Cwds, func(entry CwdEntry) bool { return !listed[entry.Pid] })
	base.Listeners = slices.DeleteFunc(base.Listeners, func(entry Listener) bool { return !listed[entry.Pid] })
	base.Connections = slices.DeleteFunc(base.Connections, func(entry Connection) bool { return !listed[entry.Pid] })
	return base, nil
}

// Facts reads what a record of a live process must hold: its start time and
// its whole argv from the read with no environment.
func (r *Reader) Facts(ctx context.Context, pid int) (time.Time, string, error) {
	processes, commands, err := r.readProcesses(ctx)
	if err != nil {
		return time.Time{}, "", err
	}
	for _, process := range processes {
		if process.Pid == pid {
			if command := commands[pid]; command != "" {
				return process.Start, command, nil
			}
		}
	}
	return time.Time{}, "", fmt.Errorf("ledger: process %d is not running", pid)
}
