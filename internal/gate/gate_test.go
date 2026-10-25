package gate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/inbox/inboxtest"
)

// The AG, Y1, Y2, GX1 and Z1e cases of the skill's message-tests.sh.

type rig struct {
	t     *testing.T
	c     *inboxtest.Case
	srv   *herdrtest.Server
	fx    *herdrtest.Fixture
	hc    *herdr.Client
	apr   string
	draft string
	mu    sync.Mutex
	now   time.Time
}

func newRig(t *testing.T) *rig {
	srv := herdrtest.Start(t)
	r := &rig{t: t, c: inboxtest.New(t), srv: srv, fx: herdrtest.Serve(srv), hc: herdr.New(srv.Socket), now: time.Now()}
	r.c.Enroll("sender-pane", "sender-session", "receiver-pane", "receiver-session")
	agents := []map[string]any{herdrtest.Agent("sender-pane", "sender-session"), herdrtest.Agent("receiver-pane", "receiver-session")}
	for _, a := range agents {
		r.fx.SetAgent(a["pane_id"].(string), a)
	}
	r.c.RelayBoundary(agents...)
	r.apr = filepath.Join(r.c.Dir, "approvals")
	r.draft = filepath.Join(r.c.Base, "draft.txt")
	r.c.Write(r.draft, "reviewed text\n")
	_ = os.MkdirAll(filepath.Join(r.apr, "interactions"), 0o700)
	return r
}

func (r *rig) clock() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.now
}

func (r *rig) advance(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = r.now.Add(d)
}

func (r *rig) digest(p string) string {
	s, err := inbox.DigestFile(p)
	if err != nil {
		r.t.Fatal(err)
	}
	return s
}

// gojson writes the go of a request and its recorded ask-tool answer
// (expires offset in seconds; edit changes the go).
func (r *rig) gojson(req string, expires int, edit func(map[string]any)) {
	g := map[string]any{"version": 1, "type": "user_go", "question_id": "choice", "request_id": req, "action": "post", "target": "pr-comment",
		"revision": "head1", "draft_hash": r.digest(r.draft), "expires_at": r.clock().Unix() + int64(expires), "text": "post this draft",
		"human_interaction": "ask-" + req}
	if edit != nil {
		edit(g)
	}
	r.c.WriteJSON(r.goFile(req), g)
	r.c.WriteJSON(r.interaction(req), map[string]any{"source": "ask-tool", "question_id": "choice", "request_id": req, "revision": "head1",
		"approved": true, "action": "post", "target": "pr-comment", "draft_hash": r.digest(r.draft)})
}

func (r *rig) goFile(req string) string { return filepath.Join(r.c.Base, "go-"+req+".json") }
func (r *rig) interaction(req string) string {
	return filepath.Join(r.apr, "interactions", "ask-"+req+".json")
}
func (r *rig) consumed(req string) string { return filepath.Join(r.apr, "consumed", req+".json") }

func (r *rig) gate(args ...string) (string, int) {
	r.t.Helper()
	var out bytes.Buffer
	o := r.c.Options(false)
	o.Now = r.clock
	o.Sleep = func(time.Duration) {}
	g := &Gate{Options: o, Client: r.hc, Stdout: &out, Stderr: &out}
	code := g.Run(context.Background(), args)
	return strings.ReplaceAll(out.String(), "\n", " ") + fmt.Sprintf("rc=%d", code), code
}

// consume runs consume for a request with the default bindings, or the ones given.
func (r *rig) consume(req string, bind ...string) string {
	b := []string{"pr-comment", "head1", r.draft}
	copy(b, bind)
	out, _ := r.gate("consume", r.goFile(req), "post", b[0], b[1], b[2])
	return out
}

func match(t *testing.T, name, re, s string) {
	t.Helper()
	if !regexp.MustCompile(re).MatchString(s) {
		t.Errorf("%s: want /%s/ in %q", name, re, s)
	}
}

func exists(p string) bool { return inbox.Exists(p) }

func TestGate(t *testing.T) {
	r := newRig(t)
	r.gojson("r1", 600, nil)
	match(t, "AG1 a bound, current go is consumed", `^CONSUME r1.*rc=0`, r.consume("r1"))
	rec := r.c.ReadJSON(r.consumed("r1"))
	if fmt.Sprintf("%v %v %v %v", rec["state"], rec["action"], rec["target"], rec["revision"]) != "consuming post pr-comment head1" {
		t.Errorf("AG1b consumption is recorded before the effect: %v", rec)
	}
	if b, _ := os.ReadFile(r.consumed("r1")); !strings.HasPrefix(string(b), "{\n  \"request_id\": \"r1\",\n") {
		t.Errorf("AG1c (Go) the record is jq's pretty JSON: %q", b)
	}
	match(t, "AG2 a repeat before the outcome must reconcile, never act again", `RECONCILE r1.*rc=5`, r.consume("r1"))
	out, _ := r.gate("complete", "r1", "posted")
	match(t, "AG3 the outcome is recorded", `COMPLETED r1 outcome=posted.*rc=0`, out)
	if b, _ := os.ReadFile(r.consumed("r1")); !regexp.MustCompile(`^\{"request_id":"r1",.*"state":"done","outcome":"posted","at":[0-9]+\}\n$`).Match(b) {
		t.Errorf("AG3a (Go) the completed record keeps the key order, compact: %q", b)
	}
	match(t, "AG3b a duplicate returns the recorded outcome", `DUPLICATE r1 outcome=posted.*rc=4`, r.consume("r1"))
	out, _ = r.gate("complete", "r1", "posted")
	match(t, "AG3c a second outcome is refused", `outcome already recorded.*rc=2`, out)
	r.c.Write(filepath.Join(r.c.Base, "draft2.txt"), "changed text\n")
	match(t, "AG4 a changed draft needs a new go", `draft differs.*rc=1`, r.consume("r1", "pr-comment", "head1", filepath.Join(r.c.Base, "draft2.txt")))
	match(t, "AG5 a changed target needs a new go", `target differs.*rc=1`, r.consume("r1", "other-pr"))
	match(t, "AG5b a changed revision needs a new go", `revision differs.*rc=1`, r.consume("r1", "pr-comment", "head2"))
	out, _ = r.gate("consume", r.goFile("r1"), "merge", "pr-comment", "head1", r.draft)
	match(t, "AG5c (Go) a changed action needs a new go", `action differs.*rc=1`, out)
	r.gojson("r2", -5, nil)
	match(t, "AG6 an expired go is refused", `go expired.*rc=1`, r.consume("r2"))
	if exists(r.consumed("r2")) {
		t.Error("AG6b and records nothing")
	}
	r.gojson("r3", 600, nil)
	r.c.Write(filepath.Join(r.apr, "cancels", "r3.json"), `{"version":1,"type":"cancel","request_id":"r3","revision":"head1","reason":"withdrawn"}`)
	match(t, "AG7 a cancelled request is refused", `request cancelled.*rc=1`, r.consume("r3"))
	if exists(r.consumed("r3")) {
		t.Error("AG7b and records nothing")
	}
	r.gojson("r4", 600, nil)
	_ = os.Remove(r.interaction("r4"))
	match(t, "AG8 a go with no recorded ask-tool answer is refused", `no trusted interaction.*rc=1`, r.consume("r4"))
	r.gojson("r5", 600, nil)
	r.c.Edit(r.interaction("r5"), func(m map[string]any) { m["request_id"] = "other" })
	match(t, "AG8b an ask-tool answer for another request is refused", `does not match this go.*rc=1`, r.consume("r5"))
	r.gojson("r6", 600, nil)
	r.c.Edit(r.interaction("r6"), func(m map[string]any) { m["source"] = "pane-text" })
	match(t, "AG8c quoted pane text is not a trusted interaction", `does not match this go.*rc=1`, r.consume("r6"))
	r.gojson("r7", 600, nil)
	_ = os.Rename(r.interaction("r7"), filepath.Join(r.c.Base, "ask-r7.json"))
	_ = os.Symlink(filepath.Join(r.c.Base, "ask-r7.json"), r.interaction("r7"))
	match(t, "AG8d a linked interaction record is refused", `no trusted interaction.*rc=1`, r.consume("r7"))
	r.gojson("r8", 600, func(m map[string]any) { delete(m, "draft_hash") })
	match(t, "AG9 a go with a missing field is refused", `fields missing or mistyped.*rc=1`, r.consume("r8"))
	r.gojson("r9", 600, func(m map[string]any) { m["expires_at"] = "soon" })
	match(t, "AG9b a mistyped expiry is refused", `fields missing or mistyped.*rc=1`, r.consume("r9"))
	r.gojson("r10", 600, nil)
	b, _ := os.ReadFile(r.goFile("r10"))
	r.c.Write(r.goFile("r10"), strings.Replace(string(b), "{", `{"action":"merge",`, 1))
	match(t, "AG9c a duplicate key is refused", `not strict JSON.*rc=1`, r.consume("r10"))
	r.gojson("r11", 600, nil)
	_ = os.Mkdir(filepath.Join(r.apr, "action.lock"), 0o700)
	match(t, "AG10 a held action lock refuses without a record", `action lock busy.*rc=2`, r.consume("r11"))
	if exists(r.consumed("r11")) || !exists(filepath.Join(r.apr, "action.lock")) {
		t.Error("AG10b nothing consumed and the lock is not removed")
	}
	_ = os.Remove(filepath.Join(r.apr, "action.lock"))
	out, _ = r.gate("complete", "r12", "posted")
	match(t, "AG11 an outcome without a consumption is refused", `no consumption recorded.*rc=2`, out)
	r.gojson("r13", 600, nil)
	r.c.Edit(r.c.Proof, func(m map[string]any) { m["boundary"].(map[string]any)["human_checked"] = false })
	match(t, "AG12 the HUMAN boundary consumes nothing", `^HUMAN.*rc=3`, r.consume("r13"))
	if exists(r.consumed("r13")) {
		t.Error("AG12b and records nothing")
	}
	r.c.Edit(r.c.Proof, func(m map[string]any) {
		m["boundary"].(map[string]any)["human_checked"] = true
		m["policy_verified"] = false
	})
	match(t, "AG12c a stale setup proof consumes nothing", `proof stale.*rc=2`, r.consume("r13"))
	r.c.Edit(r.c.Proof, func(m map[string]any) { m["policy_verified"] = true })
	out, _ = r.gate("consume", r.goFile("r13"), "post", "pr-comment", "head1", "-")
	match(t, "AG13 no draft for a draft-bound go is refused", `draft differs.*rc=1`, out)
	r.gojson("r14", 600, func(m map[string]any) { m["draft_hash"] = "none" })
	r.c.Edit(r.interaction("r14"), func(m map[string]any) { m["draft_hash"] = "none" })
	out, _ = r.gate("consume", r.goFile("r14"), "post", "pr-comment", "head1", "-")
	match(t, "AG13b a go without a draft binds to no draft", `^CONSUME r14.*rc=0`, out)
	for _, req := range r.srv.Methods() {
		if req == "agent.prompt" || strings.HasPrefix(req, "pane.") || strings.HasPrefix(req, "notification") {
			t.Errorf("AG14 the gate typed nothing and called no display mutation: %s", req)
		}
	}
	if exists(filepath.Join(r.c.Dir, inbox.MutationLock)) || exists(filepath.Join(r.apr, "action.lock")) {
		t.Error("the gate left a lock behind")
	}
}

func TestGateBindingsAndErrors(t *testing.T) {
	r := newRig(t)
	// Y1: the trusted interaction must be an affirmative answer bound to the exact action, target and draft.
	for _, tc := range []struct {
		req, want string
		edit      func(m map[string]any)
	}{
		{"y1", `REFUSED .*not an affirmative.*rc=1`, func(m map[string]any) { m["approved"] = false; m["answer"] = "No, do not post" }},
		{"y2", `REFUSED .*not an affirmative.*rc=1`, func(m map[string]any) { delete(m, "approved") }},
		{"y3", `REFUSED .*approves another action.*rc=1`, func(m map[string]any) { m["action"] = "merge" }},
		{"y4", `REFUSED .*approves another action.*rc=1`, func(m map[string]any) { m["target"] = "other-pr" }},
		{"y5", `REFUSED .*approves another action.*rc=1`, func(m map[string]any) { m["draft_hash"] = "1-1" }},
	} {
		r.gojson(tc.req, 600, nil)
		r.c.Edit(r.interaction(tc.req), tc.edit)
		match(t, "Y1 "+tc.req, tc.want, r.consume(tc.req))
	}
	if n, _ := filepath.Glob(filepath.Join(r.apr, "consumed", "*.json")); len(n) != 0 {
		t.Error("Y1f and none of them consumed anything")
	}
	r.gojson("y6", 600, nil)
	match(t, "Y1g the bound affirmative answer is the positive control", `^CONSUME y6.*rc=0`, r.consume("y6"))

	// The bindings of an earlier consumption.
	r.c.Edit(r.consumed("y6"), func(m map[string]any) { m["target"] = "elsewhere" })
	match(t, "a consumption with another binding", `consumed with another binding.*rc=1`, r.consume("y6"))
	r.c.Write(r.consumed("y6"), `{"a":1,"a":2}`)
	match(t, "an unreadable consumption", `consumed record unreadable.*rc=2`, r.consume("y6"))
	_ = os.Remove(r.consumed("y6"))
	_ = os.Symlink(filepath.Join(r.c.Base, "x"), r.consumed("y6"))
	match(t, "a linked consumption", `consumed record unreadable.*rc=2`, r.consume("y6"))
	_ = os.Remove(r.consumed("y6"))
	_ = os.Mkdir(filepath.Join(r.apr, "cancels", "y6.json"), 0o700)
	match(t, "a cancel that is no file", `nonregular state leaf.*rc=2`, r.consume("y6"))
	_ = os.Remove(filepath.Join(r.apr, "cancels", "y6.json"))

	// Request and interaction references.
	r.gojson("y7", 600, func(m map[string]any) { m["request_id"] = "bad/id" })
	match(t, "an invalid request id", `invalid request id.*rc=1`, r.consume("y7"))
	r.gojson("y8", 600, func(m map[string]any) { m["human_interaction"] = "../x" })
	match(t, "an invalid interaction reference", `invalid interaction reference.*rc=1`, r.consume("y8"))
	r.gojson("y9", 600, nil)
	match(t, "a missing draft", `draft unavailable.*rc=1`, r.consume("y9", "pr-comment", "head1", filepath.Join(r.c.Base, "none")))
	r.gojson("y10", 600, func(m map[string]any) { m["version"] = 2 })
	match(t, "another version", `fields missing.*rc=1`, r.consume("y10"))

	// Usage and arguments.
	for _, args := range [][]string{{}, {"fly"}, {"consume", "x"}, {"complete", "x"}} {
		out, code := r.gate(args...)
		if code != Error || !strings.Contains(out, "usage: herdsman approval-gate") {
			t.Errorf("%v: %q", args, out)
		}
	}
	out, _ := r.gate("complete", "bad/id", "posted")
	match(t, "complete: an invalid request id", `invalid request id.*rc=2`, out)
	out, _ = r.gate("complete", "y6", "bad outcome")
	match(t, "complete: an invalid outcome", `invalid outcome.*rc=2`, out)
	r.gojson("y11", 600, nil)
	r.consume("y11")
	_ = os.Mkdir(filepath.Join(r.apr, "action.lock"), 0o700)
	out, _ = r.gate("complete", "y11", "posted")
	match(t, "complete: a held action lock", `action lock busy.*rc=2`, out)
	_ = os.Remove(filepath.Join(r.apr, "action.lock"))

	// The input lock, held by another writer.
	r.gojson("y12", 600, nil)
	_ = os.Mkdir(filepath.Join(r.c.Dir, inbox.MutationLock), 0o700)
	match(t, "a held input lock", `input lock busy.*rc=2`, r.consume("y12"))
	if !exists(filepath.Join(r.c.Dir, inbox.MutationLock)) || exists(filepath.Join(r.apr, "action.lock")) {
		t.Error("the input lock was taken over, or the action lock left behind")
	}
	_ = os.Remove(filepath.Join(r.c.Dir, inbox.MutationLock))

	// A linked approvals folder, and an inbox that cannot be opened.
	_ = os.RemoveAll(filepath.Join(r.apr, "cancels"))
	_ = os.Symlink(r.c.Base, filepath.Join(r.apr, "cancels"))
	match(t, "a linked approval folder", `symlink approval state.*rc=2`, r.consume("y12"))
	_ = os.Remove(filepath.Join(r.apr, "cancels"))
	_ = os.WriteFile(filepath.Join(r.apr, "cancels"), nil, 0o600)
	match(t, "(Go) an approvals entry that is a file", `cannot create .*rc=2`, r.consume("y12"))
	_ = os.Remove(filepath.Join(r.apr, "cancels"))
	var b bytes.Buffer
	g := &Gate{Options: inbox.Options{Dir: "rel"}, Stdout: &b, Stderr: &b}
	if g.Run(context.Background(), []string{"complete", "a", "b"}) != Error || !strings.Contains(b.String(), "inbox must be absolute") {
		t.Errorf("an inbox that cannot be opened: %q", b.String())
	}
	// No state folder for the consumption record.
	r.gojson("y13", 600, nil)
	_ = os.RemoveAll(filepath.Join(r.c.Dir, "state"))
	match(t, "a consumption that cannot be recorded", `cannot record consumption.*rc=2`, r.consume("y13"))
}

// Z1e: a changed adapter makes the proof stale, and the gate refuses.
func TestGateRefusesAStaleProof(t *testing.T) {
	r := newRig(t)
	r.c.Write(filepath.Join(r.c.Base, "skill", "approval-gate.sh"), "#!/bin/bash\n# changed\n")
	out, _ := r.gate("complete", "r1", "posted")
	match(t, "Z1e and the changed adapter refuses to run", `proof stale.*rc=2`, out)
}

// Y2: an admission writer that runs during the authority check waits for
// the input lock, so a consumption never follows an exposure record.
func TestGateBoundaryUnderTheInputLock(t *testing.T) {
	r := newRig(t)
	r.gojson("y7", 600, nil)
	lock := filepath.Join(r.c.Dir, inbox.MutationLock)
	result := make(chan string, 1)
	var once sync.Once
	list := r.srv.Handle
	list("agent.list", func(p map[string]any) (any, string) {
		once.Do(func() {
			admit := func() {
				s := "no"
				if exists(r.consumed("y7")) {
					s = "yes"
				}
				r.c.Write(filepath.Join(r.c.Dir, "state", "boundary-tainted"), "exposure\n")
				_ = os.Remove(lock)
				result <- s
			}
			if os.Mkdir(lock, 0o700) == nil {
				admit()
				return
			}
			go func() {
				for os.Mkdir(lock, 0o700) != nil {
					time.Sleep(10 * time.Millisecond)
				}
				admit()
			}()
		})
		return map[string]any{"type": "agent_list", "agents": []any{herdrtest.Agent("sender-pane", "sender-session"), herdrtest.Agent("receiver-pane", "receiver-session")}}, ""
	})
	out := r.consume("y7")
	var at string
	select {
	case at = <-result:
	case <-time.After(10 * time.Second):
		t.Fatal("the admission never ran")
	}
	got := strings.Fields(out)[0] + " " + at
	if got != "CONSUME yes" && !(strings.HasPrefix(got, "HUMAN") && at == "no") {
		t.Errorf("Y2 an exposure during the authority check never precedes a consumption: %q", got)
	}
}

// GX1: a go that expires while the gate waits for the input lock or the
// authority check is refused.
func TestGateGoExpiresDuringTheAuthorityCheck(t *testing.T) {
	r := newRig(t)
	r.gojson("x1", 2, nil)
	r.srv.Handle("agent.list", func(map[string]any) (any, string) {
		r.advance(3 * time.Second)
		return map[string]any{"type": "agent_list", "agents": []any{herdrtest.Agent("sender-pane", "sender-session"), herdrtest.Agent("receiver-pane", "receiver-session")}}, ""
	})
	match(t, "GX1 a go that expires during the authority check is refused", `REFUSED go expired.*rc=1`, r.consume("x1"))
	if exists(r.consumed("x1")) {
		t.Error("GX1b and nothing is consumed")
	}
}

func TestSetFieldsAndJQRaw(t *testing.T) {
	out, err := setFields([]byte(`{"b": 1, "state": "consuming", "x": {"y": [1, 2]}}`), map[string]string{"state": "done", "outcome": "posted"})
	if err != nil || string(out) != `{"b":1,"state":"done","x":{"y":[1,2]},"outcome":"posted"}` {
		t.Fatalf("%s %v", out, err)
	}
	for _, bad := range []string{`[]`, `{"a":}`, `{"a"`, `{1:2}`} {
		if _, err := setFields([]byte(bad), nil); err == nil {
			t.Errorf("%s passed", bad)
		}
	}
	for v, want := range map[any]string{nil: "null", "s": "s", json.Number("3"): "3", true: "true"} {
		if jqRaw(v) != want {
			t.Errorf("%v: %s", v, jqRaw(v))
		}
	}
	if jqRaw([]any{1}) != "[1]" {
		t.Error("array")
	}
}

// The state writer refuses each bad write before the rename, and a
// failed rename leaves the target as it was.
func TestAtomicRefusals(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	g := &Gate{root: &inbox.Root{Dir: dir}}
	for name, tc := range map[string]struct {
		target string
		data   string
	}{
		"outside the inbox": {"/elsewhere/x.json", "{}"},
		"empty":             {filepath.Join(dir, "state", "a.json"), ""},
		"not strict JSON":   {filepath.Join(dir, "state", "a.json"), `{"a":1,"a":2}`},
		"onto a folder":     {filepath.Join(dir, "state"), "x"},
	} {
		if g.atomic(tc.target, []byte(tc.data)) == nil {
			t.Errorf("%s: written", name)
		}
	}
	g.root.Dir = filepath.Join(dir, "none")
	if g.atomic(filepath.Join(dir, "none", "x"), []byte("x")) == nil {
		t.Error("no state folder: written")
	}
}

func TestSetFieldsRefusesBadJSON(t *testing.T) {
	if _, err := setFields([]byte(`{"a":`), map[string]string{"b": "c"}); err == nil {
		t.Error("a broken record is set")
	}
}

// R2 finding 1: a proof that lists only an unrelated copy of message-lib.sh
// is stale for the configured installation; complete exits 2 and keeps the
// consuming record, as the script does.
func TestGateRefusesAProofOfAnUnrelatedCopy(t *testing.T) {
	r := newRig(t)
	r.gojson("u1", 600, nil)
	match(t, "the positive control", `^CONSUME u1.*rc=0`, r.consume("u1"))
	other := filepath.Join(r.c.Base, "other", "messages", "message-lib.sh")
	r.c.Write(other, "#!/bin/bash\n# other\n")
	r.c.Edit(r.c.Proof, func(m map[string]any) {
		m["scripts"] = []any{map[string]any{"path": other, "checksum": r.digest(other)}}
	})
	out, code := r.gate("complete", "u1", "posted")
	if code != 2 || !strings.Contains(out, "proof stale") {
		t.Errorf("complete on a proof of an unrelated copy: %s", out)
	}
	if st := r.c.ReadJSON(r.consumed("u1"))["state"]; st != "consuming" {
		t.Errorf("the record changed: %v", st)
	}
}

// XB3 (A26, e93bcc1): an agent outside the protected setup that the gate's
// locked authority check sees is recorded in state/boundary-tainted, the
// gate answers HUMAN (exit 3), and nothing is consumed.
func TestGateRecordsTheExposureItSees(t *testing.T) {
	r := newRig(t)
	r.gojson("xb3", 600, nil)
	r.srv.Handle("agent.list", func(map[string]any) (any, string) {
		return map[string]any{"type": "agent_list", "agents": []any{herdrtest.Agent("sender-pane", "sender-session"),
			herdrtest.Agent("receiver-pane", "receiver-session"), herdrtest.Agent("brief-pane", "brief-session")}}, ""
	})
	out := r.consume("xb3")
	match(t, "XB3 the gate refuses under the HUMAN boundary", `rc=3`, out)
	b, _ := os.ReadFile(filepath.Join(r.c.Dir, "state", "boundary-tainted"))
	if !strings.Contains(string(b), "brief-pane") || !strings.Contains(string(b), "live agent outside the protected setup") {
		t.Errorf("XB3b and records the agent it saw: %q", b)
	}
	if exists(r.consumed("xb3")) {
		t.Error("XB3c nothing is consumed")
	}
	// An exposure already recorded stays as it was.
	r.c.Write(filepath.Join(r.c.Dir, "state", "boundary-tainted"), "earlier\n")
	r.gojson("xb3b", 600, nil)
	match(t, "XB3d a recorded exposure keeps the gate at HUMAN", `rc=3`, r.consume("xb3b"))
	if b, _ := os.ReadFile(filepath.Join(r.c.Dir, "state", "boundary-tainted")); string(b) != "earlier\n" {
		t.Errorf("XB3e the earlier record stands: %q", b)
	}
}
