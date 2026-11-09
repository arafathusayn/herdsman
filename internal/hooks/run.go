package hooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const killGrace = 2 * time.Second

// Env is what a hook learns from its environment.
type Env struct {
	Task     string
	Branch   string
	Worktree string
	Route    string
}

// Runner starts the commands of a hook in Dir and logs under LogDir.
type Runner struct {
	LogDir string
	Dir    string
	Env    Env
}

func (r *Runner) environment(hook string) []string {
	return append(os.Environ(),
		"HERDSMAN_TASK="+r.Env.Task, "HERDSMAN_BRANCH="+r.Env.Branch, "HERDSMAN_WORKTREE_PATH="+r.Env.Worktree,
		"HERDSMAN_ROUTE="+r.Env.Route, "HERDSMAN_HOOK="+hook)
}

func (r *Runner) runFolder(hook string) (string, error) {
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	dir := filepath.Join(r.LogDir, safeName(r.Env.Task), safeName(hook), stamp)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("hooks: log folder: %w", err)
	}
	return dir, nil
}

// Run executes the plan and waits. Commands of a step run together; the first
// step with a failure ends the run. The result is written even on failure.
func (r *Runner) Run(ctx context.Context, hook string, plan Plan) (Result, error) {
	if len(plan) == 0 {
		return Result{Hook: hook, Task: r.Env.Task, OK: true}, nil
	}
	folder, err := r.runFolder(hook)
	if err != nil {
		return Result{Hook: hook, Task: r.Env.Task, OK: true}, err
	}
	return r.execute(ctx, hook, plan, folder)
}

// execute runs the plan with its logs and result in folder.
func (r *Runner) execute(ctx context.Context, hook string, plan Plan, folder string) (Result, error) {
	result := Result{Hook: hook, Task: r.Env.Task, OK: true, File: filepath.Join(folder, "result.json")}
	var failure error
	for index, step := range plan {
		if err := ctx.Err(); err != nil {
			failure = err
			result.OK = false
			break
		}
		finished := make([]CommandResult, len(step))
		var wg sync.WaitGroup
		for i, command := range step {
			wg.Add(1)
			go func() {
				defer wg.Done()
				finished[i] = r.runOne(ctx, hook, command, commandLog(folder, index, command.Name))
			}()
		}
		wg.Wait()
		result.Commands = append(result.Commands, finished...)
		for _, done := range finished {
			if done.Exit != 0 && failure == nil {
				result.OK = false
				failure = &FailedError{Hook: hook, Name: done.Name, Exit: done.Exit}
			}
		}
		if failure != nil {
			break
		}
	}
	return result, errors.Join(failure, writeResult(&result))
}

func (r *Runner) runOne(ctx context.Context, hook string, command Planned, logPath string) CommandResult {
	result := CommandResult{Name: command.Name, Command: command.Rendered, Exit: -1, Log: logPath}
	start := time.Now()
	defer func() { result.DurationMS = float64(time.Since(start).Microseconds()) / 1000 }()
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return result
	}
	defer log.Close()
	limited, cancel := ctx, context.CancelFunc(func() {})
	if command.Timeout > 0 {
		limited, cancel = context.WithTimeout(ctx, command.Timeout)
	}
	defer cancel()
	cmd := exec.CommandContext(limited, "/bin/sh", "-c", command.Rendered)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = r.Dir, r.environment(hook), log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = killGrace
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	err = cmd.Run()
	result.TimedOut = errors.Is(limited.Err(), context.DeadlineExceeded)
	var exit *exec.ExitError
	switch {
	case err == nil:
		result.Exit = 0
	case result.TimedOut:
	case errors.As(err, &exit):
		result.Exit = exit.ExitCode()
	default:
		fmt.Fprintln(log, "hook did not start:", err)
	}
	return result
}

// Start runs the plan in its own session and returns without waiting, so the
// commands outlive this process. A detached copy of this program runs the plan
// exactly as Run does: same order, per-command limits and result file.
func (r *Runner) Start(hook string, plan Plan) ([]string, error) {
	if len(plan) == 0 {
		return nil, nil
	}
	folder, err := r.runFolder(hook)
	if err != nil {
		return nil, err
	}
	logs := make([]string, 0, len(plan))
	for index, step := range plan {
		for _, command := range step {
			logs = append(logs, commandLog(folder, index, command.Name))
		}
	}
	spec := filepath.Join(folder, planFile)
	if err := r.savePlan(spec, hook, folder, plan); err != nil {
		return nil, err
	}
	program, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("hooks: start %s: %w", hook, err)
	}
	failures, err := os.OpenFile(filepath.Join(folder, "supervisor.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("hooks: start %s: %w", hook, err)
	}
	defer failures.Close()
	cmd := exec.Command(program)
	cmd.Dir, cmd.Stdout, cmd.Stderr = r.Dir, failures, failures
	cmd.Env = append(os.Environ(), superviseEnv+"="+spec)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("hooks: start %s: %w", hook, err)
	}
	// Waiting reaps the process when this runs inside a long-lived server.
	go func() { _ = cmd.Wait() }()
	return logs, nil
}
