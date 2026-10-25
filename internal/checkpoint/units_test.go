package checkpoint

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
)

// The records as the script's jq filter reads them.
func TestContextTokens(t *testing.T) {
	dir := t.TempDir()
	a := func(u string) string { return `{"type":"assistant","message":{"usage":` + u + `}}` + "\n" }
	for i, tc := range []struct{ text, want string }{
		{"", ""},
		{a(`{"input_tokens":1,"cache_read_input_tokens":2}`), "3"},
		{a(`{"input_tokens":1}`) + `{"type":"assistant","isSidechain":true,"message":{"usage":{"input_tokens":9}}}` + "\n", "1"},
		{a(`{"input_tokens":1}`) + "null\n" + `{"type":"user"}` + "\n", "1"},
		{a(`{"input_tokens":1}`) + "not json\n" + a(`{"input_tokens":5}`), "1"},
		{a(`{"input_tokens":1}`) + "[1]\n" + a(`{"input_tokens":5}`), "1"},
		{a(`{"input_tokens":1}`) + `{"type":"assistant","message":"x"}` + "\n" + a(`{"input_tokens":5}`), "1"},
		{a(`{"input_tokens":1}`) + a(`true`) + a(`{"input_tokens":5}`), "1"},
		{a(`{"input_tokens":1}`) + a(`{"input_tokens":"x"}`), "1"},
		{a(`{"input_tokens":1}`) + a(`{"input_tokens":"x","cache_read_input_tokens":1}`), "1"},
		{a(`{"input_tokens":null}`), "0"},
		{a(`{"cache_creation_input_tokens":4}`), "4"},
		{a(`{"input_tokens":1.5}`), "1.5"},
		{a(`{"input_tokens":1e300,"cache_read_input_tokens":1e300}`), "2.0000000000000001e+300"},
		{`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":7}}}}` + "\n", "7"},
		{`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{}}}}` + "\n", "null"},
		{`{"type":"event_msg","payload":{"type":"other"}}` + "\n", ""},
		{`{"type":"event_msg","payload":"x"}` + "\n", ""},
		{`{"type":"event_msg","payload":{"type":"token_count","info":3}}` + "\n", ""},
		{`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":3}}}` + "\n", ""},
		{`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":"s"}}}}`, "s"},
	} {
		p := filepath.Join(dir, fmt.Sprint(i))
		write(t, p, tc.text)
		if got := ContextTokens(p); got != tc.want {
			t.Errorf("%d %q: %q want %q", i, tc.text, got, tc.want)
		}
	}
	if got := ContextTokens(filepath.Join(dir, "missing")); got != "" {
		t.Errorf("missing: %q", got)
	}
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
	if got := ContextTokens(p); got != "" {
		t.Errorf("line 401 from the end counted: %q", got)
	}
	write(t, p, strings.TrimSuffix(b.String(), "\n"))
	if got := ContextTokens(p); got != "" {
		t.Errorf("no final newline: %q", got)
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
		got, err := tailLines(p, tc.n)
		if err != nil || string(got) != tc.want {
			t.Errorf("%q %d: %q %v", tc.text, tc.n, got, err)
		}
	}
	if _, err := tailLines(dir, 1); err == nil {
		t.Error("a folder read")
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
// reaches into a folder; newest breaks ties by name; -nt with a missing
// second file is true.
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
	if isFile("") || isFile(home) {
		t.Error("isFile")
	}
	// Codex: a rollout id with no file, and an empty id.
	c = Choose(Session{Agent: "codex", SID: ""}, Env{Home: home})
	if c.File != "" || c.Why != "no Codex rollout for session id '' under "+home+"/.codex/sessions" {
		t.Errorf("codex empty: %+v", c)
	}
}

func TestIntervalAndEnv(t *testing.T) {
	for in, want := range map[string]time.Duration{"900": 900 * time.Second, "1.5": 1500 * time.Millisecond, "2m": 2 * time.Minute,
		"1h": time.Hour, "1d": 24 * time.Hour, "3s": 3 * time.Second, "0": 0} {
		if d, ok := Interval(in); !ok || d != want {
			t.Errorf("%q: %v %v", in, d, ok)
		}
	}
	for _, in := range []string{"", "s", "-1", "1e3", "abc", "0x10", "inf", "99999999999999d"} {
		if _, ok := Interval(in); ok {
			t.Errorf("%q accepted", in)
		}
	}
	env := map[string]string{"TMPDIR": "/t"}
	if _, err := FromEnv(func(k string) string { return env[k] }, "/"); err != ErrNoTarget {
		t.Errorf("no target: %v", err)
	}
	env["TARGET"] = "w1:p1"
	c, err := FromEnv(func(k string) string { return env[k] }, "/")
	if err != nil || c.Log != "/t/checkpointer.log" || c.Interval != "900" || c.Limit != "250000" || c.Compact != "/compact" ||
		c.Prompt != DefaultPrompt || c.PromptSet || c.LockWait != "6" {
		t.Errorf("defaults: %+v %v", c, err)
	}
	if !atLeast(" 5", "+5") || atLeast("x", "1") || atLeast("1", "2") {
		t.Error("atLeast")
	}
}

// The session as the script's jq reads it from `herdr agent get`.
func TestSession(t *testing.T) {
	r := newRig(t)
	cp := r.checkpointer(r.config(r.dir, nil))
	ctx := context.Background()
	for i, tc := range []struct {
		agent map[string]any
		want  Session
	}{
		{map[string]any{"pane_id": "w9:p9", "agent": nil, "agent_session": nil}, Session{SID: "null", Kind: "id"}},
		{map[string]any{"pane_id": "w9:p9", "agent_session": "x"}, Session{Kind: "id"}},
		{map[string]any{"pane_id": "w9:p9", "agent": "codex", "agent_session": map[string]any{"value": false, "kind": nil}}, Session{Agent: "codex", SID: "false", Kind: "id"}},
		{map[string]any{"pane_id": "w9:p9", "agent_session": map[string]any{"value": 12, "kind": "path"}}, Session{SID: "12", Kind: "path"}},
	} {
		r.fx.SetAgent("w9:p9", tc.agent)
		if got := cp.session(ctx); got != tc.want {
			t.Errorf("%d: %+v want %+v", i, got, tc.want)
		}
	}
	r.fx.SetAgent("w9:p9", nil)
	if got := cp.session(ctx); got != (Session{SID: "null", Kind: "id"}) {
		t.Errorf("herdr error: %+v", got)
	}
	cp.Client = herdr.New(filepath.Join(r.dir, "none.sock"))
	if got := cp.session(ctx); got != (Session{Kind: "id"}) {
		t.Errorf("no herdr: %+v", got)
	}
}

// The loop: the start line, a skip with no transcript, a cycle when the
// transcript grew, a skip without activity, a failed prompt retried.
func TestLoop(t *testing.T) {
	r := newRig(t)
	r.agent("w9:p9", "claude", "id", "s")
	home := filepath.Join(r.dir, "home")
	tr := filepath.Join(home, ".claude/projects/p/s.jsonl")
	cfg := r.config(home, map[string]string{"LIMIT": "999"})
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
			cancel()
		}
	}
	if err := cp.Loop(ctx, 5*time.Second); err != context.Canceled {
		t.Fatalf("loop: %v", err)
	}
	want := strings.Join([]string{
		"09:08:07 checkpointer on w9:p9: every 900s, compact at >= 999 tokens",
		"09:08:07 no transcript found; skip",
		"09:08:07 sending memory checkpoint",
		"09:08:07 memory checkpoint done; context=10",
		"09:08:07 no activity since last checkpoint; skip",
		"09:08:07 sending memory checkpoint",
		"09:08:07 prompt failed; retry next interval",
		"09:08:07 sending memory checkpoint",
		"09:08:07 memory checkpoint done; context=100", ""}, "\n")
	if r.out.String() != want {
		t.Errorf("loop output:\n%s\nwant:\n%s", r.out.String(), want)
	}
	b, _ := os.ReadFile(cfg.Log)
	if string(b) != want {
		t.Errorf("log file:\n%s", b)
	}
	if fileSize(filepath.Join(r.dir, "none")) != "" {
		t.Error("fileSize of a missing file")
	}
	// A log file that cannot be written: stdout still gets the line.
	cp.Config.Log = r.dir
	r.out.Reset()
	cp.log("x")
	if r.out.String() != "09:08:07 x\n" || !strings.Contains(r.errs.String(), "log:") {
		t.Errorf("unwritable log: %q %q", r.out.String(), r.errs.String())
	}
	// An empty context shows "?", and the compact log line shows it empty.
	write(t, tr, "")
	cp.Config.Limit = "0"
	cp.Config.Log = cfg.Log
	r.out.Reset()
	if code, _ := cp.Cycle(context.Background(), tr); code != 0 || !strings.Contains(r.out.String(), "context=?") {
		t.Errorf("empty transcript: %d %q", code, r.out.String())
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
	code, err := cp.Once(context.Background())
	if code != 0 || err != nil || !strings.Contains(r.out.String(), "compact done; context=5") {
		t.Errorf("once: %d %v %q", code, err, r.out.String())
	}
	var subs int
	for _, q := range r.srv.Requests() {
		if q.Method == "events.subscribe" {
			subs++
		}
	}
	if subs != 2 {
		t.Errorf("working waits: %d", subs)
	}
	cp.WorkingTimeout = 0
	cp.Wait = func(_ context.Context, _ string, _ []herdr.Status, d time.Duration) (herdr.Status, error) {
		if d != 0 && d != WorkingTimeout {
			t.Errorf("timeout %v", d)
		}
		return "", nil
	}
	cp.waitWorking(context.Background())
	var nop bytes.Buffer
	cp.Stdout = &nop
	cp.Now, cp.Sleep = nil, nil
	_ = cp.now()
	cp.sleep(0)
}
