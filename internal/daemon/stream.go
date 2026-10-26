package daemon

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// streamQueue bounds one stream's queue (9.3): a stream that falls 64
// events behind is closed and resumes from the journal by its cursor.
const streamQueue = 64

// frame is one SSE frame queued for a stream: a stored record's event, or
// a derived event (reset, snapshot, registry, route_removed, shutdown).
type frame struct {
	rec   *Record
	ev    *Event
	route string
	// last ends a party stream after this frame (route_removed).
	last bool
}

// subscriber is one open stream: a party's (party set, one route) or the
// view's (every route).
type subscriber struct {
	party string
	route string
	q     chan frame
	mu    sync.Mutex
	shut  bool
}

func newSubscriber(route, party string) *subscriber {
	return &subscriber{route: route, party: party, q: make(chan frame, streamQueue)}
}

// push queues a frame without blocking; a full queue closes the stream
// (false), so a slow client costs at most its fixed queue (9.3).
func (s *subscriber) push(f frame) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shut {
		return false
	}
	select {
	case s.q <- f:
		return true
	default:
		s.shut = true
		close(s.q)
		return false
	}
}

// close ends the stream once.
func (s *subscriber) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.shut {
		s.shut = true
		close(s.q)
	}
}

// wants reports whether a stored event goes to this stream (5.3 table).
func (s *subscriber) wants(rec *Record) bool {
	if rec.Event == nil {
		return false
	}
	if s.party == "" {
		return true
	}
	switch rec.Event.Type {
	case "message", "receipt", "notice":
		return rec.Event.To == s.party
	case "health", "pair", "question":
		return true
	}
	return false
}

var cursorRe = regexp.MustCompile(`^([0-9a-f]{32})-([0-9]{1,19})$`)

// parseCursor reads "<epoch>-<seq>".
func parseCursor(c string) (string, int64, bool) {
	m := cursorRe.FindStringSubmatch(c)
	if m == nil {
		return "", 0, false
	}
	n, err := strconv.ParseInt(m[2], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return m[1], n, true
}

// subscribe attaches s at visible_seq and returns the frames to send
// before the live queue (5.4 subscription barrier): the stored events
// after the cursor, or reset and a snapshot, or (no cursor) a snapshot.
func (r *Route) subscribe(s *subscriber, cursor string) ([]frame, *APIError) {
	var out []frame
	switch {
	case cursor == "":
		out = append(out, frame{ev: r.snapshot(s.party), route: r.name})
	default:
		epoch, seq, ok := parseCursor(cursor)
		if !ok {
			return nil, Errf("bad_request", "invalid cursor: <epoch>-<seq>")
		}
		reason := ""
		switch {
		case epoch != r.epoch:
			reason = "epoch_changed"
		case seq > r.visible || (r.oldest > 0 && seq < r.oldest-1):
			reason = "cursor_expired"
		}
		if reason != "" {
			out = append(out, frame{ev: r.resetEvent(reason), route: r.name}, frame{ev: r.snapshot(s.party), route: r.name})
			break
		}
		recs, err := r.replayRange(seq)
		if err != nil {
			return nil, Errf("internal", "journal read: %v", err)
		}
		for _, rec := range recs {
			if s.wants(rec) {
				out = append(out, frame{rec: rec, route: r.name})
			}
		}
	}
	r.subs[s] = struct{}{}
	return out, nil
}

// replayRange is the stored event records after seq up to visible_seq,
// from the ring when it covers the range, else from the journal files.
func (r *Route) replayRange(seq int64) ([]*Record, error) {
	if seq >= r.visible {
		return nil, nil
	}
	if len(r.ring) > 0 && r.ring[0].Seq <= seq+1 {
		var out []*Record
		for _, rec := range r.ring {
			if rec.Seq > seq && rec.Seq <= r.visible {
				out = append(out, rec)
			}
		}
		return out, nil
	}
	recs, err := r.j.eventRecords(seq, r.visible)
	if err != nil {
		return nil, err
	}
	var out []*Record
	for _, rec := range recs {
		if rec.Epoch == r.epoch {
			out = append(out, rec)
		}
	}
	return out, nil
}

// unsubscribe detaches a stream.
func (r *Route) unsubscribe(s *subscriber) { delete(r.subs, s) }

func (r *Route) resetEvent(reason string) *Event {
	last := r.visible
	return &Event{Type: "reset", Route: r.name, Epoch: r.epoch, Seq: last, Reason: reason, LastSeq: &last}
}

// snapshot is the route's status at visible_seq (never bodies), with the
// party's unread message ids for a party stream.
func (r *Route) snapshot(party string) *Event {
	last := r.visible
	st := r.status()
	ev := &Event{Type: "snapshot", Route: r.name, Epoch: r.epoch, Seq: last, LastSeq: &last, Snapshot: &st}
	if party != "" {
		ev.Unread = r.unread(party)
	}
	return ev
}

// unread is a party's unacked message ids in ready and cur.
func (r *Route) unread(party string) []string {
	out := []string{}
	for _, f := range r.root().Unacked(r.path(party)) {
		out = append(out, strings.TrimSuffix(filepath.Base(f), ".md"))
	}
	sort.Strings(out)
	return out
}

// StatusReport is a route's status (the status --json object, 5.5) as the
// daemon sees it: headers and counts only, never bodies or claim tokens.
type StatusReport struct {
	Route    string        `json:"route"`
	Inbox    string        `json:"inbox"`
	Epoch    string        `json:"epoch"`
	LastSeq  int64         `json:"last_seq"`
	Status   string        `json:"status"`
	Flags    []string      `json:"flags"`
	Registry RegistryState `json:"registry"`
	Peers    []PeerInfo    `json:"peers"`
	Boxes    []BoxCount    `json:"boxes"`
	Holds    []HoldInfo    `json:"holds"`
}

// RegistryState is the pair's state: empty, pending or approved, with the
// pending registry's SHA-256 while a change waits.
type RegistryState struct {
	State         string `json:"state"`
	PendingSHA256 string `json:"pending_sha256,omitempty"`
}

// BoxCount is one mailbox's counts.
type BoxCount struct {
	Name       string `json:"name"`
	New        int    `json:"new"`
	Ready      int    `json:"ready"`
	Cur        int    `json:"cur"`
	Unacked    int    `json:"unacked"`
	Quarantine int    `json:"quarantine"`
	Claim      bool   `json:"claim"`
}

// HoldInfo is one held target and its owners.
type HoldInfo struct {
	Target string   `json:"target"`
	Owners []string `json:"owners"`
}

// status reads the route's status from its files.
func (r *Route) status() StatusReport {
	st := StatusReport{Route: r.name, Inbox: r.inbox, Epoch: r.epoch, LastSeq: r.visible, Status: r.routeStatus(),
		Flags: append([]string{}, r.flags...), Peers: []PeerInfo{}, Boxes: []BoxCount{}, Holds: []HoldInfo{}}
	pb, pok := r.registryBytes("peers")
	ab, aok := r.registryBytes("peers.approved")
	switch {
	case pok && aok && string(pb) == string(ab) && string(pb) != "{}":
		st.Registry.State = "approved"
	case pok && string(pb) != "{}" || (pok && aok && string(pb) != string(ab)):
		st.Registry = RegistryState{State: "pending", PendingSHA256: sha256Hex(pb)}
	default:
		st.Registry.State = "empty"
	}
	if aok {
		st.Peers = peerInfos(ab)
	}
	rt := r.root()
	for _, b := range r.boxes() {
		box := r.path(b)
		c := BoxCount{Name: b, New: inbox.CountState(filepath.Join(box, "new")), Ready: inbox.CountState(filepath.Join(box, "ready")),
			Cur: inbox.CountState(filepath.Join(box, "cur")), Quarantine: inbox.CountState(filepath.Join(box, "quarantine")),
			Unacked: len(rt.Unacked(box)), Claim: rt.ClaimValid(box)}
		st.Boxes = append(st.Boxes, c)
	}
	if ents, err := os.ReadDir(r.path("state")); err == nil {
		for _, e := range ents {
			if t, ok := strings.CutPrefix(e.Name(), "hold-"); ok {
				h := HoldInfo{Target: t, Owners: []string{}}
				if o, ok := r.holdOwners(t); ok {
					for _, x := range o {
						h.Owners = append(h.Owners, x.K)
					}
				}
				sort.Strings(h.Owners)
				st.Holds = append(st.Holds, h)
			}
		}
	}
	return st
}

// writeFrame writes one SSE frame: id (stored events, and the cursor of a
// reset or snapshot), event and one data line.
func writeFrame(w io.Writer, route string, f frame) error {
	var ev Event
	if f.rec != nil {
		ev = *f.rec.Event
		ev.Route, ev.Seq, ev.Epoch, ev.Time = route, f.rec.Seq, f.rec.Epoch, f.rec.Time
	} else {
		ev = *f.ev
	}
	ev.V = Protocol
	data, err := encode(ev)
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(w, 512)
	if ev.Epoch != "" && (f.rec != nil || ev.Type == "snapshot" || ev.Type == "reset") {
		fmt.Fprintf(bw, "id: %s-%d\n", ev.Epoch, ev.Seq)
	}
	fmt.Fprintf(bw, "event: %s\ndata: %s\n\n", ev.Type, data)
	return bw.Flush()
}
