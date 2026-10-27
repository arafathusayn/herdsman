// Package routedb reads a route database the way the out-of-process
// readers do (design sqlite-storage.md section 2): `message status` while
// the daemon is down, and the web reader. It opens the store's read-only
// reader, reads in one read transaction and closes; it never creates,
// writes or migrates a file.
package routedb

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Contract is the owner.json contract of a daemon-owned inbox whose data
// lives in its route database (design section 2).
const Contract = 3

// Path is a route's database under a private home (design section 2).
func Path(privateHome, route string) string {
	return filepath.Join(privateHome, "daemon", "routes", route+".db")
}

// StorePath is the route database named by a daemon-owned inbox of
// contract 3: state/owner.json's "store", an absolute
// <private home>/daemon/routes/<route>.db. ok is false for any other
// inbox; a contract 2 inbox keeps its files.
func StorePath(dir string) (string, bool) {
	b, ok := inbox.ReadStrict(dir, filepath.Join(dir, "state", "owner.json"))
	if !ok {
		return "", false
	}
	v, _ := inbox.Decode(b)
	o := inbox.Object(v)
	n, _ := o["contract"].(json.Number)
	p, _ := inbox.String(o, "store")
	if o["owner"] != "daemon" || n.String() != "3" || !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return "", false
	}
	name, ok := strings.CutSuffix(filepath.Base(p), ".db")
	if !ok || !inbox.NameOK(name) || filepath.Base(filepath.Dir(p)) != "routes" || filepath.Base(filepath.Dir(filepath.Dir(p))) != "daemon" {
		return "", false
	}
	return p, true
}

// Status is what the readers show of a route: its epoch and last
// committed seq, the registry, the mailboxes' counts and the holds. No
// bodies and no claim tokens.
type Status struct {
	Epoch         string
	LastSeq       int64
	Approved      bool   // exactly one approved adjutant and orchestrator, none pending
	PendingSHA256 string // of the pending rows, when there are any
	Peers         []Peer // the approved parties, by role
	Boxes         []Box  // the approved parties' and every recipient's mailbox, by name
	Holds         []Hold // by target, then owner
}

// Peer is one approved party.
type Peer struct{ Name, Role, Pane, Session string }

// Box is one mailbox's message counts. Unacked counts the claimed (cur)
// messages: an ack moves a message to handled.
type Box struct {
	Name                                 string
	New, Ready, Cur, Unacked, Quarantine int
	Claim                                bool // an active claim
	Orphan                               bool // no approved party of that name
}

// Hold is one owner's hold of a target.
type Hold struct {
	Target, Owner, Reason string
	Time                  int64
}

// Read reads path's status through the store's reader. A missing file is
// an error wrapping os.ErrNotExist; a schema this herdsman does not read
// wraps store.ErrSchemaNewer, ErrSchemaOlder or ErrSchemaCorrupt.
func Read(path string) (Status, error) {
	c, err := store.Open(path, store.Reader, store.KindRoute)
	if err != nil {
		return Status{}, err
	}
	defer c.Close()
	var st Status
	err = c.Tx(func(tx store.Conn) error {
		var err error
		st, err = read(tx)
		return err
	})
	return st, err
}

func read(c store.Conn) (Status, error) {
	st := Status{Peers: []Peer{}, Boxes: []Box{}, Holds: []Hold{}}
	if err := c.Query("SELECT v FROM meta WHERE k = 'epoch'", nil, func(r store.Row) error { st.Epoch = r.Text(0); return nil }); err != nil {
		return st, err
	}
	seq, err := store.VisibleSeq(c)
	if err != nil {
		return st, err
	}
	st.LastSeq = seq
	approved := map[string]bool{}
	roles := map[string]int{}
	pending := 0
	if err := c.Query("SELECT name, role, pane, session, state FROM parties WHERE state IN ('approved', 'pending') ORDER BY role, name", nil, func(r store.Row) error {
		if r.Text(4) == "pending" {
			pending++
			return nil
		}
		st.Peers = append(st.Peers, Peer{Name: r.Text(0), Role: r.Text(1), Pane: r.Text(2), Session: r.Text(3)})
		approved[r.Text(0)] = true
		roles[r.Text(1)]++
		return nil
	}); err != nil {
		return st, err
	}
	st.Approved = pending == 0 && roles["adjutant"] == 1 && roles["orchestrator"] == 1
	if pending > 0 {
		if st.PendingSHA256, err = store.PendingSHA256(c); err != nil {
			return st, err
		}
	}
	boxes := map[string]*Box{}
	box := func(name string) *Box {
		if b := boxes[name]; b != nil {
			return b
		}
		b := &Box{Name: name, Orphan: !approved[name]}
		boxes[name] = b
		return b
	}
	for name := range approved {
		box(name)
	}
	if err := c.Query("SELECT recipient, state, count(*) FROM messages WHERE state IN ('new', 'ready', 'cur', 'quarantined') GROUP BY recipient, state", nil, func(r store.Row) error {
		b, n := box(r.Text(0)), int(r.Int64(2))
		switch r.Text(1) {
		case "new":
			b.New = n
		case "ready":
			b.Ready = n
		case "cur":
			b.Cur, b.Unacked = n, n
		case "quarantined":
			b.Quarantine = n
		}
		return nil
	}); err != nil {
		return st, err
	}
	if err := c.Query("SELECT box FROM claims WHERE state = 'active'", nil, func(r store.Row) error { box(r.Text(0)).Claim = true; return nil }); err != nil {
		return st, err
	}
	for _, b := range boxes {
		st.Boxes = append(st.Boxes, *b)
	}
	sort.Slice(st.Boxes, func(i, j int) bool { return st.Boxes[i].Name < st.Boxes[j].Name })
	err = c.Query("SELECT target, owner, reason, time FROM holds ORDER BY target, owner", nil, func(r store.Row) error {
		st.Holds = append(st.Holds, Hold{Target: r.Text(0), Owner: r.Text(1), Reason: r.Text(2), Time: r.Int64(3)})
		return nil
	})
	return st, err
}
