package main

import (
	"encoding/json"
	"fmt"
	"io"
)

const diffHelp = `usage: herdsman diff [<task>] [--working|--committed|--remote] [--json] [-- <git diff args>]

Prints git's diff for a worktree, the current one when no task is named. With
no mode it is everything since the branch left the default branch, whether
committed, in the index, edited or new. Reading it stages nothing.

  --working    staged, unstaged and untracked changes against the last commit
  --committed  commits since the branch left the default branch
  --remote     commits the upstream does not have yet

exit codes:
  0  printed
  1  no such worktree, git refused, or --exit-code found differences
  2  bad usage
`

func init() {
	register(command{name: "diff", summary: "Show a worktree's changes.", help: diffHelp, run: runDiff})
}

// diffMode returns the one mode asked for, or false when several were.
func diffMode(working, committed, remote bool) (string, bool) {
	mode := ""
	count := 0
	for _, option := range []struct {
		on   bool
		name string
	}{{working, "working"}, {committed, "committed"}, {remote, "remote"}} {
		if option.on {
			mode = option.name
			count++
		}
	}
	return mode, count <= 1
}

func runDiff(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := verbFlags("diff", stderr)
	working := fs.Bool("working", false, "only what is not committed")
	committed := fs.Bool("committed", false, "only the branch's commits")
	remote := fs.Bool("remote", false, "only what the upstream lacks")
	asJSON := fs.Bool("json", false, "print the result as JSON; output is base64")
	positional, rest, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) > 1 {
		return usageError(stderr, "diff takes one task")
	}
	mode, ok := diffMode(*working, *committed, *remote)
	if !ok {
		return usageError(stderr, "--working, --committed and --remote exclude each other")
	}
	dir, err := workingFolder()
	if err != nil {
		return fail(stderr, err)
	}
	selector := ""
	if len(positional) == 1 {
		selector = positional[0]
	}
	data, err := callWorktree("Diff", map[string]any{"dir": dir, "selector": selector, "mode": mode, "args": rest})
	if err != nil {
		return fail(stderr, err)
	}
	var result struct {
		Output []byte `json:"output"`
		Exit   int    `json:"exit"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		fmt.Fprintln(stdout, string(data))
	} else if _, err := stdout.Write(result.Output); err != nil {
		return fail(stderr, err)
	}
	return result.Exit
}
