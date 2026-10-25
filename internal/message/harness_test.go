package message

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/inbox/inboxtest"
)

// The shell suite's names: the adjutant A and the orchestrator O, and the
// first message id.
const (
	A  = inboxtest.Adjutant
	O  = inboxtest.Orchestrator
	ID = "m000000000001"
)

// tc is one case of message-tests.sh's new_case: a private inbox with a
// setup proof, a fake herdr with two agents, and on request an enrolled
// and approved pair. The clock is fake: a sleep advances it.
type tc struct {
	t   *testing.T
	ic  *inboxtest.Case
	srv *herdrtest.Server
	fx  *herdrtest.Fixture
	now time.Time
	// env is added to every command's environment.
	env    map[string]string
	events Events
}

func newCase(t *testing.T, enroll bool) *tc {
	t.Helper()
	c := &tc{t: t, ic: inboxtest.New(t), env: map[string]string{}, now: time.Now().Truncate(time.Second)}
	c.srv = herdrtest.Start(t)
	c.fx = herdrtest.Serve(c.srv)
	c.setAgents("sender-pane", "sender-session", "receiver-pane", "receiver-session")
	c.write(c.base("body.txt"), "full brief\n")
	if enroll {
		c.enroll()
	}
	return c
}

func (c *tc) enroll() {
	c.t.Helper()
	c.mustRun("sender-pane", "register", "propose", A, "adjutant")
	c.mustRun("receiver-pane", "register", "propose", O, "orchestrator")
	c.mustRun("sender-pane", "register", "approve", c.digest(c.in("peers")))
	c.write(c.in("state", "heartbeat"), "")
}

// setAgents replaces the fixture's agents: pane, session pairs.
func (c *tc) setAgents(ps ...string) {
	for _, a := range []string{"sender-pane", "receiver-pane", "task-pane", "other-pane", "moved-pane", "rogue-pane", "courier-pane"} {
		c.fx.SetAgent(a, nil)
	}
	for i := 0; i+1 < len(ps); i += 2 {
		c.fx.SetAgent(ps[i], herdrtest.Agent(ps[i], ps[i+1]))
	}
}

// codexTask adds the Codex task agent and its resume target.
func (c *tc) codexTask() {
	a := herdrtest.Agent("task-pane", "task-session")
	a["agent"] = "codex"
	a["agent_session"] = map[string]any{"source": "hook", "agent": "codex", "kind": "id", "value": "task-session"}
	c.fx.SetAgent("task-pane", a)
	c.ic.WriteJSON(c.in("resume-targets.json"), map[string]any{"im-task": map[string]any{"role": "task-agent",
		"identity": map[string]any{"pane": "task-pane", "session": map[string]any{"source": "hook", "agent": "codex", "kind": "id", "value": "task-session"}}}})
	c.fx.SetScreen("task-pane", "Goal paused (/goal resume)\n")
}

// allowResume answers agent.prompt as the shell fake with allow-resume: the
// exact /goal resume succeeds and the pane then shows a working turn.
func (c *tc) allowResume() {
	c.srv.Handle("agent.prompt", func(p map[string]any) (any, string) {
		if p["text"] == "/goal resume" {
			c.fx.SetScreen(p["target"].(string), "Working\n")
			return map[string]any{"type": "ok"}, ""
		}
		return nil, "unsupported_fake_call"
	})
}

func (c *tc) relayBoundary() {
	c.ic.RelayBoundary(c.agentRecords()...)
}

func (c *tc) agentRecords() []map[string]any {
	cl := herdr.New(c.srv.Socket)
	list, _ := cl.AgentList(t0())
	var out []map[string]any
	for _, a := range list {
		v, _ := inbox.Decode(a.Raw)
		out = append(out, inbox.Object(v))
	}
	return out
}

func (c *tc) base(p ...string) string { return filepath.Join(append([]string{c.ic.Base}, p...)...) }
func (c *tc) in(p ...string) string   { return filepath.Join(append([]string{c.ic.Dir}, p...)...) }
func (c *tc) ob(p ...string) string   { return c.in(append([]string{O}, p...)...) }

func (c *tc) write(p, s string) { c.t.Helper(); c.ic.Write(p, s) }

func (c *tc) read(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

func (c *tc) digest(p string) string {
	d, err := inbox.DigestFile(p)
	if err != nil {
		c.t.Fatal(err)
	}
	return d
}

// editJSON is jq_edit on an object file.
func (c *tc) editJSON(p string, fn func(map[string]any)) { c.t.Helper(); c.ic.Edit(p, fn) }

// old sets a file's time to 2020-01-01, as touch -t 202001010000.
func old(t *testing.T, p string) {
	tm := time.Date(2020, 1, 1, 0, 0, 0, 0, time.Local)
	if err := os.Chtimes(p, tm, tm); err != nil {
		t.Fatal(err)
	}
}

func yn(p string) string {
	if inbox.Exists(p) {
		return "yes"
	}
	return "no"
}

func nfiles(dir, pattern string) int {
	ms, _ := filepath.Glob(filepath.Join(dir, pattern))
	return len(ms)
}

// run runs one command as pane and returns its stdout and stderr together
// (2>&1) and its exit code.
func (c *tc) run(pane string, extra map[string]string, cmd string, args ...string) (string, int) {
	c.t.Helper()
	env := map[string]string{"HERDSMAN_INBOX": c.ic.Dir, "HERDSMAN_PREFLIGHT": c.ic.Proof, "HERDSMAN_MESSAGES_DIR": c.ic.Installed,
		"HERDR_PANE_ID": pane, "WATCH_REQUIRED": "0"}
	for k, v := range c.env {
		env[k] = v
	}
	for k, v := range extra {
		env[k] = v
	}
	var out bytes.Buffer
	e := &Env{Getenv: func(k string) string { return env[k] }, Stdout: &out, Stderr: &out, Client: herdr.New(c.srv.Socket),
		Now: func() time.Time { return c.now }, Sleep: func(d time.Duration) { c.now = c.now.Add(d) }, Events: c.events, Executable: env["@EXE"]}
	var code int
	switch cmd {
	case "message":
		code = e.Main(args)
	case "register":
		code = e.Register(args)
	case "send":
		code = e.Send(args)
	case "read":
		code = e.Read(args)
	case "ack":
		code = e.Ack(args)
	case "status":
		code = e.Status(args)
	case "wait":
		code = e.Wait(args)
	case "hold":
		code = e.Hold(args)
	case "authority":
		code = e.Authority(args)
	case "courier":
		code = e.Courier(args)
	case "card-watch":
		code = e.CardWatch(args)
	case "resume":
		code = e.Resume(args)
	default:
		c.t.Fatalf("unknown command %s", cmd)
	}
	return out.String(), code
}

// runConcurrent runs a command on the real clock, for cases that race two
// processes.
func (c *tc) runConcurrent(pane, cmd string, args ...string) string {
	env := map[string]string{"HERDSMAN_INBOX": c.ic.Dir, "HERDSMAN_PREFLIGHT": c.ic.Proof, "HERDSMAN_MESSAGES_DIR": c.ic.Installed,
		"HERDR_PANE_ID": pane, "WATCH_REQUIRED": "0"}
	var out bytes.Buffer
	e := &Env{Getenv: func(k string) string { return env[k] }, Stdout: &out, Stderr: &out, Client: herdr.New(c.srv.Socket)}
	switch cmd {
	case "register":
		e.Register(args)
	case "read":
		e.Read(args)
	}
	return out.String()
}

func (c *tc) mustRun(pane, cmd string, args ...string) string {
	c.t.Helper()
	out, code := c.run(pane, nil, cmd, args...)
	if code != 0 {
		c.t.Fatalf("%s %v: exit %d: %s", cmd, args, code, out)
	}
	return out
}

// send is the suite's send: A to O, the body file, a brief.
func (c *tc) send() string {
	out, _ := c.run("sender-pane", nil, "send", A, O, c.base("body.txt"), "brief")
	return out
}

// courier is one courier run of a second (RUN_FOR=1) with extra settings.
func (c *tc) courier(extra ...string) (string, int) {
	env := map[string]string{"RUN_FOR": "1"}
	for _, kv := range extra {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	return c.run("courier-pane", env, "courier")
}

func (c *tc) watch(extra ...string) (string, int) {
	env := map[string]string{"RUN_FOR": "1", "INTERVAL": "1"}
	for _, kv := range extra {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	return c.run("courier-pane", env, "card-watch")
}

func (c *tc) wait(pane, me string, extra ...string) (string, int) {
	env := map[string]string{"DEADLINE": "2", "RECHECK": "1"}
	for _, kv := range extra {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	return c.run(pane, env, "wait", me)
}

// calls renders the fake herdr's requests as the shell fake's calls.log
// lines (the herdr CLI form).
func (c *tc) calls() string {
	var b strings.Builder
	for _, r := range c.srv.Requests() {
		p := r.Params
		s := func(k string) string { v, _ := p[k].(string); return v }
		switch r.Method {
		case "agent.get":
			fmt.Fprintf(&b, "agent get %s\n", s("target"))
		case "agent.list":
			b.WriteString("agent list\n")
		case "pane.list":
			b.WriteString("pane list\n")
		case "pane.read":
			fmt.Fprintf(&b, "pane read %s\n", s("pane_id"))
		case "pane.report_metadata":
			fmt.Fprintf(&b, "pane report-metadata %s --source %s", s("pane_id"), s("source"))
			toks, _ := p["tokens"].(map[string]any)
			for k, v := range toks {
				if v == nil {
					fmt.Fprintf(&b, " --clear-token %s", k)
				} else {
					fmt.Fprintf(&b, " --token %s=%v", k, v)
				}
			}
			b.WriteString("\n")
		case "notification.show":
			fmt.Fprintf(&b, "notification show %s --body %s\n", s("title"), s("body"))
		case "agent.prompt":
			fmt.Fprintf(&b, "agent prompt %s %s\n", s("target"), s("text"))
		case "tab.focus":
			fmt.Fprintf(&b, "tab focus %s\n", s("tab_id"))
		case "events.subscribe":
			j, _ := json.Marshal(p)
			fmt.Fprintf(&b, "events.subscribe %s\n", j)
		default:
			fmt.Fprintf(&b, "%s\n", r.Method)
		}
	}
	return b.String()
}

func (c *tc) count(re string) int {
	return len(regexp.MustCompile("(?m)"+re).FindAllString(c.calls(), -1))
}

func verifiedLog(c *tc) string {
	var b strings.Builder
	ms, _ := filepath.Glob(c.in("state", "diagnostic-*.log"))
	for _, m := range ms {
		for _, l := range strings.Split(c.read(m), "\n") {
			if strings.Contains(l, "VERIFIED") {
				b.WriteString(l + "\n")
			}
		}
	}
	return b.String()
}

// check is the suite's check: got must match the regular expression.
func check(t *testing.T, name, re, got string) {
	t.Helper()
	if !regexp.MustCompile(re).MatchString(got) {
		t.Errorf("%s: expected /%s/ got: %q", name, re, got)
	}
}

// nocheck is the suite's nocheck: got must not match.
func nocheck(t *testing.T, name, re, got string) {
	t.Helper()
	if regexp.MustCompile(re).MatchString(got) {
		t.Errorf("%s: unexpected /%s/ in: %q", name, re, got)
	}
}

func jqField(t *testing.T, p, field string) string {
	v, err := decodeFile(p)
	if err != nil {
		return ""
	}
	return raw(inbox.Object(v)[field])
}

func digestText(s string) string { return inbox.Digest([]byte(s)) }

// globT is glob for test setup, where a listing error cannot happen.
func globT(dir, pattern string) []string {
	ms, _ := glob(dir, pattern)
	return ms
}
