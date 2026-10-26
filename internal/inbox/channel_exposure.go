package inbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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

// ExposurePending reports whether an exposure record may wait for the lock:
// any pending state but none (pendingState), so every check fails closed.
func (ch *Channel) ExposurePending() bool {
	s, _ := ch.pendingState()
	return s != pendingNone
}

// The waiting-exposure path's state, as the shell's pending_state (160c070).
const (
	pendingNone          = iota // absent, or a real folder this process can read and search with no entry
	pendingEntries              // such a folder with any entry
	pendingUninspectable        // anything else: a link, another file type, missing read or search access, a failed listing
)

// pendingState classifies the pending path once for every caller
// (ExposurePending, FoldExposure and through them the gate) and returns
// the entries it listed.
func (ch *Channel) pendingState() (int, []os.DirEntry) {
	d := ch.pendingDir()
	st, err := os.Lstat(d)
	if errors.Is(err, fs.ErrNotExist) {
		return pendingNone, nil
	}
	if err != nil || !st.IsDir() {
		return pendingUninspectable, nil
	}
	ents, err := os.ReadDir(d)
	if err != nil || !searchable(d) {
		return pendingUninspectable, nil
	}
	if len(ents) > 0 {
		return pendingEntries, ents
	}
	return pendingNone, nil
}

// searchable reports whether this process may look up names in folder d
// (search access for its effective user): a lookup of a child name either
// finds it or reports its absence; a refused lookup means no search access.
func searchable(d string) bool {
	_, err := os.Lstat(filepath.Join(d, ".herdsman-search-probe"))
	return err == nil || errors.Is(err, fs.ErrNotExist)
}

// FoldExposure publishes every waiting exposure; the caller owns the input
// lock. An unreadable entry still counts as exposure. A pending path that
// cannot be inspected (a link, another file type, a folder that cannot be
// read or searched, a failed listing) is published as a generic exposure
// without agents and kept as it is, never followed: nothing that was not
// read is removed, so the incident survives a retry after the path turns
// clean (round 6, shell 160c070 fold_exposure).
func (ch *Channel) FoldExposure() error {
	state, ents := ch.pendingState()
	switch state {
	case pendingNone:
		return nil
	case pendingUninspectable:
		_ = ch.LogLine("EXPOSURE pending path cannot be inspected; recorded and kept for the human")
		return ch.publishWaiting(nil, 0)
	}
	d := ch.pendingDir()
	agents := []Unknown{}
	omitted := 0
	seen := map[string]bool{}
	for _, en := range ents {
		p := filepath.Join(d, en.Name())
		if b, ok := ReadStrict(ch.Dir, p); ok {
			var rec struct {
				Agents  []Unknown `json:"agents"`
				Omitted int       `json:"omitted"`
			}
			if json.Unmarshal(b, &rec) == nil {
				omitted += max(rec.Omitted, 0)
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
	if err := ch.publishWaiting(agents, omitted); err != nil {
		return err
	}
	for _, en := range ents {
		_ = os.Remove(filepath.Join(d, en.Name()))
	}
	return nil
}

// publishWaiting writes boundary-tainted unless one is already published.
func (ch *Channel) publishWaiting(agents []Unknown, omitted int) error {
	if Exists(ch.Path("state", "boundary-tainted")) {
		return nil
	}
	if err := ch.writeTaintedOmitted(agents, omitted); err != nil {
		return &Fatal{Msg: "cannot publish the waiting exposure record"}
	}
	return nil
}

func (ch *Channel) writeTainted(unknown []Unknown) error { return ch.writeTaintedOmitted(unknown, 0) }

func (ch *Channel) writeTaintedOmitted(unknown []Unknown, omitted int) error {
	if unknown == nil {
		unknown = []Unknown{}
	}
	return ch.Atomic(ch.Path("state", "boundary-tainted"), taintedRecord(unknown, omitted, ch.Unix()))
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
	name := fmt.Sprintf("%d-%d-%d.json", os.Getpid(), ch.Unix(), rand.IntN(1<<30))
	if err := ch.Atomic(filepath.Join(d, name), pendingRecord(unknown, ch.Unix())); err != nil {
		return &Fatal{Msg: "cannot keep the exposure record"}
	}
	return ch.LogLine("EXPOSURE input lock busy; the record waits for the next owner")
}
