package message

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
)

// Part 03: V1-V6, E1-E8.
func TestPart03NamesEnvelopes(t *testing.T) {
	c := newCase(t, true)
	for _, bad := range []string{"Upper", "has space", "abcdefghijklmnopqrstuvwxyz0123456", "-dash"} {
		out, _ := c.run("sender-pane", nil, "register", "propose", bad, "adjutant")
		check(t, "V1 invalid name refused: "+bad, "propose <name>", out)
	}
	out, _ := c.run("sender-pane", nil, "register", "propose", "route-x", "boss")
	check(t, "V2 an unknown role is refused", "propose <name>|invalid role", out)
	c.mustRun("sender-pane", "register", "retire", A, c.digest(c.in("peers")))
	out, _ = c.run("sender-pane", nil, "register", "propose", A, "adjutant")
	check(t, "V3 a retired name never reactivates", "retired name", out)

	c = newCase(t, false)
	c.setAgents("sender-pane", "a/b", "receiver-pane", "receiver-session")
	out, _ = c.run("sender-pane", nil, "register", "propose", A, "adjutant")
	check(t, "V4 a path-bearing session cannot enroll", "cannot enroll current session", out)
	c = newCase(t, false)
	c.setAgents("sender-pane", "shared-session", "receiver-pane", "shared-session")
	c.run("sender-pane", nil, "register", "propose", A, "adjutant")
	out, _ = c.run("receiver-pane", nil, "register", "propose", O, "orchestrator")
	check(t, "V5 one session in two panes cannot form a pair", "role/session already enrolled", out)
	c = newCase(t, false)
	a := herdrtest.Agent("sender-pane", "sender-session")
	delete(a, "launch_pending")
	c.fx.SetAgent("sender-pane", a)
	out, _ = c.run("sender-pane", nil, "register", "propose", A, "adjutant")
	check(t, "V6 a missing launch_pending can enroll", "^pending registry ", out)
	c = newCase(t, false)
	c.fx.Edit("sender-pane", "launch_pending", true)
	out, _ = c.run("sender-pane", nil, "register", "propose", A, "adjutant")
	check(t, "V6b a pending launch cannot enroll", "cannot enroll current session", out)

	env := newCase(t, true)
	c = env
	c.send()
	check(t, "E1 the queued envelope equals its allocation copy", "^same$", map[bool]string{true: "same"}[c.read(c.in("ids", ID, "envelope")) == c.read(c.ob("new", ID+".md"))])
	ents, _ := os.ReadDir(c.ob("tmp"))
	check(t, "E1b no write is left in tmp", "^0$", strconv.Itoa(len(ents)))
	check(t, "E1c the next allocation increases", "m000000000002 queued", c.send())
	c.editJSON(c.ob("new", "m000000000002.md"), func(m map[string]any) { m["to"] = A })
	c.fx.Edit("sender-pane", "agent_status", "unknown")
	c.courier()
	check(t, "E2 an edited recipient is quarantined", "^yes$", yn(c.ob("quarantine", "m000000000002.md")))
	check(t, "E3 an unknown lifecycle keeps the message in new, never rejected", "^yes no$", yn(c.ob("new", ID+".md"))+" "+yn(c.ob("quarantine", ID+".md")))
	c.srv.Handle("agent.get", func(map[string]any) (any, string) {
		return map[string]any{"type": "agent_info", "agent": "invalid"}, ""
	})
	c.fx.Edit("sender-pane", "agent_status", "idle")
	c.courier()
	check(t, "E4 invalid agent JSON keeps the message in new", "^yes$", yn(c.ob("new", ID+".md")))
	c.fx = herdrtest.Serve(c.srv)
	c.setAgents("sender-pane", "sender-session", "receiver-pane", "receiver-session")
	c.courier()
	check(t, "E5 a restored observation delivers it", "^yes$", yn(c.ob("ready", ID+".md")))
	c.write(c.ob("new", "m000000000007.md"), "from: route-adjutant\nto: route-orchestrator\nnot json\n")
	ready := c.read(c.ob("ready", ID+".md"))
	c.write(c.ob("new", "m000000000008.md"), "{\"version\":1,"+strings.TrimPrefix(strings.TrimSpace(ready), "{")+"\n")
	c.send()
	c.editJSON(c.ob("new", "m000000000003.md"), func(m map[string]any) { m["reply_to"] = "m000000000099" })
	c.send()
	c.editJSON(c.ob("new", "m000000000004.md"), func(m map[string]any) {
		m["to_identity"].(map[string]any)["session"].(map[string]any)["value"] = "old-generation"
	})
	c.courier()
	for _, q := range []string{"7:a body with header lines", "8:a duplicate JSON key", "3:an unknown reply id", "4:a wrong recipient generation"} {
		n, what, _ := strings.Cut(q, ":")
		check(t, "E6 quarantined: "+what, "^yes$", yn(c.ob("quarantine", "m00000000000"+n+".md")))
	}
	d := newCase(t, true)
	d.send()
	dup := "{\"kind\":\"brief\"," + strings.TrimPrefix(d.read(d.ob("new", ID+".md")), "{")
	d.write(d.ob("new", ID+".md"), dup)
	d.write(d.in("ids", ID, "envelope"), dup)
	d.courier()
	check(t, "E6b a duplicate key with an equal allocation copy is quarantined", "^yes no$", yn(d.ob("quarantine", ID+".md"))+" "+yn(d.ob("ready", ID+".md")))
	d = newCase(t, true)
	d.send()
	d.editJSON(d.ob("new", ID+".md"), func(m map[string]any) { m["body"] = "changed brief\n" })
	d.courier()
	check(t, "E6c a changed body is quarantined", "^yes no$", yn(d.ob("quarantine", ID+".md"))+" "+yn(d.ob("ready", ID+".md")))
	c = env
	readyCopy := c.read(c.ob("ready", ID+".md"))
	c.write(c.ob("new", ID+".md"), readyCopy)
	c.courier()
	check(t, "E7 a duplicate ID is held, not delivered over ready", "notification show Duplicate message held", c.calls())
	check(t, "E7b ready keeps its bytes and the copy stays in new", "^same yes$", map[bool]string{true: "same"}[readyCopy == c.read(c.ob("ready", ID+".md"))]+" "+yn(c.ob("new", ID+".md")))
	c.setAgents("sender-pane", "sender-session", "receiver-pane", "replaced-session")
	out, _ = c.run("receiver-pane", nil, "read", O)
	check(t, "E8 a new recipient session cannot claim old work", "own pane/session changed", out)
	check(t, "E8b and the work stays ready", "^yes$", yn(c.ob("ready", ID+".md")))
}
