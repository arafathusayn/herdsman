package daemon

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Journal bounds (design 4.2, 4.3, 9.1).
const (
	maxRecord      = 2048 // one journal line, newline included
	journalCurrent = "current.ndjson"
	journalPrev    = "previous.ndjson"
)

// journalRotate is the size past which a journal file rotates: 4 MiB
// (tests lower it).
var journalRotate int64 = 4 << 20

// Payload names a payload file a record refers to: its path under the
// inbox, its SHA-256 and its size (4.2).
type Payload struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// Record is one journal line (4.2): the commit record of one operation.
// It never holds a body or a list that grows with the backlog; anything
// larger than fixed fields is a payload file.
type Record struct {
	Seq   int64  `json:"seq"`
	Epoch string `json:"epoch"`
	Time  int64  `json:"time"`
	// Op is send, claim, claim_extend, ack, release, pair, health or
	// checkpoint (the Phase A operations of 4.2).
	Op string `json:"op"`
	// Operation fields; each op uses some of them.
	ID               string `json:"id,omitempty"`
	Key              string `json:"key,omitempty"`
	From             string `json:"from,omitempty"`
	To               string `json:"to,omitempty"`
	Kind             string `json:"kind,omitempty"`
	ReplyTo          string `json:"reply_to,omitempty"`
	BodySHA256       string `json:"body_sha256,omitempty"`
	Box              string `json:"box,omitempty"`
	Token            string `json:"token,omitempty"`
	ReadKey          string `json:"read_key,omitempty"`
	ExtendKey        string `json:"extend_key,omitempty"`
	N                int    `json:"n,omitempty"`
	Empty            bool   `json:"empty,omitempty"`
	State            string `json:"state,omitempty"`
	Name             string `json:"name,omitempty"`
	Interaction      string `json:"interaction,omitempty"`
	CompletedThrough int64  `json:"completed_through,omitempty"`
	// Credentials are an approval's credential records (option and
	// secret hash per party; never a secret).
	Credentials map[string]credential `json:"credentials,omitempty"`
	// Generation names an approval's staged credential generation.
	Generation string `json:"generation,omitempty"`
	// Revoked are the parties an approval revoked.
	Revoked []string `json:"revoked,omitempty"`
	// Payload is the payload file of step 2, or null.
	Payload *Payload `json:"payload"`
	// Event is the event this record publishes (5.3), or null.
	Event *Event `json:"event"`
}

// PeerInfo is one party in a pair event: its name, role, pane and the
// first 8 characters of its session id.
type PeerInfo struct {
	Name    string `json:"name"`
	Role    string `json:"role"`
	Pane    string `json:"pane"`
	Session string `json:"session"`
}

// Event is one SSE event's data (5.3). Stored events keep only their type
// fields in the journal; the stream adds v, route, seq, epoch and time.
// Field names are lower-case snake_case; fields a type does not use are
// left out.
type Event struct {
	V     int    `json:"v,omitzero"`
	Route string `json:"route,omitzero"`
	Seq   int64  `json:"seq,omitzero"`
	Epoch string `json:"epoch,omitzero"`
	Time  int64  `json:"time,omitzero"`
	// Type is message, receipt, notice, health, pair (stored), or reset,
	// snapshot, registry, route_removed, shutdown, queue (derived).
	Type string `json:"type"`
	// message: id, from, to, kind, reply_to. receipt: id, by, to (the
	// sender the receipt is for), state. notice: id, to, state.
	ID      string  `json:"id,omitzero"`
	From    string  `json:"from,omitzero"`
	To      string  `json:"to,omitzero"`
	Kind    string  `json:"kind,omitzero"`
	ReplyTo *string `json:"reply_to,omitzero"`
	By      string  `json:"by,omitzero"`
	State   string  `json:"state,omitzero"`
	// health: status, flags, changed.
	Status  string   `json:"status,omitzero"`
	Flags   []string `json:"flags,omitzero"`
	Changed []string `json:"changed,omitzero"`
	// pair: pending_checksum, peers.
	PendingChecksum string     `json:"pending_checksum,omitzero"`
	Peers           []PeerInfo `json:"peers,omitzero"`
	// reset, shutdown: reason; reset, snapshot: last_seq.
	Reason  string `json:"reason,omitzero"`
	LastSeq *int64 `json:"last_seq,omitzero"`
	// registry: the complete route membership.
	Routes []RouteRef `json:"routes,omitzero"`
	// snapshot: the route's status (no bodies) and, for a party stream,
	// the party's unacked message ids.
	Snapshot *StatusReport `json:"snapshot,omitzero"`
	Unread   []string      `json:"unread,omitzero"`
	// queue: per party counts.
	Queue []BoxCount `json:"queue,omitzero"`
}

// RouteRef is one route of the registry frame.
type RouteRef struct {
	Route string `json:"route"`
	Epoch string `json:"epoch"`
}

// encodeRecord is one journal line, newline included; a line over 2 KiB is
// a bug and refused (9.1).
func encodeRecord(r *Record) ([]byte, error) {
	b, err := encode(r)
	if err != nil {
		return nil, err
	}
	b = append(b, '\n')
	if len(b) > maxRecord {
		return nil, fmt.Errorf("journal record of %d bytes over the %d byte bound", len(b), maxRecord)
	}
	return b, nil
}

// journal is a route's event journal: state/events/current.ndjson and
// previous.ndjson, append-only, each record fsynced before its operation's
// visible files change.
type journal struct {
	dir  string
	f    *os.File
	size int64
	// broken is a failed append: the file's tail is unknown.
	broken bool
}

// errHeld is a journal the daemon will not repair by itself: an invalid
// line other than a torn last line holds the route for inspection (4.4).
var errHeld = errors.New("journal held for inspection")

// loaded is what a journal read gives the route at start.
type loaded struct {
	// epoch every record must carry.
	epoch string
	// records after the last checkpoint's completed_through, in order.
	pending []*Record
	// last is the last record (nil for an empty journal).
	last *Record
	// checkpoint is the last checkpoint record (nil when none).
	checkpoint *Record
	// oldest is the oldest kept record's seq (0 when none).
	oldest int64
	// ring is the last event records, at most ringMax.
	ring []*Record
	// cut is the number of bytes cut from a torn last line.
	cut int
}

const ringMax = 64

// openJournal opens (creating) a route's journal and reads it: a torn last
// line of current is cut and logged; any other invalid line holds the
// route (errHeld). Every record must carry epoch.
func openJournal(dir, epoch string) (*journal, *loaded, error) {
	if err := mkdirs(filepath.Dir(dir), filepath.Base(dir)); err != nil {
		return nil, nil, err
	}
	ld := &loaded{epoch: epoch}
	if err := scanJournal(filepath.Join(dir, journalPrev), false, ld); err != nil {
		return nil, nil, err
	}
	if err := scanJournal(filepath.Join(dir, journalCurrent), true, ld); err != nil {
		return nil, nil, err
	}
	cur := filepath.Join(dir, journalCurrent)
	created := !exists(cur)
	f, err := os.OpenFile(cur, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, err
	}
	if created {
		// The new file's name is durable before any record is committed
		// in it.
		if err := syncDir(dir); err != nil {
			_ = f.Close()
			return nil, nil, err
		}
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return &journal{dir: dir, f: f, size: st.Size()}, ld, nil
}

// journalOps are the operations a record may carry (4.2, Phase A).
var journalOps = map[string]bool{"send": true, "claim": true, "claim_extend": true, "ack": true, "release": true,
	"pair": true, "intake": true, "hold": true, "unhold": true, "notice": true, "health": true, "checkpoint": true}

// checkRecord validates one decoded record against what came before it:
// a known operation, the route's epoch, the next sequence number, and for
// a checkpoint a completed_through below it and not behind the last one.
func (ld *loaded) checkRecord(r *Record) error {
	switch {
	case !journalOps[r.Op]:
		return fmt.Errorf("unknown operation %q", r.Op)
	case r.Epoch != ld.epoch:
		return fmt.Errorf("record %d of epoch %q in a journal of epoch %q", r.Seq, r.Epoch, ld.epoch)
	case ld.last != nil && r.Seq != ld.last.Seq+1:
		return fmt.Errorf("sequence %d after %d", r.Seq, ld.last.Seq)
	case r.Op == "checkpoint" && (r.CompletedThrough >= r.Seq || ld.checkpoint != nil && r.CompletedThrough < ld.checkpoint.CompletedThrough):
		return fmt.Errorf("checkpoint %d completes through %d", r.Seq, r.CompletedThrough)
	case r.Op != "checkpoint" && r.CompletedThrough != 0:
		return fmt.Errorf("record %d of %s carries completed_through", r.Seq, r.Op)
	}
	return nil
}

// readLine reads one journal line of at most maxRecord bytes. A longer
// line is skipped to its end without being held in memory: over reports
// its length and line is nil.
func readLine(rd *bufio.Reader) ([]byte, int64, error) {
	var line []byte
	var over int64
	for {
		chunk, err := rd.ReadSlice('\n')
		if over == 0 && len(line)+len(chunk) <= maxRecord {
			line = append(line, chunk...)
		} else {
			over += int64(len(line)) + int64(len(chunk))
			line = nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if over > 0 {
			return nil, over, err
		}
		return line, 0, err
	}
}

// scanJournal reads one journal file into ld. In current (last), a torn
// final line is cut from the file; any other invalid line, record or
// sequence holds the route.
func scanJournal(p string, last bool, ld *loaded) error {
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	rd := bufio.NewReaderSize(f, 4096)
	var off int64
	for {
		line, over, err := readLine(rd)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(line) == 0 && over == 0 {
			return nil
		}
		torn := over == 0 && line[len(line)-1] != '\n'
		var r Record
		var bad error
		switch {
		case over > 0:
			bad = fmt.Errorf("line of %d bytes over the %d byte bound", over, maxRecord)
			torn = errors.Is(err, io.EOF) // no newline before the end
		case torn:
			bad = errors.New("torn line")
		default:
			if derr := strictDecode(bytes.TrimSuffix(line, []byte("\n")), &r); derr != nil || r.Seq <= 0 {
				bad = fmt.Errorf("invalid record: %v", derr)
			} else {
				bad = ld.checkRecord(&r)
			}
		}
		if bad != nil {
			// Only a torn last line of current is repaired.
			if !last || !torn {
				return fmt.Errorf("%w: %v at byte %d of %s", errHeld, bad, off, p)
			}
			if terr := f.Truncate(off); terr != nil {
				return terr
			}
			if serr := fsync(f); serr != nil {
				return serr
			}
			ld.cut = int(over) + len(line)
			return nil
		}
		rec := r
		if ld.oldest == 0 {
			ld.oldest = rec.Seq
		}
		ld.last = &rec
		if rec.Op == "checkpoint" {
			ld.checkpoint = &rec
			kept := ld.pending[:0]
			for _, x := range ld.pending {
				if x.Seq > rec.CompletedThrough {
					kept = append(kept, x)
				}
			}
			ld.pending = kept
		} else if ld.checkpoint == nil || rec.Seq > ld.checkpoint.CompletedThrough {
			ld.pending = append(ld.pending, &rec)
		}
		if rec.Event != nil {
			ld.ring = append(ld.ring, &rec)
			if len(ld.ring) > ringMax {
				ld.ring = ld.ring[1:]
			}
		}
		off += int64(len(line))
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

// journalWrite writes a record's bytes; tests replace it to inject a
// partial write.
var journalWrite = (*os.File).Write

// append writes one record and fsyncs the journal: the commit point. On
// any failure the journal's tail is unknown and the caller recovers it
// (Route.recoverTail) before another operation.
func (j *journal) append(r *Record) error {
	b, err := encodeRecord(r)
	if err != nil {
		return err
	}
	if _, err := journalWrite(j.f, b); err != nil {
		j.broken = true
		return err
	}
	if err := fsync(j.f); err != nil {
		j.broken = true
		return err
	}
	j.size += int64(len(b))
	return nil
}

// needsRotation reports whether current is past its bound.
func (j *journal) needsRotation() bool { return j.size >= journalRotate }

// rotate moves current to previous (dropping the old previous) and opens
// a new, empty current. The caller then appends the checkpoint record.
func (j *journal) rotate() error {
	if err := j.f.Close(); err != nil {
		return err
	}
	cur, prev := filepath.Join(j.dir, journalCurrent), filepath.Join(j.dir, journalPrev)
	if err := os.Rename(cur, prev); err != nil {
		return err
	}
	f, err := os.OpenFile(cur, os.O_CREATE|os.O_WRONLY|os.O_APPEND|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	j.f, j.size = f, 0
	return syncDir(j.dir)
}

// close closes the journal file.
func (j *journal) close() {
	if j.f != nil {
		_ = j.f.Close()
		j.f = nil
	}
}

// eventRecords reads the stored event records with after < seq <= upto
// from the journal files, in order, for a cursor older than the ring.
func (j *journal) eventRecords(after, upto int64) ([]*Record, error) {
	var out []*Record
	for _, name := range []string{journalPrev, journalCurrent} {
		f, err := os.Open(filepath.Join(j.dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 4096), maxRecord+1)
		for sc.Scan() {
			var r Record
			if json.Unmarshal(sc.Bytes(), &r) != nil {
				continue
			}
			if r.Event != nil && r.Seq > after && r.Seq <= upto {
				rec := r
				out = append(out, &rec)
			}
		}
		_ = f.Close()
	}
	return out, nil
}

// oldestSeq is the first record's seq in the kept files (0 when empty).
func (j *journal) oldestSeq() int64 {
	for _, name := range []string{journalPrev, journalCurrent} {
		f, err := os.Open(filepath.Join(j.dir, name))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 4096), maxRecord+1)
		var r Record
		ok := sc.Scan() && json.Unmarshal(sc.Bytes(), &r) == nil
		_ = f.Close()
		if ok {
			return r.Seq
		}
	}
	return 0
}
