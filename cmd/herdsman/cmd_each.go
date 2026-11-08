package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
)

const eachHelp = `usage: herdsman each [--json] -- <command> [<arg>...]

Runs a command in each worktree of the repository in turn; a failure in one
does not stop the rest. The command is run as given, without a shell; use
sh -c '...' for one. It sees HERDSMAN_TASK, HERDSMAN_BRANCH and
HERDSMAN_WORKTREE_PATH, and reads nothing from standard input.

The command's output goes to standard output. The heading before each run and
the summary go to standard error, so the output can be piped. With --json the
command's output moves to standard error too and standard output holds only the
results.

exit codes:
  0  the command succeeded everywhere
  1  it failed somewhere, or the worktrees could not be listed
  2  bad usage
`

// eachResult is the outcome in one worktree. Exit is -1 when the command did
// not run to an exit.
type eachResult struct {
	Task   string `json:"task"`
	Branch string `json:"branch"`
	Path   string `json:"path"`
	Exit   int    `json:"exit"`
	Error  string `json:"error,omitempty"`
}

func init() {
	register(command{name: "each", summary: "Run a command in every worktree.", help: eachHelp, run: runEach})
}

// redirectingGit are the variables that would aim a command's git calls at the
// repository herdsman was started from instead of the worktree it runs in.
var redirectingGit = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_PREFIX"}

func eachEnvironment(row listRow) []string {
	env := slices.DeleteFunc(os.Environ(), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		return slices.Contains(redirectingGit, name)
	})
	return append(env, "PWD="+row.Path, "HERDSMAN_TASK="+row.Task, "HERDSMAN_BRANCH="+row.Branch, "HERDSMAN_WORKTREE_PATH="+row.Path)
}

func runOneEach(command []string, row listRow, stdout, stderr io.Writer) eachResult {
	result := eachResult{Task: row.Task, Branch: row.Branch, Path: row.Path}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = row.Path, eachEnvironment(row), stdout, stderr
	err := cmd.Run()
	if err == nil {
		return result
	}
	result.Exit = cmd.ProcessState.ExitCode() // -1 when it never started.
	result.Error = err.Error()
	return result
}

func printEachSummary(stderr io.Writer, results []eachResult) int {
	failed := 0
	for _, result := range results {
		if result.Error != "" {
			failed++
			fmt.Fprintf(stderr, "failed: %s: %s\n", result.Task, result.Error)
		}
	}
	fmt.Fprintf(stderr, "%d worktrees: %d ok, %d failed\n", len(results), len(results)-failed, failed)
	return failed
}

func runEach(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := verbFlags("each", stderr)
	asJSON := fs.Bool("json", false, "print the results as JSON")
	positional, command, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) != 0 || len(command) == 0 {
		return usageError(stderr, "each needs a command after --")
	}
	dir, err := workingFolder()
	if err != nil {
		return fail(stderr, err)
	}
	list, _, err := readList(listRequest{Dir: dir})
	if err != nil {
		return fail(stderr, err)
	}
	output := stdout
	if *asJSON {
		output = stderr
	}
	results := make([]eachResult, 0, len(list.Rows))
	for _, row := range list.Rows {
		if row.Branch == "" && row.Head == "" {
			continue // A bare repository has no tree to run in.
		}
		fmt.Fprintf(stderr, "==> %s (%s)\n", row.Task, row.Path)
		results = append(results, runOneEach(command, row, output, stderr))
	}
	failed := printEachSummary(stderr, results)
	if *asJSON {
		encoded, err := json.Marshal(map[string]any{"results": results})
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintln(stdout, string(encoded))
	}
	if failed > 0 {
		return 1
	}
	return 0
}
