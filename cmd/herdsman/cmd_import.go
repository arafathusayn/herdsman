package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
)

const importHelp = `usage: herdsman import <export> [--state-dir <path>]

Rebuilds an account from an export into an empty state directory, which must
have no daemon running on it.

exit codes:
  0  imported
  1  refused or failed
  2  bad usage
`

func init() {
	register(command{name: "import", summary: "Rebuild an account from an export.", help: importHelp, run: runImport})
}

func runImport(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := verbFlags("import", stderr)
	state := fs.String("state-dir", "", "the empty state directory to fill")
	positional, _, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) != 1 {
		return usageError(stderr, "import takes one export")
	}
	source, err := filepath.Abs(positional[0])
	if err != nil {
		return fail(stderr, err)
	}
	data, err := callRecovery("Import", struct {
		Source   string `json:"source"`
		StateDir string `json:"state_dir,omitempty"`
	}{source, *state})
	if err != nil {
		return fail(stderr, err)
	}
	var result struct {
		StateDir string `json:"state_dir"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintln(stdout, "imported into", result.StateDir)
	return 0
}
