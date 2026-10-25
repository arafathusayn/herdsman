package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
)

// The token reading (A26): every valid record of the tail counts, a line
// that is not JSON or a usage record with a count that is not a whole
// non-negative number is passed over and counted, never concatenated or
// printed as a float.
func TestContextTokens(t *testing.T) {
	dir := t.TempDir()
	a := func(u string) string { return `{"type":"assistant","message":{"usage":` + u + `}}` + "\n" }
	ev := func(l string) string {
		return `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":` + l + `}}}` + "\n"
	}
	for i, tc := range []struct {
		text, want string
		skipped    int
	}{
		{"", "", 0},
		{a(`{"input_tokens":1,"cache_read_input_tokens":2}`), "3", 0},
		{a(`{"input_tokens":1}`) + `{"type":"assistant","isSidechain":true,"message":{"usage":{"input_tokens":9}}}` + "\n", "1", 0},
		{a(`{"input_tokens":1}`) + "null\n" + `{"type":"user"}` + "\n\n", "1", 0},
		{a(`{"input_tokens":1}`) + "[1]\n", "1", 1},
		// Exactly one JSON value per line: a trailing } or ] is unreadable,
		// and the next valid record still counts.
		{a(`{"input_tokens":300000}`)[:len(a(`{"input_tokens":300000}`))-1] + "}\n" + a(`{"input_tokens":4}`), "4", 1},
		{a(`{"input_tokens":4}`) + a(`{"input_tokens":300000}`)[:len(a(`{"input_tokens":300000}`))-1] + "]\n", "4", 1},
		// Integral numbers in any spelling count.
		{a(`{"input_tokens":3e5}`), "300000", 0},
		{a(`{"input_tokens":300000.0,"cache_read_input_tokens":1E2}`), "300100", 0},
		{a(`{"input_tokens":-0}`), "0", 0},
		{a(`{"input_tokens":1e-300}`), "", 1},
		{a(`{"input_tokens":9.3e18}`), "", 1},
		{`{"type":"assistant","message":"x"}` + "\n" + `{"type":5,"payload":3}` + "\n", "", 0},
		// A bad line or a half-written last line hides nothing before or after it.
		{a(`{"input_tokens":1}`) + "not json\n" + a(`{"input_tokens":5}`), "5", 1},
		{a(`{"input_tokens":5}`) + `{"type":"assistant","message":{"usa`, "5", 1},
		{a(`{"input_tokens":1}`) + `{"a":1} {"b":2}` + "\n", "1", 1},
		// The review's string usage: skipped, never concatenated to 300000.
		{a(`{"input_tokens":7}`) + a(`{"input_tokens":"300","cache_creation_input_tokens":"000","cache_read_input_tokens":""}`), "7", 1},
		{a(`true`), "", 1},
		{a(`{"input_tokens":1.1}`), "", 1},
		{a(`{"input_tokens":1e300,"cache_read_input_tokens":1e300}`), "", 1},
		{a(`{"input_tokens":1e309}`), "", 1},
		{a(`{"input_tokens":-4}`), "", 1},
		{a(`{"input_tokens":9223372036854775807,"cache_read_input_tokens":1}`), "", 1},
		{a(`{"input_tokens":null}`), "0", 0},
		{a(`{"cache_creation_input_tokens":4}`), "4", 0},
		{a(`null`), "", 0},
		{ev(`{"input_tokens":7}`), "7", 0},
		{ev(`{}`), "", 1},
		{ev(`3`), "", 1},
		{ev(`{"input_tokens":"s"}`), "", 1},
		{`{"type":"event_msg","payload":{"type":"other"}}` + "\n", "", 0},
		{`{"type":"event_msg","payload":{"type":"token_count","info":null}}` + "\n", "", 0},
	} {
		p := filepath.Join(dir, fmt.Sprint(i))
		write(t, p, tc.text)
		got := ContextTokens(p)
		if got.String() != tc.want || got.Skipped != tc.skipped {
			t.Errorf("%d %q: %q skipped %d, want %q skipped %d", i, tc.text, got.String(), got.Skipped, tc.want, tc.skipped)
		}
	}
	if got := ContextTokens(filepath.Join(dir, "missing")); got.OK {
		t.Errorf("missing: %+v", got)
	}
}

func FuzzRecord(f *testing.F) {
	f.Add(`{"type":"assistant","message":{"usage":{"input_tokens":1}}}`)
	f.Add(`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":2}}}}`)
	f.Add(`{"type":"assistant","message":{"usage":{"input_tokens":"3"}}}`)
	f.Fuzz(func(t *testing.T, line string) {
		p := filepath.Join(t.TempDir(), "t")
		if err := os.WriteFile(p, []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
		r := ContextTokens(p)
		if r.OK && r.Tokens < 0 {
			t.Errorf("negative count %d from %q", r.Tokens, line)
		}
	})
}

// tail -n 400: only the last 400 lines count, with or without a final
// newline, across read chunks.
func TestTailLines(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString(`{"type":"assistant","message":{"usage":{"input_tokens":99}}}` + "\n")
	pad := strings.Repeat(" ", 300)
	for range 400 {
		b.WriteString(`{"type":"user"}` + pad + "\n")
	}
	p := filepath.Join(dir, "t")
	write(t, p, b.String())
	if got := ContextTokens(p); got.OK {
		t.Errorf("line 401 from the end counted: %+v", got)
	}
	write(t, p, strings.TrimSuffix(b.String(), "\n"))
	if got := ContextTokens(p); got.OK {
		t.Errorf("no final newline: %+v", got)
	}
	for _, tc := range []struct {
		text string
		n    int
		want string
	}{
		{"a\nb\nc\n", 2, "b\nc\n"},
		{"a\nb\nc", 2, "b\nc"},
		{"a\n", 5, "a\n"},
		{"", 3, ""},
	} {
		write(t, p, tc.text)
		got, err := tailLines(p, tc.n, 1<<20)
		if err != nil || string(got) != tc.want {
			t.Errorf("%q %d: %q %v", tc.text, tc.n, got, err)
		}
	}
	if _, err := tailLines(dir, 1, 10); err == nil {
		t.Error("a folder read")
	}
	// The byte bound: a line cut by it is dropped; a window that starts
	// at a line start keeps its first line; one line longer than the bound
	// gives nothing.
	write(t, p, "aaaa\nbb\ncc\n")
	for limit, want := range map[int64]string{6: "bb\ncc\n", 5: "cc\n", 8: "bb\ncc\n", 2: ""} {
		if got, _ := tailLines(p, 400, limit); string(got) != want {
			t.Errorf("limit %d: %q want %q", limit, got, want)
		}
	}
}

func TestDerivedAndEscape(t *testing.T) {
	if got := Derived("/Users/a b/é_x.1"); got != "-Users-a-b---x-1" {
		t.Errorf("derived %q", got)
	}
	if got := escape(`a*?[\b`); got != `a\*\?\[\\b` {
		t.Errorf("escape %q", got)
	}
}

// A session id with glob characters is literal, and one with a slash
// reaches into a folder; newest breaks ties by name; -nt compares the
// full modification time (A26: bash 3.2 compared whole seconds).
func TestChooseEdges(t *testing.T) {
	home := t.TempDir()
	usage(t, filepath.Join(home, ".claude/projects/p/a*.jsonl"), 1)
	usage(t, filepath.Join(home, ".claude/projects/p/ab.jsonl"), 2)
	c := Choose(Session{SID: "a*"}, Env{Home: home, PWD: "/x"})
	if !strings.HasSuffix(c.File, "/a*.jsonl") {
		t.Errorf("glob in id: %+v", c)
	}
	usage(t, filepath.Join(home, ".claude/projects/p/sub/s.jsonl"), 3)
	c = Choose(Session{SID: "sub/s"}, Env{Home: home, PWD: "/x"})
	if !strings.HasSuffix(c.File, "/sub/s.jsonl") {
		t.Errorf("slash in id: %+v", c)
	}
	write(t, filepath.Join(home, ".claude/projects/file"), "")
	tm := time.Unix(1700000000, 0)
	x, y := filepath.Join(home, "x"), filepath.Join(home, "y")
	write(t, x, "")
	write(t, y, "")
	for _, p := range []string{x, y} {
		if err := os.Chtimes(p, tm, tm); err != nil {
			t.Fatal(err)
		}
	}
	if got := newest([]string{y, x, filepath.Join(home, "none")}); got != x {
		t.Errorf("tie: %s", got)
	}
	if newest(nil) != "" || !newer(x, filepath.Join(home, "none")) || newer(filepath.Join(home, "none"), x) {
		t.Error("newest/newer edges")
	}
	// Same second, 0.1 s and 0.9 s: the later file is newer, and the hint
	// names it.
	sec := time.Unix(1700000100, 0)
	p := filepath.Join(home, ".claude/projects/q")
	usage(t, filepath.Join(p, "old.jsonl"), 1)
	usage(t, filepath.Join(p, "new.jsonl"), 2)
	_ = os.Chtimes(filepath.Join(p, "old.jsonl"), sec, sec.Add(100*time.Millisecond))
	_ = os.Chtimes(filepath.Join(p, "new.jsonl"), sec, sec.Add(900*time.Millisecond))
	c = Choose(Session{SID: "old"}, Env{Home: home})
	if !strings.Contains(c.Hint, "a newer transcript exists: "+filepath.Join(p, "new.jsonl")) {
		t.Errorf("same-second hint: %+v", c)
	}
	if isFile("") || isFile(home) {
		t.Error("isFile")
	}
	// No session id from herdr: no file is searched for; the working
	// folder's newest file is only a hint.
	none := Session{Kind: "id", Missing: "agent get: down"}
	c = Choose(Session{Agent: "codex", Kind: "id", Missing: "x"}, Env{Home: home})
	if c.File != "" || c.Why != "herdr reports no session id for the target (x)" {
		t.Errorf("codex none: %+v", c)
	}
	c = Choose(none, Env{Home: home, PWD: "q"})
	if c.File != "" || c.Why != "herdr reports no session id for the target (agent get: down)" || c.Hint == "" {
		t.Errorf("claude none: %+v", c)
	}
	c = Choose(none, Env{Home: home, PWD: "/none"})
	if c.File != "" || !strings.HasSuffix(c.Why, " and no .jsonl in "+home+"/.claude/projects/-none") {
		t.Errorf("claude none, no folder: %+v", c)
	}
}

// Strict settings (A25): one line naming the variable and the value.
func TestSettings(t *testing.T) {
	for in, want := range map[string]time.Duration{"900": 900 * time.Second, "0900": 900 * time.Second, "2m": 2 * time.Minute,
		"1h": time.Hour, "1d": 24 * time.Hour, "3s": 3 * time.Second, "2d": 24 * time.Hour, "99999999999999999999": 24 * time.Hour} {
		if d, err := Duration("INTERVAL", in); err != nil || d != want {
			t.Errorf("%q: %v %v", in, d, err)
		}
	}
	for _, in := range []string{"", "s", "0", "0s", "-1", "+0", "0e0", "0x0p0", "1e-3", "1.5", "abc", " 5"} {
		_, err := Duration("INTERVAL", in)
		if err == nil || err.Error() != `INTERVAL: a whole number of seconds, with an optional s, m, h or d suffix, at least 1s (got "`+in+`")` {
			t.Errorf("%q: %v", in, err)
		}
	}
	for _, tc := range []struct {
		v  string
		ok bool
	}{{"6", true}, {"06", true}, {"60", true}, {"0", false}, {"61", false}, {"+5", false}, {"x", false}} {
		if _, err := Integer("LOCK_WAIT", tc.v, 1, 60); (err == nil) != tc.ok {
			t.Errorf("LOCK_WAIT %q: %v", tc.v, err)
		}
	}
	o, err := InboxSettings(func(k string) string { return map[string]string{"LOCK_WAIT": "06", "HEALTH_AFTER": "0300"}[k] })
	if err != nil || o.LockWait != "6" || o.HealthAfter != "300" {
		t.Errorf("canonical inbox settings: %+v %v", o, err)
	}
	if o, err := InboxSettings(func(string) string { return "" }); err != nil || o.LockWait != "6" || o.HealthAfter != "" {
		t.Errorf("default inbox settings: %+v %v", o, err)
	}
	base := map[string]string{"TARGET": "w1:p1", "TMPDIR": "/t"}
	get := func(extra map[string]string) (Config, error) {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return FromEnv(func(k string) string { return m[k] }, "/")
	}
	c, err := get(nil)
	if err != nil || c.Log != "/t/checkpointer.log" || c.Interval != 900*time.Second || c.Limit != 250000 || c.Compact != "/compact" ||
		c.Prompt != DefaultPrompt || c.PromptSet || c.LockWait != 6 {
		t.Errorf("defaults: %+v %v", c, err)
	}
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"TARGET": ""}, `TARGET: required, the orchestrator pane id (got "")`},
		{map[string]string{"INTERVAL": "0"}, `INTERVAL: a whole number`},
		{map[string]string{"LIMIT": "lots"}, `LIMIT: a whole number from 0 to 9007199254740992 (got "lots")`},
		{map[string]string{"LOCK_WAIT": "0"}, `LOCK_WAIT: a whole number from 1 to 60 (got "0")`},
		{map[string]string{"HEALTH_AFTER": "x\ny"}, `HEALTH_AFTER: a whole number from 1 to 86400 (got "x\ny")`},
		{map[string]string{"HERDSMAN_ORCHESTRATOR": "o"}, `HERDSMAN_INBOX: required in adjutant mode (HERDSMAN_ORCHESTRATOR is set) (got "")`},
	} {
		_, err := get(tc.env)
		var se *SettingError
		if !errors.As(err, &se) || !strings.HasPrefix(err.Error(), tc.want) {
			t.Errorf("%v: %v", tc.env, err)
		}
	}
}

// The session as herdr reports it: a failed read or a missing id is no
// session id (A26: the script searched for "null.jsonl").
func TestSession(t *testing.T) {
	r := newRig(t)
	cp := r.checkpointer(r.config(r.dir, nil))
	ctx := context.Background()
	for i, tc := range []struct {
		agent map[string]any
		want  Session
	}{
		{map[string]any{"pane_id": "w9:p9", "agent_session": nil}, Session{Kind: "id", Missing: "no agent_session value"}},
		{map[string]any{"pane_id": "w9:p9", "agent_session": "x"}, Session{Kind: "id", Missing: "no agent_session value"}},
		{map[string]any{"pane_id": "w9:p9", "agent": "codex", "agent_session": map[string]any{"value": 12, "kind": nil}}, Session{Agent: "codex", Kind: "id", Missing: "no agent_session value"}},
		{map[string]any{"pane_id": "w9:p9", "agent_session": map[string]any{"value": "v", "kind": "path"}}, Session{SID: "v", Kind: "path"}},
	} {
		r.fx.SetAgent("w9:p9", tc.agent)
		if got := cp.session(ctx); got != tc.want {
			t.Errorf("%d: %+v want %+v", i, got, tc.want)
		}
	}
	r.fx.SetAgent("w9:p9", nil)
	if got := cp.session(ctx); got.SID != "" || !strings.HasPrefix(got.Missing, "agent get: agent_not_found") {
		t.Errorf("herdr error: %+v", got)
	}
}

// The loop: the start line, a skip with no transcript, a cycle when the
// transcript changed, a skip without activity, a failed prompt retried, a
// rewrite of the same size counted as activity (A26).
func TestLoop(t *testing.T) {
	r := newRig(t)
	r.agent("w9:p9", "claude", "id", "s")
	home := filepath.Join(r.dir, "home")
	tr := filepath.Join(home, ".claude/projects/p/s.jsonl")
	cfg := r.config(home, map[string]string{"LIMIT": "999", "INTERVAL": "5"})
	cp := r.checkpointer(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	step := 0
	cp.Now = func() time.Time { return time.Date(2026, 1, 1, 9, 8, 7, 0, time.Local) }
	cp.Sleep = func(d time.Duration) {
		if d != 5*time.Second {
			t.Errorf("sleep %v", d)
		}
		step++
		switch step {
		case 2:
			usage(t, tr, 10)
		case 4:
			r.fx.Fail("agent.prompt", "boom")
			usage(t, tr, 100)
		case 5:
			r.fx.Fail("agent.prompt", "")
		case 6:
			usage(t, tr, 200) // the same size, a new time
			later := time.Now().Add(time.Hour)
			_ = os.Chtimes(tr, later, later)
		case 7:
			cancel()
		}
	}
	if err := cp.Loop(ctx); err != context.Canceled {
		t.Fatalf("loop: %v", err)
	}
	want := strings.Join([]string{
		"09:08:07 checkpointer on w9:p9: every 5s, compact at >= 999 tokens",
		"09:08:07 no transcript found; skip",
		"09:08:07 sending memory checkpoint",
		"09:08:07 memory checkpoint done; context=10",
		"09:08:07 no activity since last checkpoint; skip",
		"09:08:07 sending memory checkpoint",
		"09:08:07 prompt failed; retry next interval",
		"09:08:07 sending memory checkpoint",
		"09:08:07 memory checkpoint done; context=100",
		"09:08:07 sending memory checkpoint",
		"09:08:07 memory checkpoint done; context=200", ""}, "\n")
	if r.out.String() != want {
		t.Errorf("loop output:\n%s\nwant:\n%s", r.out.String(), want)
	}
	b, _ := os.ReadFile(cfg.Log)
	if string(b) != want {
		t.Errorf("log file:\n%s", b)
	}
	if stamp(filepath.Join(r.dir, "none")) != "" {
		t.Error("stamp of a missing file")
	}
	// A log file that cannot be written: stdout still gets the line.
	cp.Config.Log = r.dir
	r.out.Reset()
	cp.log("x")
	if r.out.String() != "09:08:07 x\n" || !strings.Contains(r.errs.String(), "log:") {
		t.Errorf("unwritable log: %q %q", r.out.String(), r.errs.String())
	}
}

// A cycle's failures (A26): a failed or timed-out idle wait sends nothing,
// a failed compact is logged as failed, unreadable records are logged.
func TestCycleFailures(t *testing.T) {
	r := newRig(t)
	r.agent("w9:p9", "claude", "id", "s")
	tr := filepath.Join(r.dir, "t.jsonl")
	write(t, tr, `{"type":"assistant","message":{"usage":{"input_tokens":5}}}`+"\nbad\n")
	cp := r.checkpointer(r.config(r.dir, map[string]string{"LIMIT": "1"}))
	cp.Now = func() time.Time { return time.Date(2026, 1, 1, 9, 8, 7, 0, time.Local) }
	ctx := context.Background()
	for _, tc := range []struct {
		fail  int // which idle wait fails (1: before the prompt)
		err   error
		lines string
	}{
		{1, herdr.ErrWaitTimeout, "orchestrator not idle within 900s; no input sent; retry next interval"},
		{1, errors.New("down"), "agent wait failed: down; no input sent; retry next interval"},
		{2, herdr.ErrWaitTimeout, "orchestrator not idle within 900s; no context read; retry next interval"},
		{3, herdr.ErrWaitTimeout, "orchestrator not idle within 900s; no context read; retry next interval"},
	} {
		r.out.Reset()
		n := 0
		cp.Wait = func(_ context.Context, _ string, want []herdr.Status, d time.Duration) (herdr.Status, error) {
			if want[0] == "working" {
				return "", nil
			}
			if d != 900*time.Second {
				t.Errorf("idle wait limit %v", d)
			}
			n++
			if n == tc.fail {
				return "", tc.err
			}
			return "idle", nil
		}
		mark := len(r.srv.Requests())
		if code := cp.Cycle(ctx, tr); code != 1 || !strings.Contains(r.out.String(), tc.lines) {
			t.Errorf("%v: %d %q", tc, code, r.out.String())
		}
		sent := 0
		for _, q := range r.srv.Requests()[mark:] {
			if q.Method == "agent.prompt" {
				sent++
			}
		}
		if sent != tc.fail-1 {
			t.Errorf("%v: %d prompts", tc, sent)
		}
	}
	// A working wait that fails for another reason than its timeout ends
	// the cycle, before and after the compact.
	for _, nth := range []int{1, 2} {
		r.out.Reset()
		w := 0
		cp.Wait = func(_ context.Context, _ string, want []herdr.Status, _ time.Duration) (herdr.Status, error) {
			if want[0] != "working" {
				return "idle", nil
			}
			w++
			if w == nth {
				return "", errors.New("socket closed")
			}
			return "", herdr.ErrWaitTimeout
		}
		if code := cp.Cycle(ctx, tr); code != 1 || !strings.Contains(r.out.String(), "agent wait failed: socket closed; no context read; retry next interval") ||
			strings.Contains(r.out.String(), "compact done") {
			t.Errorf("working wait %d: %d %q", nth, code, r.out.String())
		}
	}
	// The timeout line shows the effective seconds.
	for in, want := range map[string]string{"2m": "within 120s;", "2d": "within 86400s;"} {
		c2 := r.checkpointer(r.config(r.dir, map[string]string{"INTERVAL": in}))
		c2.Wait = func(context.Context, string, []herdr.Status, time.Duration) (herdr.Status, error) {
			return "", herdr.ErrWaitTimeout
		}
		c2.Cycle(ctx, tr)
		if !strings.Contains(r.out.String(), want) {
			t.Errorf("INTERVAL=%s: %q", in, r.out.String())
		}
	}
	cp.Wait = func(context.Context, string, []herdr.Status, time.Duration) (herdr.Status, error) { return "", nil }
	calls := 0
	r.srv.Handle("agent.prompt", func(map[string]any) (any, string) {
		calls++
		if calls == 2 {
			return nil, "boom"
		}
		return map[string]any{"type": "ok"}, ""
	})
	r.out.Reset()
	if code := cp.Cycle(ctx, tr); code != 1 || !strings.HasSuffix(r.out.String(), "sending /compact\n09:08:07 compact failed; retry next interval\n") ||
		!strings.Contains(r.out.String(), "passed over 1 unreadable records of "+tr) {
		t.Errorf("compact failed: %d %q", code, r.out.String())
	}
	// No reading shows "?" and sends no compact.
	write(t, tr, "")
	r.out.Reset()
	if code := cp.Cycle(ctx, tr); code != 0 || !strings.Contains(r.out.String(), "context=?") {
		t.Errorf("empty transcript: %d %q", code, r.out.String())
	}
}

// The prompt as the socket gets it: trailing newlines dropped, as the
// script's "$(memory_prompt)", for Claude and Codex.
func TestPromptPayload(t *testing.T) {
	r := newRig(t)
	tr := filepath.Join(r.dir, "t.jsonl")
	write(t, tr, `{"type":"assistant","message":{"usage":{"input_tokens":5}}}`+"\n")
	for _, tc := range []struct{ agent, prompt, want string }{
		{"claude", "/save-memory\n\n", "/save-memory"},
		{"codex", "/save-memory\n", "/save-memory"},
		{"codex", "", strings.TrimPrefix(DefaultPrompt, "/memory-with-dag ")},
		{"claude", "a\nb\n", "a\nb"},
	} {
		r.agent("w9:p9", tc.agent, "id", "s")
		r.mark = len(r.srv.Requests())
		cp := r.checkpointer(r.config(r.dir, map[string]string{"TRANSCRIPT": tr, "PROMPT": tc.prompt, "LIMIT": "999"}))
		if cp.Once(context.Background()) != 0 {
			t.Fatal(r.out.String())
		}
		var got []string
		for _, q := range r.srv.Requests()[r.mark:] {
			if q.Method == "agent.prompt" {
				got = append(got, q.Params["text"].(string))
			}
		}
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s %q: %q", tc.agent, tc.prompt, got)
		}
	}
}

// The default waits go through herdr: idle returns at once, the wait for a
// working turn ends at its timeout.
func TestDefaultWaits(t *testing.T) {
	r := newRig(t)
	r.agent("w9:p9", "claude", "id", "s")
	cp := r.checkpointer(r.config(r.dir, map[string]string{"TRANSCRIPT": filepath.Join(r.dir, "t.jsonl"), "LIMIT": "1"}))
	write(t, filepath.Join(r.dir, "t.jsonl"), `{"type":"assistant","message":{"usage":{"input_tokens":5}}}`+"\n")
	cp.Wait = nil
	cp.WorkingTimeout = 20 * time.Millisecond
	if code := cp.Once(context.Background()); code != 0 || !strings.Contains(r.out.String(), "compact done; context=5") {
		t.Errorf("once: %d %q", code, r.out.String())
	}
	cp.WorkingTimeout = 0
	cp.Wait = func(_ context.Context, _ string, _ []herdr.Status, d time.Duration) (herdr.Status, error) {
		if d != WorkingTimeout {
			t.Errorf("timeout %v", d)
		}
		return "", nil
	}
	cp.waitWorking(context.Background())
	cp.Now, cp.Sleep = nil, nil
	_ = cp.now()
	cp.sleep(0)
}

// A24: a thousand loop iterations against the fake keep the heap and the
// goroutines flat.
func TestLoopResources(t *testing.T) {
	r := newRig(t)
	r.agent("w9:p9", "claude", "id", "s")
	tr := filepath.Join(r.dir, "home/.claude/projects/p/s.jsonl")
	usage(t, tr, 5)
	cp := r.checkpointer(r.config(filepath.Join(r.dir, "home"), map[string]string{"LIMIT": "999", "LOG": os.DevNull}))
	cp.Stdout = discard{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var before runtime.MemStats
	goroutines := 0
	i := 0
	cp.Sleep = func(time.Duration) {
		i++
		switch {
		case i == 50:
			runtime.GC()
			runtime.ReadMemStats(&before)
			goroutines = runtime.NumGoroutine()
		case i == 1050:
			cancel()
		case i == 20:
			// One 8 MiB user record before the usage lines: never held.
			big := `{"type":"user","message":{"content":"` + strings.Repeat("x", 8<<20) + `"}}` + "\n"
			f, _ := os.OpenFile(tr, os.O_APPEND|os.O_WRONLY, 0o600)
			_, _ = f.WriteString(big)
			_ = f.Close()
		case i%2 == 0:
			// Activity on every other tick: the transcript grows by one
			// usage line, and a cycle runs.
			f, _ := os.OpenFile(tr, os.O_APPEND|os.O_WRONLY, 0o600)
			fmt.Fprintf(f, `{"type":"assistant","message":{"usage":{"input_tokens":%d}}}`+"\n", i)
			_ = f.Close()
		}
	}
	_ = cp.Loop(ctx)
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if grow := int64(after.HeapAlloc) - int64(before.HeapAlloc); grow > 1<<20 {
		t.Errorf("heap grew %d bytes over 1000 iterations", grow)
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > goroutines && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > goroutines {
		t.Errorf("goroutines %d, at start %d", n, goroutines)
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
