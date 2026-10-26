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

// TestDaemonLockReadersStrict: a lock owner record or an account lock
// with duplicate keys or wrong types never passes for the daemon's.
func TestDaemonLockReadersStrict(t *testing.T) {
	ph := t.TempDir()
	dir := filepath.Join(ph, "r")
	inboxtest.DaemonOwn(t, dir, "e1", true)
	lock := filepath.Join(dir, "channel.lock")
	for _, body := range []string{
		`{"owner":"courier","owner":"daemon","epoch":"e1"}`,
		`{"owner":"daemon","epoch":"e0","epoch":"e1"}`,
		`{"owner":"daemon","epoch":1}`,
	} {
		if err := os.WriteFile(filepath.Join(lock, "owner.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if inbox.DaemonRetainedLock(dir, lock) {
			t.Fatalf("lock record %s taken as the daemon's", body)
		}
	}
	acct := filepath.Join(os.Getenv("HOME"), ".herdsman-daemon.lock")
	if pid, age := inbox.DaemonLiveness(dir, time.Now()); pid != "1" || age < 0 {
		t.Fatalf("live daemon: pid %s age %d", pid, age)
	}
	for _, body := range []string{
		`{"pid":1,"private_home":"/elsewhere","private_home":"` + ph + `"}`,
		`{"pid":1,"private_home":["` + ph + `"]}`,
	} {
		if err := os.WriteFile(acct, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if pid, age := inbox.DaemonLiveness(dir, time.Now()); pid != "none" || age != -1 {
			t.Fatalf("account lock %s: pid %s age %d", body, pid, age)
		}
	}
	// A pid that is not a positive integer is reported unknown.
	if err := os.WriteFile(acct, []byte(`{"pid":"7","private_home":"`+ph+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if pid, _ := inbox.DaemonLiveness(dir, time.Now()); pid != "unknown" {
		t.Fatalf("pid %s", pid)
	}
}
