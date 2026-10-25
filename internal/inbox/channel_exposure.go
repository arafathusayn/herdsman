package inbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"time"
)

// FaultPending is a boundary check that found an exposure record waiting
// for the input lock (state/exposure-pending).
const FaultPending = "pending"

// The exposure publication rule (A26, e93bcc1 B3-001): only the owner of
// the shared input lock writes state/boundary-tainted, so a record never
// lands between another owner's boundary check and its commit. A check
// that cannot take the lock leaves the incident in state/exposure-pending;
// every boundary check fails closed while it waits, and the next owner of
// the lock publishes it (FoldExposure).

func (ch *Channel) pendingDir() string { return ch.Path("state", "exposure-pending") }

// ExposurePending reports whether an exposure record waits for the lock
// (an entry, or the folder itself as a link).
func (ch *Channel) ExposurePending() bool {
	d := ch.pendingDir()
	if st, err := os.Lstat(d); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return true
	}
	ents, err := os.ReadDir(d)
	return err == nil && len(ents) > 0
}

// FoldExposure publishes every waiting exposure; the caller owns the input
// lock. An unreadable entry still counts as exposure.
func (ch *Channel) FoldExposure() error {
	if !ch.ExposurePending() {
		return nil
	}
	d := ch.pendingDir()
	if st, err := os.Lstat(d); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return &Fatal{Msg: "exposure-pending symlink"}
	}
	ents, _ := os.ReadDir(d)
	agents := []Unknown{}
	seen := map[string]bool{}
	for _, en := range ents {
		p := filepath.Join(d, en.Name())
		if b, ok := ReadStrict(ch.Dir, p); ok {
			var rec struct {
				Agents []Unknown `json:"agents"`
			}
			if json.Unmarshal(b, &rec) == nil {
				for _, a := range rec.Agents {
					k, _ := json.Marshal(a)
					if !seen[string(k)] {
						seen[string(k)] = true
						agents = append(agents, a)
					}
				}
			}
		}
	}
	if !Exists(ch.Path("state", "boundary-tainted")) {
		if err := ch.writeTainted(agents); err != nil {
			return &Fatal{Msg: "cannot publish the waiting exposure record"}
		}
	}
	for _, en := range ents {
		_ = os.Remove(filepath.Join(d, en.Name()))
	}
	return nil
}

func (ch *Channel) writeTainted(unknown []Unknown) error {
	if unknown == nil {
		unknown = []Unknown{}
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(exposure{Reason: "live agent outside the protected setup", Agents: unknown, Time: ch.Unix()})
	return ch.Atomic(ch.Path("state", "boundary-tainted"), b.Bytes())
}

// LockMutation takes the shared input lock (tries attempts, sleep between)
// and, as its new owner, first publishes any exposure that waited for it.
// nil when the lock stays busy.
func (ch *Channel) LockMutation(tries int, sleep func(time.Duration)) (*Lock, error) {
	l := TryDir(ch.Path(MutationLock), max(tries, 1), sleep)
	if l == nil {
		return nil, nil
	}
	if err := ch.FoldExposure(); err != nil {
		return l, err
	}
	return l, nil
}

// RecordExposure is record_exposure after e93bcc1: an inventory fault is
// published under the input lock, or left waiting when another process
// owns it; a pending fault makes the caller take the lock (which
// publishes) when it does not hold it.
func (ch *Channel) RecordExposure(fault string, unknown []Unknown, mutationHeld bool) error {
	switch fault {
	case FaultInventory:
		if Exists(ch.Path("state", "boundary-tainted")) {
			return nil
		}
	case FaultPending:
		if !mutationHeld {
			l, err := ch.LockMutation(ch.LockWait, ch.Pause)
			if l != nil {
				_ = l.Release()
			}
			return err
		}
		return ch.FoldExposure()
	default:
		return nil
	}
	if !mutationHeld {
		l, err := ch.LockMutation(ch.LockWait, ch.Pause)
		if err != nil {
			if l != nil {
				_ = l.Release()
			}
			return err
		}
		if l == nil {
			return ch.keepPending(unknown)
		}
		defer l.Release()
	}
	if Exists(ch.Path("state", "boundary-tainted")) {
		return nil
	}
	return ch.writeTainted(unknown)
}

// keepPending writes one waiting exposure record.
func (ch *Channel) keepPending(unknown []Unknown) error {
	d := ch.pendingDir()
	if st, err := os.Lstat(d); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return &Fatal{Msg: "exposure-pending symlink"}
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return &Fatal{Msg: "cannot keep the exposure record"}
	}
	if unknown == nil {
		unknown = []Unknown{}
	}
	b, _ := json.Marshal(map[string]any{"agents": unknown, "time": ch.Unix()})
	name := fmt.Sprintf("%d-%d-%d.json", os.Getpid(), ch.Unix(), rand.IntN(1<<30))
	if err := ch.Atomic(filepath.Join(d, name), append(b, '\n')); err != nil {
		return &Fatal{Msg: "cannot keep the exposure record"}
	}
	return ch.LogLine("EXPOSURE input lock busy; the record waits for the next owner")
}
