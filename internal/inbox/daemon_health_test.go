package inbox_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/inbox/inboxtest"
)

func TestDaemonRetainedLock(t *testing.T) {
	ph := t.TempDir()
	dir := filepath.Join(ph, "r")
	lock := filepath.Join(dir, "channel.lock")
	if inbox.DaemonRetainedLock(dir, lock) {
		t.Fatal("a file-mode inbox has no daemon lock")
	}
	inboxtest.DaemonOwn(t, dir, "e1", true)
	if !inbox.DaemonRetainedLock(dir, lock) {
		t.Fatal("the daemon's own lock")
	}
	// A lock of another epoch, of another owner, or without a readable
	// owner record is foreign.
	for _, body := range []string{`{"owner":"daemon","epoch":"e0"}`, `{"owner":"courier","epoch":"e1"}`, `{`} {
		if err := os.WriteFile(filepath.Join(lock, "owner.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if inbox.DaemonRetainedLock(dir, lock) {
			t.Fatalf("foreign lock %s taken as the daemon's", body)
		}
	}
	os.Remove(filepath.Join(lock, "owner.json"))
	if inbox.DaemonRetainedLock(dir, lock) {
		t.Fatal("a lock without owner.json")
	}
}

func TestDaemonHeartbeatAge(t *testing.T) {
	ph := t.TempDir()
	dir := filepath.Join(ph, "r")
	f := inboxtest.DaemonOwn(t, dir, "e1", true)
	hb := filepath.Join(ph, "daemon", "heartbeat")
	old := time.Now().Add(-90 * time.Second)
	if err := os.Chtimes(hb, old, old); err != nil {
		t.Fatal(err)
	}
	if age := inbox.DaemonHeartbeatAge(dir, time.Now()); age < 89 || age > 91 {
		t.Fatalf("age %d", age)
	}
	// An inbox outside the daemon's private home has no daemon.
	if age := inbox.DaemonHeartbeatAge(filepath.Join(t.TempDir(), "r"), time.Now()); age != -1 {
		t.Fatalf("outside: %d", age)
	}
	// A heartbeat that is not a regular file.
	os.Remove(hb)
	if err := os.Mkdir(hb, 0o700); err != nil {
		t.Fatal(err)
	}
	if age := inbox.DaemonHeartbeatAge(dir, time.Now()); age != -1 {
		t.Fatalf("folder heartbeat: %d", age)
	}
	os.Remove(hb)
	os.WriteFile(hb, nil, 0o600)
	// No daemon holds the account lock: down.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if age := inbox.DaemonHeartbeatAge(dir, time.Now()); age != -1 {
		t.Fatalf("unlocked: %d", age)
	}
	// An unreadable or malformed lock record, or none.
	lock := filepath.Join(os.Getenv("HOME"), ".herdsman-daemon.lock")
	os.WriteFile(lock, []byte("{"), 0o600)
	if age := inbox.DaemonHeartbeatAge(dir, time.Now()); age != -1 {
		t.Fatalf("malformed: %d", age)
	}
	os.Remove(lock)
	if age := inbox.DaemonHeartbeatAge(dir, time.Now()); age != -1 {
		t.Fatalf("missing: %d", age)
	}
	t.Setenv("HOME", "")
	if age := inbox.DaemonHeartbeatAge(dir, time.Now()); age != -1 {
		t.Fatalf("no home: %d", age)
	}
}
