package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
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

func init() {
	register(command{name: "go", summary: "Enter a worktree.", help: goHelp, run: runGo})
}

// pickSelector prints the table and reads a selector; "" means nothing chosen.
func pickSelector(dir string, stdin io.Reader, stdout io.Writer) (string, error) {
	result, _, err := readList(listRequest{Dir: dir})
	if err != nil {
		return "", err
	}
	printTable(stdout, result.Rows, false)
	if !stdinIsTerminal(stdin) {
		return "", nil
	}
	fmt.Fprint(stdout, "go to: ")
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
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
	if len(positional) == 1 {
		selector = positional[0]
	} else if selector, err = pickSelector(dir, stdin, stdout); err != nil {
		return fail(stderr, err)
	}
	if selector == "" {
		return 0
	}
	data, err := callWorktree("Go", map[string]any{"dir": dir, "selector": selector, "focus": *focus})
	if err != nil {
		return fail(stderr, err)
	}
	var result struct {
		Path     string   `json:"path"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fail(stderr, err)
	}
	warn(stderr, result.Warnings)
	if !*noCD {
		if err := writeDirective(result.Path); err != nil {
			return fail(stderr, err)
		}
	}
	if *asJSON {
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	fmt.Fprintln(stdout, result.Path)
	return 0
}
