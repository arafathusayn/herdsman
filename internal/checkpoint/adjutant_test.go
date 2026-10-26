package checkpoint

import (
	"context"
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

// cpCase is message-tests.sh's cpcase: an inbox with the approved pair
// (the adjutant in sender-pane, the orchestrator in receiver-pane), the
// fake herdr serving both agents and accepting prompts, and a transcript
// of 10 tokens.
type cpCase struct {
	*rig
	box *inboxtest.Case
}

func newCP(t *testing.T) *cpCase {
	t.Helper()
	r := newRig(t)
	box := inboxtest.New(t)
	box.Enroll("sender-pane", "sender-session", "receiver-pane", "receiver-session")
	r.fx.SetAgent("sender-pane", herdrtest.Agent("sender-pane", "sender-session"))
	r.fx.SetAgent("receiver-pane", herdrtest.Agent("receiver-pane", "receiver-session"))
	write(t, filepath.Join(r.dir, "transcript.jsonl"),
		`{"type":"assistant","message":{"usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`+"\n")
	return &cpCase{rig: r, box: box}
}

// ckpt is the suite's ckpt: --once on target with the case's settings and
// extra assignments; it returns the exit status.
func (c *cpCase) ckpt(target string, env map[string]string) int {
	c.t.Helper()
	m := map[string]string{"TARGET": target, "TRANSCRIPT": filepath.Join(c.dir, "transcript.jsonl"), "LOG": filepath.Join(c.dir, "ckpt.log"),
		"LIMIT": "1", "PROMPT": "/save-memory", "COMPACT": "/compact", "HOME": c.dir,
		"HERDSMAN_INBOX": c.box.Dir, "HERDSMAN_PREFLIGHT": c.box.Proof, "HERDSMAN_MESSAGES_DIR": c.box.Installed}
	for k, v := range env {
		m[k] = v
	}
	cfg, err := FromEnv(func(k string) string { return m[k] }, c.dir)
	if err != nil {
		c.t.Fatal(err)
	}
	c.mark = len(c.srv.Requests())
	return c.checkpointer(cfg).Once(context.Background())
}

// sent is the suite's sent(): each prompt as "agent prompt <pane> <text>|".
func (c *cpCase) sent() string {
	var b strings.Builder
	for _, q := range c.srv.Requests()[c.mark:] {
		if q.Method == "agent.prompt" {
			b.WriteString("agent prompt " + q.Params["target"].(string) + " " + q.Params["text"].(string) + "|")
		}
	}
	return b.String()
}

func (c *cpCase) log() string {
	b, _ := os.ReadFile(filepath.Join(c.dir, "ckpt.log"))
	return string(b)
}

func check(t *testing.T, name, re, got string) {
	t.Helper()
	if !regexp.MustCompile(re).MatchString(got) {
		t.Errorf("%s: /%s/ not in %q", name, re, got)
	}
}

const orch = inboxtest.Orchestrator

// The CP cases of message-tests.sh: in adjutant mode the checkpointer checks
// the approved orchestrator identity right before each input.
func TestAdjutantCases(t *testing.T) {
	var all []*cpCase
	add := func(x *cpCase) *cpCase { all = append(all, x); return x }
	c := add(newCP(t))
	c.ckpt("receiver-pane", nil)
	check(t, "CP1 ordinary mode is unchanged: memory prompt then compact, no registry needed",
		`^agent prompt receiver-pane /save-memory\|agent prompt receiver-pane /compact\|$`, c.sent())

	c = add(newCP(t))
	rc := c.ckpt("receiver-pane", map[string]string{"HERDSMAN_ORCHESTRATOR": orch})
	check(t, "CP2 the approved orchestrator identity gets both inputs",
		`^0 agent prompt receiver-pane /save-memory\|agent prompt receiver-pane /compact\|$`, itoa(rc)+" "+c.sent())

	c = add(newCP(t))
	c.fx.Edit("receiver-pane", "agent_session", map[string]any{"source": "hook", "agent": "claude", "kind": "id", "value": "new-session"})
	rc = c.ckpt("receiver-pane", map[string]string{"HERDSMAN_ORCHESTRATOR": orch})
	check(t, "CP3 a changed orchestrator session gets no input", `^1 $`, itoa(rc)+" "+c.sent())
	check(t, "CP3b and the log names the refusal", `orchestrator session changed`, c.log())

	c = add(newCP(t))
	rc = c.ckpt("sender-pane", map[string]string{"HERDSMAN_ORCHESTRATOR": inboxtest.Adjutant})
	check(t, "CP4 the adjutant is never a checkpoint target", `^1 $`, itoa(rc)+" "+c.sent())
	check(t, "CP4b refused as not the orchestrator", `never type into the adjutant`, c.log())
	rc = c.ckpt("sender-pane", map[string]string{"HERDSMAN_ORCHESTRATOR": orch})
	check(t, "CP4c the orchestrator name with the adjutant pane is refused", `^1 $`, itoa(rc)+" "+c.sent())
	check(t, "CP4d as the wrong pane", `not the approved orchestrator pane`, c.log())

	c = add(newCP(t))
	c.fx.SetAgent("receiver-pane", nil)
	rc = c.ckpt("receiver-pane", map[string]string{"HERDSMAN_ORCHESTRATOR": orch})
	check(t, "CP5 a closed orchestrator pane gets no input", `^1 .*closed`, itoa(rc)+" "+c.sent()+grepLog(c.log(), "identity check"))

	c = add(newCP(t))
	c.box.Edit(filepath.Join(c.box.Dir, "peers"), func(m map[string]any) {
		m[orch].(map[string]any)["identity"].(map[string]any)["session"].(map[string]any)["value"] = "pending-session"
	})
	rc = c.ckpt("receiver-pane", map[string]string{"HERDSMAN_ORCHESTRATOR": orch})
	check(t, "CP6 a pending registry change holds every input", `^1 .*awaiting human approval`, itoa(rc)+" "+c.sent()+grepLog(c.log(), "identity check"))

	c = add(newCP(t))
	c.srv.Handle("agent.prompt", func(p map[string]any) (any, string) {
		c.fx.Edit(p["target"].(string), "agent_session", map[string]any{"source": "hook", "agent": "claude", "kind": "id", "value": "drifted-session"})
		return map[string]any{"type": "ok"}, ""
	})
	rc = c.ckpt("receiver-pane", map[string]string{"HERDSMAN_ORCHESTRATOR": orch})
	check(t, "CP7 drift after the memory prompt stops the compact input", `^1 agent prompt receiver-pane /save-memory\|$`, itoa(rc)+" "+c.sent())

	c = add(newCP(t))
	rc = c.ckpt("receiver-pane", map[string]string{"HERDSMAN_ORCHESTRATOR": "route-nobody"})
	check(t, "CP8 an unknown orchestrator name gets no input", `^1 .*unknown orchestrator`, itoa(rc)+" "+c.sent()+grepLog(c.log(), "identity check"))

	c = add(newCP(t))
	c.box.Edit(c.box.Proof, func(m map[string]any) { m["policy_verified"] = false })
	rc = c.ckpt("receiver-pane", map[string]string{"HERDSMAN_ORCHESTRATOR": orch})
	check(t, "CP9 a stale setup proof gets no input", `^1 .*proof stale`, itoa(rc)+" "+c.sent()+grepLog(c.log(), "identity check"))

	// N1c: in every checkpoint case, the only calls were reads, waits and
	// the exact prompts /save-memory and /compact to the orchestrator pane.
	for _, x := range all {
		for _, q := range x.srv.Requests() {
			switch q.Method {
			case "agent.get", "events.subscribe":
			case "agent.prompt":
				if q.Params["target"] != "receiver-pane" || (q.Params["text"] != "/save-memory" && q.Params["text"] != "/compact") {
					t.Errorf("N1c prompted %v %q", q.Params["target"], q.Params["text"])
				}
			default:
				t.Errorf("N1c unexpected call %s", q.Method)
			}
		}
	}
}

// Y3 of message-tests.sh: in adjutant mode the checkpointer sends input only
// while it holds the shared input lock.
func TestInputLock(t *testing.T) {
	c := newCP(t)
	lock := filepath.Join(c.box.Dir, inbox.MutationLock)
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	rc := c.ckpt("receiver-pane", map[string]string{"HERDSMAN_ORCHESTRATOR": orch, "LOCK_WAIT": "1"})
	check(t, "Y3 a held input lock stops the checkpoint input", `^1 $`, itoa(rc)+" "+c.sent())
	check(t, "Y3b the lock is not taken over and the log names it", `^yes .*input lock busy`, yn(lock)+" "+c.log())
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	rc = c.ckpt("receiver-pane", map[string]string{"HERDSMAN_ORCHESTRATOR": orch})
	check(t, "Y3c with the lock free the inputs go out and the lock is released", `^0 agent prompt receiver-pane /save-memory.* no$`, itoa(rc)+" "+c.sent()+" "+yn(lock))

	// LOCK_WAIT tries a second apart; a value test refuses tries once.
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	for lw, sleeps := range map[string]int{"3": 2, "1": 0} {
		cfg := c.config(c.dir, map[string]string{"HERDSMAN_ORCHESTRATOR": orch, "HERDSMAN_INBOX": c.box.Dir, "LOCK_WAIT": lw})
		cp := c.checkpointer(cfg)
		n := 0
		cp.Sleep = func(time.Duration) { n++ }
		if cp.inputLock(context.Background()) || n != sleeps {
			t.Errorf("LOCK_WAIT=%s: %d sleeps", lw, n)
		}
	}
}

// A run that ends while the input lock is busy stops retrying: the lock
// wait follows the run's context, so SIGTERM does not wait out LOCK_WAIT,
// sends nothing and leaves the other writer's lock in place.
func TestInputLockCancel(t *testing.T) {
	c := newCP(t)
	lock := filepath.Join(c.box.Dir, inbox.MutationLock)
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := c.config(c.dir, map[string]string{"TARGET": "receiver-pane", "TRANSCRIPT": filepath.Join(c.dir, "transcript.jsonl"), "LIMIT": "1", "LOG": filepath.Join(c.dir, "ckpt.log"),
		"HERDSMAN_ORCHESTRATOR": orch, "HERDSMAN_INBOX": c.box.Dir, "HERDSMAN_PREFLIGHT": c.box.Proof, "HERDSMAN_MESSAGES_DIR": c.box.Installed,
		"LOCK_WAIT": "60"})
	cp := c.checkpointer(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := 0
	cp.Sleep = func(time.Duration) { n++; cancel() }
	c.mark = len(c.srv.Requests())
	rc := cp.Once(ctx)
	if rc == 0 || n != 1 || c.sent() != "" || yn(lock) != "yes" {
		t.Errorf("cancelled lock wait: rc %d, %d sleeps, sent %q, lock %s, log %q", rc, n, c.sent(), yn(lock), c.log())
	}
}

// The guard on its own: checkpointer-guard.sh's lines and exit codes, and
// Z1f: a changed adapter beside the installed folder makes the proof stale.
func TestGuard(t *testing.T) {
	c := newCP(t)
	cl := herdr.New(c.srv.Socket)
	o := c.box.Options(false)
	run := func(args ...string) (string, string, int) { return Guard(context.Background(), cl, o, args) }
	if out, e, code := run(orch, "receiver-pane"); out != "OK route-orchestrator receiver-pane\n" || e != "" || code != 0 {
		t.Errorf("ok: %q %q %d", out, e, code)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, GuardUsage + "\n"},
		{[]string{orch}, GuardUsage + "\n"},
		{[]string{"Bad", "receiver-pane"}, "invalid orchestrator name\n"},
	} {
		if out, e, code := run(tc.args...); out != "" || e != tc.want || code != 2 {
			t.Errorf("%v: %q %q %d", tc.args, out, e, code)
		}
	}
	c.fx.Fail("agent.get", "temporary_failure")
	_, e, code := run(orch, "receiver-pane")
	if e != "agent get unavailable: temporary_failure: temporary_failure\norchestrator identity unavailable; no input\n" || code != 2 {
		t.Errorf("unavailable: %q %d", e, code)
	}
	c.fx.Fail("agent.get", "")
	o.Dir = filepath.Join(c.dir, "missing")
	if _, e, code := run(orch, "receiver-pane"); e != "pre-create a private inbox directory\n" || code != 2 {
		t.Errorf("no inbox: %q %d", e, code)
	}
	o = c.box.Options(false)
	c.box.Write(filepath.Join(filepath.Dir(c.box.Installed), "checkpointer.sh"), "#!/bin/bash\n# changed\n")
	_, e, _ = run(orch, "receiver-pane")
	check(t, "Z1f and the checkpointer guard refuses input", `proof stale`, e)
}

func itoa(n int) string { return string(rune('0' + n)) }

func yn(p string) string {
	if inbox.Exists(p) {
		return "yes"
	}
	return "no"
}

func grepLog(log, s string) string {
	var out []string
	for l := range strings.SplitSeq(log, "\n") {
		if strings.Contains(l, s) {
			out = append(out, l)
		}
	}
	return " " + strings.Join(out, "\n")
}
