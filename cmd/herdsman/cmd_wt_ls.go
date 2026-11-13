package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

const lsHelp = `usage: herdsman ls [--full] [--all] [--json]
       herdsman ls --statusline [--format text|json|claude-code]

Prints one row per worktree of the repository. --all adds branches without a
tree, --full adds the commit, path and every other cell. The JSON form is for scripts:

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

	CommitUnix   int64  `json:"commit_unix"`
	Remote       string `json:"remote"`
	RemoteAhead  int    `json:"remote_ahead"`
	RemoteBehind int    `json:"remote_behind"`
	Agent        string `json:"agent"`
	PR           int    `json:"pr"`
	Review       string `json:"review"`
	CI           string `json:"ci"`
	Port         int    `json:"port"`
	PortsHeld    int    `json:"ports_held"`
	Containers   int    `json:"containers"`
	DiskBytes    int64  `json:"disk_bytes"`
}

type listResult struct {
	Schema     json.RawMessage `json:"schema"`
	Rows       []listRow       `json:"rows"`
	Statusline string          `json:"statusline"`
	Warnings   []string        `json:"warnings"`
	Summary    struct {
		Worktrees int `json:"worktrees"`
		Dirty     int `json:"dirty"`
	} `json:"summary"`
}

type listRequest struct {
	Dir        string `json:"dir"`
	All        bool   `json:"all,omitempty"`
	Statusline bool   `json:"statusline,omitempty"`
	Full       bool   `json:"full,omitempty"`
	Skeleton   bool   `json:"skeleton,omitempty"`
}

// stdoutIsTerminal is replaced by tests.
var stdoutIsTerminal = func(stdout any) bool { return stdinIsTerminal(stdout) }

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
		header += "\tHEAD\tPATH\tREMOTE\tAGENT\tPR\tCI\tREVIEW\tPORTS\tCONTAINERS\tDISK\tAGE"
	}
	fmt.Fprintln(table, header)
	for _, row := range rows {
		line := fmt.Sprintf("%s\t%s\t%s\t%s\t%s", row.Task, row.Branch, row.Status+row.Marks, baseCell(row), row.Subject)
		if full {
			line += "\t" + short(row.Head) + "\t" + row.Path + "\t" + fullCells(row)
		}
		fmt.Fprintln(table, line)
	}
	table.Flush()
}

func remoteCell(row listRow) string {
	if row.Remote == "" {
		return ""
	}
	return fmt.Sprintf("%s ↑%d ↓%d", row.Remote, row.RemoteAhead, row.RemoteBehind)
}

func numberCell(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func diskCell(size int64) string {
	switch {
	case size == 0:
		return ""
	case size < 1<<20:
		return fmt.Sprintf("%dK", size>>10)
	case size < 1<<30:
		return fmt.Sprintf("%dM", size>>20)
	}
	return fmt.Sprintf("%.1fG", float64(size)/(1<<30))
}

func ageCell(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Since(time.Unix(unix, 0)).Round(time.Minute).String()
}

func fullCells(row listRow) string {
	ports := ""
	if row.Port != 0 {
		ports = strconv.Itoa(row.Port)
		if row.PortsHeld > 0 {
			ports += fmt.Sprintf(" (%d held)", row.PortsHeld)
		}
	}
	return strings.Join([]string{
		remoteCell(row), row.Agent, numberCell(row.PR), row.CI, row.Review, ports,
		numberCell(row.Containers), diskCell(row.DiskBytes), ageCell(row.CommitUnix),
	}, "\t")
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
	request := listRequest{All: *all, Statusline: *line, Full: *full}
	if *format == "claude-code" {
		request.Dir, err = harnessFolder(stdin)
	} else {
		request.Dir, err = workingFolder()
	}
	if err != nil {
		return fail(stderr, err)
	}
	painted := 0
	if !*line && !*asJSON && stdoutIsTerminal(stdout) {
		first := request
		first.Skeleton, first.Full = true, false
		if skeleton, _, err := readList(first); err == nil {
			printTable(stdout, skeleton.Rows, *full)
			painted = len(skeleton.Rows) + 1
		}
	}
	result, data, err := readList(request)
	if err != nil {
		return fail(stderr, err)
	}
	if painted > 0 {
		fmt.Fprintf(stdout, "\x1b[%dA\x1b[J", painted)
	}
	warn(stderr, result.Warnings)
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
		if *full {
			fmt.Fprintf(stdout, "%d worktrees, %d dirty\n", result.Summary.Worktrees, result.Summary.Dirty)
		}
	}
	return 0
}
