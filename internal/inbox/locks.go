package inbox

import (
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// The channel's locks are folders: mkdir is atomic on every file system, a
// script and the binary that take one name exclude each other, and the
// owner's pid sits in <lock>/pid. A lock is never removed by anyone but its
// owner: a lock that stays is reported stale (Stale) for a human to
// inspect, never taken over.
const (
	ChannelLock  = "channel.lock"        // the queue lock (lock, try_lock)
	MutationLock = "herdr-mutation.lock" // the shared input and admission lock (mutation_lock)
	ActionLock   = "action.lock"         // the approval gate's lock, under approvals/
)

// Lock is a held mkdir+pid lock.
type Lock struct{ dir string }

// Dir is the lock's folder.
func (l *Lock) Dir() string { return l.dir }

// Release removes the pid file and the folder, as the scripts' unlock does.
func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	_ = os.Remove(filepath.Join(l.dir, "pid"))
	return os.Remove(l.dir)
}

// TryDir takes the lock folder dir with tries attempts, sleeping a second
// between them (the scripts' loops), and records the pid. It returns nil
// when the lock stays busy.
func TryDir(dir string, tries int, sleep func(time.Duration)) *Lock {
	for attempt := 1; ; attempt++ {
		if err := os.Mkdir(dir, 0o700); err == nil {
			break
		}
		if attempt >= tries {
			return nil
		}
		sleep(time.Second)
	}
	l := &Lock{dir: dir}
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		_ = l.Release()
		return nil
	}
	return l
}

// Lock takes one of the inbox's locks (ChannelLock or MutationLock) with
// LOCK_WAIT attempts, as try_lock and mutation_lock do; nil when busy.
func (r *Root) Lock(name string) *Lock {
	return TryDir(r.Path(name), r.LockWait, r.sleep)
}

// Stale reports whether a lock folder is older than after seconds at now,
// as the health check flags a held lock.
func Stale(dir string, now time.Time, after int) bool {
	st, err := os.Lstat(dir)
	return err == nil && st.IsDir() && int64(now.Sub(st.ModTime())/time.Second) > int64(after)
}
