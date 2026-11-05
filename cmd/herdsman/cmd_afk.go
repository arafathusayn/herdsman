package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/arafathusayn/herdsman/internal/afk"
)

func init() {
	register(command{name: "afk", summary: "Print how long the user has been away: machine idle time or the newest message, whichever is newer.", run: runAfk})
}

const afkUsage = `usage: herdsman afk [--last-message <unix time>]... [--json]

Prints "idle <seconds> source <ioreg|logind|message>": the smaller of this
machine's input idle time (macOS: ioreg's HIDIdleTime; Linux: logind) and
the time since the newest message (each --last-message, and the web app's
last user action), or "unknown <reason>". --json prints one object
{"idle_seconds","source"} or {"unknown"}, with "ask_timeout_seconds"
(HERDSMAN_ASK_TIMEOUT, default 5m: plain seconds or s, m, h, d; 1s to 1d).

Exit codes:
  0  printed (also when unknown)
  2  usage error or an invalid setting
`

// afkProbes are afk's probes; tests replace them.
var afkProbes = defaultAfkProbes

func defaultAfkProbes() afk.Probes {
	return afk.Probes{GOOS: runtime.GOOS, Run: afk.ExecRunner, Getenv: os.Getenv, Now: time.Now, UID: os.Getuid(), WebFile: afk.DefaultWebFile()}
}

// unixTimes collects --last-message values; bad holds the first refused one.
type unixTimes struct {
	times []time.Time
	bad   error
}

func (u *unixTimes) String() string { return "" }
func (u *unixTimes) Set(v string) error {
	t, err := afk.ParseUnix(v)
	if err != nil {
		u.bad = fmt.Errorf("%v (got %q)", err, v)
		return u.bad
	}
	u.times = append(u.times, t)
	return nil
}

func runAfk(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("afk", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var msgs unixTimes
	fs.Var(&msgs, "last-message", "")
	asJSON := fs.Bool("json", false, "")
	err := fs.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, afkUsage)
		return 0
	}
	if msgs.bad != nil {
		fmt.Fprintf(stderr, "herdsman afk: --last-message: %v\n", msgs.bad)
		return 2
	}
	if err != nil || fs.NArg() > 0 {
		fmt.Fprint(stderr, afkUsage)
		return 2
	}
	ask, err := afk.AskTimeout(os.Getenv("HERDSMAN_ASK_TIMEOUT"))
	if err != nil {
		fmt.Fprintf(stderr, "herdsman afk: %v\n", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r := afk.Check(ctx, afkProbes(), msgs.times)
	r.AskTimeout = int64(ask / time.Second)
	if *asJSON {
		b, _ := json.Marshal(r)
		fmt.Fprintln(stdout, string(b))
		return 0
	}
	fmt.Fprintln(stdout, r.Line())
	return 0
}
