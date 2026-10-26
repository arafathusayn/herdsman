package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// fileStamp identifies one version of a file without reading it.
type fileStamp struct {
	ino, size uint64
	mtime     int64
}

func stampOf(p string) (fileStamp, bool) {
	st, err := os.Lstat(p)
	if err != nil || !st.Mode().IsRegular() {
		return fileStamp{}, false
	}
	return fileStamp{ino: inode(st), size: uint64(st.Size()), mtime: st.ModTime().UnixNano()}, true
}

// ackEntry is a valid ack seen with these versions of the message in cur,
// its ack file and its claim record.
type ackEntry struct{ cur, ack, claim fileStamp }

// ackCache remembers acks found valid, gated on the files' stamps, so a
// read, release or snapshot does not decode every acknowledged message
// again (A24: the cost of an operation must not grow with the history).
// A changed file, or a stamp that cannot be read, checks the ack again.
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
		if c.m == nil || len(c.m) >= ackCacheMax {
			c.m = map[string]ackEntry{} // bounded: start over when full
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
