package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// worktreeCall serves the worktree verbs. It takes a method name and a JSON
// request, the shape a generated client will have, so that moving the verbs
// onto the SDK replaces this variable and nothing else. main.go sets it.
var worktreeCall func(ctx context.Context, method string, request []byte) ([]byte, error)

const newHelp = `usage: herdsman new <task> [--base <ref>] [--from pr:<n>] [--type <type>] [--no-setup]
                    [--clobber] [--agent <command>] [--integrator] [--json] [-- <prompt>]

Makes the task's branch and worktree from the fetched base, then runs its hooks
and setup.

exit codes:
  0  the worktree is ready
  1  it could not be made, or a hook or the setup failed (the message says which)
  2  bad usage
`

type newResult struct {
	Task     string   `json:"task"`
	Branch   string   `json:"branch"`
	Path     string   `json:"path"`
	Port     int      `json:"port"`
	Launch   string   `json:"launch"`
	Warnings []string `json:"warnings"`
}

func init() {
	register(command{name: "new", summary: "Make a task's branch and worktree.", help: newHelp, run: runNew})
}

// workingFolder is the current folder with symbolic links resolved, the form
// git reports worktree paths in.
func workingFolder() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(dir)
}

// callWorktree sends one request and returns the raw response.
func callWorktree(method string, request any) ([]byte, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	return worktreeCall(context.Background(), method, data)
}

// parseVerb reads flags on either side of the positional arguments, since
// `new task --base x` is how people type it. Everything after -- stays out.
func parseVerb(fs *flag.FlagSet, args []string) (positional, rest []string, err error) {
	for {
		if err := fs.Parse(args); err != nil {
			return nil, nil, err
		}
		remaining := fs.Args()
		dashes := len(args) - len(remaining)
		if dashes > 0 && args[dashes-1] == "--" {
			return positional, remaining, nil
		}
		if len(remaining) == 0 {
			return positional, nil, nil
		}
		positional = append(positional, remaining[0])
		args = remaining[1:]
	}
}

func warn(stderr io.Writer, warnings []string) {
	for _, warning := range warnings {
		fmt.Fprintln(stderr, "herdsman: warning:", warning)
	}
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, "herdsman:", err)
	return 1
}

func usageError(stderr io.Writer, message string) int {
	fmt.Fprintln(stderr, "herdsman:", message)
	return 2
}

func verbFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func runNew(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := verbFlags("new", stderr)
	var request struct {
		Dir     string `json:"dir"`
		Task    string `json:"task"`
		Base    string `json:"base,omitempty"`
		From    string `json:"from,omitempty"`
		Type    string `json:"type,omitempty"`
		Agent   string `json:"agent,omitempty"`
		Prompt  string `json:"prompt,omitempty"`
		NoSetup bool   `json:"no_setup,omitempty"`
		Clobber bool   `json:"clobber,omitempty"`
		// Integrator gives the agent the [policy] roots.
		Integrator bool `json:"integrator,omitempty"`
	}
	fs.StringVar(&request.Base, "base", "", "ref to branch from (default: the default branch)")
	fs.StringVar(&request.From, "from", "", "start from a pull request, pr:<n>")
	fs.StringVar(&request.Type, "type", "", "the branch's type prefix (default: feat)")
	fs.StringVar(&request.Agent, "agent", "", "command that starts an agent")
	fs.BoolVar(&request.Integrator, "integrator", false, "under the restricted profile, grant the agent the [policy] roots")
	fs.BoolVar(&request.NoSetup, "no-setup", false, "skip the detected setup")
	fs.BoolVar(&request.Clobber, "clobber", false, "rename a stale folder at the path aside")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	positional, prompt, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) != 1 {
		return usageError(stderr, "new takes one task name")
	}
	request.Task, request.Prompt = positional[0], strings.Join(prompt, " ")
	if request.Dir, err = workingFolder(); err != nil {
		return fail(stderr, err)
	}
	data, err := callWorktree("New", request)
	if err != nil {
		return fail(stderr, err)
	}
	var result newResult
	if err := json.Unmarshal(data, &result); err != nil {
		return fail(stderr, err)
	}
	warn(stderr, result.Warnings)
	if *asJSON {
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	fmt.Fprintf(stdout, "%s on %s\npath: %s\nport: %d\nlaunch: %s\n", result.Task, result.Branch, result.Path, result.Port, result.Launch)
	return 0
}
