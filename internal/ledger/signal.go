package ledger

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"syscall"
	"time"
)

// DefaultGrace is how long a process has between SIGTERM and SIGKILL.
const DefaultGrace = 10 * time.Second

const defaultPoll = 100 * time.Millisecond

// Kill sends a signal; a negative pid names a process group.
type Kill func(pid int, signal syscall.Signal) error

// Outcome says what happened to one target.
type Outcome struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Level  Level  `json:"level"`
	Result string `json:"result"`
}

// The results of an Outcome.
const (
	ResultStopped = "stopped"
	ResultGone    = "gone"
	ResultForced  = "forced"
	ResultFailed  = "failed"
)

// exitStatus is a failure's errno or the tool's exit code, 0 for none and -1
// for a failure that carries neither.
func exitStatus(failure error) int {
	if failure == nil {
		return 0
	}
	var errno syscall.Errno
	if errors.As(failure, &errno) {
		return int(errno)
	}
	var exit *exec.ExitError
	if errors.As(failure, &exit) {
		return exit.ExitCode()
	}
	return -1
}

// alreadyGone reports a signal that found its process had left.
func alreadyGone(failure error) bool {
	return errors.Is(failure, syscall.ESRCH)
}

// Execution is one teardown of an approved plan.
type Execution struct {
	Plan Plan
	Read Read
	Kill Kill
	Run  Runner
	// Engine is the container tool; only needed to stop containers.
	Engine string
	// Compose is the task's compose project, stopped as one when wholly its own.
	Compose string
	// IncludeAssociated also stops the associated lines a person accepted.
	IncludeAssociated bool
	Grace             time.Duration
	Poll              time.Duration
	Log               *Log
}

func (e *Execution) wait(ctx context.Context, delay time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

// Execute stops what was approved and nothing else. It returns every outcome so
// far with the error that ended it, and a *StopError when the machine no
// longer matches what was approved. Nothing is ever removed.
func (e *Execution) Execute(ctx context.Context) ([]Outcome, error) {
	if e.Grace == 0 {
		e.Grace = DefaultGrace
	}
	if e.Poll == 0 {
		e.Poll = defaultPoll
	}
	e.Log.plan(e.Plan)
	outcomes, err := e.stopProcesses(ctx)
	if err == nil {
		var stopped []Outcome
		stopped, err = e.stopContainers(ctx)
		outcomes = append(outcomes, stopped...)
	}
	e.Log.finish(outcomes, err)
	return outcomes, err
}

// leavesFirst orders targets so that a child is signaled before its parent.
func leavesFirst(targets []Line) []Line {
	parents := make(map[int]int, len(targets))
	for _, target := range targets {
		parents[pidOf(target)] = target.Parent
	}
	depth := func(line Line) int {
		count := 0
		for pid := line.Parent; count < len(targets); pid = parents[pid] {
			if _, listed := parents[pid]; !listed {
				break
			}
			count++
		}
		return count
	}
	ordered := slices.Clone(targets)
	slices.SortStableFunc(ordered, func(a, b Line) int {
		return cmp.Or(cmp.Compare(depth(b), depth(a)), cmp.Compare(pidOf(a), pidOf(b)))
	})
	return ordered
}

// pidOf reads a process line's id, which the detectors write from an int.
func pidOf(line Line) int {
	pid, _ := strconv.Atoi(line.ID)
	return pid
}

func (e *Execution) processTargets() []Line {
	var targets []Line
	for _, line := range e.Plan.Lines {
		if line.Kind == KindProcess && line.Action == ActionStop {
			targets = append(targets, line)
		}
	}
	return leavesFirst(targets)
}

// groupOwned reports a group every member of which was approved for a stop and
// still is. A member with no line, as a person's shell has, keeps the group
// from being signaled as one.
func groupOwned(check *revalidation, approved map[lineKey]Line, group int) bool {
	if group <= 1 {
		return false
	}
	members := 0
	for _, process := range check.snapshot.Processes {
		if process.Group != group {
			continue
		}
		members++
		key := lineKey{KindProcess, strconv.Itoa(process.Pid)}
		was, wasListed := approved[key]
		now, listed := check.lines[key]
		if !wasListed || !listed || was.Action != ActionStop || now.Action != ActionStop || now.Level != LevelExclusive {
			return false
		}
	}
	return members > 0
}

// send revalidates and signals one target; delivered lists every target the
// signal reached. A target already gone is no failure.
func (e *Execution) send(ctx context.Context, target Line, signal syscall.Signal, approved map[lineKey]Line) ([]int, error) {
	check, err := revalidate(ctx, e.Read, e.Plan)
	if err != nil {
		e.Log.refused(target, err)
		return nil, err
	}
	if !check.processAlive(target) {
		return nil, nil
	}
	if err := check.stillAuthorized(target); err != nil {
		e.Log.refused(target, err)
		return nil, err
	}
	reached := []int{pidOf(target)}
	destination := pidOf(target)
	if groupOwned(check, approved, target.Group) {
		destination = -target.Group
		for _, process := range check.snapshot.Processes {
			if process.Group == target.Group {
				reached = append(reached, process.Pid)
			}
		}
		slices.Sort(reached)
		reached = slices.Compact(reached)
	}
	err = e.Kill(destination, signal)
	e.Log.action(target, destination, int(signal), err)
	if err != nil && !alreadyGone(err) {
		return nil, fmt.Errorf("ledger: signal %d to %d: %w", signal, destination, err)
	}
	return reached, nil
}

func (e *Execution) stopProcesses(ctx context.Context) ([]Outcome, error) {
	targets := e.processTargets()
	approved := indexLines(e.Plan)
	signaled := make(map[int]bool)
	var outcomes []Outcome
	result := func(target Line, text string) {
		outcomes = append(outcomes, Outcome{Kind: target.Kind, ID: target.ID, Level: target.Level, Result: text})
	}
	for _, target := range targets {
		if signaled[pidOf(target)] {
			continue
		}
		reached, err := e.send(ctx, target, syscall.SIGTERM, approved)
		if err != nil {
			return outcomes, err
		}
		if reached == nil {
			result(target, ResultGone)
		}
		for _, pid := range reached {
			signaled[pid] = true
		}
	}
	if err := e.awaitExit(ctx, targets, signaled); err != nil {
		return outcomes, err
	}
	for _, target := range targets {
		if !signaled[pidOf(target)] {
			continue
		}
		if e.Kill(pidOf(target), 0) != nil {
			result(target, ResultStopped)
			continue
		}
		reached, err := e.send(ctx, target, syscall.SIGKILL, approved)
		if err != nil {
			return outcomes, err
		}
		if reached == nil {
			result(target, ResultStopped)
		} else {
			result(target, ResultForced)
		}
	}
	return outcomes, nil
}

// awaitExit waits for the signaled targets, up to the grace period, using the
// cheap existence check; each SIGKILL revalidates in full on its own.
func (e *Execution) awaitExit(ctx context.Context, targets []Line, signaled map[int]bool) error {
	deadline := time.Now().Add(e.Grace)
	for {
		alive := false
		for _, target := range targets {
			if signaled[pidOf(target)] && e.Kill(pidOf(target), 0) == nil {
				alive = true
				break
			}
		}
		if !alive || !time.Now().Before(deadline) {
			return nil
		}
		if err := e.wait(ctx, e.Poll); err != nil {
			return err
		}
	}
}

func (e *Execution) stopsContainer(line Line) bool {
	return line.Action == ActionStop || line.Action == ActionAsk && e.IncludeAssociated
}

func (e *Execution) containerTargets() []Line {
	var targets []Line
	for _, line := range e.Plan.Lines {
		if line.Kind == KindContainer && e.stopsContainer(line) {
			targets = append(targets, line)
		}
	}
	return targets
}

// composeOwned reports a project all of whose live containers are targets, so
// that stopping it by name stops nothing else.
func (e *Execution) composeOwned(check *revalidation, targets []Line) bool {
	if e.Compose == "" {
		return false
	}
	members := 0
	live := []string{"running", "paused", "restarting"}
	for _, container := range check.snapshot.Containers {
		if container.Labels[LabelComposeProject] != e.Compose || !container.LabelsExact || !slices.Contains(live, container.State) {
			continue
		}
		line, ok := check.lines[lineKey{KindContainer, container.ID}]
		if !ok || !e.stopsContainer(line) || !slices.ContainsFunc(targets, func(t Line) bool { return t.ID == container.ID }) {
			return false
		}
		members++
	}
	return members > 0
}

// inProject reports a target the compose project's stop reached.
func (e *Execution) inProject(check *revalidation, target Line) bool {
	return slices.ContainsFunc(check.snapshot.Containers, func(c Container) bool {
		return c.ID == target.ID && c.LabelsExact && c.Labels[LabelComposeProject] == e.Compose
	})
}

// engineStop stops containers through the tool. The verbs are stop only:
// neither down nor rm is ever run, so volumes and networks stay.
func (e *Execution) engineStop(ctx context.Context, args ...string) error {
	if _, err := e.Run(ctx, e.Engine, args...); err != nil {
		return fmt.Errorf("ledger: %s %v: %w", e.Engine, args, err)
	}
	return nil
}

func (e *Execution) stopContainers(ctx context.Context) ([]Outcome, error) {
	targets := e.containerTargets()
	if len(targets) == 0 {
		return nil, nil
	}
	check, err := revalidate(ctx, e.Read, e.Plan)
	if err != nil {
		for _, target := range targets {
			e.Log.refused(target, err)
		}
		return nil, err
	}
	var outcomes []Outcome
	if e.composeOwned(check, targets) {
		var members, others []Line
		for _, target := range targets {
			if e.inProject(check, target) {
				members = append(members, target)
			} else {
				others = append(others, target)
			}
		}
		err := e.engineStop(ctx, "compose", "-p", e.Compose, "stop")
		for _, target := range members {
			e.Log.action(target, 0, 0, err)
		}
		if err != nil {
			return nil, err
		}
		for _, target := range members {
			outcomes = append(outcomes, Outcome{Kind: target.Kind, ID: target.ID, Level: target.Level, Result: ResultStopped})
		}
		targets = others
	}
	for _, target := range targets {
		if check, err = revalidate(ctx, e.Read, e.Plan); err != nil {
			e.Log.refused(target, err)
			return outcomes, err
		}
		if check.lines[lineKey{target.Kind, target.ID}].Action != target.Action {
			outcomes = append(outcomes, Outcome{Kind: target.Kind, ID: target.ID, Level: target.Level, Result: ResultGone})
			continue
		}
		err := e.engineStop(ctx, "stop", target.ID)
		e.Log.action(target, 0, 0, err)
		if err != nil {
			return append(outcomes, Outcome{Kind: target.Kind, ID: target.ID, Level: target.Level, Result: ResultFailed}), err
		}
		outcomes = append(outcomes, Outcome{Kind: target.Kind, ID: target.ID, Level: target.Level, Result: ResultStopped})
	}
	return outcomes, nil
}
