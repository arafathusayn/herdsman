package inbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Health of a daemon-owned inbox (design 4.5), for the retained readers:
// the file-only `message status` and the web reader. The per-service
// heartbeats do not exist; liveness is the daemon's.

// daemonEpoch is the epoch of a daemon-owned inbox ("" when none).
func daemonEpoch(dir string) string {
	b, ok := ReadStrict(dir, filepath.Join(dir, "state", "owner.json"))
	if !ok {
		return ""
	}
	v, _ := Decode(b)
	o := Object(v)
	if o["owner"] != "daemon" {
		return ""
	}
	e, _ := String(o, "epoch")
	return e
}

// DaemonRetainedLock reports whether a lock folder is one the daemon
// retains for this inbox: its owner.json names the daemon and the inbox's
// epoch. Such a lock is never reported stale; any other is judged by the
// script's rule.
func DaemonRetainedLock(dir, lockDir string) bool {
	epoch := daemonEpoch(dir)
	b, err := ReadBounded(filepath.Join(lockDir, "owner.json"), 4096)
	if epoch == "" || err != nil {
		return false
	}
	var o struct {
		Owner string `json:"owner"`
		Epoch string `json:"epoch"`
	}
	return json.Unmarshal(b, &o) == nil && o.Owner == "daemon" && o.Epoch == epoch
}

// DaemonHeartbeatAge is the age in seconds of the daemon's heartbeat for
// an inbox under its private home, when the account lock names a live
// daemon of that private home; -1 otherwise.
func DaemonHeartbeatAge(dir string, now time.Time) int64 {
	home, err := os.UserHomeDir()
	if err != nil {
		return -1
	}
	lock := filepath.Join(home, ".herdsman-daemon.lock")
	b, err := ReadBounded(lock, 4096)
	if err != nil {
		return -1
	}
	var l struct {
		PrivateHome string `json:"private_home"`
	}
	if json.Unmarshal(b, &l) != nil || l.PrivateHome == "" || filepath.Dir(dir) != filepath.Clean(l.PrivateHome) {
		return -1
	}
	f, err := os.Open(lock)
	if err != nil {
		return -1
	}
	defer f.Close()
	if syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) == nil {
		// Nobody holds the account lock: no daemon runs.
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return -1
	}
	st, err := os.Lstat(filepath.Join(l.PrivateHome, "daemon", "heartbeat"))
	if err != nil || !st.Mode().IsRegular() {
		return -1
	}
	// A heartbeat written within the second after the reader took its
	// clock is fresh (age 0); one dated later than that is not a
	// heartbeat of this clock and is invalid.
	age := now.Sub(st.ModTime())
	if age < -time.Second {
		return -1
	}
	return max(0, int64(age/time.Second))
}
