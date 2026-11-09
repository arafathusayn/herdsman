package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	tracev1 "github.com/arafathusayn/herdsman/sdk/trace/v1"
)

// evalSuiteFile names the tasks and arms of a suite, inside its folder.
const evalSuiteFile = "suite.json"

const defaultEvalTimeout = 30 * time.Minute

type evalTask struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
	Check  string `json:"check"`
}

type evalArm struct {
	Name    string `json:"name"`
	Command string `json:"command"`
}

type suiteSpec struct {
	Tasks          []evalTask `json:"tasks"`
	Arms           []evalArm  `json:"arms"`
	Runs           int        `json:"runs"`
	ControlRuns    int        `json:"control_runs"`
	TimeoutSeconds int        `json:"timeout_seconds"`
}

var evalNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func loadEvalSuite(folder string) (suiteSpec, error) {
	var suite suiteSpec
	data, err := os.ReadFile(filepath.Join(folder, evalSuiteFile))
	if err != nil {
		return suite, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&suite); err != nil {
		return suite, fmt.Errorf("%s: %w", evalSuiteFile, err)
	}
	if len(suite.Tasks) == 0 || len(suite.Arms) == 0 {
		return suite, fmt.Errorf("%s needs at least one task and one arm", evalSuiteFile)
	}
	if suite.Runs < 1 || suite.ControlRuns < 2 {
		return suite, fmt.Errorf("%s needs runs of at least 1 and control_runs of at least 2, so that every arm has its own A/A control", evalSuiteFile)
	}
	for _, task := range suite.Tasks {
		if !evalNamePattern.MatchString(task.ID) || task.Check == "" {
			return suite, fmt.Errorf("task %q needs a plain id and a check command", task.ID)
		}
	}
	for _, arm := range suite.Arms {
		if !evalNamePattern.MatchString(arm.Name) || arm.Command == "" {
			return suite, fmt.Errorf("arm %q needs a plain name and a command", arm.Name)
		}
	}
	return suite, nil
}

// evalUsage is what a harness may report in the file HERDSMAN_EVAL_USAGE names.
type evalUsage struct {
	Tokens     *int64 `json:"tokens"`
	CostMicros *int64 `json:"cost_micros"`
}

func readEvalUsage(path string) evalUsage {
	var usage evalUsage
	data, err := os.ReadFile(path)
	if err == nil && json.Unmarshal(data, &usage) != nil {
		return evalUsage{}
	}
	return usage
}

// runEvalShell runs one command in its own process group, so that a timeout
// also stops whatever the command started.
func runEvalShell(ctx context.Context, command, dir string, env []string, log io.Writer) int {
	shell := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	shell.Dir, shell.Stdout, shell.Stderr = dir, log, log
	shell.Env = append(os.Environ(), env...)
	shell.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	shell.Cancel = func() error { return syscall.Kill(-shell.Process.Pid, syscall.SIGKILL) }
	shell.WaitDelay = 5 * time.Second
	err := shell.Run()
	var exited *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exited) && exited.ExitCode() >= 0:
		return exited.ExitCode()
	}
	return 1
}

// evalRunner runs a suite's commands in this process and records through the
// daemon, so that the service itself never has a way to run them.
type evalRunner struct {
	folder  string
	suite   suiteSpec
	service tracev1.TraceService
	timeout time.Duration
	serial  int
}

func (r *evalRunner) addSpan(ctx context.Context, route string, span *tracev1.Span) error {
	_, err := r.service.AddSpan(ctx, &tracev1.AddSpanRequest{Route: route, Span: span})
	return err
}

func spanOf(kind, name, task string, began time.Time, exit int) *tracev1.Span {
	code := int32(exit)
	return &tracev1.Span{Kind: kind, Name: name, Task: task, Start: began.UTC().Format(time.RFC3339Nano), DurationMs: float64(time.Since(began)) / float64(time.Millisecond), Exit: &code}
}

// runOne gives a run its own route folder and working folder, runs the arm's
// command on the task, then the task's check. A run passes when both exit 0.
func (r *evalRunner) runOne(ctx context.Context, arm evalArm, task evalTask, control string) error {
	r.serial++
	label := control
	if label == "" {
		label = "run"
	}
	route := filepath.Join(r.folder, "routes", fmt.Sprintf("%05d-%s-%s-%s", r.serial, arm.Name, task.ID, label))
	work := filepath.Join(route, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(route, "00-route.md"), []byte("# "+arm.Name+" on "+task.ID+"\n"), 0o644); err != nil {
		return err
	}
	log, err := os.Create(filepath.Join(route, "harness.log"))
	if err != nil {
		return err
	}
	defer log.Close()
	usagePath := filepath.Join(route, "usage.json")
	env := []string{"HERDSMAN_EVAL_PROMPT=" + task.Prompt, "HERDSMAN_EVAL_TASK=" + task.ID, "HERDSMAN_EVAL_ARM=" + arm.Name, "HERDSMAN_EVAL_USAGE=" + usagePath, "HERDSMAN_ROUTE_FOLDER=" + route}

	limited, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	began := time.Now()
	exit := runEvalShell(limited, arm.Command, work, env, log)
	duration := float64(time.Since(began)) / float64(time.Millisecond)
	usage := readEvalUsage(usagePath)
	turn := spanOf("turn", arm.Name, task.ID, began, exit)
	turn.Tokens, turn.CostMicros = usage.Tokens, usage.CostMicros
	if err := r.addSpan(ctx, route, turn); err != nil {
		return err
	}
	pass := false
	if exit == 0 {
		began = time.Now()
		checked := runEvalShell(limited, task.Check, work, env, log)
		if err := r.addSpan(ctx, route, spanOf("check", task.ID, task.ID, began, checked)); err != nil {
			return err
		}
		pass = checked == 0
	}
	_, err = r.service.RecordRun(ctx, &tracev1.RecordRunRequest{Suite: r.folder, Run: &tracev1.Run{Task: task.ID, Arm: arm.Name, Control: control, Pass: pass, DurationMs: duration, CostMicros: usage.CostMicros}})
	return err
}

// runAll interleaves the groups round by round, so that drift over a long run
// falls on every arm and on both halves of each control alike.
func (r *evalRunner) runAll(ctx context.Context) error {
	for round := range max(r.suite.Runs, r.suite.ControlRuns) {
		for _, arm := range r.suite.Arms {
			for _, task := range r.suite.Tasks {
				var groups []string
				if round < r.suite.Runs {
					groups = append(groups, "")
				}
				if round < r.suite.ControlRuns {
					groups = append(groups, "a", "b")
				}
				for _, control := range groups {
					if err := r.runOne(ctx, arm, task, control); err != nil {
						return fmt.Errorf("%s on %s: %w", arm.Name, task.ID, rpcText(err))
					}
				}
			}
		}
	}
	return nil
}

func runEvalSuite(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("eval run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	asJSON := flags.Bool("json", false, "print JSON")
	positional, err := parseInterspersed(flags, args)
	if err != nil {
		return 2
	}
	if len(positional) != 1 {
		fmt.Fprintln(stderr, "usage: herdsman eval --experimental-eval run <suite folder> [--json]")
		return 2
	}
	paths, err := absolutePaths(positional)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	suite, err := loadEvalSuite(paths[0])
	if err != nil {
		fmt.Fprintf(stderr, "eval: %v\n", err)
		return 1
	}
	// Runs from an earlier build must not be pooled with these.
	if _, err := os.Stat(filepath.Join(paths[0], "runs.jsonl")); err == nil {
		fmt.Fprintf(stderr, "eval: %s already holds runs; use a new suite folder\n", positional[0])
		return 1
	}
	service, closeService, err := dialTrace()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer closeService()
	timeout := defaultEvalTimeout
	if suite.TimeoutSeconds > 0 {
		timeout = time.Duration(suite.TimeoutSeconds) * time.Second
	}
	runner := &evalRunner{folder: paths[0], suite: suite, service: service, timeout: timeout}
	if err := runner.runAll(context.Background()); err != nil {
		fmt.Fprintf(stderr, "eval: %v\n", err)
		return 1
	}
	report, code := buildEvalReport(positional[0], nil, false, stderr)
	if code != 0 {
		return code
	}
	printEvalReport(stdout, report, *asJSON)
	return 0
}
