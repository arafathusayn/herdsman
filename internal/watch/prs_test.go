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
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// The G and W10-W16 cases of run-tests.sh. A fake gh serves JSON fixtures
// from a folder: <n>-view.json (gh pr view --json ...), <n>-comments.json,
// <n>-inline.json and <n>-reviews.json (gh api ... --paginate); <n>-fail
// makes every call for <n> fail, as a refused read does.

type fakeGh struct {
	dir   string
	calls [][]string
	err   io.Writer
}

func (f *fakeGh) run(_ context.Context, env []string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append(append([]string{}, env...), args...))
	var n, kind string
	switch {
	case args[0] == "auth":
		if args[3] == "nobody" {
			return nil, errors.New("no token")
		}
		return []byte("tok-" + args[3] + "\n"), nil
	case args[0] == "api":
		p := strings.Split(args[1], "/")
		n, kind = p[4], p[3]+"-"+p[5]
		kind = map[string]string{"issues-comments": "comments", "pulls-comments": "inline", "pulls-reviews": "reviews"}[kind]
	case args[0] == "pr":
		n, kind = args[2], "view"
	}
	if _, err := os.Stat(filepath.Join(f.dir, n+"-viewfail")); err == nil && kind == "view" {
		return nil, errors.New("exit status 1")
	}
	if _, err := os.Stat(filepath.Join(f.dir, n+"-fail")); err == nil {
		fmt.Fprintf(f.err, "fake gh: HTTP 502 for pull request %s\n", n)
		return nil, errors.New("exit status 1")
	}
	b, err := os.ReadFile(filepath.Join(f.dir, n+"-"+kind+".json"))
	if err != nil {
		if kind == "view" {
			return []byte(`{"state":"OPEN","headRefOid":"","statusCheckRollup":[]}`), nil
		}
		return []byte("[]"), nil
	}
	return b, nil
}

type prRig struct {
	t   *testing.T
	gh  *fakeGh
	dir string
}

func newPRRig(t *testing.T) *prRig {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "gh"), 0o755)
	return &prRig{t: t, gh: &fakeGh{dir: filepath.Join(dir, "gh")}, dir: dir}
}

func (r *prRig) fixture(name string, v any) {
	b, _ := json.Marshal(v)
	_ = os.WriteFile(filepath.Join(r.gh.dir, name), b, 0o644)
}

func (r *prRig) touch(name string) { _ = os.WriteFile(filepath.Join(r.gh.dir, name), nil, 0o644) }

func (r *prRig) remove(name string) { _ = os.Remove(filepath.Join(r.gh.dir, name)) }

func view(state, head string, checks ...map[string]any) map[string]any {
	if checks == nil {
		checks = []map[string]any{}
	}
	return map[string]any{"state": state, "headRefOid": head, "statusCheckRollup": checks}
}

func statusCtx(context, state string) map[string]any {
	return map[string]any{"__typename": "StatusContext", "context": context, "state": state}
}

func checkRun(name, st, conclusion, started, completed string, workflow ...string) map[string]any {
	wf := ""
	if len(workflow) > 0 {
		wf = workflow[0]
	}
	return map[string]any{"__typename": "CheckRun", "name": name, "status": st, "conclusion": conclusion, "startedAt": started, "completedAt": completed, "workflowName": wf}
}

// run is one watcher run; the deadline in seconds, polls every second.
func (r *prRig) run(prs, stateDir string, deadline int, extra ...string) (string, int) {
	r.t.Helper()
	env := map[string]string{"HERDSMAN_PRS": prs, "HERDSMAN_REPO": "org/repo", "HERDSMAN_PR_STATE": filepath.Join(r.dir, stateDir),
		"HERDSMAN_PR_DEADLINE": fmt.Sprint(deadline), "HERDSMAN_PR_INTERVAL": "1"}
	for i := 0; i+1 < len(extra); i += 2 {
		env[extra[i]] = extra[i+1]
	}
	var out bytes.Buffer
	r.gh.err = &out
	now := time.Now()
	d := Deps{Stdout: &out, Stderr: &out, Gh: r.gh.run, Now: func() time.Time { return now },
		Sleep: func(dd time.Duration) bool { now = now.Add(dd); return true }}
	code := PRs(context.Background(), func(k string) (string, bool) { v, ok := env[k]; return v, ok }, d)
	return out.String(), code
}

func TestPRWatcherBaselineAndMerge(t *testing.T) {
	r := newPRRig(t)
	r.fixture("7-view.json", view("OPEN", "abcd1234ffff", statusCtx("test", "PENDING")))
	out, _ := r.run("7", "pr-state", 0)
	match(t, "G1 the first pass records a baseline and does not wake", `^TICK no PR change`, out)
	r.fixture("7-view.json", view("OPEN", "abcd1234ffff", statusCtx("test", "PENDING"), statusCtx("lint", "SUCCESS")))
	out, _ = r.run("7", "pr-state", 0)
	match(t, "G2 a passing check that no setting names stays silent", `^TICK no PR change`, out)
	r.fixture("7-view.json", view("MERGED", "abcd1234ffff", statusCtx("test", "PENDING"), statusCtx("lint", "SUCCESS")))
	out, code := r.run("7", "pr-state", 0)
	match(t, "G3 the state change to MERGED wakes at once", `^PR [7] NEW state MERGED`, out)
	noMatch(t, "G3b with no TICK", `TICK`, out)
	match(t, "G3 the wake ends with the time", `^at [0-9]{2}:[0-9]{2}:[0-9]{2}$`, out)
	if code != 0 {
		t.Errorf("exit %d", code)
	}
	out, _ = r.run("7", "pr-state", 0)
	noMatch(t, "G3c the merge is not reported again", `MERGED`, out)
	r.fixture("8-view.json", view("MERGED", "9999aaaa"))
	out, _ = r.run("8", "pr-state", 0)
	match(t, "G4 a pull request that is merged at the first pass is reported once", `^PR [8] NEW state MERGED`, out)
	out, _ = r.run("8", "pr-state", 0)
	noMatch(t, "G4b and not again", `MERGED`, out)

	// Comments, inline comments, reviews and a named check reaching SUCCESS wake it.
	r.fixture("7-comments.json", []any{map[string]any{"id": 101, "user": map[string]any{"login": "greptile"}}})
	r.fixture("7-inline.json", []any{map[string]any{"id": 202, "user": map[string]any{"login": "bot"}, "path": "a.go"}})
	r.fixture("7-reviews.json", []any{map[string]any{"id": 303, "user": map[string]any{"login": "rv"}, "state": "COMMENTED"}})
	r.fixture("7-view.json", view("MERGED", "abcd1234ffff", statusCtx("test", "SUCCESS"), statusCtx("lint", "SUCCESS")))
	out, _ = r.run("7", "pr-state", 0)
	for _, want := range []string{`^PR 7 NEW comment 101 greptile$`, `^PR 7 NEW inline 202 bot a.go$`, `^PR 7 NEW review 303 rv COMMENTED$`, `^PR 7 NEW check test=SUCCESS$`} {
		match(t, "G5 (Go) a new item wakes", want, out)
	}
	noMatch(t, "G5b a check no setting names stays silent", `lint`, out)
	r.fixture("7-view.json", view("MERGED", "abcd1234ffff", statusCtx("test", "PENDING"), statusCtx("lint", "SUCCESS"), statusCtx("claude-review", "SUCCESS")))
	out, _ = r.run("7", "pr-state", 0, "HERDSMAN_PR_WAKE_CHECKS", "lint")
	match(t, "G5c the pending check is recorded silently", `^TICK`, out)
}

func TestPRWatcherChecks(t *testing.T) {
	r := newPRRig(t)
	r.fixture("21-view.json", view("OPEN", "aaaa1111bbbb", checkRun("test", "COMPLETED", "SUCCESS", "T10:00:00Z", "T10:01:00Z")))
	out, _ := r.run("21", "w10", 0)
	match(t, "W10 the first pass records a baseline", `^TICK no PR change`, out)
	r.fixture("21-view.json", view("OPEN", "aaaa1111bbbb", checkRun("test", "IN_PROGRESS", "", "T10:05:00Z", ""), checkRun("test", "COMPLETED", "CANCELLED", "T10:00:00Z", "T10:03:00Z")))
	out, _ = r.run("21", "w10", 0)
	match(t, "W10a an old cancelled run beside a newer run of the same name does not wake", `^TICK no PR change`, out)
	noMatch(t, "W10b and no event reports the cancelled result", `CANCELLED`, out)

	r.fixture("22-view.json", view("OPEN", "aaaa1111bbbb", checkRun("test", "COMPLETED", "SUCCESS", "T10:00:00Z", "T10:01:00Z")))
	r.run("22", "w11", 0)
	r.fixture("22-view.json", view("OPEN", "aaaa1111bbbb", checkRun("test", "COMPLETED", "SUCCESS", "T10:00:00Z", "T10:01:00Z"), checkRun("test", "COMPLETED", "FAILURE", "T10:05:00Z", "T10:06:00Z")))
	out, _ = r.run("22", "w11", 0)
	match(t, "W11 a newer failed run of a name wakes", `^PR .* NEW check test=FAILURE$`, out)
	noMatch(t, "W11b with no TICK", `TICK`, out)

	r.fixture("28-view.json", view("OPEN", "aaaa1111bbbb", checkRun("build", "COMPLETED", "SUCCESS", "T09:00:00Z", "T09:01:00Z", "CI")))
	r.run("28", "w16", 0)
	r.fixture("28-view.json", view("OPEN", "aaaa1111bbbb", checkRun("build", "COMPLETED", "FAILURE", "T10:00:00Z", "T10:01:00Z", "CI"), checkRun("build", "COMPLETED", "SUCCESS", "T10:00:05Z", "T10:01:00Z", "Deploy preview")))
	out, _ = r.run("28", "w16", 0)
	match(t, "W16 a failed run of a job name wakes although another workflow's job of that name started later", `^PR .* NEW check build=FAILURE$`, out)
	noMatch(t, "W16b with no TICK", `TICK`, out)
}

func TestPRWatcherFailedReads(t *testing.T) {
	r := newPRRig(t)
	r.touch("23-fail")
	out, code := r.run("23", "w12", 5)
	match(t, "W12 the third failed poll in a row ends the run with an error line", `^PR-WATCH-ERROR PR .* could not be read$`, out)
	noMatch(t, "W12b and not with a TICK", `TICK`, out)
	if code != 1 {
		t.Errorf("W12c exit 1: %d", code)
	}
	if n := strings.Count(out, "HTTP 502"); n != 3 {
		t.Errorf("W12d after exactly three failed polls: %d", n)
	}
	r.touch("24-fail")
	out, code = r.run("24", "w13", 0)
	match(t, "W13 a TICK after a failed read names the pull request", `^TICK no PR change \(not read: 24\)`, out)
	if code != 0 {
		t.Errorf("W13b exit 0: %d", code)
	}
	r.touch("25-fail")
	r.run("25", "w14", 0)
	out, _ = r.run("25", "w14", 0)
	noMatch(t, "W14 two failed polls in a row end no run", `PR-WATCH-ERROR`, out)
	r.remove("25-fail")
	r.fixture("25-view.json", view("OPEN", "aaaa1111"))
	out, _ = r.run("25", "w14", 0)
	noMatch(t, "W14b a good read clears the failed reads from the TICK", `not read`, out)
	r.touch("25-fail")
	out, _ = r.run("25", "w14", 0)
	match(t, "W14c a failed poll after a good read counts as the first", `not read: 25`, out)
	noMatch(t, "W14d and ends no run", `PR-WATCH-ERROR`, out)
	r.fixture("26-view.json", view("OPEN", "aaaa1111"))
	r.touch("27-fail")
	r.run("26 27", "w15", 0)
	r.run("26 27", "w15", 0)
	r.fixture("26-view.json", view("OPEN", "bbbb2222"))
	out, code = r.run("26 27", "w15", 0)
	match(t, "W15 the event of the readable pull request is printed", `^PR .* NEW head bbbb2222$`, out)
	match(t, "W15b and the unreadable one ends the run", `^PR-WATCH-ERROR PR .* could not be read$`, out)
	if code != 1 {
		t.Errorf("W15c exit 1: %d", code)
	}
	r.touch("28-viewfail")
	out, _ = r.run("28", "w16", 0)
	match(t, "(Go) a failed pr view after good comment reads is a failed read", `not read: 28`, out)
}

func TestPRWatcherSettings(t *testing.T) {
	r := newPRRig(t)
	var out bytes.Buffer
	if code := PRs(context.Background(), func(string) (string, bool) { return "", false }, Deps{Stdout: &out, Stderr: &out}); code != 1 ||
		!strings.Contains(out.String(), "HERDSMAN_PRS: set HERDSMAN_PRS") {
		t.Errorf("required settings: %d %q", code, out.String())
	}
	o, code := r.run("9", "acct", 0, "HERDSMAN_GH_ACCOUNT", "nobody")
	if code != 1 || !strings.Contains(o, "PR-WATCH-ERROR no gh token for nobody") {
		t.Errorf("no token: %d %q", code, o)
	}
	r.gh.calls = nil
	r.run("9", "acct", 0, "HERDSMAN_GH_ACCOUNT", "arafat")
	if len(r.gh.calls) < 2 || r.gh.calls[1][0] != "GH_TOKEN=tok-arafat" {
		t.Errorf("the token reaches gh: %v", r.gh.calls)
	}
	// Malformed JSON from gh is a failed read.
	_ = os.WriteFile(filepath.Join(r.gh.dir, "10-comments.json"), []byte("[{"), 0o644)
	o, _ = r.run("10", "bad", 0)
	match(t, "malformed comments", `not read: 10`, o)
	_ = os.Remove(filepath.Join(r.gh.dir, "10-comments.json"))
	_ = os.WriteFile(filepath.Join(r.gh.dir, "10-view.json"), []byte("{"), 0o644)
	o, _ = r.run("10", "bad", 0)
	match(t, "malformed view", `not read: 10`, o)
	// The sleep stub ends a run that polls.
	d := Deps{Stdout: io.Discard, Stderr: io.Discard, Gh: r.gh.run, Sleep: func(time.Duration) bool { return false }}
	env := map[string]string{"HERDSMAN_PRS": "11", "HERDSMAN_REPO": "o/r", "HERDSMAN_PR_STATE": filepath.Join(r.dir, "s11")}
	if PRs(context.Background(), func(k string) (string, bool) { v, ok := env[k]; return v, ok }, d) != 0 {
		t.Error("a stopped sleep ends with 0")
	}
}

func TestPRViewHelpers(t *testing.T) {
	lines, err := prView([]byte(`{"state":null,"headRefOid":null,"statusCheckRollup":[
		{"name":"a","conclusion":false,"state":null,"status":"QUEUED","startedAt":null},
		{"name":"a","conclusion":"SUCCESS","startedAt":5},
		{"name":"b","conclusion":"X","startedAt":true},{"name":"b","conclusion":"Y","startedAt":false},
		{"name":"c","conclusion":{"o":1},"startedAt":"z","completedAt":2},{"name":"c","conclusion":"Z","startedAt":"z","completedAt":1}]}`))
	want := []string{"state null", "head null", "check a=SUCCESS", "check b=X", `check c={"o":1}`}
	if err != nil || !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines %q %v", lines, err)
	}
	if got := added([]string{"a", "a", "b"}, []string{"a", "a", "a", "c"}); !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Fatalf("added %q", got)
	}
	if jqCompare(map[string]any{}, []any{}) != 0 || jqCompare("a", "b") >= 0 {
		t.Fatal("compare")
	}
}

// RunGh runs the gh program on PATH: a fake one here.
func TestRunGh(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "gh")
	_ = os.WriteFile(fake, []byte("#!/bin/sh\necho \"$GH_TOKEN $*\"\necho warn >&2\n"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var errb bytes.Buffer
	out, err := RunGh(&errb)(context.Background(), []string{"GH_TOKEN=t"}, "pr", "view", "1")
	if err != nil || string(out) != "t pr view 1\n" || errb.String() != "warn\n" {
		t.Fatalf("out %q err %v stderr %q", out, err, errb.String())
	}
	if _, err := exec.LookPath("gh"); err != nil {
		t.Fatal(err)
	}
}

// R5 finding 5 (A24): a large unchanged history is read as a stream that
// keeps only the fields the snapshot needs: 1000 comments of 16 KiB each
// (16 MiB of JSON) allocate far less than the JSON's size per poll.
func TestPRSnapshotStreamsLargeHistory(t *testing.T) {
	r := newPRRig(t)
	body := strings.Repeat("x", 16<<10)
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < 1000; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":%d,"user":{"login":"u%d"},"body":%q}`, i, i%7, body)
	}
	b.WriteString("]")
	big := filepath.Join(r.dir, "big.json")
	_ = os.WriteFile(big, []byte(b.String()), 0o644)
	r.fixture("7-view.json", view("OPEN", "abcd1234ffff"))
	d := Deps{Gh: r.gh.run, GhStream: func(ctx context.Context, env []string, read func(io.Reader) error, args ...string) error {
		if strings.Contains(strings.Join(args, " "), "issues/7/comments") {
			f, err := os.Open(big)
			if err != nil {
				return err
			}
			defer f.Close()
			return read(f)
		}
		out, err := r.gh.run(ctx, env, args...)
		if err != nil {
			return err
		}
		return read(bytes.NewReader(out))
	}}
	snapshot(context.Background(), d, nil, "org/repo", "7") // warm up
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	snap, err := snapshot(context.Background(), d, nil, "org/repo", "7")
	runtime.ReadMemStats(&m1)
	if err != nil || len(snap) != 1002 || snap[0] != "comment 0 u0" {
		t.Fatalf("%d lines, %v, first %q", len(snap), err, snap[0])
	}
	if alloc := m1.TotalAlloc - m0.TotalAlloc; alloc > 4<<20 {
		t.Errorf("one poll of a 16 MiB history allocated %d bytes", alloc)
	}
}

// R5 finding 9: the streaming reader's branches, and the streaming runner.
func TestStreamPagesAndRunGhStream(t *testing.T) {
	n := 0
	count := func(ghItem) { n++ }
	if err := streamPages(strings.NewReader(`null [{"id":1,"user":"x"}] [] [{"id":2}]`), count); err != nil || n != 2 {
		t.Errorf("null and empty pages: %d %v", n, err)
	}
	for _, bad := range []string{`{"id":1}`, `[1]`, `[{"id":1}`, `[{"id":1} x`, `[`} {
		if err := streamPages(strings.NewReader(bad), count); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
	if (ghItem{User: json.RawMessage(`"x"`)}).login() != nil {
		t.Error("a user that is no object has no login")
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\nif [ \"$1\" = fail ]; then echo boom >&2; exit 3; fi\necho \"[{\\\"id\\\":7,\\\"user\\\":{\\\"login\\\":\\\"$GH_TOKEN\\\"}}]\"\n"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var errb bytes.Buffer
	var got []string
	err := RunGhStream(&errb)(context.Background(), []string{"GH_TOKEN=t"}, func(r io.Reader) error {
		return streamPages(r, func(it ghItem) { got = append(got, str(it.ID)+" "+str(it.login())) })
	}, "api", "x")
	if err != nil || len(got) != 1 || got[0] != "7 t" {
		t.Errorf("%v %v", got, err)
	}
	if err := RunGhStream(&errb)(context.Background(), nil, func(r io.Reader) error { _, err := io.ReadAll(r); return err }, "fail"); err == nil || !strings.Contains(errb.String(), "boom") {
		t.Errorf("a failing gh: %v %q", err, errb.String())
	}
	if err := RunGhStream(&errb)(context.Background(), nil, func(io.Reader) error { return io.ErrUnexpectedEOF }, "api"); err != io.ErrUnexpectedEOF {
		t.Errorf("a reader error: %v", err)
	}
	t.Setenv("PATH", dir+"/none")
	if err := RunGhStream(&errb)(context.Background(), nil, func(io.Reader) error { return nil }, "api"); err == nil {
		t.Error("no gh: no error")
	}
}

// Review 6, finding 2: the comparison with the snapshot on disk streams
// the file against the sorted new lines; growing ids add only the new ones,
// repeats count, and a file in another order falls back to the full count.
func TestAddedSinceTheSnapshotOnDisk(t *testing.T) {
	dir := t.TempDir()
	write := func(lines ...string) string {
		p := filepath.Join(dir, "pr.txt")
		_ = os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
		return p
	}
	var old, cur []string
	for i := 0; i < 5000; i++ {
		old = append(old, fmt.Sprintf("comment %06d u", i))
	}
	cur = append(append([]string{}, old...), "comment 005000 u", "comment 005001 u")
	sort.Strings(cur)
	got := addedSince(write(old...), cur)
	if len(got) != 2 || got[0] != "comment 005000 u" || got[1] != "comment 005001 u" {
		t.Errorf("growing ids: %v", got)
	}
	if got := addedSince(write("a", "a", "b"), []string{"a", "a", "a", "b", "c"}); strings.Join(got, ",") != "a,c" {
		t.Errorf("repeats: %v", got)
	}
	if got := addedSince(write("b", "a"), []string{"a", "b", "c"}); strings.Join(got, ",") != "c" {
		t.Errorf("an unsorted file: %v", got)
	}
	if got := addedSince(filepath.Join(dir, "none"), []string{"a"}); strings.Join(got, ",") != "a" {
		t.Errorf("no file: %v", got)
	}
}
