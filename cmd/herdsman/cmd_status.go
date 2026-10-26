package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/settings"
	"github.com/arafathusayn/herdsman/internal/status"
)

func init() {
	register(command{name: "status", summary: "Print a health snapshot of every agent in HERDSMAN_STATUS_SPECS and of the machine.", help: statusHelp, run: runStatus})
}

// herdrClient is the client of the herdr session the commands talk to;
// tests point HERDR_SOCKET_PATH at a fake server.
func herdrClient() *herdr.Client {
	sock, err := herdr.SocketPath("")
	if err != nil {
		return herdr.New("")
	}
	return herdr.New(sock)
}

// statusLimit reads LIMIT_MIN strictly (A25): unset or empty is 30; a
// whole number of minutes from 0, read as test reads it and printed as
// written; anything else is an error.
func statusLimit() (int, string, error) {
	v := os.Getenv("LIMIT_MIN")
	n, err := settings.Int("LIMIT_MIN", v, 30, 0, 1<<30)
	if v == "" {
		v = "30"
	}
	return int(n), v, err
}

// statusSnapshot writes the snapshot of HERDSMAN_STATUS_SPECS; its callers
// have checked LIMIT_MIN.
func statusSnapshot(ctx context.Context, w io.Writer, specs string) {
	limit, label, _ := statusLimit()
	status.Write(ctx, w, status.Options{Specs: status.ParseSpecs(specs), LimitMin: limit, LimitLabel: label, Client: herdrClient(), Now: now(), Machine: status.ReadMachine()})
}

func runStatus(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	args, asJSON := takeJSON(args)
	if len(args) > 0 {
		fmt.Fprintln(stderr, "usage: herdsman status (set HERDSMAN_STATUS_SPECS)")
		return 2
	}
	specs := os.Getenv("HERDSMAN_STATUS_SPECS")
	if specs == "" {
		fmt.Fprintf(stderr, "herdsman status: %v\n", settings.Missing("HERDSMAN_STATUS_SPECS"))
		return settings.ExitInvalid
	}
	if _, _, err := statusLimit(); err != nil {
		fmt.Fprintf(stderr, "herdsman status: %v\n", err)
		return settings.ExitInvalid
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if asJSON {
		var b bytes.Buffer
		statusSnapshot(ctx, &b, specs)
		_, _ = stdout.Write(statusJSON(b.String()))
		return 0
	}
	statusSnapshot(ctx, stdout, specs)
	return 0
}
