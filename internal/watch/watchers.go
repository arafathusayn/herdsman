package watch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	implBlockRE = posix(`Password for|Device not configured|Allow once|Allow always|Yes, proceed|approval required|Switch to gpt|Usage limit|Do you trust|Permission required|Queued follow-up inputs|Type your answer`)
	implWorkRE  = posix(`esc to interrupt|Pursuing goal`)
	revBlockRE  = posix(`Password for|Device not configured|Allow reads outside|Do you want to proceed|Yes, and don|usage limit|rate limit|Permission required`)
	revWorkRE   = posix(`esc to interrupt|thinking|Incubating|tokens|Pursuing goal`)
)

// The poll intervals of the watchers. The reviewer's is 60 s or less: a
// permission prompt closes itself after about a minute.
const (
	ImplementersPoll = 120 * time.Second
	ReviewerPoll     = 60 * time.Second
)

// Implementers is the implementer and integrator watcher (`herdsman watch
// implementers`, from watch-implementers.sh). It never ends on its own:
// REPORT, REPORT-UPDATED, BLOCKED (once per screen change), STALL and IDLE
// lines, every ImplementersPoll.
func Implementers(ctx context.Context, env Env, d Deps) int {
	d.defaults(ctx)
	reports := env.or("HERDSMAN_REPORTS", "/ABSOLUTE/PATH/TO/writable-root/reports")
	st := state(env.or("HERDSMAN_STATE", "/ABSOLUTE/PATH/TO/scratchpad/watch-state"))
	_ = os.MkdirAll(string(st), 0o755)
	for {
		for _, name := range watched(env) {
			task := strings.TrimPrefix(name, "im-")
			report := filepath.Join(reports, task+".md")
			if isFile(report) {
				m := mtime(report)
				prevm := st.read("mtime-" + task)
				st.write("mtime-"+task, m)
				if !st.has("report-" + task) {
					st.touch("report-" + task)
					fmt.Fprintf(d.Stdout, "REPORT %s %s %s\n", task, report, clock(d.Now()))
				} else if prevm != "" && m != prevm {
					fmt.Fprintf(d.Stdout, "REPORT-UPDATED %s %s %s\n", task, report, clock(d.Now()))
				}
			}
			pane := paneOf(env, name)
			text := readPane(ctx, d.Client, pane, 40)
			h := hash(text)
			prev := st.read("hash-" + name)
			st.write("hash-"+name, h)
			reason, blocked := blockReason(ctx, d.Client, pane, text, implBlockRE)
			switch {
			case blocked:
				if h != prev {
					fmt.Fprintf(d.Stdout, "BLOCKED %s %s %s\n", name, reason, clock(d.Now()))
				}
			case implWorkRE.MatchString(text):
				if h == prev {
					fmt.Fprintf(d.Stdout, "STALL %s screen unchanged %s\n", name, clock(d.Now()))
				}
			default:
				if h != prev {
					fmt.Fprintf(d.Stdout, "IDLE %s %s\n", name, clock(d.Now()))
				}
			}
		}
		if !d.Sleep(ImplementersPoll) {
			return 0
		}
	}
}

// Reviewer is the reviewer watcher (`herdsman watch reviewer`, from
// watch-reviewer.sh). It never ends on its own: REVIEW, BLOCKED and IDLE
// lines, every ReviewerPoll.
func Reviewer(ctx context.Context, env Env, d Deps) int {
	d.defaults(ctx)
	reports := env.or("HERDSMAN_REPORTS", "/ABSOLUTE/PATH/TO/writable-root/reports")
	st := state(env.or("HERDSMAN_STATE", "/ABSOLUTE/PATH/TO/scratchpad/watch-state"))
	pane := env.or("HERDSMAN_REVIEWER_PANE", "w2:pR")
	name := env.or("HERDSMAN_REVIEWER_NAME", "rv-1")
	_ = os.MkdirAll(string(st), 0o755)
	for {
		for _, f := range reviews(reports) {
			key := filepath.Base(f)
			if !st.has("seen-" + key) {
				st.touch("seen-" + key)
				fmt.Fprintf(d.Stdout, "REVIEW %s %s\n", f, clock(d.Now()))
			}
		}
		text := readPane(ctx, d.Client, pane, 30)
		h := hash(text)
		prev := st.read("hash-" + name)
		st.write("hash-"+name, h)
		// Once per screen change, like the waiter: an unchanged prompt on screen is the same event.
		if reason, blocked := blockReason(ctx, d.Client, pane, text, revBlockRE); blocked {
			if h != prev {
				fmt.Fprintf(d.Stdout, "BLOCKED %s %s %s\n", name, reason, clock(d.Now()))
			}
		} else if !revWorkRE.MatchString(text) && h != prev {
			fmt.Fprintf(d.Stdout, "IDLE %s %s\n", name, clock(d.Now()))
		}
		if !d.Sleep(ReviewerPoll) {
			return 0
		}
	}
}
