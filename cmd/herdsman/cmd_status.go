package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/status"
)

func init() {
	register(command{name: "status", summary: "Print a health snapshot of every agent in HERDSMAN_STATUS_SPECS and of the machine.", run: runStatus})
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

// statusSnapshot writes the snapshot of HERDSMAN_STATUS_SPECS.
func statusSnapshot(ctx context.Context, w io.Writer, specs string) {
	// As ${LIMIT_MIN:-30}: unset or empty is 30, an explicit 0 flags every
	// job, and a value that is no number flags none (the script's test fails).
	limit := 30
	if v := os.Getenv("LIMIT_MIN"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			n = math.MaxInt
		}
		limit = n
	}
	status.Write(ctx, w, status.Options{Specs: status.ParseSpecs(specs), LimitMin: limit, Client: herdrClient(), Now: now(), Machine: status.ReadMachine()})
}

func runStatus(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "usage: herdsman status (set HERDSMAN_STATUS_SPECS)")
		return 2
	}
	specs := os.Getenv("HERDSMAN_STATUS_SPECS")
	if specs == "" {
		fmt.Fprintln(stderr, "herdsman status: HERDSMAN_STATUS_SPECS: set HERDSMAN_STATUS_SPECS")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	statusSnapshot(ctx, stdout, specs)
	return 0
}
