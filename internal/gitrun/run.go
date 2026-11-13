// Package gitrun is the one place the worktree layer starts git.
//
// Each call names its repository with -C, runs without the variables that
// would point git somewhere else, and is written to a trace with its exit code
// and duration. Callers read porcelain output and exit codes only.
package gitrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// TraceFile is the name of the trace inside its logs folder.
const TraceFile = "commands.jsonl"

// Entry is one finished git call. Exit is -1 when git did not run to an exit.
type Entry struct {
	Time       time.Time `json:"time"`
	Dir        string    `json:"dir"`
	Args       []string  `json:"args"`
	Exit       int       `json:"exit"`
	DurationMS float64   `json:"duration_ms"`
}

// Tracer receives every finished call. It must be safe for concurrent use.
type Tracer interface {
	Trace(Entry)
}

// Result is what a call printed and how it ended.
type Result struct {
	Stdout []byte
	Stderr []byte
	Exit   int
}

// ExitError reports a git process that ran and exited non-zero.
type ExitError struct {
	Args   []string
	Code   int
	Stderr string
}

func (e *ExitError) Error() string {
	message := fmt.Sprintf("git %s: exit %d", strings.Join(e.Args, " "), e.Code)
	if e.Stderr != "" {
		message += ": " + e.Stderr
	}
	return message
}

// Runner starts git. The zero value runs "git" from PATH with no trace.
type Runner struct {
	Tracer Tracer
	Git    string // Program to run; empty means "git".
	Index  string // Index file to use in place of the repository's; empty means its own.
}

// Run executes git in the repository at dir, which must be absolute. A
// non-zero exit returns the Result together with an *ExitError.
func (r *Runner) Run(ctx context.Context, dir string, args ...string) (Result, error) {
	if !filepath.IsAbs(dir) {
		return Result{Exit: -1}, fmt.Errorf("gitrun: directory %q is not absolute", dir)
	}
	if len(args) == 0 {
		return Result{Exit: -1}, errors.New("gitrun: no git arguments")
	}
	program := r.Git
	if program == "" {
		program = "git"
	}
	cmd := exec.CommandContext(ctx, program, append([]string{"-C", dir}, args...)...)
	cmd.Env = scrub(os.Environ())
	if r.Index != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+r.Index)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	started := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(started)

	result := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	err := classify(ctx, args, runErr, &result)
	if r.Tracer != nil {
		r.Tracer.Trace(Entry{
			Time: started.UTC(), Dir: dir, Args: slices.Clone(args), Exit: result.Exit,
			DurationMS: float64(elapsed.Microseconds()) / 1000,
		})
	}
	return result, err
}

// classify sets result.Exit and returns the error a caller should see.
func classify(ctx context.Context, args []string, runErr error, result *Result) error {
	if runErr == nil {
		return nil
	}
	result.Exit = -1
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("gitrun: git %s: %w", args[0], ctxErr)
	}
	var exited *exec.ExitError
	if errors.As(runErr, &exited) {
		result.Exit = exited.ExitCode()
		return &ExitError{Args: slices.Clone(args), Code: result.Exit, Stderr: strings.TrimSpace(string(result.Stderr))}
	}
	return fmt.Errorf("gitrun: git %s: %w", args[0], runErr)
}

// redirecting holds the variables git itself lists as local to a repository
// (git rev-parse --local-env-vars); the test compares against that output.
var redirecting = []string{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_CONFIG", "GIT_CONFIG_COUNT",
	"GIT_CONFIG_PARAMETERS", "GIT_DIR", "GIT_GRAFT_FILE", "GIT_IMPLICIT_WORK_TREE", "GIT_INDEX_FILE",
	"GIT_INTERNAL_SUPER_PREFIX", "GIT_NO_REPLACE_OBJECTS", "GIT_OBJECT_DIRECTORY", "GIT_PREFIX",
	"GIT_REPLACE_REF_BASE", "GIT_SHALLOW_FILE", "GIT_WORK_TREE",
}

// scrub removes what would make git act on another repository or config. The
// numbered GIT_CONFIG_KEY_n and GIT_CONFIG_VALUE_n pairs go with their count.
func scrub(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		return slices.Contains(redirecting, name) || name == "GIT_NAMESPACE" ||
			strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_")
	})
}

// FileTrace appends one JSON line per call to commands.jsonl in a private
// folder. A line that cannot be written is counted, never returned: a full
// disk must not fail the git call being traced.
type FileTrace struct {
	dir     string
	dropped atomic.Int64
}

// NewFileTrace returns a trace that writes into the logs folder dir.
func NewFileTrace(dir string) *FileTrace {
	return &FileTrace{dir: dir}
}

// Dropped counts the entries that could not be written.
func (f *FileTrace) Dropped() int64 {
	return f.dropped.Load()
}

// Trace writes e as one line, with one write call so that lines from
// concurrent processes do not interleave.
func (f *FileTrace) Trace(e Entry) {
	if err := f.write(e); err != nil {
		f.dropped.Add(1)
	}
}

func (f *FileTrace) write(e Entry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(f.dir, TraceFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(line, '\n'))
	return err
}
