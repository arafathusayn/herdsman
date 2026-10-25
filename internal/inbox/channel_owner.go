package inbox

import "time"

// AsOwner runs fn with whether this run owns the shared input lock, the
// answer held fixed for fn's duration (Channel.Owner). fn must not take or
// free a lock.
func (ch *Channel) AsOwner(fn func(held bool) error) error {
	if ch.Owner == nil {
		return fn(false)
	}
	return ch.Owner(ch.Path(MutationLock), fn)
}

// KeepPending leaves one exposure record waiting for the next owner of the
// input lock (the caller could not take it).
func (ch *Channel) KeepPending(unknown []Unknown) error { return ch.keepPending(unknown) }

// TryOnce takes the lock folder dir with one attempt; nil when busy. A
// caller that must keep its lock list exact retries itself, between
// attempts, with nothing held half-way.
func TryOnce(dir string) *Lock { return TryDir(dir, 1, func(time.Duration) {}) }
