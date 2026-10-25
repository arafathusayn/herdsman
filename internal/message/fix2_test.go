package message

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// D1: a new message during a standing health flag is a MESSAGE, and the
// unchanged flag does not wake the waiter again.
func TestD1MessageDuringHold(t *testing.T) {
	c := newCase(t, true)
	c.write(c.in("state", "automation-held"), "HUMAN held")
	out, code := c.wait("receiver-pane", O)
	check(t, "D1 the hold is reported once", "^1 HEALTH automation-held", strconv.Itoa(code)+" "+out)
	c.send()
	c.courier()
	out, code = c.wait("receiver-pane", O)
	check(t, "D1 a message during the hold is a MESSAGE", "^0 MESSAGE "+O, strconv.Itoa(code)+" "+out)
	out, code = c.wait("receiver-pane", O)
	check(t, "D1 nothing new: TICK, not HEALTH again", "^0 TICK", strconv.Itoa(code)+" "+out)
}

// D2: the change is reported before it is recorded; a failed record after
// the report leaves the change to report again.
func TestD2ReportBeforeRecord(t *testing.T) {
	c := newCase(t, true)
	c.send()
	c.courier()
	restore := inbox.SetFileOps(inbox.FileOps{Rename: func(o, n string) error {
		if strings.Contains(n, "wait-") {
			return errors.New("injected")
		}
		return os.Rename(o, n)
	}})
	out, code := c.wait("receiver-pane", O)
	restore()
	check(t, "D2 the MESSAGE line comes first, then the failed record", "^[12] MESSAGE "+O, strconv.Itoa(code)+" "+out)
	out, code = c.wait("receiver-pane", O)
	check(t, "D2 the unrecorded change is reported again", "^0 MESSAGE "+O, strconv.Itoa(code)+" "+out)
}

type failingEvents struct{ opens int }

func (f *failingEvents) Open(context.Context, []map[string]any) (Stream, error) {
	f.opens++
	return nil, errors.New("refused")
}

// D4: a subscription that cannot be opened is retried with backoff (1 s
// doubling to 60 s), not every second, and sweeps keep their schedule.
func TestD4SubscriptionBackoff(t *testing.T) {
	c := newCase(t, true)
	f := &failingEvents{}
	c.events = f
	c.send()
	out, code := c.courier("RUN_FOR=600", "SWEEP=60")
	if code != 0 {
		t.Fatalf("courier %d %q", code, out)
	}
	if f.opens > 16 || f.opens < 8 {
		t.Errorf("600 s without herdr events: %d subscription tries, want about 14 (1 s doubling to 60 s)", f.opens)
	}
	check(t, "D4 the message is still delivered by the sweep", "^yes$", yn(c.ob("ready", ID+".md")))
}

// D8: ring markers older than an hour are pruned by the sweep.
func TestD8RingMarkersPruned(t *testing.T) {
	c := newCase(t, true)
	old, fresh := c.in("state", "ring-old"), c.in("state", "ring-fresh")
	c.write(old, "m000000000001")
	c.write(fresh, "m000000000002")
	past := c.now.Add(-2 * time.Hour)
	os.Chtimes(old, past, past)
	os.Chtimes(fresh, c.now, c.now)
	c.courier("RUN_FOR=2", "SWEEP=1")
	check(t, "D8 old marker pruned, fresh kept", "^no yes$", yn(old)+" "+yn(fresh))
}

// D12: the allocation copy is written atomically: a failed write leaves no
// torn copy and queues nothing.
func TestD12AtomicAllocationCopy(t *testing.T) {
	c := newCase(t, true)
	restore := inbox.SetFileOps(inbox.FileOps{Rename: func(o, n string) error {
		if strings.HasSuffix(n, "envelope") {
			return errors.New("injected")
		}
		return os.Rename(o, n)
	}})
	out, code := c.run("sender-pane", nil, "send", A, O, c.base("body.txt"), "brief")
	restore()
	check(t, "D12 the send fails", "^2 cannot record the allocation copy", strconv.Itoa(code)+" "+out)
	check(t, "D12 no torn copy and nothing queued", "^no 0$", yn(c.in("ids", ID, "envelope"))+" "+strconv.Itoa(nfiles(c.ob("new"), "*.md")))
}

// D13: an incident whose alert lock is busy is not dropped silently: the
// command waits LOCK_WAIT tries and then says so.
func TestD13BusyAlertLock(t *testing.T) {
	c := newCase(t, true)
	os.Remove(c.in("state", "heartbeat"))
	os.MkdirAll(c.in("alerts", "courier-down.lock"), 0o700)
	start := c.now
	out := c.send()
	check(t, "D13 a busy alert lock is reported, not swallowed", "WARNING alert courier-down not recorded", out)
	if c.now.Sub(start) < 2*time.Second {
		t.Errorf("no wait for the busy lock: %s", c.now.Sub(start))
	}
	os.Remove(c.in("alerts", "courier-down.lock"))

	c.send()
	check(t, "D13 with the lock free the incident is recorded", "^yes$", yn(c.in("alerts", "courier-down.sent")))
}

// D20: a held watcher records automation-held once, not on every pass.
func TestD20HeldWrittenOnce(t *testing.T) {
	c := newCase(t, true) // no relay boundary: the watcher is held
	c.ic.WriteJSON(c.in("watch-targets.json"), []any{map[string]any{"pane": "receiver-pane", "session": map[string]any{"source": "hook", "agent": "claude", "kind": "id", "value": "receiver-session"}}})
	c.watch()
	st1, err := os.Stat(c.in("state", "automation-held"))
	if err != nil {
		t.Fatal(err)
	}
	c.watch("RUN_FOR=5")
	st2, _ := os.Stat(c.in("state", "automation-held"))
	if !os.SameFile(st1, st2) {
		t.Error("automation-held was rewritten while the hold stood")
	}
}
