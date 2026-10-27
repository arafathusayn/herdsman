package checkpoint

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
)

// rig is a fake herdr with one target pane and a checkpointer on it, as the
// shell suites' fake herdr: every agent wait returns at once and is
// recorded.
type rig struct {
	t     *testing.T
	srv   *herdrtest.Server
	fx    *herdrtest.Fixture
	dir   string
	out   bytes.Buffer
	errs  bytes.Buffer
	waits [][]herdr.Status
	mark  int
}

func newRig(t *testing.T) *rig {
	t.Helper()
	srv := herdrtest.Start(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, srv: srv, fx: herdrtest.Serve(srv), dir: dir}
}

// agent sets the fake `agent get` of w9:p9 as the old checkpointer suite's fake:
// FAKE_AGENT, FAKE_KIND, FAKE_SID.
func (r *rig) agent(pane, agent, kind, sid string) {
	r.fx.SetAgent(pane, map[string]any{"pane_id": pane, "agent": agent, "agent_status": "idle",
		"agent_session": map[string]any{"kind": kind, "value": sid}})
}

// config is the run() environment of the suites: HOME, LOG, TARGET.
func (r *rig) config(home string, env map[string]string) Config {
	r.t.Helper()
	m := map[string]string{"TARGET": "w9:p9", "HOME": home, "LOG": filepath.Join(r.dir, "log")}
	for k, v := range env {
		m[k] = v
	}
	pwd := m["PWD"]
	if pwd == "" {
		pwd = r.dir
	}
	c, err := FromEnv(func(k string) string { return m[k] }, pwd)
	if err != nil {
		r.t.Fatal(err)
	}
	return c
}

func (r *rig) checkpointer(c Config) *Checkpointer {
	r.out.Reset()
	r.errs.Reset()
	r.waits = nil
	return &Checkpointer{Config: c, Client: herdr.New(r.srv.Socket), Stdout: &r.out, Stderr: &r.errs,
		Sleep: func(time.Duration) {},
		Wait: func(_ context.Context, _ string, want []herdr.Status, _ time.Duration) (herdr.Status, error) {
			r.waits = append(r.waits, want)
			return want[0], nil
		}}
}

// once runs --once and returns the exit status.
func (r *rig) once(home string, env map[string]string) int {
	r.t.Helper()

	r.mark = len(r.srv.Requests())
	return r.checkpointer(r.config(home, env)).Once(context.Background())
}

// prompts is the suite's prompts(): the first 20 characters of each prompt
// since the last run, each followed by |.
func (r *rig) prompts() string {
	var b strings.Builder
	for _, q := range r.srv.Requests()[r.mark:] {
		if q.Method == "agent.prompt" {
			text, _ := q.Params["text"].(string)
			if len(text) > 20 {
				text = text[:20]
			}
			b.WriteString(text + "|")
		}
	}
	return b.String()
}

func (r *rig) probe(home string, env map[string]string) string {
	r.t.Helper()
	if code := r.checkpointer(r.config(home, env)).Probe(context.Background()); code != 0 {
		r.t.Fatalf("probe exit %d", code)
	}
	return r.out.String()
}

func write(t *testing.T, p, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func usage(t *testing.T, p string, n int) {
	write(t, p, fmt.Sprintf(`{"type":"assistant","message":{"usage":{"input_tokens":%d,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`+"\n", n))
}

func touch(t *testing.T, p string, when string) {
	t.Helper()
	tm, err := time.ParseInLocation("200601021504", when, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, tm, tm); err != nil {
		t.Fatal(err)
	}
}

func tc(n int) string {
	return fmt.Sprintf(`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":%d,"cached_input_tokens":0,"output_tokens":7,"reasoning_output_tokens":0,"total_tokens":%d},"total_token_usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"model_context_window":400000}}}`+"\n", n, n+7)
}

func want(t *testing.T, name, got, w string) {
	t.Helper()
	if got != w {
		t.Errorf("%s: got [%s] want [%s]", name, got, w)
	}
}

// The cases of the old checkpointer suite, in its order.
func TestCheckpointerSuite(t *testing.T) {
	r := newRig(t)
	r.agent("w9:p9", "claude", "id", "fake")
	home := filepath.Join(r.dir, "home")
	write(t, filepath.Join(home, ".claude/projects/p/fake.jsonl"),
		`{"type":"assistant","message":{"usage":{"input_tokens":10,"cache_creation_input_tokens":90,"cache_read_input_tokens":300000}}}`+"\n")

	r.once(home, nil)
	want(t, "over limit: memory prompt then /compact", r.prompts(), "/memory-with-dag Cap|/compact|")
	r.once(home, map[string]string{"LIMIT": "400000"})
	want(t, "under limit: memory prompt only", r.prompts(), "/memory-with-dag Cap|")
	r.once(home, map[string]string{"PROMPT": "/save-now", "COMPACT": "/squash"})
	want(t, "custom prompt and compact command", r.prompts(), "/save-now|/squash|")
	r.once(home, map[string]string{"LIMIT": "400000"})
	idle := 0
	for _, w := range r.waits {
		if fmt.Sprint(w) == "[idle done]" {
			idle++
		}
		for _, s := range w {
			if s == "blocked" {
				t.Errorf("waits for blocked: %v", w)
			}
		}
	}
	want(t, "waits for idle or done, never blocked", fmt.Sprint(idle), "2")

	out := r.probe(home, nil)
	want(t, "probe reads context", strings.TrimSpace(out[strings.LastIndex(out, "context=")+len("context="):]), "300100")

	// The named file (session id "fake", 5000 tokens, the oldest) is read,
	// not the newest file of another session (300100 tokens).
	home2 := filepath.Join(r.dir, "home2")
	p2 := filepath.Join(home2, ".claude/projects/p")
	usage(t, filepath.Join(p2, "fake.jsonl"), 5000)
	touch(t, filepath.Join(p2, "fake.jsonl"), "202601010000")
	usage(t, filepath.Join(p2, "mid.jsonl"), 6000)
	touch(t, filepath.Join(p2, "mid.jsonl"), "202601020000")
	usage(t, filepath.Join(p2, "top.jsonl"), 300100)
	r.once(home2, nil)
	want(t, "a newer file of another session: the named file is read, no /compact", r.prompts(), "/memory-with-dag Cap|")
	r.once(home2, map[string]string{"TRANSCRIPT": filepath.Join(p2, "mid.jsonl")})
	want(t, "TRANSCRIPT names an older file: no /compact from the newest file", r.prompts(), "/memory-with-dag Cap|")
	r.once(home2, map[string]string{"TRANSCRIPT": filepath.Join(p2, "top.jsonl")})
	want(t, "TRANSCRIPT names a file over the limit: /compact", r.prompts(), "/memory-with-dag Cap|/compact|")
	rc := r.once(home2, map[string]string{"TRANSCRIPT": filepath.Join(p2, "missing.jsonl")})
	want(t, "TRANSCRIPT names a missing file: exit 1", fmt.Sprint(rc), "1")
	want(t, "and no prompt is sent", r.prompts(), "")

	// A session id with no file: no transcript, even when the working
	// folder of the pane holds a file over the limit.
	work := filepath.Join(r.dir, "work")
	usage(t, filepath.Join(home2, ".claude/projects", Derived(work), "lone.jsonl"), 300100)
	r.agent("w9:p9", "claude", "id", "gone")
	rc = r.once(home2, map[string]string{"PWD": work})
	want(t, "no file for the id: exit 1, although the working folder holds a file", fmt.Sprint(rc), "1")
	want(t, "and no prompt is sent from that file", r.prompts(), "")

	// Codex rollouts.
	cx := filepath.Join(r.dir, "codex/sessions/2000/01/02")
	sid := "0a1b2c3d-0000-4000-8000-000000000001"
	rollout := filepath.Join(cx, "rollout-2000-01-02T01-02-03-"+sid+".jsonl")
	write(t, rollout, `{"type":"session_meta","payload":{"id":"`+sid+`"}}`+"\n"+tc(1000)+tc(300100)+
		`{"type":"event_msg","payload":{"type":"token_count","info":null}}`+"\n")
	codexHome := filepath.Join(r.dir, "codex")
	r.agent("w9:p9", "codex", "id", sid)
	out = r.probe(home, map[string]string{"CODEX_HOME": codexHome})
	want(t, "codex probe finds the rollout of the session id", fmt.Sprint(strings.Count(out, "rollout-2000-01-02T01-02-03-"+sid+".jsonl context=")), "1")
	want(t, "codex probe reads the last token_count with usage", strings.TrimSpace(out[strings.LastIndex(out, "context=")+8:]), "300100")
	want(t, "codex probe says why", fmt.Sprint(strings.Count(out, "transcript: the Codex rollout of the session id that Herdr reports ('"+sid+"')")), "1")
	r.once(home, map[string]string{"CODEX_HOME": codexHome})
	want(t, "codex over limit: memory prompt without the slash command, then /compact", r.prompts(), "Capture all learning|/compact|")
	r.once(home, map[string]string{"CODEX_HOME": codexHome, "LIMIT": "400000"})
	want(t, "codex under limit: memory prompt only", r.prompts(), "Capture all learning|")
	r.once(home, map[string]string{"CODEX_HOME": codexHome, "PROMPT": "/save-now"})
	want(t, "codex with PROMPT set: sent as is", r.prompts(), "/save-now|/compact|")
	r.agent("w9:p9", "codex", "id", "missing")
	rc = r.once(home, map[string]string{"CODEX_HOME": codexHome})
	want(t, "codex with no rollout for the id: exit 1", fmt.Sprint(rc), "1")
	want(t, "and no prompt is sent", r.prompts(), "")
	// Without CODEX_HOME: ~/.codex.
	home3 := filepath.Join(r.dir, "home3")
	write(t, filepath.Join(home3, ".codex/sessions/2000/01/01/rollout-2000-01-01T09-00-00-"+sid+".jsonl"), tc(60000))
	r.agent("w9:p9", "codex", "id", sid)
	out = r.probe(home3, nil)
	want(t, "codex without CODEX_HOME reads ~/.codex/sessions", strings.TrimSpace(out[strings.LastIndex(out, "context=")+8:]), "60000")
	r.agent("w9:p9", "codex", "path", rollout)
	out = r.probe(home, nil)
	want(t, "codex session reported as a path", strings.TrimSpace(out[strings.LastIndex(out, "context=")+8:]), "300100")
	r.agent("w9:p9", "claude", "id", "fake")
	out = r.probe(home, map[string]string{"TRANSCRIPT": rollout})
	want(t, "TRANSCRIPT names a Codex rollout: its context is read", strings.TrimSpace(out[strings.LastIndex(out, "context=")+8:]), "300100")
}

// The P cases of the old shell suite: --probe names the transcript and why. The
// fake reports only a session id (no harness), as the suite's fake herdr.
func TestProbeCases(t *testing.T) {
	r := newRig(t)
	cpp := filepath.Join(r.dir, "cp/home/.claude/projects")
	home := filepath.Join(r.dir, "cp/home")
	work := filepath.Join(r.dir, "cp/work")
	empty := filepath.Join(r.dir, "cp/empty")
	for _, d := range []string{work, empty} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	usage(t, filepath.Join(cpp, "projA/old.jsonl"), 111)
	touch(t, filepath.Join(cpp, "projA/old.jsonl"), "202601010000")
	usage(t, filepath.Join(cpp, "projA/cur.jsonl"), 222)
	usage(t, filepath.Join(cpp, "projB/other.jsonl"), 999)
	session := func(sid string) {
		r.fx.SetAgent("w9:p9", map[string]any{"pane_id": "w9:p9", "agent_session": map[string]any{"value": sid}})
	}
	probe := func(sid, pwd, transcript string) string {
		session(sid)
		return r.probe(home, map[string]string{"PWD": pwd, "TRANSCRIPT": transcript})
	}
	match := func(name, re, out string) {
		t.Helper()
		if !regexp.MustCompile(re).MatchString(out) {
			t.Errorf("%s: /%s/ not in %q", name, re, out)
		}
	}
	nomatch := func(name, re, out string) {
		t.Helper()
		if regexp.MustCompile(re).MatchString(out) {
			t.Errorf("%s: /%s/ in %q", name, re, out)
		}
	}
	out := probe("cur", work, "")
	match("P1 the file of the reported id is used when it is the newest of its folder", `projA/cur.jsonl context=222`, out)
	match("P1b and the probe says why", `transcript: the file of the session id that Herdr reports`, out)
	nomatch("P1c and gives no hint, because no newer file exists", `hint:`, out)
	out = probe("old", work, "")
	match("P2 the file of the reported id wins over a newer file of its folder", `projA/old.jsonl context=111`, out)
	nomatch("P2b and the newer file is not read", `cur.jsonl context=`, out)
	match("P2c and the probe says why", `transcript: the file of the session id that Herdr reports \('old'\)`, out)
	match("P2d and shows the newer file as a hint", `hint: a newer transcript exists: .*projA/cur.jsonl; if the id is stale, set TRANSCRIPT=<path>`, out)
	derived := Derived(work)
	usage(t, filepath.Join(cpp, derived, "first.jsonl"), 333)
	touch(t, filepath.Join(cpp, derived, "first.jsonl"), "202601010000")
	usage(t, filepath.Join(cpp, derived, "second.jsonl"), 444)
	out = probe("gone", work, "")
	match("P3 no file for the id: no transcript is chosen", `transcript: no file for session id 'gone'`, out)
	nomatch("P3b and no file of the working folder's project is read", `context=`, out)
	match("P3c and the newest file of that folder is shown as a hint", `hint: .*projects/`+regexp.QuoteMeta(derived)+`/second.jsonl; if the id is stale, set TRANSCRIPT=<path>`, out)
	out = probe("gone", empty, "")
	match("P4 no file for the id and no project folder: the probe says so", `transcript: no file for session id 'gone' and no .jsonl in`, out)
	nomatch("P4b and names no file", `cur.jsonl|second.jsonl|other.jsonl`, out)
	nomatch("P4c and prints no context line and no error", `context=|No such file|tail:`, out+r.errs.String())
	session("gone")
	rc := r.once(home, map[string]string{"PWD": empty})
	want(t, "P5 --once with no transcript exits 1", fmt.Sprint(rc), "1")
	rc = r.once(home, map[string]string{"PWD": work})
	out = r.out.String() + r.errs.String()
	match("P5b --once with no file for the id stops, although the working folder holds a file", `no transcript found`, out)
	nomatch("P5c and sends no checkpoint", `sending`, out)
	want(t, "P5b exit", fmt.Sprint(rc), "1")
	out = probe("cur", work, filepath.Join(cpp, "projB/other.jsonl"))
	match("P6 TRANSCRIPT names the file to read, whatever the session id says", `projB/other.jsonl context=999`, out)
	match("P6b and the probe says so", `transcript: TRANSCRIPT is set`, out)
	out = probe("cur", work, filepath.Join(r.dir, "cp/no-such.jsonl"))
	match("P6c TRANSCRIPT names a missing file: the probe says so", `transcript: TRANSCRIPT is set but the file does not exist`, out)
	nomatch("P6d and no other file is read", `context=`, out)
}
