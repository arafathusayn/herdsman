package message

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox/inboxtest"
)

// TestStatusHealthyDaemonInbox: the file-only status of a daemon-owned
// inbox reads the daemon's liveness, not the courier and watcher
// heartbeats, and never flags the daemon's retained channel.lock (4.5).
func TestStatusHealthyDaemonInbox(t *testing.T) {
	c := newCase(t, true)
	inboxtest.DaemonOwn(t, c.ic.Dir, "e1", true)
	hb := c.ic.Dir + "/../daemon/heartbeat"
	os.Chtimes(hb, c.now, c.now) // the injected clock, not the wall clock
	os.Remove(c.in("state", "heartbeat"))
	old := time.Now().Add(-time.Hour)
	os.Chtimes(c.in("channel.lock"), old, old)
	out, _ := c.run("receiver-pane", map[string]string{"WATCH_REQUIRED": "1", "HERDSMAN_NAME": O}, "message", "status")
	if !strings.Contains(out, "courier pid") && !strings.Contains(out, "courier") {
		t.Fatalf("status did not run:\n%s", out)
	}
	for _, f := range []string{"courier-down", "watch-down", "channel-lock-stale", "daemon-down"} {
		if strings.Contains(out, f) {
			t.Fatalf("flag %s on a healthy daemon inbox:\n%s", f, out)
		}
	}
}

// TestStatusDaemonDown: a daemon-owned inbox whose daemon is gone, or
// whose heartbeat is old, is flagged daemon-down.
func TestStatusDaemonDown(t *testing.T) {
	c := newCase(t, true)
	f := inboxtest.DaemonOwn(t, c.ic.Dir, "e1", true)
	old := c.now.Add(-time.Hour)
	hb := c.ic.Dir + "/../daemon/heartbeat"
	os.Chtimes(hb, old, old)
	out, _ := c.run("receiver-pane", map[string]string{"HERDSMAN_NAME": O}, "message", "status")
	check(t, "an old daemon heartbeat", "daemon-down", out)
	os.Chtimes(hb, time.Now(), time.Now())
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	out, _ = c.run("receiver-pane", map[string]string{"HERDSMAN_NAME": O}, "message", "status")
	check(t, "no daemon holds the account lock", "daemon-down", out)
}

// TestForeignStaleLockStillFlagged: a stale channel.lock that is not the
// daemon's retained lock (another epoch) is still flagged.
func TestForeignStaleLockStillFlagged(t *testing.T) {
	c := newCase(t, true)
	inboxtest.DaemonOwn(t, c.ic.Dir, "e1", true)
	os.WriteFile(c.in("channel.lock", "owner.json"), []byte(`{"owner":"daemon","epoch":"e0"}`), 0o600)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(c.in("channel.lock"), old, old)
	out, _ := c.run("receiver-pane", map[string]string{"HERDSMAN_NAME": O}, "message", "status")
	check(t, "a foreign stale lock", "channel-lock-stale", out)
}

// TestStatusHealthFrozenClock: a daemon heartbeat dated more than a
// second after the reader's clock is invalid (daemon-down); one within
// the second of the reader's own clock read is fresh.
func TestStatusHealthFrozenClock(t *testing.T) {
	c := newCase(t, true)
	inboxtest.DaemonOwn(t, c.ic.Dir, "e1", true)
	hb := c.ic.Dir + "/../daemon/heartbeat"
	for _, d := range []time.Duration{time.Second + time.Millisecond, time.Hour} {
		future := c.now.Add(d)
		if err := os.Chtimes(hb, future, future); err != nil {
			t.Fatal(err)
		}
		out, _ := c.run("receiver-pane", map[string]string{"WATCH_REQUIRED": "1", "HERDSMAN_NAME": O}, "message", "status")
		if !strings.Contains(out, "daemon-down") {
			t.Fatalf("a heartbeat %v in the future reads as fresh:\n%s", d, out)
		}
	}
	near := c.now.Add(500 * time.Millisecond)
	_ = os.Chtimes(hb, near, near)
	out, _ := c.run("receiver-pane", map[string]string{"WATCH_REQUIRED": "1", "HERDSMAN_NAME": O}, "message", "status")
	if strings.Contains(out, "daemon-down") {
		t.Fatalf("a heartbeat within the clock read reads as down:\n%s", out)
	}
}
