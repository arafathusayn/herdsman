package main

import (
	"context"
	"fmt"
	"io"
	"os"
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

// checkName is what a span calls a check: the program and its first word.
// Arguments can hold secrets and spans leave the machine, so nothing else.
func checkName(args []string) string {
	var words []string
	for _, arg := range args {
		switch {
		case len(words) == 2:
			return strings.Join(words, " ")
		case len(words) == 0 && (arg == "env" || strings.Contains(arg, "=")):
		case strings.HasPrefix(arg, "-") || strings.Contains(arg, "="):
		default:
			words = append(words, arg)
		}
	}
	return strings.Join(words, " ")
}

// recordCheckSpan adds the check to its route's trace. A check's own result
// stands whatever happens here, so a failure is only a warning.
func recordCheckSpan(args []string, began time.Time, code int, stderr io.Writer) {
	folder := os.Getenv("HERDSMAN_ROUTE_FOLDER")
	if folder == "" || len(args) == 0 {
		return
	}
	name := checkName(args)
	if name == "" {
		name = "check"
	}
	exit := int32(code)
	span := &tracev1.Span{Kind: "check", Name: name, Start: began.Format(time.RFC3339Nano), DurationMs: float64(time.Since(began)) / float64(time.Millisecond), Exit: &exit}
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
