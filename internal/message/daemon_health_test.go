package message

import (
	"encoding/json"
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
	if !strings.Contains(out, "daemon pid=1 heartbeat-age=0;") {
		t.Fatalf("no daemon role line:\n%s", out)
	}
	for _, f := range []string{"courier-down", "watch-down", "channel-lock-stale", "daemon-down", "courier pid=", "watch pid="} {
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

// TestStatusDaemonRoleJSON: the --json roles of a daemon-owned inbox are
// the daemon's liveness, never the obsolete service heartbeats (4.5).
func TestStatusDaemonRoleJSON(t *testing.T) {
	c := newCase(t, true)
	inboxtest.DaemonOwn(t, c.ic.Dir, "e1", true)
	hb := c.ic.Dir + "/../daemon/heartbeat"
	at := c.now.Add(-7 * time.Second)
	os.Chtimes(hb, at, at)
	os.WriteFile(c.in("state", "heartbeat"), nil, 0o600)
	out, _ := c.run("receiver-pane", map[string]string{"HERDSMAN_NAME": O}, "message", "status", "--json")
	var rep StatusReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(rep.Roles) != 1 || rep.Roles[0] != (RoleStatus{Role: "daemon", PID: "1", HeartbeatAge: "7"}) {
		t.Fatalf("roles %+v", rep.Roles)
	}
}

// TestForeignStaleServiceLockFlagged: on a daemon-owned inbox a stale
// courier.lock or watch.lock without the daemon's record is a foreign or
// old writer's and is flagged; the daemon's retained ones never are.
func TestForeignStaleServiceLockFlagged(t *testing.T) {
	c := newCase(t, true)
	inboxtest.DaemonOwn(t, c.ic.Dir, "e1", true)
	hb := c.ic.Dir + "/../daemon/heartbeat"
	os.Chtimes(hb, c.now, c.now)
	old := time.Now().Add(-time.Hour)
	for _, l := range []string{"courier.lock", "watch.lock"} {
		os.Mkdir(c.in(l), 0o700)
		os.WriteFile(c.in(l, "owner.json"), []byte(`{"owner":"daemon","epoch":"e1"}`), 0o600)
		os.Chtimes(c.in(l), old, old)
	}
	out, _ := c.run("receiver-pane", map[string]string{"HERDSMAN_NAME": O}, "message", "status")
	if strings.Contains(out, "lock-stale") {
		t.Fatalf("the daemon's retained locks flagged:\n%s", out)
	}
	os.Remove(c.in("courier.lock", "owner.json"))
	os.WriteFile(c.in("watch.lock", "owner.json"), []byte(`{"owner":"daemon","epoch":"e0"}`), 0o600)
	os.Chtimes(c.in("courier.lock"), old, old)
	os.Chtimes(c.in("watch.lock"), old, old)
	out, _ = c.run("receiver-pane", map[string]string{"HERDSMAN_NAME": O}, "message", "status")
	check(t, "a foreign stale courier lock", "courier-lock-stale", out)
	check(t, "a stale watch lock of another epoch", "watch-lock-stale", out)
}
