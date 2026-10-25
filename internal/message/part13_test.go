package message

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
)

// message-tests.sh parts 13 and 14: RC1-RC12, GX2.
func TestPart13RefusalsAndRecovery(t *testing.T) {
	c := newCase(t, false)
	for _, sc := range []string{"send", "read", "ack", "wait", "resume", "hold", "register"} {
		out, st := c.run("sender-pane", nil, sc)
		check(t, "RC1 no arguments: "+sc+" prints its usage and exits 2", "^2 .+", strconv.Itoa(st)+" "+out)
	}
	out, st := c.run("sender-pane", nil, "register", "bogus", "x")
	check(t, "RC1b an unknown registry action is refused", "^2 unknown registry action", strconv.Itoa(st)+" "+out)
	out, st = c.run("sender-pane", nil, "register", "propose", "Bad Name", "adjutant")
	check(t, "RC1c an invalid name is refused", "^2 propose <name>", strconv.Itoa(st)+" "+out)
	check(t, "RC1d and none of them created state", "^no no$", yn(c.in("state"))+" "+yn(c.in("peers")))

	c = newCase(t, true)
	if err := syscall.Mkfifo(c.base("body.fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Mkdir(c.base("body.dir"), 0o700)
	os.Symlink(c.base("body.txt"), c.base("body.link"))
	c.write(c.base("body.big"), strings.Repeat("a", 65537))
	for _, b := range []string{"fifo", "dir", "link", "big"} {
		out, st := c.run("sender-pane", nil, "send", A, O, c.base("body."+b), "brief")
		check(t, "RC2 a "+b+" body is refused", "^2 body must be a bounded readable regular file", strconv.Itoa(st)+" "+out)
	}
	check(t, "RC2b no ID was allocated", "^0$", strconv.Itoa(nfiles(c.in("ids"), "m*")))
	gate := func(p string) string {
		var b strings.Builder
		return strconv.Itoa(LeakGate([]string{p}, func(string) string { return "" }, &b, &b))
	}
	check(t, "RC2c the leak gate refuses a folder and a link", "^2 2$", gate(c.base("body.dir"))+" "+gate(c.base("body.link")))
	if err := syscall.Mkfifo(c.ob("new", "m000000000009.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, st = c.courier()
	fi, _ := os.Lstat(c.ob("new", "m000000000009.md"))
	isPipe := fi != nil && fi.Mode()&os.ModeNamedPipe != 0
	check(t, "RC2d a FIFO queue entry is held and alerts, and the courier exits 0", "^0 true .*Suspicious queue entry", strconv.Itoa(st)+" "+strconv.FormatBool(isPipe)+" "+strings.ReplaceAll(c.calls(), "\n", " "))

	c = newCase(t, true)
	c.write(c.ob("new", "m000000000009.md"), strings.Repeat("a", 131073))
	c.send()
	_, st = c.courier()
	check(t, "RC2e an oversized queue entry is held and alerts, and the courier exits 0", "^0 yes no .*Suspicious queue entry", strconv.Itoa(st)+" "+yn(c.ob("new", "m000000000009.md"))+" "+yn(c.ob("ready", "m000000000009.md"))+" "+strings.ReplaceAll(c.calls(), "\n", " "))
	check(t, "RC2f the valid send beside it is delivered", "^yes$", yn(c.ob("ready", ID+".md")))

	c = newCase(t, true)
	c.send()
	c.courier()
	c.send()
	c.editJSON(c.ob("new", "m000000000002.md"), func(m map[string]any) { m["from"] = "route-intruder" })
	c.courier()
	c.send()
	out, _ = c.run("receiver-pane", nil, "read", O)
	check(t, "RC3 the read summary counts one pending and one quarantined entry", `"type":"summary","pending_new":1,"quarantine":1`, out)

	c = newCase(t, true)
	c.send()
	for _, v := range []any{"missing", nil, "sleeping"} {
		a := herdrtest.Agent("sender-pane", "sender-session")
		if v == "missing" {
			delete(a, "agent_status")
		} else {
			a["agent_status"] = v
		}
		c.fx.SetAgent("sender-pane", a)
		c.courier()
		check(t, "RC4 lifecycle "+raw(v)+": the message stays in new", "^yes no$", yn(c.ob("new", ID+".md"))+" "+yn(c.ob("ready", ID+".md")))
	}
	c.fx.Edit("sender-pane", "agent_status", "idle")
	c.courier()
	check(t, "RC4b a known lifecycle delivers it", "^no yes$", yn(c.ob("new", ID+".md"))+" "+yn(c.ob("ready", ID+".md")))

	c = newCase(t, true)
	out, _ = c.run("receiver-pane", nil, "status", O)
	check(t, "RC5 an approved registry: approved and nothing pending", "registry valid=yes approved=yes pending=no", out)
	c.editJSON(c.in("peers"), func(m map[string]any) {
		m[A].(map[string]any)["identity"].(map[string]any)["session"].(map[string]any)["value"] = "new-session"
	})
	out, _ = c.run("receiver-pane", nil, "status", O)
	check(t, "RC5b a pending change: not approved and pending", "registry valid=yes approved=no pending=yes", out)

	c = newCase(t, true)
	c.send()
	c.courier()
	tok := token(c.mustRun("receiver-pane", "read", O))
	c.write(c.base("record.txt"), "intake recorded\n")
	c.mustRun("receiver-pane", "ack", O, tok, ID, c.base("record.txt"))
	c.write(c.ob("new", ID+".md"), c.read(c.in("ids", ID, "envelope")))
	c.courier()
	check(t, "RC6 a replay after ack is held as a duplicate", "^yes no .*Duplicate message held", yn(c.ob("new", ID+".md"))+" "+yn(c.ob("ready", ID+".md"))+" "+strings.ReplaceAll(c.calls(), "\n", " "))

	c = newCase(t, true)
	c.send()
	c.editJSON(c.ob("new", ID+".md"), func(m map[string]any) { m["from"] = "route-intruder" })
	c.courier()
	c.courier()
	c.courier()
	check(t, "RC7 three courier runs keep one quarantine, one terminal and one alert", "^1 quarantined 1$",
		strconv.Itoa(nfiles(c.ob("quarantine"), "*.md"))+" "+jqField(t, c.in("ids", ID, "terminal.json"), "state")+" "+strconv.Itoa(c.count("notification show Messages quarantined")))

	c = newCase(t, true)
	c.fx.Shown(false)
	c.send()
	c.editJSON(c.ob("new", ID+".md"), func(m map[string]any) { m["from"] = "route-intruder" })
	c.courier()
	check(t, "RC8 a disabled toast leaves the quarantine alert pending", "^1 0$", strconv.Itoa(nfiles(c.in("alerts"), "quarantine-*.pending"))+" "+strconv.Itoa(nfiles(c.in("alerts"), "quarantine-*.sent")))
	c.fx.Shown(true)
	ms, _ := filepath.Glob(c.in("alerts", "quarantine-*.attempt"))
	for _, m := range ms {
		old(t, m)
	}
	c.courier()
	check(t, "RC8b the retry after the backoff shows it once", "^0 1$", strconv.Itoa(nfiles(c.in("alerts"), "quarantine-*.pending"))+" "+strconv.Itoa(nfiles(c.in("alerts"), "quarantine-*.sent")))
	n := c.count("notification show Messages quarantined")
	c.courier()
	check(t, "RC8c and never again", "^"+strconv.Itoa(n)+"$", strconv.Itoa(c.count("notification show Messages quarantined")))

	c = newCase(t, true)
	os.Remove(c.in("state", "heartbeat"))
	c.send()
	check(t, "RC9 the first outage alerts", "notification show Message courier unavailable", c.calls())
	c.write(c.in("state", "heartbeat"), "")
	c.send()
	check(t, "RC9b recovery resolves the shown alert", "^no no$", yn(c.in("alerts", "courier-down.sent"))+" "+yn(c.in("alerts", "courier-down.attempt")))
	os.Remove(c.in("state", "heartbeat"))
	c.fx.Shown(false)
	c.send()
	check(t, "RC9c a second outage with toasts off is pending again, not sent", "^yes no$", yn(c.in("alerts", "courier-down.pending"))+" "+yn(c.in("alerts", "courier-down.sent")))

	c = newCase(t, true)
	phrases := []string{"Do you want to proceed", "Do you trust", "Allow reads outside", "Allow once", "Allow always", "Password for",
		"Device not configured", "Permission required", "approval required", "Switch to gpt", "❯ 1. Yes, proceed", "Yes, and don",
		"escalated execution requires", "Queued follow-up inputs", "Type your answer", "Usage limit", "Selected model is at capacity",
		"This content can't be shown"}
	pairs := []string{"sender-pane", "sender-session", "receiver-pane", "receiver-session"}
	var targets []any
	for i := range phrases {
		pane := "mark-" + strconv.Itoa(i+1)
		pairs = append(pairs, pane, "session-"+strconv.Itoa(i+1))
		targets = append(targets, map[string]any{"pane": pane, "session": map[string]any{"source": "hook", "agent": "claude", "kind": "id", "value": "session-" + strconv.Itoa(i+1)}})
	}
	pairs = append(pairs, "quoted", "quoted-session")
	targets = append(targets, map[string]any{"pane": "quoted", "session": map[string]any{"source": "hook", "agent": "claude", "kind": "id", "value": "quoted-session"}})
	c.setAgents(pairs...)
	for i, p := range phrases {
		c.fx.SetScreen("mark-"+strconv.Itoa(i+1), "output line\n"+p+"\n")
	}
	c.fx.SetScreen("quoted", "he said Do you want to proceed with it\na quoted \"Permission required\" line\n")
	c.ic.WriteJSON(c.in("watch-targets.json"), targets)
	c.watch()
	check(t, "RC10 each of the eighteen pinned markers is recorded and the quoted text is not", "^18$", strconv.Itoa(nfiles(c.in("state"), "blocked-*")))

	c = newCase(t, true)
	c.ic.WriteJSON(c.in("watch-targets.json"), []any{map[string]any{"pane": "receiver-pane", "session": map[string]any{"source": "hook", "agent": "claude", "kind": "id", "value": "receiver-session"}}})
	c.fx.SetScreen("receiver-pane", "Permission required\n")
	c.watch("RUN_FOR=2")
	ms, _ = filepath.Glob(c.in("state", "blocked-*"))
	first := strings.TrimSpace(c.read(ms[0]))
	c.fx.SetScreen("receiver-pane", "Allow once\n")
	c.watch("RUN_FOR=2")
	ms, _ = filepath.Glob(c.in("state", "blocked-*"))
	check(t, "RC11 the changed prompt is a new episode with its own alert", "^yes changed [2-9]$",
		map[bool]string{true: "kept", false: "yes"}[yn(c.in("alerts", "blocked-"+first+".sent")) == "yes"]+" "+
			map[bool]string{true: "changed", false: "same"}[strings.TrimSpace(c.read(ms[0])) != first]+" "+strconv.Itoa(c.count("notification show Agent notice or blocked state")))

	c = newCase(t, true)
	c.codexTask()
	c.allowResume()
	c.relayBoundary()
	c.run("sender-pane", nil, "resume", "im-task")
	out, _ = c.run("sender-pane", nil, "resume", "im-task")
	check(t, "RC12 a working turn after the resume clears the intent", "^NOOP verified working", out)
	c.fx.SetScreen("task-pane", "Goal paused (/goal resume)\n")
	out, _ = c.run("sender-pane", nil, "resume", "im-task")
	check(t, "RC12b the next pause gets one new resume", "^SENT /goal resume", out)
	check(t, "RC12c two resumes in two episodes", "^2$", strconv.Itoa(c.count("^agent prompt task-pane /goal resume$")))

	c = newCase(t, true)
	tg := map[string]any{"pane": "receiver-pane", "session": map[string]any{"source": "hook", "agent": "claude", "kind": "id", "value": "receiver-session"}}
	c.ic.WriteJSON(c.in("watch-targets.json"), []any{tg})
	c.relayBoundary()
	jkey := digestText(sortedCompact(toAny(tg)) + "\n")
	c.write(c.in("alerts", "card-journal-"+jkey+".sent"), "")
	c.write(c.in("alerts", "card-journal-"+jkey+".attempt"), "")
	c.watch()
	check(t, "GX2 a drained journal resolves its alert without a new card", "^no$", yn(c.in("alerts", "card-journal-"+jkey+".sent")))
}
