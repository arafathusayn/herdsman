package message

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Parts 07 and 08: S1-S16.
func TestPart07SenderAcksClaimsBounds(t *testing.T) {
	c := newCase(t, true)
	c.send()
	c.editJSON(c.ob("new", ID+".md"), func(m map[string]any) { m["from"] = O })
	c.write(c.in("ids", ID, "envelope"), c.read(c.ob("new", ID+".md")))
	c.courier()
	check(t, "S1 a forged from name with a matching allocation copy is still quarantined", "^yes no$", yn(c.ob("quarantine", ID+".md"))+" "+yn(c.ob("ready", ID+".md")))
	out, st := c.run("sender-pane", nil, "send", O, A, c.base("body.txt"), "brief")
	check(t, "S2 the adjutant's pane cannot send under the orchestrator's name", "^2 own pane/session changed", strconv.Itoa(st)+" "+out)
	check(t, "S2b and nothing is queued for the adjutant", "^0$", strconv.Itoa(nfiles(c.in(A, "new"), "*")))

	c = newCase(t, true)
	c.setAgents("moved-pane", "sender-session", "receiver-pane", "receiver-session")
	out, st = c.run("sender-pane", nil, "send", A, O, c.base("body.txt"), "brief")
	check(t, "S3 a moved sender pane must enroll again before it sends", "^2 own identity unavailable", strconv.Itoa(st)+" "+out)
	c = newCase(t, true)
	c.fx.Edit("sender-pane", "agent_session", map[string]any{"source": "other-namespace", "agent": "claude", "kind": "id", "value": "sender-session"})
	out, st = c.run("sender-pane", nil, "send", A, O, c.base("body.txt"), "brief")
	check(t, "S4 the same session value in another identity namespace cannot send", "^2 own pane/session changed", strconv.Itoa(st)+" "+out)

	c = newCase(t, false)
	for _, p := range [][]string{{"sender-pane", "sender-session"}, {"receiver-pane", "receiver-session"}} {
		a := herdrtest.Agent(p[0], p[1])
		a["agent_session"] = map[string]any{"value": p[1], "source": "hook", "kind": "id", "agent": "claude"}
		c.fx.SetAgent(p[0], a)
	}
	c.enroll()
	c.send()
	c.courier()
	check(t, "S5 reordered identity keys still deliver", "^yes$", yn(c.ob("ready", ID+".md")))
	out, _ = c.run("receiver-pane", nil, "read", O)
	check(t, "S5b and read", `"id":"`+ID+`"`, out)
	targets := []any{}
	for _, r := range c.agentRecords() {
		targets = append(targets, map[string]any{"pane": r["pane_id"], "session": r["agent_session"]})
	}
	c.ic.WriteJSON(c.in("watch-targets.json"), targets)
	c.watch()
	check(t, "S5c and the card watcher accepts both targets", "^[2-9]", strconv.Itoa(c.count("^pane read")))

	c = newCase(t, true)
	c.send()
	c.courier()
	out, _ = c.run("receiver-pane", nil, "read", O)
	tok := token(out)
	sum := c.digest(c.ob("cur", ID+".md"))
	for _, value := range []string{"", "{}", `{"id":"` + ID + `","claim":"wrong","time":1,"processing_record":"record","envelope_checksum":"` + sum + `"}`} {
		c.write(c.ob("ack", ID+".md.json"), value)
		out, _ = c.run("receiver-pane", nil, "read", O, tok)
		check(t, "S6 an invalid ack ("+value+") leaves the message unhandled", `"id":"`+ID+`"`, out)
	}

	c = newCase(t, true)
	c.send()
	c.courier()
	tok = token(c.mustRun("receiver-pane", "read", O))
	c.write(c.base("record.txt"), "intake recorded\n")
	c.mustRun("receiver-pane", "ack", O, tok, ID, c.base("record.txt"))
	c.mustRun("receiver-pane", "read", O, tok, "--release")
	c.send()
	c.courier()
	out, _ = c.run("receiver-pane", nil, "read", O)
	check(t, "S7 a new claim lists the new message", `"id":"m000000000002"`, out)
	nocheck(t, "S7b and does not revive the released, acked one", `"id":"`+ID+`"`, out)

	c = newCase(t, true)
	c.send()
	c.courier()
	os.Mkdir(c.ob("claim"), 0o700)
	out, st = c.run("receiver-pane", nil, "read", O)
	check(t, "S8 an incomplete claim is held", "^2 .*reader claim held", strconv.Itoa(st)+" "+out)
	os.Remove(c.ob("claim"))
	tok = token(c.mustRun("receiver-pane", "read", O))
	check(t, "S8b after the human clears it, a complete claim is written", "^"+tok+" yes$", inbox.CatText(c.ob("claim", "token"))+" "+yn(c.ob("claim", "owner")))

	c = newCase(t, true)
	c.write(c.base("untouched"), "original\n")
	os.Symlink(c.base("untouched"), c.in("alerts", "courier-down.pending"))
	old(t, c.in("state", "heartbeat"))
	out, st = c.wait("receiver-pane", O, "DEADLINE=1")
	check(t, "S9 a linked alert state file wakes the waiter with HEALTH and is not followed", "^1 original HEALTH", strconv.Itoa(st)+" "+strings.TrimSpace(c.read(c.base("untouched")))+" "+out)
	c = newCase(t, true)
	c.write(c.base("outside"), "outside\n")
	os.Mkdir(c.in("courier.lock"), 0o700)
	os.Symlink(c.base("outside"), c.in("courier.lock", "pid"))
	_, st = c.run("receiver-pane", nil, "status")
	check(t, "S10 a linked courier lock owner is refused", "^2 outside$", strconv.Itoa(st)+" "+strings.TrimSpace(c.read(c.base("outside"))))
	c = newCase(t, true)
	c.write(c.base("body.txt"), strings.Repeat("\x01", 60000))
	out, st = c.run("sender-pane", nil, "send", A, O, c.base("body.txt"), "brief")
	check(t, "S11 an oversized encoded envelope is refused before publication", "^2 .*encoded envelope", strconv.Itoa(st)+" "+out)
	check(t, "S11b and nothing is queued", "^0$", strconv.Itoa(nfiles(c.ob("new"), "*")))
	c = newCase(t, true)
	c.send()
	old(t, c.ob("new", ID+".md"))
	c.courier("ALERT_AFTER=1")
	n := c.count("notification show Unread messages")
	c.send()
	old(t, c.ob("new", "m000000000002.md"))
	c.courier("ALERT_AFTER=1")
	check(t, "S12 a newly overdue message alerts again although the old backlog was alerted", "^"+strconv.Itoa(n+1)+"$", strconv.Itoa(c.count("notification show Unread messages")))
	out, st = c.courier("SWEEP=3600", "HEALTH_AFTER=180")
	check(t, "S13 a sweep longer than the health threshold refuses the courier", "^2 .*scan allowance", strconv.Itoa(st)+" "+out)

	c = newCase(t, true)
	name := map[string]string{"HERDSMAN_NAME": O}
	out, st = c.run("receiver-pane", name, "message", "release", "missing")
	check(t, "S14 release with no matching claim fails", "^2 .*no such active claim", strconv.Itoa(st)+" "+out)
	c.send()
	c.courier()
	out, _ = c.wait("receiver-pane", O, "DEADLINE=1")
	check(t, "S15 the first delivery wakes the waiter", "MESSAGE", out)
	out, _ = c.run("receiver-pane", name, "message", "read")
	tok = token(out)
	out, _ = c.wait("receiver-pane", O, "DEADLINE=1")
	check(t, "S15b the claimed, unchanged state does not wake it", "TICK", out)
	c.send()
	c.courier()
	out, _ = c.wait("receiver-pane", O, "DEADLINE=1")
	check(t, "S15c a second delivery wakes it again", "MESSAGE", out)
	c.write(c.base("record.txt"), "intake recorded\n")
	c.run("receiver-pane", nil, "read", O, tok)
	c.run("receiver-pane", nil, "ack", O, tok, ID, c.base("record.txt"))
	c.run("receiver-pane", nil, "ack", O, tok, "m000000000002", c.base("record.txt"))
	_, st = c.run("receiver-pane", name, "message", "release", tok)
	check(t, "S16 release of a fully acked claim succeeds and leaves no claim", "^0 no$", strconv.Itoa(st)+" "+yn(c.ob("claim")))
}
