package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PRs is the pull request watcher (`herdsman watch prs`, from
// watch-prs.sh). It polls every HERDSMAN_PR_INTERVAL seconds (default 120)
// and returns on the first change on any watched pull request: a new
// comment, inline comment or review (by id), a new head, a state change, or
// a check whose newest run's result changes. Only failures, and the checks
// named in HERDSMAN_PR_WAKE_CHECKS reaching SUCCESS, wake it among the
// checks. The first pass per pull request records a baseline (a merged one
// is reported once). It returns 0 with "TICK ..." after HERDSMAN_PR_DEADLINE
// seconds, and 1 with "PR-WATCH-ERROR ..." after three failed polls of a
// pull request in a row or without a gh token. gh's JSON is read here, not
// with --jq.
func PRs(ctx context.Context, env Env, d Deps) int {
	d.defaults(ctx)
	for _, k := range []string{"HERDSMAN_PRS", "HERDSMAN_REPO", "HERDSMAN_PR_STATE"} {
		if env.get(k) == "" {
			fmt.Fprintf(d.Stderr, "herdsman watch prs: %s: set %s\n", k, k)
			return 1
		}
	}
	prs := strings.Fields(env.get("HERDSMAN_PRS"))
	repo := env.get("HERDSMAN_REPO")
	st := state(env.get("HERDSMAN_PR_STATE"))
	interval := atoi(env.or("HERDSMAN_PR_INTERVAL", "120"))
	deadlineText := env.or("HERDSMAN_PR_DEADLINE", "1500")
	deadline := testInt(deadlineText)
	wake := env.or("HERDSMAN_PR_WAKE_CHECKS", "claude-review|test")
	_ = os.MkdirAll(string(st), 0o755)
	var ghEnv []string
	if account := env.get("HERDSMAN_GH_ACCOUNT"); account != "" {
		tok, err := d.Gh(ctx, nil, "auth", "token", "--user", account)
		if err != nil {
			fmt.Fprintf(d.Stdout, "PR-WATCH-ERROR no gh token for %s\n", account)
			return 1
		}
		ghEnv = []string{"GH_TOKEN=" + strings.TrimSpace(string(tok))}
	}
	wanted := regexp.MustCompile(`^check .*=(FAILURE|CANCELLED|TIMED_OUT|ACTION_REQUIRED|STARTUP_FAILURE|ERROR)$|^check (` + wake + `)=SUCCESS$`)
	start := d.Now()
	for {
		var events []string
		for _, n := range prs {
			snap, err := snapshot(ctx, d, ghEnv, repo, n)
			if err != nil {
				// A failed read is not "no change". Count the failed polls in a row.
				failed := atoi(st.read("fail-"+n)) + 1
				st.write("fail-"+n, strconv.Itoa(failed)+"\n")
				if failed >= 3 {
					for _, e := range events {
						fmt.Fprintln(d.Stdout, e)
					}
					fmt.Fprintf(d.Stdout, "PR-WATCH-ERROR PR %s could not be read\n", n)
					return 1
				}
				continue
			}
			st.remove("fail-" + n)
			old := "pr-" + n + ".txt"
			if !st.has(old) {
				st.write(old, strings.Join(snap, "\n")+"\n")
				// A baseline is silent, except for a pull request that is already merged.
				for _, l := range snap {
					if l == "state MERGED" {
						events = append(events, fmt.Sprintf("PR %s NEW state MERGED", n))
					}
				}
				continue
			}
			added := added(strings.Split(strings.TrimRight(st.read(old), "\n"), "\n"), snap)
			if len(added) == 0 {
				continue
			}
			st.write(old, strings.Join(snap, "\n")+"\n")
			// Wake only on what needs a decision: comments, reviews, heads, state,
			// failed checks, and the named checks reaching SUCCESS.
			var checks []string
			for _, l := range added {
				if strings.HasPrefix(l, "check ") {
					if wanted.MatchString(l) {
						checks = append(checks, l)
					}
					continue
				}
				events = append(events, fmt.Sprintf("PR %s NEW %s", n, l))
			}
			for _, l := range checks {
				events = append(events, fmt.Sprintf("PR %s NEW %s", n, l))
			}
		}
		if len(events) > 0 {
			for _, e := range events {
				fmt.Fprintln(d.Stdout, e)
			}
			fmt.Fprintf(d.Stdout, "at %s\n", clock(d.Now()))
			return 0
		}
		if int(d.Now().Sub(start)/time.Second) >= deadline {
			var unread []string
			for _, n := range prs {
				if st.has("fail-" + n) {
					unread = append(unread, n)
				}
			}
			note := ""
			if len(unread) > 0 {
				note = " (not read: " + strings.Join(unread, " ") + ")"
			}
			fmt.Fprintf(d.Stdout, "TICK no PR change%s in %ss %s\n", note, deadlineText, clock(d.Now()))
			return 0
		}
		if !d.Sleep(time.Duration(interval) * time.Second) {
			return 0
		}
	}
}

// added is comm -13 of two line lists: the lines of b that a lacks,
// counting repeats.
func added(a, b []string) []string {
	have := map[string]int{}
	for _, l := range a {
		have[l]++
	}
	var out []string
	for _, l := range b {
		if have[l] > 0 {
			have[l]--
			continue
		}
		out = append(out, l)
	}
	return out
}

// snapshot reads one pull request: its comments, inline comments and
// reviews by id, its state, head and the newest run of each check, as
// sorted lines.
func snapshot(ctx context.Context, d Deps, env []string, repo, n string) ([]string, error) {
	var lines []string
	for _, q := range []struct{ path, kind string }{
		{"repos/" + repo + "/issues/" + n + "/comments", "comment"},
		{"repos/" + repo + "/pulls/" + n + "/comments", "inline"},
		{"repos/" + repo + "/pulls/" + n + "/reviews", "review"},
	} {
		out, err := d.Gh(ctx, env, "api", q.path, "--paginate")
		if err != nil {
			return nil, err
		}
		items, err := pages(out)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			user, _ := it["user"].(map[string]any)
			l := q.kind + " " + str(it["id"]) + " " + str(user["login"])
			switch q.kind {
			case "inline":
				l += " " + str(it["path"])
			case "review":
				l += " " + str(it["state"])
			}
			lines = append(lines, l)
		}
	}
	out, err := d.Gh(ctx, env, "pr", "view", n, "--repo", repo, "--json", "state,headRefOid,statusCheckRollup")
	if err != nil {
		return nil, err
	}
	view, err := prView(out)
	if err != nil {
		return nil, err
	}
	lines = append(lines, view...)
	var kept []string
	for _, l := range lines {
		if l != "" {
			kept = append(kept, l)
		}
	}
	sort.Strings(kept)
	return kept, nil
}

// pages reads the objects of gh's --paginate output: one JSON array per page.
func pages(b []byte) ([]map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out []map[string]any
	for {
		var page []map[string]any
		if err := dec.Decode(&page); errors.Is(err, io.EOF) {
			return out, nil
		} else if err != nil {
			return nil, err
		}
		out = append(out, page...)
	}
}

// prView reads `gh pr view --json state,headRefOid,statusCheckRollup`:
// "state X", "head <8 chars>", and per check name and workflow the result of
// its newest run (by start time, then end time).
func prView(b []byte) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v struct {
		State  any              `json:"state"`
		Head   any              `json:"headRefOid"`
		Rollup []map[string]any `json:"statusCheckRollup"`
	}
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	head := str(v.Head)
	if r := []rune(head); len(r) > 8 {
		head = string(r[:8])
	}
	lines := []string{"state " + str(v.State), "head " + head}
	type group struct {
		key  [2]any
		runs []map[string]any
	}
	var groups []*group
	for _, run := range v.Rollup {
		key := [2]any{alt(run["name"], run["context"]), alt(run["workflowName"], "")}
		var g *group
		for _, x := range groups {
			if jqCompare(x.key[0], key[0]) == 0 && jqCompare(x.key[1], key[1]) == 0 {
				g = x
			}
		}
		if g == nil {
			g = &group{key: key}
			groups = append(groups, g)
		}
		g.runs = append(g.runs, run)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if c := jqCompare(groups[i].key[0], groups[j].key[0]); c != 0 {
			return c < 0
		}
		return jqCompare(groups[i].key[1], groups[j].key[1]) < 0
	})
	for _, g := range groups {
		sort.SliceStable(g.runs, func(i, j int) bool {
			a, b := g.runs[i], g.runs[j]
			if c := jqCompare(a["startedAt"], b["startedAt"]); c != 0 {
				return c < 0
			}
			return jqCompare(a["completedAt"], b["completedAt"]) < 0
		})
		last := g.runs[len(g.runs)-1]
		result := alt(alt(last["conclusion"], last["state"]), last["status"])
		lines = append(lines, "check "+str(alt(last["name"], last["context"]))+"="+str(result))
	}
	return lines, nil
}

// alt is jq's a // b: b when a is null or false.
func alt(a, b any) any {
	if a == nil || a == false {
		return b
	}
	return a
}

// str prints a value as jq's string interpolation does.
func str(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case json.Number:
		return x.String()
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// jqCompare orders values as jq does: null, false, true, numbers, strings,
// then anything else.
func jqCompare(a, b any) int {
	rank := func(v any) int {
		switch x := v.(type) {
		case nil:
			return 0
		case bool:
			if !x {
				return 1
			}
			return 2
		case json.Number:
			return 3
		case string:
			return 4
		}
		return 5
	}
	ra, rb := rank(a), rank(b)
	if ra != rb {
		return ra - rb
	}
	switch x := a.(type) {
	case json.Number:
		fa, _ := x.Float64()
		fb, _ := b.(json.Number).Float64()
		switch {
		case fa < fb:
			return -1
		case fa > fb:
			return 1
		}
	case string:
		return strings.Compare(x, b.(string))
	}
	return 0
}

// RunGh runs the gh program: its stdout is returned, its stderr goes to stderr.
func RunGh(stderr io.Writer) func(ctx context.Context, env []string, args ...string) ([]byte, error) {
	return func(ctx context.Context, env []string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "gh", args...)
		cmd.Env = append(os.Environ(), env...)
		cmd.Stderr = stderr
		return cmd.Output()
	}
}
