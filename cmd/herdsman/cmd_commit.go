package main

import (
	"encoding/json"
	"fmt"
	"io"
)

const commitHelp = `usage: herdsman commit [--squash <target>] [--dry-run] [-m <message>] [--type <type>] [--scope <scope>] [--json]

Stages the current worktree by its [commit] stage policy and commits. The
message is written by the configured generator, from the staged diff, and must
be a Conventional Commits message. When no generator can answer, the command
writes a plain one from the branch and the file names and says so; an answer
that is still invalid after one retry stops the commit.

--squash folds every commit since the branch left <target> into this one.
--dry-run shows the request, which program gets it and its answer; nothing changes.

exit codes:
  0  committed, or the dry run printed
  1  nothing to commit, an invalid message, or git refused
  2  bad usage
`

type commitResult struct {
	Task    string `json:"task"`
	Commit  string `json:"commit"`
	Message string `json:"message"`
	Source  string `json:"source"`
	Notice  string `json:"notice"`
	Prompt  string `json:"prompt"`
	Command string `json:"command"`
}

func init() {
	register(command{name: "commit", summary: "Commit a worktree with a Conventional Commits message.", help: commitHelp, run: runCommit})
}

func printDryRun(stdout io.Writer, result commitResult) {
	if result.Prompt != "" {
		fmt.Fprintf(stdout, "--- prompt\n%s\n", result.Prompt)
	}
	if result.Command != "" {
		fmt.Fprintf(stdout, "--- command\n%s\n", result.Command)
	}
	fmt.Fprintf(stdout, "--- message (%s)\n%s\n", result.Source, result.Message)
}

func runCommit(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := verbFlags("commit", stderr)
	squash := fs.String("squash", "", "fold the commits since this target into one")
	dryRun := fs.Bool("dry-run", false, "show the request and the answer, change nothing")
	message := fs.String("m", "", "use this message")
	kind := fs.String("type", "", "fix the commit type")
	scope := fs.String("scope", "", "fix the commit scope")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	positional, rest, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) != 0 || len(rest) != 0 {
		return usageError(stderr, "commit takes no arguments")
	}
	dir, err := workingFolder()
	if err != nil {
		return fail(stderr, err)
	}
	data, err := callWorktree("Commit", map[string]any{
		"dir": dir, "squash": *squash, "dry_run": *dryRun, "message": *message, "type": *kind, "scope": *scope,
	})
	if err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	var result commitResult
	if err := json.Unmarshal(data, &result); err != nil {
		return fail(stderr, err)
	}
	if result.Notice != "" {
		warn(stderr, []string{result.Notice})
	}
	if *dryRun {
		printDryRun(stdout, result)
		return 0
	}
	fmt.Fprintf(stdout, "%s %s\n", short(result.Commit), result.Message)
	return 0
}
