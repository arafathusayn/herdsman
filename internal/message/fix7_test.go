package message

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/inbox"
)

// openEnv is an Env on the case's inbox, opened for writing.
func openEnv(t *testing.T, c *tc, extra map[string]string) *Env {
	t.Helper()
	vars := map[string]string{"HERDSMAN_INBOX": c.ic.Dir, "HERDSMAN_PREFLIGHT": c.ic.Proof, "LOCK_WAIT": "1"}
	for k, v := range extra {
		vars[k] = v
	}
	e := &Env{Getenv: func(k string) string { return vars[k] }, Stdout: io.Discard, Stderr: io.Discard, Client: herdr.New(c.srv.Socket), Now: func() time.Time { return c.now }, Sleep: func(time.Duration) {}}
	if err := e.open(false); err != nil {
		t.Fatal(err)
	}
	return e
}

func pendingRecord(t *testing.T, c *tc) {
	t.Helper()
	os.MkdirAll(c.in("state", "exposure-pending"), 0o700)
	c.write(c.in("state", "exposure-pending", "1.json"), `{"agents":[{"pane":"rogue-pane","agent":"claude","session":null}],"time":1}`+"\n")
}

// Review 3, finding 1: once the signal path drained the run, the command
// no longer owns the input lock: it takes no new lock and publishes no
// waiting exposure, and a boundary check sees no owner.
func TestR3DrainedRunPublishesNothing(t *testing.T) {
	c := newCase(t, true)
	e := openEnv(t, c, nil)
	l, err := e.takeMutation(1)
	if err != nil || l == nil {
		t.Fatalf("take: %v %v", l, err)
	}
	e.Release()
	pendingRecord(t, c)
	if ownedNow(e) {
		t.Error("a drained run still reports the input lock as its own")
	}
	if l2, err := e.takeMutation(1); l2 != nil || err != nil {
		t.Errorf("a drained run took the input lock again: %v %v", l2, err)
	}
	if inbox.Exists(c.in("state", "boundary-tainted")) || !e.ch.ExposurePending() {
		t.Error("a drained run published the waiting exposure")
	}
	if inbox.Exists(c.in(inbox.MutationLock)) {
		t.Error("a drained run left the input lock behind")
	}
}

// Review 3, finding 1: the courier's retirement takes the queue lock in the
// run's own lock list, so the signal path frees it.
func TestR3RetirementLockFreedBySignal(t *testing.T) {
	c := newCase(t, true)
	c.send()
	os.Remove(c.ob("new", ID+".md"))
	past := c.now.Add(-2 * time.Hour)
	os.Chtimes(c.in("ids", ID, "envelope"), past, past)
	e := openEnv(t, c, nil)
	stranded := false
	signalled := false
	e.Now = func() time.Time {
		if !signalled && inbox.Exists(c.in(inbox.ChannelLock)) {
			signalled = true
			e.Release() // the signal arrives while the queue lock is held
			stranded = inbox.Exists(c.in(inbox.ChannelLock))
		}
		return c.now
	}
	cr := &courier{Env: e}
	_ = cr.retireAbandoned()
	if !signalled {
		t.Fatal("the retirement never held the queue lock")
	}
	if stranded {
		t.Error("the signal path left the retirement's queue lock behind")
	}
}

// Review 3, finding 2 (A24): an incident retried a thousand times while
// its lock stays busy keeps one queued record, and the heap stays flat;
// distinct incidents past the bound are refused, never dropped.
func TestR3QueuedIncidentBounded(t *testing.T) {
	c := newCase(t, true)
	e := openEnv(t, c, nil)
	if err := os.Mkdir(c.in("alerts", "busy.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	queued := func() int { q, _ := filepath.Glob(c.in("alerts", "busy.queued-*")); return len(q) }
	round := func() {
		for range 1000 {
			if err := e.notify("busy", "Busy title", "the same incident"); err != nil {
				t.Fatal(err)
			}
			e.retryAlerts()
		}
	}
	round()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	round()
	runtime.GC()
	runtime.ReadMemStats(&after)
	if n := queued(); n != 1 {
		t.Errorf("2000 retries of one incident left %d queued records, want 1", n)
	}
	if grow := int64(after.HeapAlloc) - int64(before.HeapAlloc); grow > 1<<20 {
		t.Errorf("heap grew %d bytes over 1000 retries", grow)
	}
	if lines := strings.Count(c.read(c.in("state", "diagnostic-"+strconv.Itoa(os.Getpid())+".log")), "ALERT busy queued"); lines > 1 {
		t.Errorf("one queued incident logged %d times", lines)
	}
	var err error
	for i := range queuedMax + 1 {
		if err = e.notify("busy", "Busy title", fmt.Sprintf("distinct incident %d", i)); err != nil {
			break
		}
	}
	if err == nil {
		t.Error("distinct incidents past the bound were not refused")
	}
	if n := queued(); n != queuedMax {
		t.Errorf("queued records %d, want the bound %d", n, queuedMax)
	}
	os.Remove(c.in("alerts", "busy.lock"))
	e.retryAlerts()
	if n := queued(); n != 0 {
		t.Errorf("the free lock left %d queued records", n)
	}
}

// Review 3, finding 3 (A24): the courier's abandoned-allocation recovery
// does not rescan the permanent allocation history: with thousands of
// tombstones and ids/ unlistable, an old cut allocation is still retired.
func TestR3RetirementSkipsHistory(t *testing.T) {
	c := newCase(t, true)
	for i := 2; i < 3002; i++ {
		d := c.in("ids", fmt.Sprintf("m%012d", 100000+i))
		os.MkdirAll(d, 0o700)
		os.WriteFile(filepath.Join(d, "envelope"), []byte("{}\n"), 0o600)
		os.WriteFile(filepath.Join(d, "terminal.json"), []byte(`{"state":"handled"}`+"\n"), 0o600)
	}
	c.courier() // the first sweep after the upgrade lists the history once
	c.send()
	os.Remove(c.ob("new", ID+".md"))
	past := c.now.Add(-2 * time.Hour)
	os.Chtimes(c.in("ids", ID, "envelope"), past, past)
	if err := os.Chmod(c.in("ids"), 0o300); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(c.in("ids"), 0o700)
	out, code := c.courier("RUN_FOR=30")
	os.Chmod(c.in("ids"), 0o700)
	if code != 0 {
		t.Fatalf("courier %d %q", code, out)
	}
	m, _ := decodeObject(c.read(c.in("ids", ID, "terminal.json")))
	check(t, "R3 the cut allocation is retired without listing ids/", "^abandoned$", raw(m["state"]))
	open, _ := os.ReadDir(c.in("state", "allocations"))
	check(t, "R3 no unresolved allocation stays tracked", "^0$", strconv.Itoa(len(open)))
}

// Review 3, finding 4 (D18): a status-only transition after the first scan,
// with no screen update, is seen on the next pass, not at the 300 s
// reconcile.
func TestR3StatusOnlyTransition(t *testing.T) {
	c := watchedCase(t)
	c.fx.SetScreen("receiver-pane", "ordinary content\n")
	c.events = &scripted{c: c, steps: []string{"@sleep 20", "@blocked receiver-pane receiver-session",
		`{"event":"pane.agent_status_changed","data":{"pane_id":"receiver-pane","workspace_id":"w1","agent_status":"blocked"}}`}}
	c.srv.ResetRequests()
	out, code := c.run("courier-pane", map[string]string{"RUN_FOR": "60", "INTERVAL": "5"}, "card-watch")
	if code != 0 {
		t.Fatalf("watch %d %q", code, out)
	}
	if !strings.Contains(c.calls(), "notification show Agent notice or blocked state") {
		t.Error("a status-only blocked transition was not seen within 60 s")
	}
	subs := strings.Join(c.events.(*scripted).subs, " ")
	if !strings.Contains(subs, "pane.agent_status_changed") {
		t.Errorf("card-watch subscriptions %s", subs)
	}
}

func setBlocked(c *tc, pane, session string) {
	a := herdrtest.Agent(pane, session)
	a["agent_status"] = "blocked"
	c.fx.SetAgent(pane, a)
}

func ownedNow(e *Env) bool {
	owned := false
	_ = e.ch.AsOwner(func(held bool) error { owned = held; return nil })
	return owned
}

// Review 3 probes (reviewer's cases): an old allocation with a live entry
// in any queue folder is not retired, and neither is one whose copy cannot
// be read, decoded or bounded.
func TestR3LiveOrUnclearAllocationKept(t *testing.T) {
	for _, mode := range []string{"new", "ready", "cur", "quarantine", "unreadable", "malformed", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			c := newCase(t, true)
			c.send()
			folder := mode
			switch mode {
			case "unreadable", "malformed", "oversized":
				folder = "ready"
			}
			if folder != "new" {
				if err := os.Rename(c.ob("new", ID+".md"), c.ob(folder, ID+".md")); err != nil {
					t.Fatal(err)
				}
			}
			alloc := c.in("ids", ID, "envelope")
			switch mode {
			case "malformed":
				c.write(alloc, "broken allocation JSON\n")
			case "oversized":
				c.write(alloc, strings.Repeat("x", inbox.MaxSize+1))
			}
			past := c.now.Add(-2 * time.Hour)
			os.Chtimes(alloc, past, past)
			if mode == "unreadable" {
				os.Chmod(alloc, 0)
				defer os.Chmod(alloc, 0o600)
			}
			e := openEnv(t, c, nil)
			if err := (&courier{Env: e}).retireAbandoned(); err != nil {
				t.Fatal(err)
			}
			if inbox.Exists(c.in("ids", ID, "terminal.json")) {
				t.Errorf("%s: a live or unclear allocation was retired", mode)
			}
		})
	}
}
