package watch

import (
	"bufio"
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

	"github.com/arafathusayn/herdsman/internal/settings"
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
	durs, err := readSettings(env, []durSpec{{"HERDSMAN_PR_INTERVAL", 120 * time.Second, true}, {"HERDSMAN_PR_DEADLINE", 1500 * time.Second, false}})
	wake := env.or("HERDSMAN_PR_WAKE_CHECKS", "claude-review|test")
	var wanted *regexp.Regexp
	if err == nil {
		// The script handed this to grep -E; a pattern that does not compile is a bad setting.
		pattern := `^check .*=(FAILURE|CANCELLED|TIMED_OUT|ACTION_REQUIRED|STARTUP_FAILURE|ERROR)$|^check (` + wake + `)=SUCCESS$`
		if wanted, err = regexp.Compile(pattern); err != nil {
			err = &settings.Error{Var: "HERDSMAN_PR_WAKE_CHECKS", Reason: "not a pattern", Got: wake}
		}
	}
	if err != nil {
		fmt.Fprintf(d.Stderr, "herdsman watch prs: %v\n", err)
		return settings.ExitInvalid
	}
	interval, deadline := durs[0], durs[1]
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
	start := d.Now()
	for {
		var events []string
		for _, n := range prs {
			snap, err := snapshot(ctx, d, ghEnv, repo, n)
			if err != nil {
				// A failed read is not "no change". Count the failed polls in a row.
				failed := count(st, "fail-"+n, d.Stderr, "watch prs") + 1
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
			added := addedSince(st.path(old), snap)
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
		if d.Now().Sub(start) >= deadline {
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
			fmt.Fprintf(d.Stdout, "TICK no PR change%s in %ds %s\n", note, int64(deadline/time.Second), clock(d.Now()))
			return 0
		}
		if !d.Sleep(interval) {
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
		err := d.ghStream(ctx, env, func(r io.Reader) error {
			return streamPages(r, func(it ghItem) {
				l := q.kind + " " + str(it.ID) + " " + str(it.login())
				switch q.kind {
				case "inline":
					l += " " + str(it.Path)
				case "review":
					l += " " + str(it.State)
				}
				lines = append(lines, l)
			})
		}, "api", q.path, "--paginate")
		if err != nil {
			return nil, err
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

// ghItem is the part of a comment, inline comment or review the snapshot
// keeps; the body and every other field are skipped while decoding.
type ghItem struct {
	ID    any             `json:"id"`
	User  json.RawMessage `json:"user"`
	Path  any             `json:"path"`
	State any             `json:"state"`
}

func (it ghItem) login() any {
	var u struct {
		Login any `json:"login"`
	}
	if len(it.User) > 0 && it.User[0] == '{' {
		dec := json.NewDecoder(bytes.NewReader(it.User))
		dec.UseNumber()
		_ = dec.Decode(&u)
	}
	return u.Login
}

// streamPages reads gh's --paginate output (one JSON array per page) item
// by item, so a long history is never held whole (A24).
func streamPages(r io.Reader, fn func(ghItem)) error {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if tok == nil {
			continue // a null page
		}
		if d, ok := tok.(json.Delim); !ok || d != '[' {
			return fmt.Errorf("gh page: want an array, got %v", tok)
		}
		for dec.More() {
			var it ghItem
			if err := dec.Decode(&it); err != nil {
				return err
			}
			fn(it)
		}
		if _, err := dec.Token(); err != nil {
			return err
		}
	}
}

// ghStream runs gh and hands its output to read as a stream: GhStream when
// set, else Gh's output.
func (d Deps) ghStream(ctx context.Context, env []string, read func(io.Reader) error, args ...string) error {
	if d.GhStream != nil {
		return d.GhStream(ctx, env, read, args...)
	}
	out, err := d.Gh(ctx, env, args...)
	if err != nil {
		return err
	}
	return read(bytes.NewReader(out))
}

// RunGhStream runs the gh program and streams its stdout to read; its
// stderr goes to stderr.
func RunGhStream(stderr io.Writer) func(ctx context.Context, env []string, read func(io.Reader) error, args ...string) error {
	return func(ctx context.Context, env []string, read func(io.Reader) error, args ...string) error {
		cmd := exec.CommandContext(ctx, "gh", args...)
		cmd.Env = append(os.Environ(), env...)
		cmd.Stderr = stderr
		out, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		if err := cmd.Start(); err != nil {
			return err
		}
		rerr := read(out)
		if rerr != nil {
			_, _ = io.Copy(io.Discard, out)
		}
		if err := cmd.Wait(); err != nil {
			return err
		}
		return rerr
	}
}

// addedSince is comm -13 of the snapshot file at path and the sorted new
// lines, counting repeats: the file is streamed and merged against cur
// (A24: the prior history is never held whole). A file whose lines are not
// in byte order (an older writer's locale sort) falls back to counting
// every line; a missing file adds everything.
func addedSince(path string, cur []string) []string {
	f, err := os.Open(path)
	if err != nil {
		return append([]string(nil), cur...)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var out []string
	i, prev := 0, ""
	for sc.Scan() {
		l := sc.Text()
		if l == "" {
			continue
		}
		if l < prev {
			return added(strings.Split(strings.TrimRight(readAll(path), "\n"), "\n"), cur)
		}
		prev = l
		for i < len(cur) && cur[i] < l {
			out = append(out, cur[i])
			i++
		}
		if i < len(cur) && cur[i] == l {
			i++
		}
	}
	return append(out, cur[i:]...)
}

func readAll(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}
