package inboxtest

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// DaemonOwn marks dir as owned by a daemon of epoch, with the daemon's
// retained channel.lock, and points HOME at a new folder whose account
// lock names dir's parent as the private home. With live, the account
// lock is held (until the test ends) and the daemon heartbeat is fresh.
// It returns the account lock's file, to release it early.
func DaemonOwn(t *testing.T, dir, epoch string, live bool) *os.File {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	ph := filepath.Dir(dir)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "state"), 0o700))
	must(os.WriteFile(filepath.Join(dir, "state", "owner.json"), []byte(`{"owner":"daemon","epoch":"`+epoch+`","contract":2}`+"\n"), 0o600))
	must(os.MkdirAll(filepath.Join(dir, "channel.lock"), 0o700))
	must(os.WriteFile(filepath.Join(dir, "channel.lock", "owner.json"), []byte(`{"owner":"daemon","epoch":"`+epoch+`"}`+"\n"), 0o600))
	lock := filepath.Join(home, ".herdsman-daemon.lock")
	must(os.WriteFile(lock, []byte(`{"pid":1,"version":"1.0.0","start":1,"private_home":"`+ph+`","socket":"x"}`+"\n"), 0o600))
	f, err := os.Open(lock)
	must(err)
	t.Cleanup(func() { f.Close() })
	if live {
		must(syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
		must(os.MkdirAll(filepath.Join(ph, "daemon"), 0o700))
		must(os.WriteFile(filepath.Join(ph, "daemon", "heartbeat"), nil, 0o600))
	}
	return f
}
