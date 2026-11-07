package inbox

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// Health of a daemon-owned inbox (design 4.5), for the retained readers:
// the file-only `message status` and the web reader. The per-service
// heartbeats do not exist; liveness is the daemon's. Every record read
// here is strict JSON (duplicate keys and wrong types refused), so a
// malformed record never passes for the daemon's.

// strictObject reads a bounded regular file as one strict JSON object.
func strictObject(p string, max int64) map[string]any {
	b, err := ReadBounded(p, max)
	if err != nil || StrictJSON(b) != nil {
		return nil
	}
	v, _ := Decode(b)
	return Object(v)
}

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
	if epoch == "" {
		return false
	}
	o := strictObject(filepath.Join(lockDir, "owner.json"), 4096)
	e, ok := String(o, "epoch")
	return o["owner"] == "daemon" && ok && e == epoch
}

// DaemonServiceLocks are the service locks a daemon-owned inbox's
// readers judge beside the script's two: the daemon retains them, so
// one without the daemon's record is a foreign or old writer's.
var DaemonServiceLocks = []string{"courier", "watch"}

// DaemonHeartbeatAge is the age in seconds of the daemon's heartbeat for
// an inbox under its private home, when the account lock names a live
// daemon of that private home; -1 otherwise.
func DaemonHeartbeatAge(dir string, now time.Time) int64 {
	_, age := DaemonLiveness(dir, now)
	return age
}

// DaemonLiveness is the daemon's pid as its account lock records it and
// its heartbeat age (DaemonHeartbeatAge); "none" and -1 when no live
// daemon of the inbox's private home holds the lock.
func DaemonLiveness(dir string, now time.Time) (pid string, age int64) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "none", -1
	}
	lock := filepath.Join(home, ".herdsman-daemon.lock")
	l := strictObject(lock, 4096)
	ph, ok := String(l, "private_home")
	if !ok || ph == "" || filepath.Dir(dir) != filepath.Clean(ph) {
		return "none", -1
	}
	f, err := os.Open(lock)
	if err != nil {
		return "none", -1
	}
	defer f.Close()
	if syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) == nil {
		// Nobody holds the account lock: no daemon runs.
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return "none", -1
	}
	pid = "unknown"
	if n, ok := Int(l, "pid"); ok && n > 0 {
		pid = strconv.FormatInt(n, 10)
	}
	st, err := os.Lstat(filepath.Join(ph, "daemon", "heartbeat"))
	if err != nil || !st.Mode().IsRegular() {
		return pid, -1
	}
	// A heartbeat written within the second after the reader took its
	// clock is fresh (age 0); one dated later than that is not a
	// heartbeat of this clock and is invalid.
	d := now.Sub(st.ModTime())
	if d < -time.Second {
		return pid, -1
	}
	return pid, max(0, int64(d/time.Second))
}
