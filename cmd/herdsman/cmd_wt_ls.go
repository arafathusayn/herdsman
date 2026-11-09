package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
)

const lsHelp = `usage: herdsman ls [--full] [--all] [--json]
       herdsman ls --statusline [--format text|json|claude-code]

Prints one row per worktree of the repository. --all adds branches without a
tree, --full adds the commit and path. The JSON form is for scripts:

  herdsman ls --json | jq -r '.rows[] | select(.status != "") | .task'

exit codes:
  0  printed
  1  the table could not be read
  2  bad usage
`

type listRow struct {
	Task    string `json:"task"`
	Branch  string `json:"branch"`
	Path    string `json:"path"`
	Head    string `json:"head"`
	Status  string `json:"status"`
	Marks   string `json:"marks"`
	Ahead   int    `json:"ahead"`
	Behind  int    `json:"behind"`
	HasBase bool   `json:"has_base"`
	Subject string `json:"subject"`
	Error   string `json:"error"`
}

type listResult struct {
	Schema     json.RawMessage `json:"schema"`
	Rows       []listRow       `json:"rows"`
	Statusline string          `json:"statusline"`
}

type listRequest struct {
	Dir        string `json:"dir"`
	All        bool   `json:"all,omitempty"`
	Statusline bool   `json:"statusline,omitempty"`
}

func init() {
	register(command{name: "ls", summary: "List the repository's worktrees.", help: lsHelp, run: runLs})
}

func baseCell(row listRow) string {
	if !row.HasBase {
		return ""
	}
	return "↑" + strconv.Itoa(row.Ahead) + " ↓" + strconv.Itoa(row.Behind)
}

func short(head string) string {
	if len(head) > 7 {
		return head[:7]
	}
	return head
}

func printTable(w io.Writer, rows []listRow, full bool) {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	header := "TASK\tBRANCH\tSTATUS\tBASE\tSUBJECT"
	if full {
		header += "\tHEAD\tPATH"
	}
	fmt.Fprintln(table, header)
	for _, row := range rows {
		line := fmt.Sprintf("%s\t%s\t%s\t%s\t%s", row.Task, row.Branch, row.Status+row.Marks, baseCell(row), row.Subject)
		if full {
			line += "\t" + short(row.Head) + "\t" + row.Path
		}
		fmt.Fprintln(table, line)
	}
	table.Flush()
}

func readList(request listRequest) (listResult, []byte, error) {
	var result listResult
	data, err := callWorktree("List", request)
	if err != nil {
		return result, nil, err
	}
	return result, data, json.Unmarshal(data, &result)
}

func harnessFolder(stdin io.Reader) (string, error) {
	var input struct {
		Workspace struct {
			CurrentDir string `json:"current_dir"`
		} `json:"workspace"`
	}
	if err := json.NewDecoder(stdin).Decode(&input); err != nil {
		return "", fmt.Errorf("read the harness's status input: %w", err)
	}
	return input.Workspace.CurrentDir, nil
}

func runLs(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := verbFlags("ls", stderr)
	full := fs.Bool("full", false, "add the commit and path")
	all := fs.Bool("all", false, "add branches without a worktree")
	asJSON := fs.Bool("json", false, "print the table as JSON")
	line := fs.Bool("statusline", false, "print one line for a shell prompt or harness")
	format := fs.String("format", "", "statusline format: text, json or claude-code")
	positional, _, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) != 0 {
		return usageError(stderr, "ls takes no arguments")
	}
	if *format != "" && !*line {
		return usageError(stderr, "--format needs --statusline")
	}
	if *format != "" && *format != "text" && *format != "json" && *format != "claude-code" {
		return usageError(stderr, "--format is text, json or claude-code")
	}
	request := listRequest{All: *all, Statusline: *line}
	if *format == "claude-code" {
		request.Dir, err = harnessFolder(stdin)
	} else {
		request.Dir, err = workingFolder()
	}
	if err != nil {
		return fail(stderr, err)
	}
	result, data, err := readList(request)
	if err != nil {
		return fail(stderr, err)
	}
	switch {
	case *line && (*format == "json" || *asJSON):
		encoded, _ := json.Marshal(map[string]any{"schema": result.Schema, "statusline": result.Statusline})
		fmt.Fprintln(stdout, string(encoded))
	case *line:
		fmt.Fprintln(stdout, result.Statusline)
	case *asJSON:
		fmt.Fprintln(stdout, string(data))
	default:
		printTable(stdout, result.Rows, *full)
	}
	return 0
}
