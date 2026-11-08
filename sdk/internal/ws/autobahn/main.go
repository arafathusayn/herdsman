// Command autobahn drives the ws package through the Autobahn test suite: an
// echo server for the suite's client mode, a client for its server mode, and a
// verdict over the report the suite writes.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/sdk/internal/ws"
)

// defaultMinCases is below the suite's count without sections 12 and 13, and
// far above what a run that stopped early would report.
const defaultMinCases = 250

var (
	exit             = os.Exit
	stderr io.Writer = os.Stderr
)

// suiteLimits fit the suite's largest case, a 16 MiB message cut into small
// frames. Production callers keep the package defaults.
var suiteLimits = ws.Options{
	MaxMessageSize: 64 << 20,
	MaxFragments:   1 << 20,
	WriteTimeout:   60 * time.Second,
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(stderr, err)
		exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: autobahn server|client|verify [flags]")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	switch args[0] {
	case "server":
		addr := flags.String("addr", "127.0.0.1:9001", "listen address")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		return serve(ctx, *addr, out)
	case "client":
		base := flags.String("url", "", "the suite's ws:// URL")
		agent := flags.String("agent", "herdsman", "agent name in the report")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		return runCases(ctx, *base, *agent, out)
	case "verify":
		minCases := flags.Int("min", defaultMinCases, "fewest cases a passing report holds")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		return verify(flags.Arg(0), *minCases, out)
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func echo(c *ws.Conn) {
	for {
		op, data, err := c.ReadMessage()
		if err != nil {
			return
		}
		// A failed write surfaces as the next read's error.
		c.WriteMessage(op, data)
	}
}

func handleEcho(w http.ResponseWriter, r *http.Request) {
	c, err := ws.Upgrade(w, r, ws.ServerOptions{
		Options:     suiteLimits,
		CheckOrigin: func(*http.Request) bool { return true },
	})
	if err != nil {
		return
	}
	echo(c)
}

func serve(ctx context.Context, addr string, out io.Writer) error {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: http.HandlerFunc(handleEcho), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(l)
	fmt.Fprintf(out, "listening %s\n", l.Addr())
	<-ctx.Done()
	return srv.Close()
}

func runCases(ctx context.Context, base, agent string, out io.Writer) error {
	if base == "" {
		return errors.New("client: -url is required")
	}
	opts := ws.ClientOptions{Options: suiteLimits}
	base = strings.TrimRight(base, "/")

	c, err := ws.Dial(ctx, base+"/getCaseCount", opts)
	if err != nil {
		return err
	}
	_, data, err := c.ReadMessage()
	if err != nil {
		return err
	}
	c.Close(ws.CloseNormal, "")
	count, err := strconv.Atoi(string(data))
	if err != nil {
		return fmt.Errorf("case count: %w", err)
	}

	for i := 1; i <= count; i++ {
		query := url.Values{"case": {strconv.Itoa(i)}, "agent": {agent}}
		c, err := ws.Dial(ctx, base+"/runCase?"+query.Encode(), opts)
		if err != nil {
			return fmt.Errorf("case %d: %w", i, err)
		}
		echo(c)
	}

	c, err = ws.Dial(ctx, base+"/updateReports?"+url.Values{"agent": {agent}}.Encode(), opts)
	if err != nil {
		return err
	}
	c.Close(ws.CloseNormal, "")
	fmt.Fprintf(out, "ran %d cases\n", count)
	return nil
}

type caseResult struct {
	Behavior      string `json:"behavior"`
	BehaviorClose string `json:"behaviorClose"`
}

func verify(path string, minCases int, out io.Writer) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var report map[string]map[string]caseResult
	if err := json.Unmarshal(data, &report); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	total := 0
	var failed []string
	for agent, cases := range report {
		for id, result := range cases {
			total++
			behaviorOK := slices.Contains([]string{"OK", "NON-STRATEGIC", "INFORMATIONAL"}, result.Behavior)
			closeOK := slices.Contains([]string{"OK", "INFORMATIONAL"}, result.BehaviorClose)
			if !behaviorOK || !closeOK {
				failed = append(failed, fmt.Sprintf("%s %s: %s/%s", agent, id, result.Behavior, result.BehaviorClose))
			}
		}
	}
	if len(failed) > 0 {
		slices.Sort(failed)
		return fmt.Errorf("%d cases failed:\n%s", len(failed), strings.Join(failed, "\n"))
	}
	if total < minCases {
		return fmt.Errorf("report holds %d cases, want at least %d", total, minCases)
	}
	fmt.Fprintf(out, "%d cases passed\n", total)
	return nil
}
