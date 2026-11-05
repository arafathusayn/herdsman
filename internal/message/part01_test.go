package message

import (
	"context"
	"os"
	"strings"
	"testing"
)

func t0() context.Context { return context.Background() }

func token(out string) string {
	for _, l := range strings.Split(out, "\n") {
		if m, ok := decodeObject(l); ok && m["type"] == "claim" {
			return m["token"].(string)
		}
	}
	return ""
}

// Part 01: R, D, K, M1-M3, Q.
func TestPart01RegisterDeliverReadAck(t *testing.T) {
	c := newCase(t, true)
	reg := c.ic.ReadJSON(c.in("peers.approved"))
	sess := func(n string) string {
		return reg[n].(map[string]any)["identity"].(map[string]any)["session"].(map[string]any)["value"].(string)
	}
	check(t, "R1 both full identities approved", "^sender-session receiver-session$", sess(A)+" "+sess(O))
	before := c.digest(c.in("peers"))
	c.run("sender-pane", nil, "register", "propose", A, "adjutant")
	check(t, "R1b repeating the same enrollment leaves the registry unchanged", "^"+before+"$", c.digest(c.in("peers")))
	out, _ := c.run("sender-pane", nil, "register", "propose", "../escape", "adjutant")
	check(t, "R2 a traversal name is refused", "propose <name>", out)
	check(t, "R2b and no mailbox escapes the inbox", "^no$", yn(c.base("escape")))
	out, _ = c.run("sender-pane", nil, "register", "approve", "1-2")
	check(t, "R3 approve needs the inspected checksum", "inspected pending checksum", out)
	c.setAgents("sender-pane", "sender-session", "receiver-pane", "receiver-session", "other-pane", "other-session")
	out, _ = c.run("other-pane", nil, "register", "propose", "route-adjutant-2", "adjutant")
	check(t, "R4 a second adjutant cannot join a live pair", "role/session already enrolled", out)

	c = newCase(t, true)
	check(t, "D1 send queues and says delivery is not confirmed", "^"+ID+" queued", c.send())
	check(t, "D1b the envelope waits in new", "^yes$", yn(c.ob("new", ID+".md")))
	c.courier()
	check(t, "D2 the courier verifies and moves it to ready", "^yes no$", yn(c.ob("ready", ID+".md"))+" "+yn(c.ob("new", ID+".md")))
	check(t, "D2b the recipient's count is one", "report-metadata receiver-pane --source herdsman-message-count --token messages=1", c.calls())
	check(t, "D2c and the delivery is logged as verified", "VERIFIED "+ID, verifiedLog(c))
	out, _ = c.run("sender-pane", nil, "send", A, "route-nobody", c.base("body.txt"), "brief")
	check(t, "D3 an unknown recipient is refused before queueing", "unknown peer", out)
	check(t, "D3b and gets no mailbox", "^no$", yn(c.in("route-nobody")))

	out, _ = c.run("receiver-pane", nil, "read", O)
	check(t, "K1 read prints the claim, the manifest and the summary", `"type":"claim".*"type":"manifest".*"id":"m000000000001".*"type":"summary"`, strings.ReplaceAll(out, "\n", " "))
	nocheck(t, "K1b read never prints the body", "full brief", out)
	tok := token(out)
	out, _ = c.run("receiver-pane", nil, "read", O)
	check(t, "K2 a second reader is refused", "reader claim held", out)
	out, _ = c.run("receiver-pane", nil, "read", O, tok, "--release")
	check(t, "K3 release with an unacked message is refused", "acknowledge all delivered work", out)
	c.write(c.base("record.txt"), "intake recorded\n")
	out, _ = c.run("receiver-pane", nil, "ack", O, "wrong-token", ID, c.base("record.txt"))
	check(t, "K4 a wrong claim token cannot ack", "wrong active reader claim", out)
	out, _ = c.run("receiver-pane", nil, "ack", O, tok, ID, c.base("record.txt"))
	check(t, "K5 ack with the claim token records intake", "acknowledged "+ID, out)
	out, _ = c.run("receiver-pane", nil, "read", O, tok, "--release")
	check(t, "K6 release after every ack", `"type":"released"`, out)
	c.courier()
	check(t, "K7 an acked message clears the count", "report-metadata receiver-pane --source herdsman-message-count --clear-token messages", c.calls())

	c = newCase(t, true)
	c.send()
	c.editJSON(c.ob("new", ID+".md"), func(m map[string]any) { m["from"] = O })
	c.courier()
	check(t, "M1 forged from name: quarantined", "^yes no$", yn(c.ob("quarantine", ID+".md"))+" "+yn(c.ob("ready", ID+".md")))
	check(t, "M1b with a permanent quarantined disposition", "^quarantined$", jqField(t, c.in("ids", ID, "terminal.json"), "state"))
	nocheck(t, "M1c and never logged as verified", "VERIFIED", verifiedLog(c))
	c = newCase(t, true)
	c.send()
	c.editJSON(c.ob("new", ID+".md"), func(m map[string]any) { m["from"] = "route-intruder" })
	c.courier()
	check(t, "M1d forged unenrolled from name: quarantined", "^yes no$", yn(c.ob("quarantine", ID+".md"))+" "+yn(c.ob("ready", ID+".md")))
	out, _ = c.run("receiver-pane", nil, "status", O)
	check(t, "M1e status counts the quarantine", "(?m)^"+O+" orphan=[^ ]* new=0 ready=0 cur=0 quarantine=1$", out)
	nocheck(t, "M1f status never prints the rejected body", "full brief", out)
	c = newCase(t, true)
	out, _ = c.run("receiver-pane", nil, "status", O)
	check(t, "M1g a clean mailbox shows a zero quarantine count", "(?m)^"+O+" orphan=[^ ]* .*quarantine=0$", out)

	ws := newCase(t, true)
	ws.send()
	ws.editJSON(ws.ob("new", ID+".md"), func(m map[string]any) {
		m["from_identity"].(map[string]any)["session"].(map[string]any)["value"] = "other-session"
	})
	ws.courier()
	check(t, "M2 right name, edited wrong session: quarantined", "^yes no$", yn(ws.ob("quarantine", ID+".md"))+" "+yn(ws.ob("ready", ID+".md")))
	c = newCase(t, true)
	c.send()
	c.setAgents("sender-pane", "replaced-session", "receiver-pane", "receiver-session")
	c.courier()
	check(t, "M2b right name, live session changed after send: quarantined", "^yes no$", yn(c.ob("quarantine", ID+".md"))+" "+yn(c.ob("ready", ID+".md")))
	nocheck(t, "M2c and never logged as verified", "VERIFIED", verifiedLog(c))
	c = newCase(t, true)
	c.send()
	c.setAgents("receiver-pane", "receiver-session")
	c.courier()
	check(t, "M3 sender pane closed before verification: quarantined", "^yes no$", yn(c.ob("quarantine", ID+".md"))+" "+yn(c.ob("ready", ID+".md")))
	check(t, "M3b the sender's mailbox gets a notice", "^yes$", yn(c.in(A, "notices", ID+".md.json")))

	c = ws
	out, _ = c.wait("sender-pane", A)
	check(t, "Q1 a quarantine notice wakes the sender", "HEALTH sender notice", out)
	out, _ = c.run("sender-pane", nil, "send", A, O, c.base("body.txt"), "brief", ID)
	check(t, "Q2 a fresh correlated resend is queued", "m000000000002 queued", out)
	c.courier()
	check(t, "Q2b and delivered while the original stays quarantined", "^yes yes$", yn(c.ob("ready", "m000000000002.md"))+" "+yn(c.ob("quarantine", ID+".md")))
	_ = os.Remove
}
