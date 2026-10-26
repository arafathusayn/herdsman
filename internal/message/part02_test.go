package message

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
)

// message-tests.sh part 02: M4-M6, W, P1, P4, V3a/V3b, L1-L3.
func TestPart02HoldsAlertsGrammarProofLeak(t *testing.T) {
	c := newCase(t, true)
	c.send()
	c.editJSON(c.in("peers"), func(m map[string]any) {
		m[A].(map[string]any)["identity"].(map[string]any)["session"].(map[string]any)["value"] = "new-session"
	})
	c.courier()
	check(t, "M4 peers changed: delivery held in new", "^yes no$", yn(c.ob("new", ID+".md"))+" "+yn(c.ob("ready", ID+".md")))
	check(t, "M4b the user is told", "notification show Message peers changed", c.calls())
	c.courier()
	check(t, "M4c still held after a courier restart", "^yes no$", yn(c.ob("new", ID+".md"))+" "+yn(c.ob("ready", ID+".md")))
	out, _ := c.run("sender-pane", nil, "send", A, O, c.base("body.txt"), "brief")
	check(t, "M4d send refuses while the change awaits approval", "awaiting human approval", out)
	c.write(c.in("peers"), c.read(c.in("peers.approved")))
	c.courier()
	check(t, "M4e the restored registry delivers the held message", "^no yes$", yn(c.ob("new", ID+".md"))+" "+yn(c.ob("ready", ID+".md")))

	c = newCase(t, true)
	c.send()
	c.courier()
	nocheck(t, "M5 a fresh message raises no unread alert", "notification show Unread messages", c.calls())
	old(t, c.ob("ready", ID+".md"))
	c.courier("ALERT_AFTER=60")
	check(t, "M5b a message older than ALERT_AFTER raises the unread alert", "notification show Unread messages", c.calls())
	n := c.count("notification show Unread messages")
	c.courier("ALERT_AFTER=60")
	check(t, "M5c the same backlog is not alerted twice", "^"+strconv.Itoa(n)+"$", strconv.Itoa(c.count("notification show Unread messages")))

	c = newCase(t, true)
	c.send()
	c.fx.Fail("agent.get", "temporary_failure")
	c.courier()
	check(t, "M6 an unavailable sender check keeps the message in new", "^yes no$", yn(c.ob("new", ID+".md"))+" "+yn(c.ob("ready", ID+".md")))
	check(t, "M6b and never rejects it", "^no$", yn(c.ob("quarantine", ID+".md")))
	c.fx.Fail("agent.get", "")
	c.courier()
	check(t, "M6c the restarted courier delivers the message left in new", "^no yes$", yn(c.ob("new", ID+".md"))+" "+yn(c.ob("ready", ID+".md")))

	out, _ = c.wait("receiver-pane", O)
	check(t, "W1 the waiter wakes on a ready message", "MESSAGE "+O, out)
	out, _ = c.wait("receiver-pane", O)
	check(t, "W2 the same unread state does not wake it again", "TICK", out)
	os.Remove(c.in("state", "heartbeat"))
	out, _ = c.wait("receiver-pane", O)
	check(t, "W3 a missing courier heartbeat wakes HEALTH", "HEALTH courier-down", out)

	name := map[string]string{"HERDSMAN_NAME": O}
	out, _ = c.run("receiver-pane", name, "message", "bogus")
	check(t, "P1 an unknown message command fails with the grammar", `status\|wait\|read`, out)
	for _, form := range []string{"status extra", "wait extra", "read a b", "release", "ack a b", "send a", "hold a", "unhold", "unhold a b"} {
		out, code := c.run("receiver-pane", name, "message", strings.Fields(form)...)
		check(t, "P1b a wrong argument count prints the same grammar: "+form, `^2 message: status\|wait\|read`, strconv.Itoa(code)+" "+out)
	}

	c = newCase(t, false)
	c.editJSON(c.ic.Proof, func(m map[string]any) { m["policy_verified"] = false })
	out, _ = c.run("sender-pane", nil, "register", "propose", A, "adjutant")
	check(t, "P4 a setup proof without verified sandbox policies is refused", "proof stale; repeat human setup", out)
	check(t, "P4b before any state is created", "^no$", yn(c.in("state")))
	c.editJSON(c.ic.Proof, func(m map[string]any) { m["policy_verified"] = true })
	c.write(c.base("ignore-rule"), "sandbox policy\nchanged rule\n")
	out, _ = c.run("sender-pane", nil, "register", "propose", A, "adjutant")
	check(t, "P4c a changed sandbox policy file after the setup is refused", "proof stale; repeat human setup", out)
	check(t, "P4d before any state is created", "^no$", yn(c.in("state")))
	c.write(c.base("ignore-rule"), "sandbox policy\n")
	c.editJSON(c.ic.Proof, func(m map[string]any) { m["binary"].(map[string]any)["checksum"] = "1-1" })
	out, _ = c.run("sender-pane", nil, "register", "propose", A, "adjutant")
	check(t, "P4e a changed protected binary checksum is refused", "proof stale; repeat human setup", out)
	c.ic.Prove()
	for _, reserved := range []string{"setup", "peers", "state", "ids", "alerts"} {
		out, _ = c.run("sender-pane", nil, "register", "propose", reserved, "adjutant")
		check(t, "V3a the reserved name "+reserved+" cannot enroll", "propose <name> adjutant|orchestrator", out)
	}
	check(t, "V3b and no mailbox or state was created", "^no no$", yn(c.in("state"))+" "+yn(c.in("setup", "new")))
	c.editJSON(c.ic.Proof, func(m map[string]any) { m["scripts"] = []any{} })
	out, _ = c.run("sender-pane", nil, "register", "propose", A, "adjutant")
	check(t, "P4f an old proof that lists scripts is refused, with how to set up again", `proof stale; repeat human setup \(the proof lists \.scripts\[\].*replace \.scripts\[\] with "binary"`, out)
	c.ic.Prove()
	check(t, "P4g and no state exists", "^no$", yn(c.in("state")))

	leak := func(env map[string]string, args ...string) (string, int) {
		var b strings.Builder
		code := LeakGate(args, func(k string) string { return env[k] }, &b, &b)
		return b.String(), code
	}
	dir := t.TempDir()
	c.write(dir+"/leak.md", "See /Users/someone/project for details.\n")
	c.write(dir+"/clean.md", "All checks passed on the current head.\n")
	out, code := leak(nil, dir+"/leak.md")
	check(t, "L1 a private path fails the gate", "LEAK", out)
	check(t, "L1b exit 1", "^1$", strconv.Itoa(code))
	_, code = leak(nil, dir+"/clean.md")
	check(t, "L2 clean prose passes", "^0$", strconv.Itoa(code))
	_, code = leak(nil, dir+"/missing.md")
	check(t, "L3 a missing file is an error, not clean", "^2$", strconv.Itoa(code))
	_ = herdrtest.Agent
}
