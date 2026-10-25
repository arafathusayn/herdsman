package message

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Review 3 coverage: the reachable error and edge branches of the round 3
// code, each driven by a fault-injected folder or a direct call.

func chmodT(t *testing.T, p string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(p, 0o700) })
}

// A busy incident whose alerts folder cannot be listed, or cannot be
// written, is refused with an error, never dropped.
func TestR3QueueIncidentFaults(t *testing.T) {
	for _, mode := range []os.FileMode{0o300, 0o500} {
		t.Run(strconv.FormatUint(uint64(mode), 8), func(t *testing.T) {
			c := newCase(t, true)
			e := openEnv(t, c, nil)
			os.Mkdir(c.in("alerts", "busy.lock"), 0o700)
			chmodT(t, c.in("alerts"), mode)
			if err := e.notify("busy", "Busy title", "an incident"); err == nil {
				t.Error("an incident that cannot be queued was not refused")
			}
		})
	}
}

// markAllocation refuses a marks path that is not a folder, a state folder
// it cannot create in, and a marks folder it cannot write; send stops
// before allocating.
func TestR3MarkAllocationFaults(t *testing.T) {
	c := newCase(t, true)
	e := openEnv(t, c, nil)
	c.write(c.in("state", "allocations"), "a file\n")
	if err := e.markAllocation(ID); err == nil {
		t.Error("a marks file in place of the folder was accepted")
	}
	c.write(c.base("body.txt"), "body\n")
	out, code := c.run("sender-pane", nil, "send", A, O, c.base("body.txt"), "brief")
	if code != 2 || !strings.Contains(out, "state/allocations is not a folder") || inbox.Exists(c.in("ids", ID)) {
		t.Errorf("send allocated without a mark: exit %d %q", code, out)
	}
	os.Remove(c.in("state", "allocations"))
	chmodT(t, c.in("state"), 0o500)
	if err := e.markAllocation(ID); err == nil {
		t.Error("an uncreatable marks folder was accepted")
	}
	os.Chmod(c.in("state"), 0o700)
	os.Mkdir(c.in("state", "allocations"), 0o700)
	chmodT(t, c.in("state", "allocations"), 0o500)
	if err := e.markAllocation(ID); err == nil {
		t.Error("an unwritable marks folder was accepted")
	}
}

// The courier's retirement: a busy queue lock waits; seeding errors stop
// the sweep; a mark that is not an id stays; an old mark with no allocation
// goes; a terminal record or log line that cannot be written fails.
func TestR3RetirementEdges(t *testing.T) {
	c := newCase(t, true)
	e := openEnv(t, c, nil)
	cr := &courier{Env: e}
	os.Mkdir(c.in(inbox.ChannelLock), 0o700)
	if err := cr.retireAbandoned(); err != nil || inbox.Exists(c.in("state", "allocations-seeded")) {
		t.Errorf("a busy queue lock did not wait: %v", err)
	}
	os.Remove(c.in(inbox.ChannelLock))

	chmodT(t, c.in("ids"), 0o300)
	if err := cr.retireAbandoned(); err == nil {
		t.Error("an unlistable ids/ did not stop the seeding")
	}
	os.Chmod(c.in("ids"), 0o700)

	os.MkdirAll(c.in("ids", ID), 0o700)
	c.write(c.in("state", "allocations"), "a file\n")
	if err := cr.retireAbandoned(); err == nil {
		t.Error("a seeding mark that cannot be written did not stop the sweep")
	}
	os.Remove(c.in("state", "allocations"))
	os.Remove(c.in("ids", ID))

	if err := cr.retireAbandoned(); err != nil {
		t.Fatal(err)
	}
	c.write(c.in("state", "allocations", "not-an-id"), "")
	c.write(c.in("state", "allocations", "m000000000099"), "")
	past := c.now.Add(-2 * time.Hour)
	os.Chtimes(c.in("state", "allocations", "m000000000099"), past, past)
	if err := cr.retireAbandoned(); err != nil {
		t.Fatal(err)
	}
	check(t, "R3 a mark that is not an id stays", "^yes$", yn(c.in("state", "allocations", "not-an-id")))
	check(t, "R3 an old mark with no allocation goes", "^no$", yn(c.in("state", "allocations", "m000000000099")))

	c.send()
	os.Remove(c.ob("new", ID+".md"))
	os.Chtimes(c.in("ids", ID, "envelope"), past, past)
	chmodT(t, c.in("ids", ID), 0o500)
	if err := cr.retireAbandoned(); err == nil {
		t.Error("a terminal record that cannot be written did not fail the sweep")
	}
	os.Chmod(c.in("ids", ID), 0o700)
	logf := c.in("state", "diagnostic-"+strconv.Itoa(os.Getpid())+".log")
	os.Remove(logf)
	if err := os.Symlink(c.base("elsewhere.log"), logf); err != nil {
		t.Fatal(err)
	}
	if err := cr.retireAbandoned(); err == nil {
		t.Error("a log line that cannot be written did not fail the sweep")
	}
	os.Remove(logf)
	m, _ := decodeObject(c.read(c.in("ids", ID, "terminal.json")))
	check(t, "R3 the allocation was retired before the log failure", "^abandoned$", raw(m["state"]))
}

// The run's ownership: an unlisted lock is still freed by release; a
// listed adopted lock is freed by Release; exposure records: an inventory
// fault with a published record is quiet, a fold refusal propagates, a
// pending fault with the input lock busy waits.
func TestR3OwnershipEdges(t *testing.T) {
	c := newCase(t, true)
	e := openEnv(t, c, nil)
	stray := inbox.TryOnce(c.in("stray.lock"))
	if err := e.release(stray); err != nil || inbox.Exists(c.in("stray.lock")) {
		t.Errorf("an unlisted lock was not freed: %v", err)
	}
	adopted := inbox.TryOnce(c.in("adopted.lock"))
	if e.hold(adopted) == nil || e.hold(nil) != nil {
		t.Fatal("hold")
	}
	sub := e.sub(nil, nil, nil)
	if err := sub.open(false); err != nil {
		t.Fatal(err)
	}
	if l := sub.tryChannel(); l == nil {
		t.Fatal("sub-run queue lock")
	}
	sub.releaseAll()
	if !inbox.Exists(c.in("adopted.lock")) || inbox.Exists(c.in(inbox.ChannelLock)) {
		t.Error("a sub-run's releaseAll freed its parent's lock or kept its own")
	}
	e.Release()
	if inbox.Exists(c.in("adopted.lock")) {
		t.Error("Release left an adopted lock")
	}
	if e.hold(inbox.TryOnce(c.in("late.lock"))) == nil || inbox.Exists(c.in("late.lock")) {
		t.Error("a lock adopted after Release was kept")
	}

	e = openEnv(t, c, nil)
	c.write(c.in("state", "boundary-tainted"), "{}\n")
	if err := e.recordExposure(inbox.FaultInventory, nil); err != nil {
		t.Error(err)
	}
	os.Remove(c.in("state", "boundary-tainted"))
	if err := e.recordExposure(inbox.FaultList, nil); err != nil {
		t.Error(err)
	}
	os.Mkdir(c.in(inbox.MutationLock), 0o700)
	pendingRecord(t, c)
	if err := e.recordExposure(inbox.FaultPending, nil); err != nil || !e.ch.ExposurePending() {
		t.Errorf("a pending fault with the lock busy: %v", err)
	}
	os.Remove(c.in(inbox.MutationLock))
	os.RemoveAll(c.in("state", "exposure-pending"))
	os.Mkdir(c.base("outside"), 0o700)
	os.Symlink(c.base("outside"), c.in("state", "exposure-pending"))
	if err := e.recordExposure(inbox.FaultInventory, nil); err == nil {
		t.Error("a refused fold did not propagate from the exposure record")
	}
}

// Relay: a boundary that turns pending between the pass and the relay
// records it; a record that cannot be published fails the watcher.
func TestR3RelayExposureFailure(t *testing.T) {
	c := newCase(t, true)
	c.ic.WriteJSON(c.in("watch-targets.json"), []any{map[string]any{"pane": "receiver-pane", "session": map[string]any{"source": "hook", "agent": "claude", "kind": "id", "value": "receiver-session"}}})
	c.fx.SetScreen("receiver-pane", "output line\n✦ Heads up · first finding\n")
	c.relayBoundary()
	base := c.fx.Handler("agent.list")
	calls := 0
	c.srv.Handle("agent.list", func(p map[string]any) (any, string) {
		calls++
		if calls == 1 {
			os.Mkdir(c.base("outside"), 0o700)
			os.Symlink(c.base("outside"), c.in("state", "exposure-pending"))
		}
		return base(p)
	})
	out, code := c.watch()
	if code == 0 {
		t.Errorf("a relay exposure that cannot be published did not fail the watcher: %q", out)
	}
}
