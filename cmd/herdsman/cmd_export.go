package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
)

const exportHelp = `usage: herdsman export <destination>

Copies the whole account to a new folder: every database, every snapshot the
records name and the rest of the state directory. Other operations wait until
it ends. The folder appears complete or not at all.

exit codes:
  0  exported
  1  refused or failed
  2  bad usage
`

func init() {
	register(command{name: "export", summary: "Copy the whole account to a new folder.", help: exportHelp, run: runExport})
}

func runExport(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := verbFlags("export", stderr)
	positional, _, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) != 1 {
		return usageError(stderr, "export takes one destination")
	}
	dest, err := filepath.Abs(positional[0])
	if err != nil {
		return fail(stderr, err)
	}
	data, err := callRecovery("Export", struct {
		Dest string `json:"dest"`
	}{dest})
	if err != nil {
		return fail(stderr, err)
	}
	var op struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &op); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "exported to %s (operation %s)\n", dest, op.ID)
	return 0
}
