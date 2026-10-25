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
		if inbox.IDOK(id) && !inbox.Exists(filepath.Join(d, "terminal.json")) {
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
			// counts: a folder that cannot be searched keeps its mark.
			if mt, err := os.Lstat(mark); err == nil && e.unix()-mt.ModTime().Unix() >= abandonAfter {
				_ = os.Remove(mark)
			}
			continue
		}
		if serr != nil || !st.Mode().IsRegular() || e.unix()-st.ModTime().Unix() < abandonAfter {
			continue // unclear, or young: a human may have moved the entry aside for a moment
		}
		// Only a valid envelope for this id, whose recipient has no entry in
		// any of its queue folders, is abandoned; anything unclear stays
		// for a human (a live queue entry is never retired).
		m, ok := inbox.AllocationEnvelope(e.ch.Dir, env, id)
		to, _ := m["to"].(string)
		if !ok || !inbox.NameOK(to) || queuedAnywhere(e, to, id) {
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

// queuedAnywhere reports whether id has an entry in one of to's queue
// folders; a folder that cannot be checked counts as holding it.
func queuedAnywhere(e *Env, to, id string) bool {
	for _, s := range []string{"new", "ready", "cur", "quarantine"} {
		if _, err := os.Lstat(e.path(to, s, id+".md")); !errors.Is(err, fs.ErrNotExist) {
			return true
		}
	}
	return false
}
