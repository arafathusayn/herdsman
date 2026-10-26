package message

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Unresolved allocations (A26 D12, A24 review 3): send marks each new id in
// state/allocations before it allocates it; the courier visits only these
// marks, so its idle work follows the messages in flight, not the
// permanent ids/ history. A mark goes when its allocation has a terminal
// record. Inboxes from before the marks get one full listing, recorded by
// state/allocations-seeded.

// abandonAfter is how old an unqueued allocation must be to be retired.
const abandonAfter = 3600

func (e *Env) allocationMark(id string) string { return e.path("state", "allocations", id) }

// markAllocation records id as unresolved.
func (e *Env) markAllocation(id string) error {
	d := e.path("state", "allocations")
	if st, err := os.Lstat(d); err == nil && !st.IsDir() {
		return die("state/allocations is not a folder; inspect it")
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return die("cannot track the allocation")
	}
	if err := e.ch.SafeTouch(e.allocationMark(id)); err != nil {
		return die("cannot track the allocation")
	}
	return nil
}

// seedAllocations marks, once, every allocation of an inbox from before the
// marks that has no terminal record.
func (c *courier) seedAllocations() error {
	e := c.Env
	seeded := e.path("state", "allocations-seeded")
	if inbox.Exists(seeded) {
		return nil
	}
	ids, err := glob(e.path("ids"), "m*")
	if err != nil {
		return err
	}
	for _, d := range ids {
		id := filepath.Base(d)
		// An existing mark keeps its age.
		if inbox.IDOK(id) && !inbox.Exists(filepath.Join(d, "terminal.json")) && !inbox.Exists(e.allocationMark(id)) {
			if err := e.markAllocation(id); err != nil {
				return err
			}
		}
	}
	return e.ch.SafeTouch(seeded)
}

// retireAbandoned finds allocations that a send cut off between the
// allocation copy and the queue move (a crash, or a failed retire): an
// envelope with no terminal record and no copy in its recipient's queue
// folders. Under the queue lock no send is between those steps, so each is
// retired as abandoned (A26, D12). A busy lock waits for the next sweep.
func (c *courier) retireAbandoned() error {
	e := c.Env
	l := e.tryChannel()
	if l == nil {
		return nil
	}
	defer e.release(l) // only the queue lock: courier.lock stays held
	if err := c.seedAllocations(); err != nil {
		return err
	}
	marks, err := os.ReadDir(e.path("state", "allocations"))
	if err != nil {
		return nil // none tracked yet
	}
	for _, m := range marks {
		id := m.Name()
		mark := e.allocationMark(id)
		d := e.path("ids", id)
		env := filepath.Join(d, "envelope")
		switch {
		case !inbox.IDOK(id):
			continue // not ours: left for a human
		case inbox.Exists(filepath.Join(d, "terminal.json")):
			_ = os.Remove(mark)
			continue
		}
		st, serr := os.Lstat(env)
		if errors.Is(serr, fs.ErrNotExist) {
			// Marked but never allocated (a cut before the mkdir or the
			// copy): the mark goes once it is old. Only an explicit absence
			// under real folders counts: a folder that cannot be searched, a
			// link or a dangling parent keeps its mark (review 4, 5).
			if !absentUnderRealFolders(e.path("ids"), d) {
				continue
			}
			if mt, err := os.Lstat(mark); err == nil && e.unix()-mt.ModTime().Unix() >= abandonAfter {
				_ = os.Remove(mark)
			}
			continue
		}
		if serr != nil || !st.Mode().IsRegular() || e.unix()-st.ModTime().Unix() < abandonAfter {
			continue // unclear, or young: a human may have moved the entry aside for a moment
		}
		// Only a valid envelope for this id is abandoned, and only while the
		// id (unique across the channel) has no entry in any approved
		// mailbox's queue folders nor its named recipient's; anything
		// unclear stays for a human (a live queue entry is never retired).
		m, ok := inbox.AllocationEnvelope(e.ch.Dir, env, id)
		to, _ := m["to"].(string)
		if !ok || !inbox.NameOK(to) || queuedInChannel(e, to, id) {
			continue
		}
		if err := e.ch.Atomic(filepath.Join(d, "terminal.json"), pretty(obj("id", id, "state", "abandoned"))); err != nil {
			return writeFailed(err)
		}
		_ = os.Remove(mark)
		if err := e.ch.LogLine("ABANDONED " + id + " (allocated, never queued)"); err != nil {
			return err
		}
	}
	return nil
}

// absentUnderRealFolders reports whether the allocation folder d is
// explicitly absent, or a real folder, directly under ids, itself a real
// folder (no link, no dangling parent).
func absentUnderRealFolders(ids, d string) bool {
	if st, err := os.Lstat(ids); err != nil || !st.IsDir() {
		return false
	}
	st, err := os.Lstat(d)
	return errors.Is(err, fs.ErrNotExist) || (err == nil && st.IsDir())
}

// queuedInChannel reports whether id has an entry in the queue folders of
// to or of any approved peer; a registry that cannot be read counts as
// holding it.
func queuedInChannel(e *Env, to, id string) bool {
	peers, ok := e.ch.Registry()
	if !ok {
		return true
	}
	if queuedAnywhere(e, to, id) {
		return true
	}
	for _, p := range peers {
		if queuedAnywhere(e, p.Name, id) {
			return true
		}
	}
	return false
}

// queuedAnywhere reports whether id has an entry in one of to's queue
// folders; a folder that cannot be checked counts as holding it.
// Absence counts only under real folders: a mailbox or queue folder that
// is missing, a link (dangling or not) or another file type is unclear and
// counts as holding the entry (review 6).
func queuedAnywhere(e *Env, to, id string) bool {
	if !realDir(e.path(to)) {
		return true
	}
	for _, s := range []string{"new", "ready", "cur", "quarantine"} {
		q := e.path(to, s)
		if !realDir(q) {
			return true
		}
		if _, err := os.Lstat(filepath.Join(q, id+".md")); !errors.Is(err, fs.ErrNotExist) {
			return true
		}
	}
	return false
}

// realDir reports whether p is a folder itself, not a link to one.
func realDir(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.IsDir()
}
