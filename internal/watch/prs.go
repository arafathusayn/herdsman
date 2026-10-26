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
// pull request in a row or without a gh token; a missing or invalid
// setting exits 2 before any poll. gh's JSON is read here, not
// with --jq.
func PRs(ctx context.Context, env Env, d Deps) int {
	d.defaults(ctx)
	for _, k := range []string{"HERDSMAN_PRS", "HERDSMAN_REPO", "HERDSMAN_PR_STATE"} {
		if env.get(k) == "" {
			fmt.Fprintf(d.Stderr, "herdsman watch prs: %v\n", settings.Missing(k))
			return settings.ExitInvalid
		}
	}
	prs := strings.Fields(env.get("HERDSMAN_PRS"))
	repo := env.get("HERDSMAN_REPO")
	stateDir := env.get("HERDSMAN_PR_STATE")
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
	st, err := openState(stateDir)
	if err != nil {
		fmt.Fprintf(d.Stderr, "herdsman watch prs: cannot save state: %v\n", err)
		return 1
	}
	start := d.Now()
	// The script waited on gh for as long as it ran (A26): a gh that hung
	// held the watcher past its deadline. Each gh run gets what is left of
	// the deadline (helperLimit), and one that does not finish in it is a
	// failed read.
	gh := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(ctx, helperLimit(deadline-d.Now().Sub(start), interval))
	}
	var ghEnv []string
	if account := env.get("HERDSMAN_GH_ACCOUNT"); account != "" {
		actx, cancel := gh()
		tok, err := d.Gh(actx, nil, "auth", "token", "--user", account)
		cancel()
		if err != nil {
			fmt.Fprintf(d.Stdout, "PR-WATCH-ERROR no gh token for %s\n", account)
			return 1
		}
		ghEnv = []string{"GH_TOKEN=" + strings.TrimSpace(string(tok))}
	}
	for {
		var events []string
		for _, n := range prs {
			next := st.path("next-" + n + ".txt")
			old := "pr-" + n + ".txt"
			baseline := !st.has(old)
			pctx, cancel := gh()
			err := snapshotTo(pctx, d, ghEnv, repo, n, next)
			cancel()
			var added []string
			if err == nil && !baseline {
				// A comparison that fails (an old snapshot that cannot be
				// sorted, a read error) is a failed read too, never "no
				// change", and never a whole-history fallback (A24).
				added, err = addedSince(st.path(old), next)
			}
			if err != nil {
				// A failed read is not "no change". Count the failed polls in a
				// row; the last committed snapshot stays.
				_ = os.Remove(next)
				failed := count(st, "fail-"+n, d.Stderr, "watch prs") + 1
				st.write("fail-"+n, strconv.Itoa(failed)+"\n")
				if failed >= 3 {
					if stateFailed(st, d.Stderr, "watch prs") {
						return 1
					}
					for _, e := range events {
						fmt.Fprintln(d.Stdout, e)
					}
					fmt.Fprintf(d.Stdout, "PR-WATCH-ERROR PR %s could not be read\n", n)
					return 1
				}
				continue
			}
			if baseline || len(added) > 0 {
				// The script ignored a failed rename (A26): the snapshot was
				// never committed, and each run took a silent new baseline.
				if err := os.Rename(next, st.path(old)); err != nil {
					_ = os.Remove(next)
					st.fail(err)
					continue
				}
			} else {
				_ = os.Remove(next)
			}
			st.remove("fail-" + n)
			if baseline {
				// A baseline is silent, except for a pull request that is already merged.
				if hasLine(st.path(old), "state MERGED") {
					events = append(events, fmt.Sprintf("PR %s NEW state MERGED", n))
				}
				continue
			}
			if len(added) == 0 {
				continue
			}
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
		if stateFailed(st, d.Stderr, "watch prs") {
			return 1
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

// snapshotTo writes one pull request to dst: its comments, inline
// comments and reviews by id, its state, head and the newest run of each
// check, as lines in byte order. At most runLines lines are held at once
// (A24): full runs are sorted into files next to dst and merged.
func snapshotTo(ctx context.Context, d Deps, env []string, repo, n, dst string) error {
	w := &runWriter{dst: dst}
	defer w.cleanup()
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
				w.add(l)
			})
		}, "api", q.path, "--paginate")
		if err != nil {
			return err
		}
	}
	out, err := d.Gh(ctx, env, "pr", "view", n, "--repo", repo, "--json", "state,headRefOid,statusCheckRollup")
	if err != nil {
		return err
	}
	view, err := prView(out)
	if err != nil {
		return err
	}
	for _, l := range view {
		w.add(l)
	}
	return w.finish()
}

// runLines bounds the snapshot lines held in memory at once.
var runLines = 4096

// runWriter is an external sort: lines are buffered up to runLines, each
// full buffer is sorted into a run file, and finish merges the runs and the
// last buffer into dst.
type runWriter struct {
	dst  string
	buf  []string
	runs []string
	err  error
}

func (w *runWriter) add(l string) {
	if l == "" || w.err != nil {
		return
	}
	w.buf = append(w.buf, l)
	if len(w.buf) >= runLines {
		sort.Strings(w.buf)
		p := w.dst + ".run" + strconv.Itoa(len(w.runs))
		w.runs = append(w.runs, p)
		w.err = writeLines(p, w.buf)
		clear(w.buf)
		w.buf = w.buf[:0]
	}
}

func (w *runWriter) cleanup() {
	for _, p := range w.runs {
		_ = os.Remove(p)
	}
	_ = os.Remove(w.dst + ".tmp")
}

// mergeSource is one sorted run being merged.
type mergeSource struct {
	sc   *bufio.Scanner
	line string
	ok   bool
}

func (s *mergeSource) next() { s.ok = s.sc.Scan(); s.line = s.sc.Text() }

func (w *runWriter) finish() error {
	if w.err != nil {
		return w.err
	}
	sort.Strings(w.buf)
	var srcs []*mergeSource
	for _, p := range w.runs {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		s := &mergeSource{sc: lineScanner(f)}
		s.next()
		srcs = append(srcs, s)
	}
	f, err := os.Create(w.dst + ".tmp")
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(f)
	i := 0
	for {
		var best *mergeSource
		for _, s := range srcs {
			if s.ok && (best == nil || s.line < best.line) {
				best = s
			}
		}
		if i < len(w.buf) && (best == nil || w.buf[i] <= best.line) {
			_, _ = bw.WriteString(w.buf[i] + "\n")
			i++
			continue
		}
		if best == nil {
			break
		}
		_, _ = bw.WriteString(best.line + "\n")
		best.next()
	}
	for _, s := range srcs {
		if err := s.sc.Err(); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(w.dst+".tmp", w.dst)
}

func lineScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4<<10), 1<<20)
	return sc
}

func writeLines(p string, lines []string) error {
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(f)
	for _, l := range lines {
		_, _ = bw.WriteString(l + "\n")
	}
	if err := bw.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// hasLine reports whether the file at p has the line l.
func hasLine(p, l string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := lineScanner(f)
	for sc.Scan() {
		if sc.Text() == l {
			return true
		}
	}
	return false
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
		// A gh stopped by ctx does not wait on a child holding its pipes.
		cmd.WaitDelay = helperWaitDelay
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

// login is user.login, read without a decoder per item (A24: a decoder
// per item was most of a long history's garbage). A number stays its
// literal text, as UseNumber reads it.
func (it ghItem) login() any {
	if len(it.User) == 0 || it.User[0] != '{' {
		return nil
	}
	var u struct {
		Login json.RawMessage `json:"login"`
	}
	if json.Unmarshal(it.User, &u) != nil || len(u.Login) == 0 {
		return nil
	}
	switch c := u.Login[0]; {
	case c == '"':
		var s string
		_ = json.Unmarshal(u.Login, &s)
		return s
	case c == '-' || (c >= '0' && c <= '9'):
		return json.Number(u.Login)
	case c == 'n':
		return nil
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(u.Login))
	dec.UseNumber()
	_ = dec.Decode(&v)
	return v
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
		cmd.WaitDelay = helperWaitDelay
		out, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		if err := cmd.Start(); err != nil {
			return err
		}
		// The read ends with ctx too: a child gh left behind can hold the
		// pipe open after gh itself is stopped.
		defer context.AfterFunc(ctx, func() { _ = out.Close() })()
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
// snapshot file at next, counting repeats: both files are streamed and
// merged (A24: no history is held whole). An old file whose lines are not
// in byte order (an older writer's locale sort) is sorted once on disk
// through the bounded sort. Any error (a file that cannot be read, a sort
// that fails) is returned: the caller counts it as a failed read.
func addedSince(path, next string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	g, err := os.Open(next)
	if err != nil {
		return nil, err
	}
	defer g.Close()
	sc := lineScanner(f)
	cur := &mergeSource{sc: lineScanner(g)}
	cur.next()
	var out []string
	keep := func() {
		if cur.line != "" {
			out = append(out, cur.line)
		}
		cur.next()
	}
	prev := ""
	for sc.Scan() {
		l := sc.Text()
		if l == "" {
			continue
		}
		if l < prev {
			// An older writer's order: sort the old file once, in bounded
			// runs, and keep it sorted on disk (also when nothing is new),
			// then stream the comparison (A24).
			_ = f.Close()
			if err := sortFile(path); err != nil {
				return nil, err
			}
			return addedSince(path, next)
		}
		prev = l
		for cur.ok && cur.line < l {
			keep()
		}
		if cur.ok && cur.line == l {
			cur.next()
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	for cur.ok {
		keep()
	}
	if err := cur.sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// sortFile rewrites the file at path in byte order through the bounded
// external sort.
func sortFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	w := &runWriter{dst: path}
	defer w.cleanup()
	sc := lineScanner(f)
	for sc.Scan() {
		w.add(sc.Text())
	}
	err = sc.Err()
	_ = f.Close()
	if err != nil {
		return err
	}
	return w.finish()
}
