package watch

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

var (
	implBlockRE = posix(`Password for|Device not configured|Allow once|Allow always|Yes, proceed|approval required|Switch to [[:alnum:]][[:alnum:]._-]*|Usage limit|Do you trust|Permission required|Queued follow-up inputs|Type your answer`)
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
// lines, every ImplementersPoll. A pass's lines are printed after its
// state is saved; state it cannot save stops it with exit 1.
func Implementers(ctx context.Context, env Env, d Deps) int {
	d.defaults(ctx)
	reports := env.or("HERDSMAN_REPORTS", "/ABSOLUTE/PATH/TO/writable-root/reports")
	st, err := openState(env.or("HERDSMAN_STATE", "/ABSOLUTE/PATH/TO/scratchpad/watch-state"))
	if err != nil {
		fmt.Fprintf(d.Stderr, "herdsman watch implementers: cannot save state: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var panes []string
	var targets []target
	for _, name := range watched(env) {
		panes = append(panes, paneOf(env, name))
		targets = append(targets, target{name, paneOf(env, name)})
	}
	d.wakeOn(ctx, panes)
	for {
		var out []string
		say := func(format string, a ...any) { out = append(out, fmt.Sprintf(format, a...)) }
		for _, name := range watched(env) {
			task := strings.TrimPrefix(name, "im-")
			report := filepath.Join(reports, task+".md")
			if isFile(report) {
				m := mtime(report)
				prevm := st.read("mtime-" + task)
				st.write("mtime-"+task, m)
				if !st.has("report-" + task) {
					st.touch("report-" + task)
					say("REPORT %s %s %s", task, report, clock(d.Now()))
				} else if prevm != "" && m != prevm {
					say("REPORT-UPDATED %s %s %s", task, report, clock(d.Now()))
				}
			}
			pane := paneOf(env, name)
			text := readPane(ctx, d.Client, pane, 40)
			h := hash(text)
			prev := st.read("hash-" + name)
			st.write("hash-"+name, h)
			reason, blocked := screenBlock(agentStatus(ctx, d.Client, pane), text, implBlockRE, implWorkRE, nil)
			tell := newBlock(st, name, blocked, h, prev)
			switch {
			case blocked:
				if tell {
					say("BLOCKED %s %s %s", name, reason, clock(d.Now()))
				}
			case implWorkRE.MatchString(text):
				if h == prev {
					say("STALL %s screen unchanged %s", name, clock(d.Now()))
				}
			default:
				if h != prev {
					say("IDLE %s %s", name, clock(d.Now()))
				}
			}
		}
		if !printSaved(d, st, "watch implementers", out) {
			return 1
		}
		// The watcher never ends on its own: an event's BLOCKED lines are
		// printed and the sleep goes on to the next scheduled sample.
		if code, stop := d.sleepPass(d.Now().Add(ImplementersPoll), st, "watch implementers", func() []string {
			return statusRecheck(ctx, d, st, targets, implBlockRE, implWorkRE, nil)
		}); stop {
			return code
		}
	}
}

// Reviewer is the reviewer watcher (`herdsman watch reviewer`, from
// watch-reviewer.sh). It never ends on its own: REVIEW, BLOCKED and IDLE
// lines, every ReviewerPoll. State it cannot save stops it with exit 1.
func Reviewer(ctx context.Context, env Env, d Deps) int {
	d.defaults(ctx)
	reports := env.or("HERDSMAN_REPORTS", "/ABSOLUTE/PATH/TO/writable-root/reports")
	pane := env.or("HERDSMAN_REVIEWER_PANE", "w2:pR")
	name := env.or("HERDSMAN_REVIEWER_NAME", "rv-1")
	st, err := openState(env.or("HERDSMAN_STATE", "/ABSOLUTE/PATH/TO/scratchpad/watch-state"))
	if err != nil {
		fmt.Fprintf(d.Stderr, "herdsman watch reviewer: cannot save state: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.wakeOn(ctx, []string{pane})
	for {
		var out []string
		for _, f := range reviews(reports) {
			key := filepath.Base(f)
			if !st.has("seen-" + key) {
				st.touch("seen-" + key)
				out = append(out, fmt.Sprintf("REVIEW %s %s", f, clock(d.Now())))
			}
		}
		text := readPane(ctx, d.Client, pane, 30)
		h := hash(text)
		prev := st.read("hash-" + name)
		st.write("hash-"+name, h)
		// Once per screen change, like the waiter: an unchanged prompt on screen is the same event.
		reason, blocked := screenBlock(agentStatus(ctx, d.Client, pane), text, revBlockRE, revWorkRE, nil)
		tell := newBlock(st, name, blocked, h, prev)
		if blocked {
			if tell {
				out = append(out, fmt.Sprintf("BLOCKED %s %s %s", name, reason, clock(d.Now())))
			}
		} else if !revWorkRE.MatchString(text) && h != prev {
			out = append(out, fmt.Sprintf("IDLE %s %s", name, clock(d.Now())))
		}
		if !printSaved(d, st, "watch reviewer", out) {
			return 1
		}
		if code, stop := d.sleepPass(d.Now().Add(ReviewerPoll), st, "watch reviewer", func() []string {
			return statusRecheck(ctx, d, st, []target{{name, pane}}, revBlockRE, revWorkRE, nil)
		}); stop {
			return code
		}
	}
}

// printSaved prints a pass's lines once its state is saved, and says
// false (with the state error on stderr) when it is not.
func printSaved(d Deps, st *state, who string, lines []string) bool {
	if stateFailed(st, d.Stderr, who) {
		return false
	}
	for _, l := range lines {
		fmt.Fprintln(d.Stdout, l)
	}
	return true
}

// sleepPass sleeps to next, printing the BLOCKED lines of each event's
// recheck as it comes. It says stop, with the exit code, when the sleep
// was stopped (0) or a recheck's state could not be saved (1).
func (d Deps) sleepPass(next time.Time, st *state, who string, recheck func() []string) (int, bool) {
	for {
		ok, lines := d.sleepUntil(next, recheck)
		for i, l := range lines {
			lines[i] = l + " " + clock(d.Now())
		}
		if !printSaved(d, st, who, lines) {
			return 1, true
		}
		if !ok {
			return 0, true
		}
		if len(lines) == 0 {
			return 0, false
		}
	}
}
