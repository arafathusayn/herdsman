package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/update"
)

const updateUsage = "usage: herdsman update [--check] [--version <version>] [--allow-downgrade]\n"

const updateHelp = updateUsage + `
Installs the latest release over this binary. The release's checksum list must
carry a signature from a key this binary trusts, and the archive must match
that list, before anything is replaced; the new binary must also run and report
its version. A binary installed by a package manager is left to that manager.

  --check            Say whether a newer release exists; change nothing.
  --version <v>      Install this version instead of the latest.
  --allow-downgrade  Allow a version older than this binary.

exit codes:
  0  the binary is current, was updated, or --check answered
  1  the release was refused or could not be fetched; the binary is unchanged
  2  usage error
`

func init() {
	register(command{name: "update", summary: "Install the latest signed release over this binary.", help: updateHelp, run: runUpdate})
}

// updateRun is replaced by tests.
var updateRun = update.Run

func runUpdate(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	check := fs.Bool("check", false, "report only")
	target := fs.String("version", "", "version to install")
	downgrade := fs.Bool("allow-downgrade", false, "allow an older version")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprint(stderr, updateUsage)
		return 2
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "herdsman update: %v\n", err)
		return 1
	}
	keys, err := update.TrustedKeys()
	if err != nil {
		fmt.Fprintf(stderr, "herdsman update: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	err = updateRun(ctx, update.Options{
		Client:         &http.Client{Timeout: 10 * time.Minute},
		API:            "https://api.github.com",
		Base:           "https://github.com",
		Keys:           keys,
		Current:        version,
		Exe:            exe,
		GOOS:           runtime.GOOS,
		GOARCH:         runtime.GOARCH,
		Smoke:          update.Smoke,
		Stdout:         stdout,
		Target:         strings.TrimPrefix(*target, "v"),
		Check:          *check,
		AllowDowngrade: *downgrade,
	})
	if err != nil {
		fmt.Fprintf(stderr, "herdsman update: %v\n", err)
		return 1
	}
	return 0
}
