package web

import (
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox/inboxtest"
)

// TestWebHealthDaemonInbox: the web reader shows a daemon-owned inbox's
// single daemon liveness instead of the courier and watcher heartbeats,
// never flags the daemon's retained channel.lock, and shows the daemon
// down when no daemon holds the account lock (4.5).
func TestWebHealthDaemonInbox(t *testing.T) {
	root := newInbox(t)
	f := inboxtest.DaemonOwn(t, root, "e1", true)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(root, "channel.lock"), old, old)
	ch := readChannel(root, time.Now())
	if len(ch.Health) != 1 || ch.Health[0].Name != "daemon" || !ch.Health[0].OK {
		t.Fatalf("health %+v", ch.Health)
	}
	if slices.Contains(ch.Flags, "channel-lock-stale") {
		t.Fatalf("flags %v", ch.Flags)
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	ch = readChannel(root, time.Now())
	if len(ch.Health) != 1 || ch.Health[0].OK || ch.Health[0].Age != -1 {
		t.Fatalf("down: %+v", ch.Health)
	}
	// A lock of another epoch is foreign and still flagged.
	os.WriteFile(filepath.Join(root, "channel.lock", "owner.json"), []byte(`{"owner":"daemon","epoch":"e0"}`), 0o600)
	os.Chtimes(filepath.Join(root, "channel.lock"), old, old)
	ch = readChannel(root, time.Now())
	if !slices.Contains(ch.Flags, "channel-lock-stale") {
		t.Fatalf("foreign lock: %v", ch.Flags)
	}
}
