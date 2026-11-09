package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// streamQueue bounds one stream's queue (9.3): a stream that falls 64
// events behind is closed and resumes from the journal by its cursor.
const streamQueue = 64

// frame is one event queued for a party: a stored record's event, or
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
	// replay is the stored events between the stream's cursor and its
	// subscription barrier, when the ring does not cover them.
	replay *replay
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
		if seq >= r.visible {
			break
		}
		if len(r.ring) > 0 && r.ring[0].Seq <= seq+1 {
			for _, rec := range r.ring {
				if rec.Seq > seq && rec.Seq <= r.visible && s.wants(rec) {
					out = append(out, frame{rec: rec, route: r.name})
				}
			}
			break
		}
		p, err := r.openReplay(seq)
		if err != nil {
			return nil, Errf("internal", "journal read: %v", err)
		}
		s.replay = p
	}
	r.subs[s] = struct{}{}
	return out, nil
}

// replay reads a stream's stored events after its cursor up to its
// subscription barrier from the journal files, one record at a time, so a
// stream never holds more than its queue (9.3). The files are opened on
// the actor at the barrier; an open file survives a rotation, so the range
// stays whole while the route goes on.
type replay struct {
	files       []*os.File
	sc          *bufio.Scanner
	after, upto int64
	epoch       string
}

// openReplay opens the journal files for the records after seq up to
// visible_seq; it runs on the actor.
func (r *Route) openReplay(seq int64) (*replay, error) {
	if r.j == nil {
		return nil, errors.New("no journal")
	}
	p := &replay{after: seq, upto: r.visible, epoch: r.epoch}
	for _, name := range []string{journalPrev, journalCurrent} {
		f, err := os.Open(filepath.Join(r.j.dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			p.close()
			return nil, err
		}
		p.files = append(p.files, f)
	}
	return p, nil
}

// next is the next stored event record of the range, or nil at its end.
func (p *replay) next() (*Record, error) {
	for len(p.files) > 0 {
		if p.sc == nil {
			p.sc = bufio.NewScanner(p.files[0])
			p.sc.Buffer(make([]byte, 0, 4096), maxRecord+1)
		}
		if !p.sc.Scan() {
			if err := p.sc.Err(); err != nil {
				return nil, err
			}
			_ = p.files[0].Close()
			p.files, p.sc = p.files[1:], nil
			continue
		}
		var rec Record
		if json.Unmarshal(p.sc.Bytes(), &rec) != nil {
			continue
		}
		if rec.Seq > p.upto {
			p.close()
			break
		}
		if rec.Event != nil && rec.Seq > p.after && rec.Epoch == p.epoch {
			return &rec, nil
		}
	}
	return nil, nil
}

// close frees the files still open; a nil replay closes nothing.
func (p *replay) close() {
	if p == nil {
		return
	}
	for _, f := range p.files {
		_ = f.Close()
	}
	p.files, p.sc = nil, nil
}

// unsubscribe detaches a stream.
func (r *Route) unsubscribe(s *subscriber) { delete(r.subs, s) }

// always runs fn on the actor outside the operation rules (a degraded or
// removed route, a full queue) and waits for it; a stopped route runs
// nothing, and its stop has already dropped every stream. A function that has
// started is waited for even as the route stops, because the caller reads what
// it wrote.
func (r *Route) always(fn func()) {
	done := make(chan struct{})
	var state atomic.Int32 // 0 queued, 1 started, 2 abandoned
	op := func() {
		if !state.CompareAndSwap(0, 1) {
			return
		}
		fn()
		close(done)
	}
	select {
	case r.ops <- op:
	case <-r.quit:
		return
	}
	select {
	case <-done:
	case <-r.quit:
		if !state.CompareAndSwap(0, 2) {
			<-done
		}
	}
}

// detach forgets a closed stream, whatever the route's state, so no
// stream outlives its connection on any route (9.3).
func (r *Route) detach(s *subscriber) { r.always(func() { r.unsubscribe(s) }) }

func (r *Route) resetEvent(reason string) *Event {
	last := r.visible
	return &Event{Type: "reset", Time: r.now().Unix(), Route: r.name, Epoch: r.epoch, Seq: last, Reason: reason, LastSeq: &last}
}

// snapshot is the route's status at visible_seq (never bodies), with the
// party's unread message ids for a party stream.
func (r *Route) snapshot(party string) *Event {
	last := r.visible
	st := r.status()
	ev := &Event{Type: "snapshot", Time: r.now().Unix(), Route: r.name, Epoch: r.epoch, Seq: last, LastSeq: &last, Snapshot: &st}
	if party != "" {
		ev.Unread = r.unread(party)
	}
	return ev
}

// unread is a party's unacked message ids in ready and cur.
func (r *Route) unread(party string) []string {
	out := []string{}
	for _, f := range r.unacked(r.path(party)) {
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

// RouteStatus is a route's Status call and message status --json
// (5.5): the daemon object, the route's status, every message's headers
// (never a body) and the pending alerts' payloads.
type RouteStatus struct {
	Daemon DaemonState `json:"daemon"`
	StatusReport
	Messages []MessageHeader   `json:"messages"`
	Alerts   []json.RawMessage `json:"alerts"`
}

// DaemonState is the daemon as a status reader sees it: state "up" or
// "down", its version, and the age of its heartbeat in seconds.
type DaemonState struct {
	State        string `json:"state"`
	Version      string `json:"version,omitempty"`
	HeartbeatAge *int64 `json:"heartbeat_age,omitempty"`
}

// MessageHeader is one queued message's envelope headers, its mailbox and
// state folder, and whether it has a valid ack.
type MessageHeader struct {
	Box          string `json:"box"`
	ID           string `json:"id"`
	From         string `json:"from"`
	To           string `json:"to"`
	Kind         string `json:"kind"`
	ReplyTo      string `json:"reply_to"`
	State        string `json:"state"`
	Acknowledged bool   `json:"acknowledged"`
}

// statusDetail is the message headers and pending alerts of an inbox,
// read the guarded way; a file that does not read as its kind is left
// out. acked reports a message's ack.
func statusDetail(rt *inbox.Root, boxes []string, acked func(box, id string) bool) ([]MessageHeader, []json.RawMessage) {
	msgs, alerts := []MessageHeader{}, []json.RawMessage{}
	for _, b := range boxes {
		box := filepath.Join(rt.Dir, b)
		for _, state := range []string{"new", "ready", "cur", "quarantine"} {
			for _, f := range inbox.MdFiles(filepath.Join(box, state)) {
				m, ok := inbox.Envelope(rt.Dir, f)
				if !ok {
					continue
				}
				str := func(k string) string { v, _ := m[k].(string); return v }
				id := strings.TrimSuffix(filepath.Base(f), ".md")
				msgs = append(msgs, MessageHeader{Box: b, ID: str("id"), From: str("from"), To: str("to"), Kind: str("kind"),
					ReplyTo: str("reply_to"), State: state, Acknowledged: acked(box, id)})
			}
		}
	}
	files, _ := filepath.Glob(filepath.Join(rt.Dir, "alerts", "*.pending"))
	for _, f := range files {
		if !inbox.Regular(rt.Dir, f) {
			continue
		}
		if b, err := inbox.ReadBounded(f, inbox.MaxSize); err == nil && inbox.StrictJSON(b) == nil {
			alerts = append(alerts, json.RawMessage(b))
		}
	}
	return msgs, alerts
}

// heartbeatAge is the age of a private home's daemon heartbeat in whole
// seconds (nil when it cannot be read).
func heartbeatAge(privateHome string, now time.Time) *int64 {
	st, err := os.Stat(filepath.Join(privateHome, "daemon", "heartbeat"))
	if err != nil {
		return nil
	}
	age := max(int64(now.Sub(st.ModTime())/time.Second), 0)
	return &age
}

// fullStatus is the route's RouteStatus as the daemon serves it.
func (r *Route) fullStatus() RouteStatus {
	st := RouteStatus{Daemon: DaemonState{State: "up", Version: r.d.cfg.Version, HeartbeatAge: heartbeatAge(r.d.cfg.PrivateHome, r.now())},
		StatusReport: r.status()}
	rt := r.root()
	st.Messages, st.Alerts = statusDetail(rt, r.boxes(), func(box, id string) bool { return r.acks.valid(rt, box, id) })
	return st
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
	// Orphan is a mailbox of no approved peer.
	Orphan bool `json:"orphan"`
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
			Unacked: len(r.unacked(box)), Claim: rt.ClaimValid(box), Orphan: !hasPeer(st.Peers, b)}
		st.Boxes = append(st.Boxes, c)
	}
	st.Holds = append(st.Holds, holdInfos(r.inbox)...)
	return st
}

// holdInfos lists the saved state/hold-* records of an inbox with their
// owners; a record not in the owner format is listed with no owners.
func holdInfos(dir string) []HoldInfo {
	var out []HoldInfo
	ents, err := os.ReadDir(filepath.Join(dir, "state"))
	if err != nil {
		return out
	}
	for _, e := range ents {
		if t, ok := strings.CutPrefix(e.Name(), "hold-"); ok {
			h := HoldInfo{Target: t, Owners: []string{}}
			if o, ok := readHoldOwners(dir, t); ok {
				for _, x := range o {
					h.Owners = append(h.Owners, x.K)
				}
			}
			sort.Strings(h.Owners)
			out = append(out, h)
		}
	}
	return out
}

// eventFrame is one queued event as a Wait reply carries it: the cursor of a
// stored event, a reset or a snapshot, its type and its JSON.
func eventFrame(route string, f frame) *daemonv1.EventFrame {
	var ev Event
	if f.rec != nil {
		ev = *f.rec.Event
		ev.Route, ev.Seq, ev.Epoch, ev.Time = route, f.rec.Seq, f.rec.Epoch, f.rec.Time
	} else {
		ev = *f.ev
	}
	ev.V = Protocol
	data, _ := encode(ev)
	out := &daemonv1.EventFrame{Type: ev.Type, Payload: string(data)}
	if ev.Epoch != "" && (f.rec != nil || ev.Type == "snapshot" || ev.Type == "reset") {
		out.Cursor = ev.Epoch + "-" + strconv.FormatInt(ev.Seq, 10)
	}
	return out
}

// hasPeer reports a name among the approved peers.
func hasPeer(peers []PeerInfo, name string) bool {
	for _, p := range peers {
		if p.Name == name {
			return true
		}
	}
	return false
}
