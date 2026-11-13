package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

const goHelp = `usage: herdsman go [<task>|<branch>|<path>|^|@|-|pr:<n>|<url>] [--no-cd] [--focus] [--json]

Finds a worktree, making one for a pull request, and enters it when the shell
wrapper is installed (herdsman config shell install). With no argument it prints
the table and, on a terminal, reads a selector.

exit codes:
  0  found
  1  nothing matched, or the switch hook failed
  2  bad usage
`

// directiveFile names the file the shell wrapper reads after this process ends.
const directiveFile = "HERDSMAN_DIRECTIVE_FILE"

// stdinIsTerminal is replaced by tests.
var stdinIsTerminal = func(stdin any) bool {
	file, ok := stdin.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err != nil || !os.SameFile(info, null)
}

type goResult struct {
	Schema     schemaField `json:"schema"`
	Task       string      `json:"task"`
	Branch     string      `json:"branch"`
	Path       string      `json:"path"`
	Created    bool        `json:"created"`
	Focused    bool        `json:"focused"`
	PushRemote string      `json:"push_remote,omitempty"`
	Warnings   []string    `json:"warnings,omitempty"`
}

func init() {
	register(command{name: "go", summary: "Enter a worktree.", help: goHelp, run: runGo})
}

// pickSelector shows the table on screen and reads a selector; an empty one
// means nothing was chosen. It also returns the table as JSON.
func pickSelector(dir string, stdin io.Reader, screen io.Writer) (string, []byte, error) {
	result, table, err := readList(listRequest{Dir: dir})
	if err != nil {
		return "", nil, err
	}
	printTable(screen, result.Rows, false)
	if !stdinIsTerminal(stdin) {
		return "", table, nil
	}
	fmt.Fprint(screen, "go to: ")
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", nil, err
	}
	return strings.TrimSpace(line), table, nil
}

func writeDirective(path string) error {
	name := os.Getenv(directiveFile)
	if name == "" {
		return nil
	}
	file, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = fmt.Fprintln(file, path)
	return err
}

func runGo(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if code, opened := openGoForm(&args, stdin, stdout, stderr); opened {
		return code
	}
	fs := verbFlags("go", stderr)
	noCD := fs.Bool("no-cd", false, "print the path and stay here")
	focus := fs.Bool("focus", false, "also focus the worktree's herdr pane")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	addFormFlags(fs)
	positional, _, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) > 1 {
		return usageError(stderr, "go takes one selector")
	}
	dir, err := workingFolder()
	if err != nil {
		return fail(stderr, err)
	}
	selector := ""
	var table []byte
	if len(positional) == 1 {
		selector = positional[0]
	} else {
		// With --json, standard output carries nothing but the JSON.
		screen := stdout
		if *asJSON {
			screen = stderr
		}
		if selector, table, err = pickSelector(dir, stdin, screen); err != nil {
			return fail(stderr, err)
		}
	}
	if selector == "" {
		if *asJSON {
			fmt.Fprintln(stdout, string(table))
		}
		return 0
	}
	response, err := callTask(func(ctx context.Context, service daemonv1.TaskService) (*daemonv1.TaskServiceGoResponse, error) {
		return service.Go(ctx, &daemonv1.TaskServiceGoRequest{Dir: dir, Selector: selector, Focus: *focus, Environ: os.Environ()})
	})
	if err != nil {
		return fail(stderr, err)
	}
	result := goResult{
		Schema: schemaField{Version: response.GetSchemaVersion()}, Task: response.GetTask(), Branch: response.GetBranch(),
		Path: response.GetPath(), Created: response.GetCreated(), Focused: response.GetFocused(),
		PushRemote: response.GetPushRemote(), Warnings: response.GetWarnings(),
	}
	warn(stderr, result.Warnings)
	if !*noCD {
		if err := writeDirective(result.Path); err != nil {
			return fail(stderr, err)
		}
	}
	if *asJSON {
		data, err := json.Marshal(result)
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	fmt.Fprintln(stdout, result.Path)
	return 0
}
