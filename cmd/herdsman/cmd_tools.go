package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/arafathusayn/herdsman/internal/doctor"
	"github.com/arafathusayn/herdsman/internal/review"
)

func init() {
	register(command{name: "doctor", summary: "Check that this machine can run a route: herdr, gh, the binary, the inbox, the sandbox, the load.", run: runDoctor})
	register(command{name: "review", summary: "Prepare a reviewer: herdsman review start --worktree <path> --head <commit>.", run: runReview})
}

const doctorUsage = `usage: herdsman doctor [--json]

Prints one line per check, "ok|warn|fail <check> <detail>": herdr, gh,
binary, inbox (when HERDSMAN_INBOX is set), sandbox and load
(HERDSMAN_LOAD_LIMIT, default the core count; HERDSMAN_MIN_FREE, default 10).
--json prints one object {"ok": bool, "checks": [{"level","check","detail"}]}.

Exit codes:
  0  no check failed
  1  a check failed
  2  usage error or an invalid setting
`

// doctorProbes are the doctor's probes; tests replace them.
var doctorProbes = doctor.Default

// doctorReport is doctor's --json output.
//
// JSON fields: ok (no check failed) and checks (each a doctor.Check).
type doctorReport struct {
	OK     bool           `json:"ok"`
	Checks []doctor.Check `json:"checks"`
}

func runDoctor(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, doctorUsage)
			return 0
		}
		fmt.Fprint(stderr, doctorUsage)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	checks, err := doctor.Run(ctx, doctorProbes())
	if err != nil {
		fmt.Fprintf(stderr, "herdsman doctor: %v\n", err)
		return 2
	}
	if *asJSON {
		b, _ := json.Marshal(doctorReport{OK: !doctor.Failed(checks), Checks: checks})
		fmt.Fprintln(stdout, string(b))
	} else {
		for _, c := range checks {
			fmt.Fprintf(stdout, "%s %s %s\n", c.Level, c.Name, c.Detail)
		}
	}
	if doctor.Failed(checks) {
		return 1
	}
	return 0
}

const reviewUsage = `usage: herdsman review start --worktree <path> --head <commit> [--repo <git dir or checkout>] [--cache-root <dir>] [--json]

Makes the worktree detached at <commit> (git worktree add --detach), or
checks <commit> out detached in it when it exists and has no changes, and
makes <cache-root>/<worktree name>-go-build and -staticcheck (default cache
root: the system temp folder, /private/tmp on macOS). Prints worktree=,
head=, GOCACHE= and STATICCHECK_CACHE= lines; --json prints one object
{"worktree","head","gocache","staticcheck_cache"}.

Exit codes:
  0  ready
  1  the worktree has changes (they are listed), the head is no commit, or git failed
  2  usage error
`

func runReview(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		fmt.Fprint(stdout, reviewUsage)
		return 0
	}
	if len(args) == 0 || args[0] != "start" {
		fmt.Fprint(stderr, reviewUsage)
		return 2
	}
	fs := flag.NewFlagSet("review start", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var o review.Options
	fs.StringVar(&o.Worktree, "worktree", "", "")
	fs.StringVar(&o.Head, "head", "", "")
	fs.StringVar(&o.Repo, "repo", "", "")
	fs.StringVar(&o.CacheRoot, "cache-root", "", "")
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 || o.Worktree == "" || o.Head == "" {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, reviewUsage)
			return 0
		}
		fmt.Fprint(stderr, reviewUsage)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	r, err := review.Start(ctx, o)
	if err != nil {
		fmt.Fprintf(stderr, "herdsman review start: %v\n", err)
		return 1
	}
	if *asJSON {
		b, _ := json.Marshal(r)
		fmt.Fprintln(stdout, string(b))
		return 0
	}
	for _, l := range r.Lines() {
		fmt.Fprintln(stdout, l)
	}
	return 0
}
