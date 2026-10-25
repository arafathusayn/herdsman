package message

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// message-tests.sh parts 15-18: the V3R regressions.
func TestPart15Regressions(t *testing.T) {
	c := newCase(t, true)
	c.send()
	base := c.read(c.in("peers.approved"))
	c.write(c.in("peers.approved"), "not json\n")
	out, _ := c.run("receiver-pane", nil, "status")
	check(t, "V3R1 a corrupt registry still shows the queued message", "(?m)^"+O+" .*new=1", out)
	c.write(c.in("peers.approved"), base)
	c.editJSON(c.ic.Proof, func(m map[string]any) { m["policy_verified"] = false })
	out, _ = c.run("receiver-pane", nil, "status")
	check(t, "V3R1b a stale setup proof still shows the queue and flags the proof", "setup-proof-stale.*"+O+" .*new=1", strings.ReplaceAll(out, "\n", " "))

	c = newCase(t, true)
	c.send()
	c.courier()
	tok := token(c.mustRun("receiver-pane", "read", O))
	os.Rename(c.ob("claim"), c.base("claim-copy"))
	os.Symlink(c.base("claim-copy"), c.ob("claim"))
	c.write(c.base("record.txt"), "intake recorded\n")
	out, st := c.run("receiver-pane", nil, "ack", O, tok, ID, c.base("record.txt"))
	check(t, "V3R2 a linked claim folder cannot ack", "^2 wrong active reader claim", strconv.Itoa(st)+" "+out)
	check(t, "V3R2b and no intake is published", "^no no$", yn(c.ob("ack", ID+".md.json"))+" "+yn(c.in("ids", ID, "terminal.json")))

	c = newCase(t, true)
	c.codexTask()
	c.allowResume()
	c.relayBoundary()
	for _, m := range []string{"Working", "Pursuing goal", "Goal active", "Incubating", "thinking", "press esc to interrupt"} {
		c.fx.SetScreen("task-pane", m+"\nGoal paused (/goal resume)\n")
		out, _ := c.run("sender-pane", nil, "resume", "im-task")
		check(t, "V3R3 the active marker '"+m+"' wins over a stale paused footer", "^NOOP verified working", out)
	}
	check(t, "V3R3b and no input was sent", "^0$", strconv.Itoa(c.prompts()))

	c = newCase(t, true)
	c.codexTask()
	c.ic.WriteJSON(c.in("watch-targets.json"), []any{map[string]any{"pane": "receiver-pane", "session": map[string]any{"source": "hook", "agent": "claude", "kind": "id", "value": "receiver-session"}}})
	c.relayBoundary()
	// allow-prompt: every prompt succeeds and the screen stays paused.
	c.watch()
	c.watch()
	check(t, "V3R4 an unchanged read-back is kept and sends no second resume", "^1 yes$", strconv.Itoa(c.count("^agent prompt task-pane"))+" "+map[bool]string{true: "yes"}[nfiles(c.in("state"), "resume-*.readback") == 1])
	check(t, "V3R4b and the held resume alerts on the next interval", "notification show Goal resume held", c.calls())

	c = newCase(t, true)
	c.ic.WriteJSON(c.in("watch-targets.json"), []any{map[string]any{"pane": "receiver-pane", "session": map[string]any{"source": "hook", "agent": "claude", "kind": "id", "value": "receiver-session"}}})
	c.fx.SetScreen("receiver-pane", "✦ Heads up · target finding\n")
	c.relayBoundary()
	replaced := false
	read := c.fx.Handler("pane.read")
	c.srv.Handle("pane.read", func(p map[string]any) (any, string) {
		if !replaced {
			replaced = true
			c.write(c.in("watch-targets.json"), "[]\n")
		}
		return read(p)
	})
	c.watch()
	check(t, "V3R5 a replaced watch-target file holds automation and relays nothing", "^yes 0 .*notification show Automatic coordination held",
		yn(c.in("state", "automation-held"))+" "+strconv.Itoa(nfiles(c.in(A, "new"), "*.md"))+" "+strings.ReplaceAll(c.calls(), "\n", " "))

	c = newCase(t, true)
	c.ic.WriteJSON(c.in("watch-targets.json"), []any{map[string]any{"pane": "receiver-pane", "session": map[string]any{"source": "hook", "agent": "claude", "kind": "id", "value": "receiver-session"}}})
	c.fx.SetScreen("receiver-pane", "✦ Heads up · c1\n✦ Heads up · c2\n✦ Heads up · c3\n✦ Heads up · c4\n✦ Heads up · c5\n✦ Heads up · c6\n")
	c.relayBoundary()
	c.watch()
	check(t, "V3R6 six cards in one capture journal four and alert", "^4 .*notification show Side-agent cards need inspection", strconv.Itoa(nfiles(c.in("card-events"), "*.json"))+" "+strings.ReplaceAll(c.calls(), "\n", " "))
	c.fx.SetScreen("receiver-pane", "✦ Heads up · again\n")
	c.watch()
	c.fx.SetScreen("receiver-pane", "idle prompt\n")
	c.watch()
	c.fx.SetScreen("receiver-pane", "✦ Heads up · again\n")
	c.watch()
	again := 0
	for _, f := range glob(c.in("card-events"), "*.json") {
		if strings.Contains(c.read(f), "again") {
			again++
		}
	}
	check(t, "V3R6b the same headline after an absent poll is a new episode", "^2$", strconv.Itoa(again))

	c = newCase(t, true)
	c.courier()
	c.send()
	out, _ = c.wait("receiver-pane", O)
	check(t, "V3R7 a fresh new entry does not wake the waiter", "^TICK", out)
	old(t, c.ob("new", ID+".md"))
	out, _ = c.wait("receiver-pane", O)
	check(t, "V3R7b an entry older than one sweep wakes it as pending", "HEALTH pending delivery", out)

	c = newCase(t, true)
	tok = token(c.mustRun("receiver-pane", "read", O))
	check(t, "V3R8 an empty read still gets a claim", "^.+$", tok)
	out, _ = c.run("receiver-pane", nil, "status", O)
	nocheck(t, "V3R8b status never prints the claim token", tok, out)
	out, _ = c.run("receiver-pane", nil, "read", O, tok, "--release")
	check(t, "V3R8c the empty active claim is released", `"type":"released"`, out)

	c = newCase(t, true)
	c.watchTask("failed finding")
	c.setAgents("sender-pane", "sender-session", "task-pane", "task-session")
	c.watch()
	check(t, "V3R9 a refused send alerts as a failed relay", "notification show Side-agent relay failed", c.calls())
	nocheck(t, "V3R9b and not as a budget overflow", "Source relay budget", c.calls())

	c = newCase(t, true)
	c.send()
	c.courier()
	tok = token(c.mustRun("receiver-pane", "read", O))
	c.write(c.base("record.txt"), "intake recorded\n")
	c.mustRun("receiver-pane", "ack", O, tok, ID, c.base("record.txt"))
	os.Remove(c.in("ids", ID, "terminal.json"))
	out, st = c.run("receiver-pane", nil, "ack", O, tok, ID, c.base("record.txt"))
	check(t, "V3R10 an ack retry recreates the missing terminal", "^0 acknowledged "+ID+"\n handled$", strconv.Itoa(st)+" "+out+" "+jqField(t, c.in("ids", ID, "terminal.json"), "state"))

	c = newCase(t, true)
	c.send()
	c.editJSON(c.ob("new", ID+".md"), func(m map[string]any) {
		m["from_identity"].(map[string]any)["session"].(map[string]any)["value"] = "forged"
	})
	c.courier()
	out, _ = c.run("sender-pane", nil, "status", A)
	check(t, "V3R11 the sender's status lists the quarantine notice", `"id":"`+ID+`".*"state":"quarantined".*"authority":"notice-only"`, out)
	nocheck(t, "V3R11b and never the body", "full brief", out)

	for _, e := range []struct {
		name string
		fn   func(m map[string]any)
	}{
		{`.version="1"`, func(m map[string]any) { m["version"] = "1" }},
		{`.time="soon"`, func(m map[string]any) { m["time"] = "soon" }},
		{`.kind="bogus"`, func(m map[string]any) { m["kind"] = "bogus" }},
		{`.reply_to="m1"`, func(m map[string]any) { m["reply_to"] = "m1" }},
		{`.body=7`, func(m map[string]any) { m["body"] = 7 }},
		{`.to_identity.session.kind="name"`, func(m map[string]any) {
			m["to_identity"].(map[string]any)["session"].(map[string]any)["kind"] = "name"
		}},
		{`.extra=1`, func(m map[string]any) { m["extra"] = 1 }},
	} {
		c := newCase(t, true)
		c.send()
		c.editJSON(c.ob("new", ID+".md"), e.fn)
		c.courier()
		check(t, "V3R12 a mistyped envelope field is quarantined: "+e.name, "^yes no$", yn(c.ob("quarantine", ID+".md"))+" "+yn(c.ob("ready", ID+".md")))
	}

	c = newCase(t, true)
	c.send()
	c.fx.Fail("agent.get", "temporary_failure")
	c.courier()
	c.courier()
	check(t, "V3R13 two unavailable checks keep the message in new", "^yes no$", yn(c.ob("new", ID+".md"))+" "+yn(c.ob("ready", ID+".md")))
	c.fx.Fail("agent.get", "")
	c.courier()
	check(t, "V3R13b the next available check delivers it", "^no yes$", yn(c.ob("new", ID+".md"))+" "+yn(c.ob("ready", ID+".md")))

	c = newCase(t, true)
	c.write(c.in("alerts", "courier-down.pending"), `{"title":"Message courier unavailable","body":"inspect"}`+"\n")
	os.Mkdir(c.in("alerts", "courier-down.lock"), 0o700)
	c.send()
	check(t, "V3R14 a resolution under another owner leaves the incident pending", "^yes no$", yn(c.in("alerts", "courier-down.pending"))+" "+yn(c.in("alerts", "courier-down.resolved")))
	check(t, "V3R14b and does not take the owner's lock", "^yes$", yn(c.in("alerts", "courier-down.lock")))
	os.Remove(c.in("alerts", "courier-down.lock"))
	c.send()
	check(t, "V3R14c after the release the resolution completes", "^no yes$", yn(c.in("alerts", "courier-down.pending"))+" "+yn(c.in("alerts", "courier-down.resolved")))

	c = newCase(t, true)
	c.send()
	c.courier()
	old(t, c.ob("ready", ID+".md"))
	os.Mkdir(c.in("herdr-mutation.lock"), 0o700)
	old(t, c.in("herdr-mutation.lock"))
	restore := inbox.SetFileOps(inbox.FileOps{Mtime: func(p string) (int64, error) {
		if strings.HasSuffix(p, "/ready/"+ID+".md") || strings.HasSuffix(p, "herdr-mutation.lock") {
			return 0, errors.New("vanished")
		}
		st, err := os.Lstat(p)
		if err != nil {
			return 0, err
		}
		return st.ModTime().Unix(), nil
	}})
	_, st = c.courier("ALERT_AFTER=60")
	check(t, "V3R15 the courier skips the vanished entry and exits 0", "^0$", strconv.Itoa(st))
	nocheck(t, "V3R15b and raises no unread alert from a missing age", "notification show Unread messages", c.calls())
	out, st = c.run("receiver-pane", nil, "status", O)
	check(t, "V3R15c status skips the lock with a missing age", "^0 no$", strconv.Itoa(st)+" "+map[bool]string{true: "bad", false: "no"}[strings.Contains(out, "mutation-lock-stale")])
	out, _ = c.wait("receiver-pane", O)
	nocheck(t, "V3R15d the waiter has no arithmetic failure", "integer|syntax error|arithmetic", out)
	restore()
	os.Remove(c.in("herdr-mutation.lock"))

	c = newCase(t, true)
	c.send()
	c.courier()
	tok = token(c.mustRun("receiver-pane", "read", O))
	c.write(c.base("record.txt"), "intake recorded\n")
	c.mustRun("receiver-pane", "ack", O, tok, ID, c.base("record.txt"))
	c.send()
	c.courier()
	c.run("receiver-pane", nil, "read", O, tok)
	os.Remove(c.ob("claim", "owner"))
	out, st = c.run("receiver-pane", nil, "read", O, tok)
	check(t, "V3R16 a claim without its owner is held, even with the recorded token", "^2 .*reader claim held", strconv.Itoa(st)+" "+out)
	os.Rename(c.ob("claim"), c.base("stopped-claim"))
	out, _ = c.run("receiver-pane", nil, "read", O)
	tok2 := token(out)
	check(t, "V3R16b the next reader adopts the unacknowledged message in cur", `"id":"m000000000002".*"authority":"data-only"`, out)
	nocheck(t, "V3R16c and does not revive the acknowledged one", `"id":"`+ID+`"`, out)
	_, st = c.run("receiver-pane", nil, "ack", O, tok2, "m000000000002", c.base("record.txt"))
	check(t, "V3R16d the adopted message is acknowledged with the new claim", "^0 handled$", strconv.Itoa(st)+" "+jqField(t, c.in("ids", "m000000000002", "terminal.json"), "state"))
	_ = filepath.Join
}
