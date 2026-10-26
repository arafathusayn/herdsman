package message

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/inbox"
)

// e93bcc1 XB1 (B3-001): an exposure seen while another process owns the
// input lock is never published under that owner; it waits in
// state/exposure-pending, every check fails closed meanwhile, and the next
// owner of the lock publishes it.
func TestE93XB1ExposureWaitsForTheLock(t *testing.T) {
	c := newCase(t, true)
	c.ic.WriteJSON(c.in("watch-targets.json"), []any{map[string]any{"pane": "receiver-pane", "session": map[string]any{"source": "hook", "agent": "claude", "kind": "id", "value": "receiver-session"}}})
	c.relayBoundary()
	c.fx.SetAgent("rogue-pane", herdrtest.Agent("rogue-pane", "rogue-session"))
	lock := c.in(inbox.MutationLock)
	os.Mkdir(lock, 0o700)
	c.write(lock+"/pid", "99999\n")
	c.watch("LOCK_WAIT=1")
	check(t, "XB1 no record while another process owns the input lock", "^no 99999$", yn(c.in("state", "boundary-tainted"))+" "+strings.TrimSpace(c.read(lock+"/pid")))
	check(t, "XB1b the incident waits for the next owner", "^[1-9]", strconv.Itoa(nfiles(c.in("state", "exposure-pending"), "*.json")))
	c.fx.SetAgent("rogue-pane", nil)
	out, code := c.run("receiver-pane", nil, "authority")
	check(t, "XB1c after that agent exits, a check still fails closed", "^3 HUMAN", strconv.Itoa(code)+" "+out)
	os.Remove(lock + "/pid")
	os.Remove(lock)
	c.run("receiver-pane", nil, "hold", O, "hold", A, "inspect")
	check(t, "XB1d the next owner of the lock publishes it, naming the agent; nothing pending", "(?s)^0 .*rogue-pane",
		strconv.Itoa(nfiles(c.in("state", "exposure-pending"), "*.json"))+" "+c.read(c.in("state", "boundary-tainted")))
}

// e93bcc1 XB2 (B3-004): an agent that only the resume helper's locked
// recheck sees is recorded, and the resume is held.
func TestE93XB2ResumeRecordsExposure(t *testing.T) {
	c := newCase(t, true)
	c.codexTask()
	c.allowResume()
	c.relayBoundary()
	base := c.fx.Handler("agent.list")
	c.srv.Handle("agent.list", func(p map[string]any) (any, string) {
		res, code := base(p)
		if inbox.Exists(c.in(inbox.MutationLock)) && code == "" {
			m := res.(map[string]any)
			brief := herdrtest.Agent("brief-pane", "brief-session")
			m["agents"] = append(append([]map[string]any{}, m["agents"].([]map[string]any)...), brief)
		}
		return res, code
	})
	out, _ := c.run("sender-pane", nil, "resume", "im-task")
	check(t, "XB2 the locked recheck holds the resume", "^HELD HUMAN boundary changed", out)
	check(t, "XB2b and records the agent it saw", "brief-pane", c.read(c.in("state", "boundary-tainted")))
	check(t, "XB2c no input was sent", "^0$", strconv.Itoa(c.prompts()))
}

// e93bcc1 AR1-AR3: archive replays and a cut quarantine move.
func TestE93ArchiveAndQuarantineCut(t *testing.T) {
	c := newCase(t, true)
	c.send()
	c.courier()
	tok := token(c.mustRun("receiver-pane", "read", O))
	c.write(c.base("record.txt"), "intake recorded\n")
	c.mustRun("receiver-pane", "ack", O, tok, ID, c.base("record.txt"))
	os.MkdirAll(c.base("archive"), 0o700)
	os.Rename(c.ob("cur", ID+".md"), c.base("archive", ID+".md"))
	os.Rename(c.ob("ack", ID+".md.json"), c.base("archive", ID+".md.json"))
	c.write(c.ob("new", ID+".md"), c.read(c.in("ids", ID, "envelope")))
	c.srv.ResetRequests()
	c.courier()
	check(t, "AR1 a replay after the archive of a handled message is held in new", "^yes no no$", yn(c.ob("new", ID+".md"))+" "+yn(c.ob("ready", ID+".md"))+" "+yn(c.ob("quarantine", ID+".md")))
	check(t, "AR1b and alerts as a duplicate", "notification show Duplicate message held", c.calls())

	c = newCase(t, true)
	c.send()
	c.editJSON(c.ob("new", ID+".md"), func(m map[string]any) {
		m["from_identity"].(map[string]any)["session"].(map[string]any)["value"] = "forged"
	})
	c.courier()
	os.MkdirAll(c.base("archive"), 0o700)
	os.Rename(c.ob("quarantine", ID+".md"), c.base("archive", ID+".md"))
	c.write(c.ob("new", ID+".md"), c.read(c.base("archive", ID+".md")))
	c.srv.ResetRequests()
	c.courier()
	m, _ := decodeObject(c.read(c.in("ids", ID, "terminal.json")))
	check(t, "AR2 a replay after the archive of a quarantined message is held in new", "^yes no no quarantined$",
		yn(c.ob("new", ID+".md"))+" "+yn(c.ob("ready", ID+".md"))+" "+yn(c.ob("quarantine", ID+".md"))+" "+raw(m["state"]))
	check(t, "AR2b and alerts as a duplicate", "notification show Duplicate message held", c.calls())

	c = newCase(t, true)
	c.send()
	c.editJSON(c.ob("new", ID+".md"), func(m map[string]any) {
		m["from_identity"].(map[string]any)["session"].(map[string]any)["value"] = "forged"
	})
	os.Rename(c.ob("new", ID+".md"), c.ob("quarantine", ID+".md"))
	c.srv.ResetRequests()
	c.courier()
	m, _ = decodeObject(c.read(c.in("ids", ID, "terminal.json")))
	check(t, "AR3 the missing terminal is written as quarantined", "^quarantined$", raw(m["state"]))
	check(t, "AR3b the sender's notice is published", "^yes yes$", yn(c.in(A, "notices", ID+".md.json"))+" "+yn(c.in("ids", ID, "notice-published")))
	check(t, "AR3c the human is told", "notification show Messages quarantined", c.calls())
}
