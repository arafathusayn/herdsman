package watch

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/internal/status"

	"github.com/arafathusayn/herdsman/internal/settings"
	"github.com/arafathusayn/herdsman/internal/shnum"
)

// The waiter's screen patterns.
var (
	waitBlockRE = posix(`Password for|Device not configured|Allow once|Allow always|Yes, proceed|approval required|Switch to gpt|Usage limit|Queued follow-up inputs|Type your answer|Allow reads outside|Do you want to proceed|Permission required|Do you trust`)
	// Claude Code shows "… (12s ·" while it works; Codex shows "esc to interrupt" or "Pursuing goal".
	waitWorkRE = posix(`esc to interrupt|Pursuing goal|Incubating|thinking|… \([0-9]`)
	// A Codex footer can keep this text while the agent works, so it counts as a block only where no work text shows.
	waitStalledRE = posix(`Goal stalled|Apply for Daybreak`)
)

// Wait is the one-shot waiter (`herdsman wait`, from wait-event.sh): it
// polls every POLL seconds and returns after the first pass with an event,
// printing every event of that pass, or after DEADLINE seconds with a TICK
// line. Events: REPORT, REPORT-UPDATED, REVIEW, BLOCKED, GOAL-DONE-NO-REPORT,
// STALL, OVERDUE, HEALTH, each ending in the time. It returns 0.
//
// The health check runs when HERDSMAN_STATUS_SPECS is set, every
// HERDSMAN_HEALTH_EVERY seconds (the old name HEALTH_EVERY is still read;
// default 300). Without HERDSMAN_STATUS_SCRIPT it takes the snapshot in
// this process (Deps.Status); with it, it runs that program directly (no
// shell), which must be executable, and reads its output as a snapshot.
func Wait(ctx context.Context, env Env, d Deps) int {
	d.defaults(ctx)
	healthName := "HERDSMAN_HEALTH_EVERY"
	if env.get(healthName) == "" && env.get("HEALTH_EVERY") != "" {
		healthName = "HEALTH_EVERY"
	}
	durs, err := readSettings(env, []durSpec{{"DEADLINE", 1500 * time.Second, false}, {"POLL", 60 * time.Second, true}, {healthName, 300 * time.Second, false}})
	dues := map[string]int64{}
	if err == nil {
		for _, name := range watched(env) {
			k := "HERDSMAN_DUE_" + underscore(name)
			if v := env.get(k); v != "" {
				dues[name], err = settings.Int(k, v, 0, 0, math.MaxInt64)
				if err != nil {
					break
				}
			}
		}
	}
	if err != nil {
		fmt.Fprintf(d.Stderr, "herdsman wait: %v\n", err)
		return settings.ExitInvalid
	}
	deadline, poll, healthEvery := durs[0], durs[1], durs[2]
	reports := env.or("HERDSMAN_REPORTS", "/ABSOLUTE/PATH/TO/writable-root/reports")
	st := state(env.or("HERDSMAN_STATE", "/ABSOLUTE/PATH/TO/scratchpad/watch-state"))
	_ = os.MkdirAll(string(st), 0o755)
	rpane := env.or("HERDSMAN_REVIEWER_PANE", "w2:pR")
	rname := env.or("HERDSMAN_REVIEWER_NAME", "rv-1")
	reviewers := strings.Fields(env.or("HERDSMAN_REVIEWERS", rname+":"+rpane))
	start := d.Now()
	for {
		var events []string
		now := clock(d.Now())
		for _, name := range watched(env) {
			task := env.or("HERDSMAN_TASK_"+underscore(name), strings.TrimPrefix(name, "im-"))
			report := filepath.Join(reports, task+".md")
			hasReport := false
			if isFile(report) {
				hasReport = true
				m := mtime(report)
				prevm := st.read("mtime-" + task)
				st.write("mtime-"+task, m)
				if !st.has("report-" + task) {
					st.touch("report-" + task)
					events = append(events, fmt.Sprintf("REPORT %s %s %s", task, report, now))
				} else if prevm != "" && m != prevm {
					events = append(events, fmt.Sprintf("REPORT-UPDATED %s %s %s", task, report, now))
				}
			}
			// Agents ignore stop times written in their own briefs, so the waiter
			// enforces the due time. The baseline is the report's mtime when this
			// due time was first seen, so a fix round whose report already exists
			// is overdue until the report changes, and a new due time re-arms.
			if due := env.get("HERDSMAN_DUE_" + underscore(name)); due != "" {
				cur := mtime(report)
				if cur == "" {
					cur = "none"
				}
				if st.read("due-"+name) != due {
					st.write("due-"+name, due)
					st.write("duebase-"+name, cur)
					st.remove("overdue-" + name)
				}
				if at := dues[name]; d.Now().Unix() >= at &&
					!st.has("overdue-"+name) && cur == st.read("duebase-"+name) {
					st.touch("overdue-" + name)
					events = append(events, fmt.Sprintf("OVERDUE %s due %s no report since the due time was set %s", name, time.Unix(at, 0).Format("15:04"), now))
				}
			}
			text := readPane(ctx, d.Client, paneOf(env, name), 40)
			h := hash(text)
			prev := st.read("hash-" + name)
			st.write("hash-"+name, h)
			switch {
			case waitBlockRE.MatchString(text):
				if h != prev {
					events = append(events, fmt.Sprintf("BLOCKED %s %s %s", name, first(waitBlockRE, text), now))
				}
				st.write("stall-"+name, "0")
			case waitWorkRE.MatchString(text):
				st.remove("goaldone-" + name)
				if h == prev {
					c := count(st, "stall-"+name, d.Stderr, "wait") + 1
					st.write("stall-"+name, strconv.Itoa(c))
					if c >= 3 {
						st.write("stall-"+name, "0")
						events = append(events, fmt.Sprintf("STALL %s screen unchanged for 3 polls %s", name, now))
					}
				} else {
					st.write("stall-"+name, "0")
				}
			default:
				st.write("stall-"+name, "0")
				if h != prev && waitStalledRE.MatchString(text) {
					events = append(events, fmt.Sprintf("BLOCKED %s %s %s", name, first(waitStalledRE, text), now))
				}
				if !hasReport && strings.Contains(text, "Goal achieved") && !st.has("goaldone-"+name) {
					st.touch("goaldone-" + name)
					events = append(events, fmt.Sprintf("GOAL-DONE-NO-REPORT %s %s", name, now))
				}
			}
		}
		for _, f := range reviews(reports) {
			key := filepath.Base(f)
			if !st.has("seen-" + key) {
				st.touch("seen-" + key)
				events = append(events, fmt.Sprintf("REVIEW %s %s", f, now))
			}
		}
		for _, spec := range reviewers {
			name, pane, _ := strings.Cut(spec, ":")
			if !strings.Contains(spec, ":") {
				pane = spec
			}
			text := readPane(ctx, d.Client, pane, 30)
			h := hash(text)
			prev := st.read("hash-" + name)
			st.write("hash-"+name, h)
			if waitBlockRE.MatchString(text) && h != prev {
				events = append(events, fmt.Sprintf("BLOCKED %s %s %s", name, first(waitBlockRE, text), now))
			} else if !waitWorkRE.MatchString(text) && waitStalledRE.MatchString(text) && h != prev {
				events = append(events, fmt.Sprintf("BLOCKED %s %s %s", name, first(waitStalledRE, text), now))
			}
		}
		// Health: the user's periodic check of every agent runs here, so a quiet
		// route needs no orchestrator turn. Each flag is reported once, until it clears.
		if specs := env.get("HERDSMAN_STATUS_SPECS"); specs != "" {
			events = append(events, health(ctx, env, d, st, specs, healthEvery, now)...)
		}
		if len(events) > 0 {
			for _, e := range events {
				fmt.Fprintln(d.Stdout, e)
			}
			return 0
		}
		elapsed := d.Now().Sub(start)
		if elapsed >= deadline {
			fmt.Fprintf(d.Stdout, "TICK no event in %ds %s\n", int64(elapsed/time.Second), now)
			return 0
		}
		if !d.Sleep(poll) {
			return 0
		}
	}
}

// health runs the status check when it is due and returns the HEALTH
// events of the flags that are new since the last check.
func health(ctx context.Context, env Env, d Deps, st state, specs string, every time.Duration, now string) []string {
	script, scripted := env("HERDSMAN_STATUS_SCRIPT")
	if scripted && script == "" {
		scripted = false
	}
	if scripted && !isFile(script) {
		return nil // as the script: no status program, no health check
	}
	last := st.read("health-at")
	at := d.Now().Unix()
	if last == "" {
		st.write("health-at", strconv.FormatInt(at, 10)+"\n")
		return nil
	}
	// The script's $(( now - last )) ended the whole pass on a time bash
	// could not read (A26): the clock starts again instead, with a line.
	lastAt, ok := shnum.Test(strings.TrimSpace(last))
	if !ok {
		fmt.Fprintf(d.Stderr, "herdsman wait: state file health-at is not a number (got %q); the health clock starts again\n", strings.TrimSpace(last))
		st.write("health-at", strconv.FormatInt(at, 10)+"\n")
		return nil
	}
	if at-lastAt < int64(every/time.Second) {
		return nil
	}
	var out bytes.Buffer
	if scripted {
		if syscall.Access(script, 1) != nil { // X_OK
			fmt.Fprintf(d.Stderr, "herdsman wait: HERDSMAN_STATUS_SCRIPT %s is not executable; no health check\n", script)
			return nil
		}
		st.write("health-at", strconv.FormatInt(at, 10)+"\n")
		cmd := exec.CommandContext(ctx, script)
		cmd.Stdout = &out
		_ = cmd.Run()
	} else {
		st.write("health-at", strconv.FormatInt(at, 10)+"\n")
		if d.Status != nil {
			d.Status(ctx, &out, specs)
		}
	}
	flags := status.Flags(out.String())
	prev := strings.Split(st.read("health-flags"), "\n")
	st.write("health-flags", strings.Join(flags, "\n")+"\n")
	var events []string
	for _, f := range flags {
		if f != "" && !slices.Contains(prev, f) {
			events = append(events, fmt.Sprintf("HEALTH %s %s", f, now))
		}
	}
	return events
}
