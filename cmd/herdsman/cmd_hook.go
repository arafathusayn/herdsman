package main

import (
	"encoding/json"
	"fmt"
	"io"
)

const hookHelp = `usage: herdsman hook <type> [--dry-run] [--approve] [--from <task>] [--json]
       herdsman hook show [<type>]

Runs one hook point for the current worktree, or for --from's. --dry-run prints
the expanded commands, --approve accepts exactly these templates, and show
lists what is configured.

exit codes:
  0  done
  1  the hook failed or is not approved
  2  bad usage
`

func init() {
	register(command{name: "hook", summary: "Show, approve or run a worktree hook.", help: hookHelp, run: runHook})
}

func runHook(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := verbFlags("hook", stderr)
	dryRun := fs.Bool("dry-run", false, "print the commands without running them")
	approve := fs.Bool("approve", false, "accept these templates")
	from := fs.String("from", "", "use this task's worktree")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	positional, _, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	kind := ""
	switch {
	case len(positional) == 1 && positional[0] == "show":
		*dryRun = true
	case len(positional) == 2 && positional[0] == "show":
		kind, *dryRun = positional[1], true
	case len(positional) == 1:
		kind = positional[0]
	default:
		return usageError(stderr, "hook takes one hook name, or show")
	}
	dir, err := workingFolder()
	if err != nil {
		return fail(stderr, err)
	}
	data, err := callWorktree("Hook", map[string]any{"dir": dir, "type": kind, "from": *from, "dry_run": *dryRun, "approve": *approve})
	if err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	var result struct {
		Commands []struct {
			Hook     string `json:"hook"`
			Name     string `json:"name"`
			Rendered string `json:"rendered"`
			Approved bool   `json:"approved"`
		} `json:"commands"`
		Approved []string `json:"approved"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fail(stderr, err)
	}
	for _, command := range result.Commands {
		state := "needs approval"
		if command.Approved {
			state = "approved"
		}
		fmt.Fprintf(stdout, "%s %s (%s): %s\n", command.Hook, command.Name, state, command.Rendered)
	}
	for _, template := range result.Approved {
		fmt.Fprintf(stdout, "approved: %s\n", template)
	}
	return 0
}
