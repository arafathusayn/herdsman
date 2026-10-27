package message

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/inbox"
)

func (c *tc) watchTask(card string) {
	c.setAgents("sender-pane", "sender-session", "receiver-pane", "receiver-session", "task-pane", "task-session")
	c.ic.WriteJSON(c.in("watch-targets.json"), []any{map[string]any{"pane": "task-pane", "session": map[string]any{"source": "hook", "agent": "claude", "kind": "id", "value": "task-session"}}})
	c.fx.SetScreen("task-pane", "✦ Heads up · "+card+"\n")
	c.relayBoundary()
}

func rogue() map[string]any {
	a := herdrtest.Agent("rogue-pane", "rogue-session")
	return a
}

// Z2-Z10 (parts 09 and 10) and Y4, Y5.
func TestPart10BoundaryRacesHolds(t *testing.T) {
	c := newCase(t, true)
	c.watchTask("taint finding")
	out, _ := c.run("receiver-pane", nil, "authority")
	check(t, "Z2 the protected boundary is the positive control", "^RELAY", out)
	c.fx.SetAgent("rogue-pane", rogue())
	c.watch()
	check(t, "Z2b an unknown live agent is recorded as boundary exposure", "rogue-pane", c.read(c.in("state", "boundary-tainted")))
	c.fx.SetAgent("rogue-pane", nil)
	c.watch()
	out, _ = c.run("receiver-pane", nil, "authority")
	check(t, "Z2c after that agent exits the authority stays HUMAN", "^HUMAN", out)
	check(t, "Z2d and no card was relayed", "^0 yes$", strconv.Itoa(nfiles(c.in(A, "new"), "*.md"))+" "+yn(c.in("state", "automation-held")))

	c = newCase(t, true)
	c.setAgents("sender-pane", "sender-session", "receiver-pane", "receiver-session", "task-pane", "task-session")
	c.relayBoundary()
	c.setAgents("sender-pane", "sender-session", "receiver-pane", "receiver-session")
	out, _ = c.run("receiver-pane", nil, "authority")
	check(t, "Z2e a closed lower-trust pane keeps RELAY", "^RELAY", out)
	c.ic.WriteJSON(c.in("watch-targets.json"), []any{map[string]any{"pane": "receiver-pane", "session": map[string]any{"source": "hook", "agent": "claude", "kind": "id", "value": "receiver-session"}}})
	failOnce := true
	c.srv.Handle("agent.list", func(p map[string]any) (any, string) {
		if failOnce {
			failOnce = false
			return nil, "temporary_failure"
		}
		return map[string]any{"type": "agent_list", "agents": c.agentRecordsRaw()}, ""
	})
	c.watchPasses(1)
	nocheck(t, "Z2f one failed agent list does not alert the human", "notification show Automatic coordination held", c.calls())
	check(t, "Z2g and records no exposure", "^no$", yn(c.in("state", "boundary-tainted")))
	c.fx.Fail("agent.list", "temporary_failure")
	c.srv.Handle("agent.list", func(map[string]any) (any, string) { return nil, "temporary_failure" })
	c.watch()
	c.watch()
	check(t, "Z2h a repeated agent list failure alerts", "notification show Automatic coordination held", c.calls())
	check(t, "Z2i and still records no exposure", "^no$", yn(c.in("state", "boundary-tainted")))

	c = newCase(t, true)
	c.relayBoundary()
	c.editJSON(c.ic.Proof, func(m map[string]any) { delete(m["boundary"].(map[string]any), "launch_admission") })
	out, _ = c.run("receiver-pane", nil, "authority")
	check(t, "Z3 without the launch admission record the authority is HUMAN", "^HUMAN", out)
	c.editJSON(c.ic.Proof, func(m map[string]any) { m["boundary"].(map[string]any)["launch_admission"] = false })
	out, _ = c.run("receiver-pane", nil, "authority")
	check(t, "Z3b a false admission record is HUMAN", "^HUMAN", out)

	// Z4: an admission during the transport's own identity check.
	c = newCase(t, true)
	c.watchTask("race finding")
	admitted := false
	base := c.fxAgentGet()
	c.srv.Handle("agent.get", func(p map[string]any) (any, string) {
		if p["target"] == "receiver-pane" && !admitted {
			os.Mkdir(c.in("herdr-mutation.lock"), 0o700)
			c.write(c.in("state", "boundary-tainted"), "unsandboxed admission during the send\n")
			c.fx.SetAgent("rogue-pane", rogue())
			os.Remove(c.in("herdr-mutation.lock"))
			admitted = true
		}
		return base(p)
	})
	c.watch()
	check(t, "Z4 the admission happened during the relay", "^true$", strconv.FormatBool(admitted))
	check(t, "Z4b no card was queued after the admission", "^0$", strconv.Itoa(nfiles(c.in(A, "new"), "*.md")))
	out, _ = c.run("receiver-pane", nil, "authority")
	check(t, "Z4c and the authority is HUMAN", "^HUMAN", out)

	// Z4d: an admission writer that starts during the send's agent list waits for the input lock.
	c = newCase(t, true)
	c.watchTask("window finding")
	lists := 0
	var wg sync.WaitGroup
	queued := -1
	baseList := c.fxAgentList()
	c.srv.Handle("agent.list", func(p map[string]any) (any, string) {
		lists++
		if lists == 3 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for os.Mkdir(c.in("herdr-mutation.lock"), 0o700) != nil {
					time.Sleep(10 * time.Millisecond)
				}
				queued = nfiles(c.in(A, "new"), "*.md")
				os.Remove(c.in("herdr-mutation.lock"))
			}()
			time.Sleep(50 * time.Millisecond)
		}
		return baseList(p)
	})
	c.watch()
	wg.Wait()
	check(t, "Z4d an admission during the send's boundary check waits until the card is queued", "^1$", strconv.Itoa(queued))

	c = newCase(t, true)
	c.watchTask("brief finding")
	c.srv.Handle("agent.list", func(p map[string]any) (any, string) {
		agents := c.agentRecordsRaw()
		if inbox.Exists(c.in("herdr-mutation.lock")) {
			b := herdrtest.Agent("brief-pane", "brief-session")
			agents = append(agents, b)
		}
		return map[string]any{"type": "agent_list", "agents": agents}, ""
	})
	c.watch()
	check(t, "Z4e a short-lived agent seen only by the send's recheck is recorded", "brief-pane", c.read(c.in("state", "boundary-tainted")))
	check(t, "Z4f and no card was queued", "^0$", strconv.Itoa(nfiles(c.in(A, "new"), "*.md")))

	// Y4: a card relay observes Herdr outside the queue lock.
	c = newCase(t, true)
	c.watchTask("observe finding")
	underLock := false
	baseList = c.fxAgentList()
	c.srv.Handle("agent.list", func(p map[string]any) (any, string) {
		if inbox.Exists(c.in("channel.lock")) {
			underLock = true
		}
		return baseList(p)
	})
	c.watch()
	check(t, "Y4 the card is relayed and no agent list ran under the queue lock", "^1 false$", strconv.Itoa(nfiles(c.in(A, "new"), "*.md"))+" "+strconv.FormatBool(underLock))

	// Y5: the watcher writes the exposure record under the shared input lock.
	c = newCase(t, true)
	c.watchTask("x")
	c.fx.SetScreen("task-pane", "idle\n")
	c.fx.SetAgent("rogue-pane", rogue())
	barrier := ""
	restore := inbox.SetFileOps(inbox.FileOps{Rename: func(o, n string) error {
		if strings.HasSuffix(n, "boundary-tainted") {
			barrier = map[bool]string{true: "held", false: "free"}[inbox.Exists(c.in("herdr-mutation.lock"))]
		}
		return os.Rename(o, n)
	}})
	c.watch()
	restore()
	check(t, "Y5 the exposure record is published while the input lock is held", "^held$", barrier)
	check(t, "Y5b and the lock is released afterwards", "^no$", yn(c.in("herdr-mutation.lock")))

	// Z5: an ack retry never overwrites a different permanent disposition.
	c = newCase(t, true)
	c.send()
	c.courier()
	tok := token(c.mustRun("receiver-pane", "read", O))
	c.write(c.base("record.txt"), "intake recorded\n")
	c.mustRun("receiver-pane", "ack", O, tok, ID, c.base("record.txt"))
	out, st := c.run("receiver-pane", nil, "ack", O, tok, ID, c.base("record.txt"))
	check(t, "Z5 a retry with the matching terminal is idempotent", "^0 acknowledged "+ID, strconv.Itoa(st)+" "+out)
	c.write(c.in("ids", ID, "terminal.json"), `{"id":"`+ID+`","state":"quarantined","evidence":"keep"}`+"\n")
	before := c.digest(c.in("ids", ID, "terminal.json"))
	out, st = c.run("receiver-pane", nil, "ack", O, tok, ID, c.base("record.txt"))
	check(t, "Z5b a retry over a conflicting terminal is refused", "^2 .*terminal", strconv.Itoa(st)+" "+out)
	check(t, "Z5c and the conflicting terminal is kept", "^"+before+"$", c.digest(c.in("ids", ID, "terminal.json")))
	c.write(c.in("ids", ID, "terminal.json"), "not json\n")
	_, st = c.run("receiver-pane", nil, "ack", O, tok, ID, c.base("record.txt"))
	check(t, "Z5d a corrupt terminal is held too", "^2 not json$", strconv.Itoa(st)+" "+strings.TrimSpace(c.read(c.in("ids", ID, "terminal.json"))))

	// Z6: a disposed sender notice stays disposed; an unfinished publication is repaired.
	c = newCase(t, true)
	c.send()
	c.editJSON(c.ob("new", ID+".md"), func(m map[string]any) {
		m["from_identity"].(map[string]any)["session"].(map[string]any)["value"] = "forged"
	})
	c.courier()
	check(t, "Z6 the forged envelope is quarantined with a sender notice", "^yes$", yn(c.in(A, "notices", ID+".md.json")))
	os.Rename(c.in(A, "notices", ID+".md.json"), c.base("disposed-notice.json"))
	old(t, c.in("ids", ID, "terminal.json"))
	st0, _ := os.Stat(c.in("ids", ID, "terminal.json"))
	c.courier()
	check(t, "Z6b the next sweep does not recreate a disposed notice", "^no yes$", yn(c.in(A, "notices", ID+".md.json"))+" "+yn(c.ob("quarantine", ID+".md")))
	st1, _ := os.Stat(c.in("ids", ID, "terminal.json"))
	check(t, "Z6c and does not rewrite the reconciled terminal", "^true$", strconv.FormatBool(st0.ModTime().Equal(st1.ModTime())))
	os.Remove(c.in("ids", ID, "notice-published"))
	c.courier()
	check(t, "Z6d a notice whose publication was not completed is repaired", "^yes$", yn(c.in(A, "notices", ID+".md.json")))

	// Z7: two overlapping resume helpers send one resume per episode.
	c = newCase(t, true)
	c.codexTask()
	c.allowResume()
	c.relayBoundary()
	rkey := inbox.Digest([]byte(sortedCompact(toAny(map[string]any{"pane": "task-pane", "session": map[string]any{"source": "hook", "agent": "codex", "kind": "id", "value": "task-session"}})) + "\n"))
	var buf bytes.Buffer
	e := &Env{Getenv: func(k string) string {
		return map[string]string{"HERDSMAN_INBOX": c.ic.Dir, "HERDSMAN_PREFLIGHT": c.ic.Proof, "HERDR_PANE_ID": "sender-pane"}[k]
	}, Stdout: &buf, Stderr: &buf, Client: herdr.New(c.srv.Socket)}
	err := e.resumeRun([]string{"im-task"}, func() { c.write(c.in("state", "resume-"+rkey+".sent"), "other helper intent\n") })
	e.releaseAll()
	h, _ := err.(*heldErr)
	check(t, "Z7 the helper that lost the race holds", "^reconcile the previous resume", map[bool]string{true: h.Error()}[h != nil])
	check(t, "Z7b and sends no second resume", "^0$", strconv.Itoa(c.prompts()))

	// Z8: a stale proof during a sweep moves nothing and alerts.
	c = newCase(t, true)
	c.send()
	baseGet := c.fxAgentGet()
	c.srv.Handle("agent.get", func(p map[string]any) (any, string) {
		if p["target"] == "sender-pane" {
			c.write(c.base("ignore-rule"), "sandbox policy\nchanged while the courier runs\n")
		}
		return baseGet(p)
	})
	c.courier()
	check(t, "Z8 a policy changed during the sweep moves nothing to ready", "^yes no$", yn(c.ob("new", ID+".md"))+" "+yn(c.ob("ready", ID+".md")))
	check(t, "Z8b and the human is alerted", "notification show Message setup proof stale", c.calls())
	c.courier()
	check(t, "Z8c a courier started on the stale proof refuses", "^yes no$", yn(c.ob("new", ID+".md"))+" "+yn(c.ob("ready", ID+".md")))

	// Z9: hold and unhold.
	c = newCase(t, true)
	c.codexTask()
	c.relayBoundary()
	nO, nA := map[string]string{"HERDSMAN_NAME": O}, map[string]string{"HERDSMAN_NAME": A}
	out, st = c.run("receiver-pane", nO, "message", "hold", "im-task", "user paused this task")
	check(t, "Z9 hold records the named hold", "^0 HOLD im-task", strconv.Itoa(st)+" "+out)
	reason := func(who string) string {
		v, _ := decodeFile(c.in("state", "hold-im-task"))
		h, _ := inbox.Object(inbox.Object(v)["holds"])[who].(map[string]any)
		r, _ := h["reason"].(string)
		return r
	}
	check(t, "Z9b with the holder and the reason", "^user paused this task$", reason(O))
	out, _ = c.run("sender-pane", nil, "resume", "im-task")
	check(t, "Z9c the resume helper then holds", "HELD user pause or freeze", out)
	os.Mkdir(c.in("herdr-mutation.lock"), 0o700)
	out, st = c.run("receiver-pane", map[string]string{"HERDSMAN_NAME": O, "LOCK_WAIT": "1"}, "message", "unhold", "im-task")
	check(t, "Z9d unhold waits for the shared lock and refuses when busy", "^2 .*lock busy", strconv.Itoa(st)+" "+out)
	os.Remove(c.in("herdr-mutation.lock"))
	out, st = c.run("receiver-pane", nO, "message", "unhold", "im-task")
	check(t, "Z9e unhold clears the hold", "^0 UNHOLD im-task by "+O+"; no hold remains\n no$", strconv.Itoa(st)+" "+out+" "+yn(c.in("state", "hold-im-task")))
	out, st = c.run("receiver-pane", nO, "message", "hold", "../x", "reason")
	check(t, "Z9f an invalid hold name is refused", "^2 ", strconv.Itoa(st)+" "+out)
	out, st = c.run("receiver-pane", nO, "message", "hold", "im-typo", "reason")
	check(t, "Z9f2 a hold for a name that is neither a peer nor a resume target is refused", "^2 .*unknown hold target", strconv.Itoa(st)+" "+out)
	c.run("sender-pane", nA, "message", "hold", "im-task", "the user paused this task")
	out, st = c.run("receiver-pane", nO, "message", "unhold", "im-task")
	check(t, "Z9g the orchestrator cannot clear the adjutant's user-pause hold", "^2 .*no hold by "+O, strconv.Itoa(st)+" "+out)
	check(t, "Z9h and the adjutant's hold stays", "^the user paused this task$", reason(A))
	c.run("receiver-pane", nO, "message", "hold", "im-task", "stopping the task")
	check(t, "Z9i a second owner's hold does not overwrite the first", "^the user paused this task stopping the task$", reason(A)+" "+reason(O))
	c.run("receiver-pane", nO, "message", "unhold", "im-task")
	owners, _ := holdOwners([]byte(c.read(c.in("state", "hold-im-task"))))
	check(t, "Z9j the orchestrator's unhold removes only its own hold", "^"+A+"$", owners)
	out, _ = c.run("sender-pane", nil, "resume", "im-task")
	check(t, "Z9k the resume stays held and names the remaining owner", "^HELD user pause or freeze.*"+A, out)
	check(t, "Z9l and no resume was sent", "^0$", strconv.Itoa(c.prompts()))
	c.run("sender-pane", nA, "message", "unhold", "im-task")
	check(t, "Z9m the last owner's unhold clears the record", "^no$", yn(c.in("state", "hold-im-task")))
	c.write(c.in("state", "hold-im-task"), "held by hand\n")
	out, st = c.run("receiver-pane", nO, "message", "unhold", "im-task")
	check(t, "Z9n a hold record in another format is never changed", "^2 .*owner format.* held by hand$", strconv.Itoa(st)+" "+strings.TrimSpace(out)+" "+strings.TrimSpace(c.read(c.in("state", "hold-im-task"))))
	out, _ = c.run("sender-pane", nil, "resume", "im-task")
	check(t, "Z9o and still holds the resume", "^HELD user pause or freeze", out)

	// Z10: a deliberate hold is quiet in the watcher.
	c = newCase(t, true)
	c.codexTask()
	c.ic.WriteJSON(c.in("watch-targets.json"), []any{map[string]any{"pane": "receiver-pane", "session": map[string]any{"source": "hook", "agent": "claude", "kind": "id", "value": "receiver-session"}}})
	c.relayBoundary()
	c.write(c.in("state", "route-paused"), "")
	c.watch()
	c.watch()
	nocheck(t, "Z10 a user pause sends no resume-held alert", "notification show Goal resume held", c.calls())
	out, _ = c.run("receiver-pane", nil, "status", O)
	check(t, "Z10b the quiet hold is still visible in status", "hold-or-intent: resume-held-im-task", out)
}

// watchPasses runs the watcher for one pass (RUN_FOR=1 with a long interval
// still makes two passes; one failure must be the only one seen).
func (c *tc) watchPasses(n int) {
	c.run("courier-pane", map[string]string{"RUN_FOR": strconv.Itoa(n), "INTERVAL": "1"}, "card-watch")
}

func (c *tc) agentRecordsRaw() []map[string]any {
	return c.agentRecordsVia(c.fxAgentList())
}

func (c *tc) agentRecordsVia(h herdrtest.Handler) []map[string]any {
	r, _ := h(nil)
	m := r.(map[string]any)
	return m["agents"].([]map[string]any)
}

// fxAgentGet and fxAgentList are the fixture's own handlers, for wrappers.
func (c *tc) fxAgentGet() herdrtest.Handler {
	return c.fx.Handler("agent.get")
}

func (c *tc) fxAgentList() herdrtest.Handler {
	return c.fx.Handler("agent.list")
}
