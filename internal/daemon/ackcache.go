package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// fileStamp identifies one version of a file without reading it, as
// inbox's digest cache does: the same device and inode with the same
// size, modification time and change time has the same bytes. A writer
// can set the modification time back but not the change time, so a
// same-length rewrite in place always changes the stamp.
type fileStamp struct {
	dev, ino, size uint64
	mtime, ctime   int64
}

func stampOf(p string) (fileStamp, bool) {
	st, err := os.Lstat(p)
	if err != nil || !st.Mode().IsRegular() {
		return fileStamp{}, false
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return fileStamp{}, false
	}
	return fileStamp{dev: uint64(s.Dev), ino: uint64(s.Ino), size: uint64(st.Size()), mtime: st.ModTime().UnixNano(), ctime: changeTime(s)}, true
}

// ackEntry is a valid ack seen with these versions of the message in cur,
// its ack file and its claim record.
type ackEntry struct{ cur, ack, claim fileStamp }

// ackCache remembers acks found valid, gated on the files' stamps, so a
// send, read, release or snapshot does not read and decode every
// acknowledged message's files again. It saves the decode only: each
// operation still stamps and path-checks every acknowledged message (a few
// file status calls each), so in file mode its cost still grows with the
// number of acknowledged messages in the mailbox, until the daemon serves
// from the route database. A changed file, or a stamp that cannot be read,
// checks the ack again.
type ackCache struct {
	mu sync.Mutex
	m  map[string]ackEntry // "<box>/<id>"
}

func (c *ackCache) stamps(box, id string, r *inbox.Root) (ackEntry, bool) {
	cur, ok1 := stampOf(filepath.Join(box, "cur", id+".md"))
	ack, ok2 := stampOf(filepath.Join(box, "ack", id+".md.json"))
	claim, ok3 := stampOf(r.Path("ids", id, "claim"))
	return ackEntry{cur, ack, claim}, ok1 && ok2 && ok3
}

// ackCacheMax bounds the cache (about 100 bytes an entry).
const ackCacheMax = 4096

// valid is r.AckValid(box, id) with the cache in front.
func (c *ackCache) valid(r *inbox.Root, box, id string) bool {
	e, ok := c.stamps(box, id, r)
	key := filepath.Base(box) + "/" + id
	if ok {
		c.mu.Lock()
		hit := c.m[key] == e
		c.mu.Unlock()
		// A hit keeps AckValid's path guards: every folder from the
		// inbox down to each file is a real folder, not a link.
		if hit && inbox.GuardPath(r.Dir, filepath.Join(box, "cur", id+".md")) &&
			inbox.GuardPath(r.Dir, filepath.Join(box, "ack", id+".md.json")) &&
			inbox.GuardPath(r.Dir, r.Path("ids", id, "claim")) {
			return true
		}
	}
	if !r.AckValid(box, id) {
		return false
	}
	if ok {
		c.mu.Lock()
		if c.m == nil {
			c.m = map[string]ackEntry{}
		}
		if _, known := c.m[key]; !known && len(c.m) >= ackCacheMax {
			// Bounded: one entry goes for each new one. Go's map order
			// makes it a random one, so a scan just past the bound
			// still hits for almost every message.
			for k := range c.m {
				delete(c.m, k)
				break
			}
		}
		c.m[key] = e
		c.mu.Unlock()
	}
	return true
}

// unacked is inbox.Root.Unacked through the cache: the ready and cur
// messages of a mailbox without a valid ack.
func (r *Route) unacked(box string) []string {
	rt := r.root()
	var out []string
	for _, d := range []string{"ready", "cur"} {
		for _, f := range inbox.MdFiles(filepath.Join(box, d)) {
			if _, err := os.Stat(f); err != nil {
				continue
			}
			if !r.acks.valid(rt, box, strings.TrimSuffix(filepath.Base(f), ".md")) {
				out = append(out, f)
			}
		}
	}
	return out
}
