package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/runcheck"
	"github.com/arafathusayn/herdsman/internal/settings"
	tracev1 "github.com/arafathusayn/herdsman/sdk/trace/v1"
)

func init() {
	register(command{name: "run-check", summary: "Run one heavy check at a time on this machine, behind a load gate (--publish <hours>: the load reading).", help: runCheckHelp, run: runRunCheck})
}

func runRunCheck(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c, err := runcheck.Load(os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "herdsman run-check: %v\n", err)
		return settings.ExitInvalid
	}
	if len(args) > 0 && args[0] == "--publish" {
		hours := ""
		if len(args) > 1 {
			hours = args[1]
		}
		longRunning()
		return runcheck.Publish(c, hours, stderr)
	}
	began := time.Now().UTC()
	code := runcheck.Run(c, args, stdin, stdout, stderr)
	recordCheckSpan(args, began, code, stderr)
	return code
}

var checkLabel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// checkName is what a span calls a check: the program's base name and nothing
// else. Spans leave the machine and any argument can hold a secret, a shell's
// command string above all. An env wrapper with options is not unpicked, since
// an option's value could be mistaken for the program.
func checkName(args []string) string {
	const unnamed = "check"
	rest := args
	if len(rest) > 0 && rest[0] == "env" {
		rest = rest[1:]
		for len(rest) > 0 && strings.Contains(rest[0], "=") && !strings.HasPrefix(rest[0], "-") {
			rest = rest[1:]
		}
		if len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
			return unnamed
		}
	}
	if len(rest) == 0 {
		return unnamed
	}
	label := filepath.Base(rest[0])
	if !checkLabel.MatchString(label) {
		return unnamed
	}
	return label
}

// recordCheckSpan adds the check to its route's trace. A check's own result
// stands whatever happens here, so a failure is only a warning.
func recordCheckSpan(args []string, began time.Time, code int, stderr io.Writer) {
	folder := os.Getenv("HERDSMAN_ROUTE_FOLDER")
	if folder == "" || len(args) == 0 {
		return
	}
	exit := int32(code)
	span := &tracev1.Span{Kind: "check", Name: checkName(args), Start: began.Format(time.RFC3339Nano), DurationMs: float64(time.Since(began)) / float64(time.Millisecond), Exit: &exit}
	service, closeService, err := dialTrace()
	if err == nil {
		defer closeService()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err = service.AddSpan(ctx, &tracev1.AddSpanRequest{Route: folder, Span: span})
	}
	if err != nil {
		fmt.Fprintf(stderr, "herdsman run-check: warning: the route's trace has no record of this check: %v\n", rpcText(err))
	}
}
