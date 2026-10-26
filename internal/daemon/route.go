package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Route bounds (9.1, 9.3, 5.4).
const (
	actorQueue     = 128              // route actor input
	keyWindow      = 10 * time.Minute // a send or extend key's retry window
	keyWindowMax   = 512              // send keys inside the window
	keyKeepMax     = 24 * time.Hour   // keys past the window, kept while there is room
	extendKeyMax   = 64               // extend keys of one claim inside the window
	backlogMax     = 1000             // pending messages per mailbox
	sideBudget     = 32               // side-agent messages per mailbox
	sidePerSource  = 8                // side-agent messages per source
	claimMaxIDs    = 1000             // ids in one claim manifest payload
	checkpointMax  = 256 << 10        // one checkpoint payload
	contractDaemon = 2                // owner.json contract of a daemon-owned inbox
)

// Owner is state/owner.json of a daemon-owned inbox (4.1, 5.7).
type Owner struct {
	Owner       string  `json:"owner"`
	Epoch       string  `json:"epoch"`
	Contract    int     `json:"contract"`
	Protocol    int     `json:"protocol"`
	AdoptedFrom *string `json:"adopted_from"`
	MinWriter   string  `json:"min_writer,omitempty"`
}

// ReadOwner reads a daemon owner record; ok is false when the inbox has
// none or it is not the daemon's.
func ReadOwner(dir string) (Owner, bool) {
	b, err := readSmall(filepath.Join(dir, "state", "owner.json"), 4096)
	if err != nil {
		return Owner{}, false
	}
	var o Owner
	if strictDecode(b, &o) != nil || o.Owner != "daemon" || len(o.Epoch) != 32 {
		return Owner{}, false
	}
	return o, true
}

// sendKey is one remembered send idempotency key (5.4).
type sendKey struct {
	Key        string `json:"key"`
	ID         string `json:"id"`
	From       string `json:"from"`
	To         string `json:"to"`
	Kind       string `json:"kind"`
	ReplyTo    string `json:"reply_to"`
	BodySHA256 string `json:"body_sha256"`
	Time       int64  `json:"time"`
	Seq        int64  `json:"seq"`
}

// claimKey is one remembered read or extend key of a claim.
type claimKey struct {
	Key   string `json:"key"`
	Token string `json:"token"`
	Box   string `json:"box"`
	N     int    `json:"n"`
	Empty bool   `json:"empty,omitempty"`
	Time  int64  `json:"time"`
}

// checkpoint is a checkpoint payload (4.2): what replay needs that the
// queue files do not hold.
type checkpoint struct {
	LastMessageID string     `json:"last_message_id"`
	SendKeys      []sendKey  `json:"send_keys"`
	ReadKeys      []claimKey `json:"read_keys"`
	ExtendKeys    []claimKey `json:"extend_keys"`
	Questions     []any      `json:"questions"`
}

// crashSignal is what a test's crash hook panics with to stop an
// operation at an injected point, as a killed process stops.
type crashSignal struct{ point string }

// Route is one route's actor: the only goroutine that writes the route's
// inbox. Every operation runs on it, one at a time.
type Route struct {
	d     *Daemon
	name  string
	inbox string

	ops  chan func()
	quit chan struct{}
	wg   sync.WaitGroup

	// Actor-owned state.
	lock       *os.File
	j          *journal
	epoch      string
	seq        int64
	visible    int64
	oldest     int64
	lastID     int64
	ring       []*Record
	sendKeys   map[string]*sendKey
	readKeys   map[string]*claimKey
	extendKeys map[string]*claimKey
	subs       map[*subscriber]struct{}
	flags      []string
	failures   []time.Time
	degraded   atomic.Bool
	removed    bool
	retained   []*inbox.Lock
	creds      map[string]credential // party name to its credential record
	hashes     hashCache
	acks       ackCache
	// replaying is set while open replays the journal.
	replaying bool
	// keychainSeen are parties that had a keychain secret.
	keychainSeen map[string]bool
	// unfinished are committed records whose step 4 failed, in order.
	unfinished []*Record
	// cpLastID is the last checkpoint's last_message_id, as a number.
	cpLastID int64
}

// routeStatus is the route's status in health.
func (r *Route) routeStatus() string {
	switch {
	case r.degraded.Load():
		return "degraded"
	case r.j == nil:
		return "unowned"
	case len(r.flags) > 0:
		return "held"
	}
	return "ok"
}

func (r *Route) now() time.Time { return r.d.now() }

// crash is an injected crash point (tests only).
func (r *Route) crash(point string) {
	if r.d.cfg.Crash != nil {
		r.d.cfg.Crash(point)
	}
}

func (r *Route) path(names ...string) string {
	return filepath.Join(append([]string{r.inbox}, names...)...)
}

// newRoute makes a route actor for an inbox; open owns it.
func newRoute(d *Daemon, name, dir string) *Route {
	return &Route{d: d, name: name, inbox: dir, ops: make(chan func(), actorQueue), quit: make(chan struct{}),
		sendKeys: map[string]*sendKey{}, readKeys: map[string]*claimKey{}, extendKeys: map[string]*claimKey{},
		subs: map[*subscriber]struct{}{}, creds: map[string]credential{}, keychainSeen: map[string]bool{}}
}

// start runs the actor goroutine.
func (r *Route) start() {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		for {
			select {
			case op := <-r.ops:
				op()
			case <-r.quit:
				return
			}
		}
	}()
}

// stop ends the actor and frees the route's locks; queued operations are
// answered busy by do.
func (r *Route) stop() {
	select {
	case <-r.quit:
	default:
		close(r.quit)
	}
	r.wg.Wait()
	r.closeFiles()
}

// closeFiles frees the journal and the route lock (the kernel would on a
// crash); the retained mkdir locks are freed only on route removal.
func (r *Route) closeFiles() {
	if r.j != nil {
		r.j.close()
		r.j = nil
	}
	if r.lock != nil {
		_ = r.lock.Close()
		r.lock = nil
	}
	for s := range r.subs {
		s.close()
		delete(r.subs, s)
	}
}

// do runs fn on the actor and waits for it. A full actor queue is busy
// (503); a panic in fn is recovered, counted and answered internal (3.2).
func (r *Route) do(ctx context.Context, fn func() *APIError) *APIError {
	done := make(chan *APIError, 1)
	op := func() {
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(crashSignal); ok {
					panic(p)
				}
				r.d.logf("route %s: operation panicked: %v", r.name, p)
				r.failed()
				done <- Errf("internal", "the route's operation failed; see the daemon log")
			}
		}()
		// Work whose request expired while it waited is never started.
		if ctx.Err() != nil {
			done <- Errf("busy", "request cancelled before it ran; retry")
			return
		}
		done <- r.runOp(fn)
	}
	select {
	case r.ops <- op:
	default:
		return Errf("busy", "route %s is busy; retry", r.name)
	}
	select {
	case e := <-done:
		return e
	case <-r.quit:
		return Errf("busy", "route %s is stopping; retry", r.name)
	case <-ctx.Done():
		// The operation still runs to its end on the actor; only the
		// answer is lost, and the client retries with its key.
		return Errf("busy", "request cancelled")
	}
}

// runOp is one operation's turn on the actor: refused on a degraded or
// removed route, after any unfinished committed record is finished and
// any due rotation is done.
func (r *Route) runOp(fn func() *APIError) *APIError {
	if r.degraded.Load() {
		return Errf("busy", "route %s is degraded after repeated failures; run herdsman daemon restart", r.name)
	}
	if r.removed {
		return Errf("unknown_route", "route %s was removed", r.name)
	}
	// A committed operation is finished before any other is admitted.
	if e := r.finishUnfinished(); e != nil {
		return e
	}
	// Rotation happens only between operations, so no payload of an
	// operation in flight is pruned by its checkpoint (4.2).
	if r.j != nil && r.j.needsRotation() {
		if err := r.writeCheckpoint(true); err != nil {
			return Errf("internal", "journal rotation: %v", err)
		}
	}
	return fn()
}

// failed records one recovered failure; three in ten minutes degrade the
// route (3.2).
func (r *Route) failed() {
	now := r.now()
	kept := r.failures[:0]
	for _, t := range r.failures {
		if now.Sub(t) < 10*time.Minute {
			kept = append(kept, t)
		}
	}
	r.failures = append(kept, now)
	if len(r.failures) >= 3 {
		r.degraded.Store(true)
	}
}

// open takes ownership of the route's inbox and replays its journal (4.4
// steps 1 to 6). It runs before the actor starts.
func (r *Route) open() error {
	if err := r.takeLock(); err != nil {
		return err
	}
	o, ok := ReadOwner(r.inbox)
	if !ok {
		r.closeFiles()
		return fmt.Errorf("route %s: inbox %s has no daemon owner record; human inspection required", r.name, r.inbox)
	}
	r.epoch = o.Epoch
	if err := r.retainLocks(); err != nil {
		r.closeFiles()
		return err
	}
	if r.journalLost() {
		// The history is gone but the route's files are not: the old
		// epoch's cursors and keys cannot be trusted. A new durable epoch
		// starts; the mailboxes and their unacked files stay (4.3).
		epoch := r.d.newOwner(false).Epoch
		r.d.logf("route %s: journal missing for an inbox with history; new event epoch %s", r.name, epoch)
		if err := r.setEpoch(epoch); err != nil {
			r.closeFiles()
			return fmt.Errorf("route %s: new epoch: %w", r.name, err)
		}
	}
	j, ld, err := openJournal(r.path("state", "events"), r.epoch)
	if err != nil {
		r.closeFiles()
		return fmt.Errorf("route %s: %w", r.name, err)
	}
	r.j = j
	if ld.cut > 0 {
		r.d.logf("route %s: cut a torn journal line of %d bytes", r.name, ld.cut)
	}
	if ld.last != nil {
		r.seq = ld.last.Seq
	}
	r.oldest = ld.oldest
	r.ring = ld.ring
	if ld.checkpoint != nil {
		if err := r.loadCheckpoint(ld.checkpoint); err != nil {
			r.closeFiles()
			return err
		}
	}
	if err := r.loadSequence(ld.pending); err != nil {
		r.closeFiles()
		return err
	}
	r.loadCredentials()
	r.replaying = true
	err = r.replay(ld.pending)
	r.replaying = false
	if err != nil {
		r.closeFiles()
		return err
	}
	// Secrets are pruned once, against the final records.
	if err := r.pruneSecrets(); err != nil {
		r.closeFiles()
		return fmt.Errorf("route %s: credentials: %w", r.name, err)
	}
	r.recoverOrphans()
	// Step 6: a checkpoint completes everything replayed.
	if err := r.writeCheckpoint(false); err != nil {
		r.closeFiles()
		return err
	}
	r.visible = r.seq
	r.flags = r.healthFlags()
	return nil
}

// takeLock takes state/route.lock with flock, non-blocking (4.4 step 1).
func (r *Route) takeLock() error {
	if err := mkdirs(r.inbox, "state"); err != nil {
		return err
	}
	f, err := os.OpenFile(r.path("state", "route.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return fmt.Errorf("route %s: %w", r.name, errUnowned)
	}
	r.lock = f
	return nil
}

var errUnowned = errors.New("another process holds the route lock")

// retainedLocks are the file-mode locks the daemon holds for as long as it
// owns a route, so a file-mode writer of any version fails on its own lock
// (12.2). The shared input lock is not among them (3.7).
var retainedLocks = []string{inbox.ChannelLock, "courier.lock", "watch.lock"}

// retainLocks takes (or, after a crash, keeps) the retained locks with the
// daemon's owner record. A lock folder held by anyone else holds the route.
func (r *Route) retainLocks() error {
	r.retained = r.retained[:0]
	for _, name := range retainedLocks {
		l, err := takeDaemonLock(r.path(name), r.epoch)
		if err != nil {
			return fmt.Errorf("route %s: %w", r.name, err)
		}
		r.retained = append(r.retained, l)
	}
	return nil
}

// lockOwner is the owner record the daemon writes inside a lock folder.
type lockOwner struct {
	Owner string `json:"owner"`
	Epoch string `json:"epoch"`
	PID   int    `json:"pid,omitempty"`
	Start int64  `json:"start,omitempty"`
}

// takeDaemonLock takes a mkdir lock with the daemon's records ("daemon
// <pid>" in pid, owner.json naming the daemon and epoch). A folder that
// already holds the daemon's record of this epoch is the daemon's own lock
// from before a restart and is kept.
func takeDaemonLock(dir, epoch string) (*inbox.Lock, error) {
	if err := mkdirDurable(dir); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if o, ok := daemonLockOwner(dir); ok && (epoch == "" || o.Epoch == epoch) {
			_ = writeDurable(filepath.Join(dir, "pid"), []byte("daemon "+strconv.Itoa(os.Getpid())+"\n"), 0o600)
			return inbox.Adopt(dir), nil
		}
		return nil, fmt.Errorf("lock %s is held by another owner (%s); inspect it, never remove it automatically", filepath.Base(dir), lockHolder(dir))
	}
	if err := writeDurable(filepath.Join(dir, "pid"), []byte("daemon "+strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	b, _ := encode(lockOwner{Owner: "daemon", Epoch: epoch, PID: os.Getpid(), Start: time.Now().Unix()})
	if err := writeDurable(filepath.Join(dir, "owner.json"), append(b, '\n'), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return inbox.Adopt(dir), nil
}

// retakeDaemonLock takes a lock for adoption: a folder that holds a
// daemon owner record (of any epoch: a crashed adoption's) is retaken and
// its record rewritten with epoch; any other holder refuses.
func retakeDaemonLock(dir, epoch string) (*inbox.Lock, error) {
	if _, ok := daemonLockOwner(dir); ok {
		b, _ := encode(lockOwner{Owner: "daemon", Epoch: epoch, PID: os.Getpid(), Start: time.Now().Unix()})
		if err := writeDurable(filepath.Join(dir, "owner.json"), append(b, '\n'), 0o600); err != nil {
			return nil, err
		}
	}
	return takeDaemonLock(dir, epoch)
}

// daemonLockOwner reads a lock folder's daemon owner record.
func daemonLockOwner(dir string) (lockOwner, bool) {
	b, err := readSmall(filepath.Join(dir, "owner.json"), 4096)
	if err != nil {
		return lockOwner{}, false
	}
	var o lockOwner
	if json.Unmarshal(b, &o) != nil || o.Owner != "daemon" {
		return lockOwner{}, false
	}
	return o, true
}

// lockHolder is a lock folder's recorded owner, for the human.
func lockHolder(dir string) string {
	b, err := readSmall(filepath.Join(dir, "pid"), 256)
	if err != nil {
		return "no owner record"
	}
	return "pid file: " + strings.TrimSpace(string(b))
}

// releaseLock removes a daemon lock folder with its records.
func releaseLock(l *inbox.Lock) {
	if l == nil {
		return
	}
	_ = os.Remove(filepath.Join(l.Dir(), "owner.json"))
	_ = l.Release()
}

// loadSequence sets the message id high-water mark: the largest of
// state/sequence, the checkpoint's last_message_id, every send record
// still in the journal and every allocated ids/<id> folder, so a lost or
// stale sequence file never lets an id be allocated twice.
func (r *Route) loadSequence(pending []*Record) error {
	var n int64
	b, err := readSmall(r.path("state", "sequence"), 64)
	switch {
	case err == nil:
		n, err = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		if err != nil || n < 0 || n > 999999999999 {
			return fmt.Errorf("route %s: %w: invalid state/sequence", r.name, errHeld)
		}
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	high := max(n, r.cpLastID)
	for _, rec := range pending {
		if rec.Op == "send" {
			high = max(high, idNumber(rec.ID))
		}
	}
	if ents, err := os.ReadDir(r.path("ids")); err == nil {
		for _, e := range ents {
			if inbox.IDOK(e.Name()) {
				high = max(high, idNumber(e.Name()))
			}
		}
	}
	if high != n {
		r.d.logf("route %s: state/sequence %d is behind the route's files; continuing from %d", r.name, n, high)
	}
	r.lastID = high
	return nil
}

// idNumber is a message id's number (0 for an invalid id).
func idNumber(id string) int64 {
	if !inbox.IDOK(id) {
		return 0
	}
	n, _ := strconv.ParseInt(id[1:], 10, 64)
	return n
}

// journalLost reports an inbox whose journal is gone while its history
// is not: no journal file, but a checkpoint payload, an allocated id or a
// sequence on disk.
func (r *Route) journalLost() bool {
	dir := r.path("state", "events")
	if exists(filepath.Join(dir, journalCurrent)) || exists(filepath.Join(dir, journalPrev)) {
		return false
	}
	if cps, _ := os.ReadDir(r.path("state", "checkpoints")); len(cps) > 0 {
		return true
	}
	if ids, _ := os.ReadDir(r.path("ids")); len(ids) > 0 {
		return true
	}
	return exists(r.path("state", "sequence"))
}

// setEpoch writes a new epoch to owner.json and to the retained locks'
// owner records, durably, before any record of it is written.
func (r *Route) setEpoch(epoch string) error {
	o, ok := ReadOwner(r.inbox)
	if !ok {
		return errors.New("owner record missing")
	}
	o.Epoch = epoch
	b, _ := encode(o)
	if err := writeDurable(r.path("state", "owner.json"), append(b, '\n'), 0o600); err != nil {
		return err
	}
	r.epoch = epoch
	for _, l := range r.retained {
		b, _ := encode(lockOwner{Owner: "daemon", Epoch: epoch, PID: os.Getpid(), Start: r.now().Unix()})
		if err := writeDurable(filepath.Join(l.Dir(), "owner.json"), append(b, '\n'), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// loadCheckpoint restores the key indexes from a checkpoint payload.
func (r *Route) loadCheckpoint(c *Record) error {
	if c.Payload == nil {
		return nil
	}
	b, err := r.readPayload(c.Payload, checkpointMax)
	if err != nil {
		return fmt.Errorf("route %s: checkpoint payload: %w", r.name, err)
	}
	var cp checkpoint
	if err := strictDecode(b, &cp); err != nil {
		return fmt.Errorf("route %s: %w: checkpoint payload: %v", r.name, errHeld, err)
	}
	r.cpLastID = idNumber(cp.LastMessageID)
	for i := range cp.SendKeys {
		k := cp.SendKeys[i]
		r.sendKeys[k.Key] = &k
	}
	for i := range cp.ReadKeys {
		k := cp.ReadKeys[i]
		r.readKeys[k.Key] = &k
	}
	for i := range cp.ExtendKeys {
		k := cp.ExtendKeys[i]
		r.extendKeys[k.Key] = &k
	}
	return nil
}

// readPayload reads a payload file and checks its hash against the record.
func (r *Route) readPayload(p *Payload, max int64) ([]byte, error) {
	if !validRel(p.Path) {
		return nil, fmt.Errorf("payload path %q outside the inbox", p.Path)
	}
	b, err := readSmall(r.path(p.Path), max)
	if err != nil {
		return nil, err
	}
	if sha256Hex(b) != p.SHA256 || int64(len(b)) != p.Bytes {
		return nil, fmt.Errorf("%w: payload %s does not match its record", errHeld, p.Path)
	}
	return b, nil
}

// validRel reports whether p is a clean relative path inside the inbox.
func validRel(p string) bool {
	return p != "" && !filepath.IsAbs(p) && filepath.Clean(p) == p && !strings.HasPrefix(p, "..")
}

// writePayload writes a payload file durably (step 2) and returns its
// reference.
func (r *Route) writePayload(rel string, data []byte) (*Payload, error) {
	if err := mkdirs(r.inbox, filepath.Dir(rel)); err != nil {
		return nil, err
	}
	if err := writeDurable(r.path(rel), data, 0o600); err != nil {
		return nil, err
	}
	return &Payload{Path: rel, SHA256: sha256Hex(data), Bytes: int64(len(data))}, nil
}

// commit appends a record (step 3) with the next sequence number; the
// caller then applies step 4 and calls publish.
func (r *Route) commit(rec *Record) error {
	if r.j == nil {
		return errors.New("the route's journal is closed")
	}
	rec.Seq = r.seq + 1
	rec.Epoch = r.epoch
	rec.Time = r.now().Unix()
	if err := r.j.append(rec); err != nil {
		if !r.j.broken {
			return err // refused before any byte was written
		}
		landed, rerr := r.recoverTail(rec.Seq)
		if rerr != nil {
			r.degraded.Store(true)
			r.d.logf("route %s: journal tail unknown after a failed write (%v), then %v; route stopped", r.name, err, rerr)
			return err
		}
		if !landed {
			return err
		}
		r.d.logf("route %s: record %d is durable after a reported write failure: %v", r.name, rec.Seq, err)
	}
	r.seq = rec.Seq
	if r.oldest == 0 {
		r.oldest = rec.Seq
	}
	return nil
}

// recoverTail reopens the journal after a failed append: it rescans the
// file (cutting a torn tail), fsyncs it, and reports whether record seq
// is durable in it. Any other last sequence is an error: a sequence is
// never reused.
func (r *Route) recoverTail(seq int64) (bool, error) {
	r.j.close()
	r.j = nil
	j, ld, err := openJournal(r.path("state", "events"), r.epoch)
	if err != nil {
		return false, err
	}
	if err := fsync(j.f); err != nil {
		j.close()
		return false, err
	}
	var last int64
	if ld.last != nil {
		last = ld.last.Seq
	}
	switch last {
	case seq:
		r.j = j
		return true, nil
	case seq - 1:
		r.j = j
		return false, nil
	}
	j.close()
	return false, fmt.Errorf("journal ends at %d, expected %d or %d", last, seq-1, seq)
}

// publish raises visible_seq to the record (step 5) and sends its event to
// every subscriber it concerns.
func (r *Route) publish(rec *Record) {
	r.visible = rec.Seq
	if rec.Event == nil {
		return
	}
	r.ring = append(r.ring, rec)
	if len(r.ring) > ringMax {
		r.ring = r.ring[1:]
	}
	for s := range r.subs {
		if s.wants(rec) && !s.push(frame{rec: rec, route: r.name}) {
			delete(r.subs, s)
		}
	}
}

// writeCheckpoint writes a checkpoint (4.2 rotation, 4.4 step 6). With
// rotate, the journal rotates first, so the checkpoint is the first record
// of the new file.
func (r *Route) writeCheckpoint(rotate bool) error {
	if len(r.unfinished) > 0 {
		return fmt.Errorf("record %d is committed but unfinished; no checkpoint may pass it", r.unfinished[0].Seq)
	}
	r.pruneKeys()
	cp := checkpoint{LastMessageID: fmt.Sprintf("m%012d", r.lastID), SendKeys: []sendKey{}, ReadKeys: []claimKey{}, ExtendKeys: []claimKey{}, Questions: []any{}}
	for _, k := range r.sendKeys {
		cp.SendKeys = append(cp.SendKeys, *k)
	}
	sort.Slice(cp.SendKeys, func(i, j int) bool { return cp.SendKeys[i].Seq < cp.SendKeys[j].Seq })
	for _, k := range r.readKeys {
		cp.ReadKeys = append(cp.ReadKeys, *k)
	}
	for _, k := range r.extendKeys {
		cp.ExtendKeys = append(cp.ExtendKeys, *k)
	}
	sort.Slice(cp.ReadKeys, func(i, j int) bool { return cp.ReadKeys[i].Key < cp.ReadKeys[j].Key })
	sort.Slice(cp.ExtendKeys, func(i, j int) bool { return cp.ExtendKeys[i].Key < cp.ExtendKeys[j].Key })
	data, err := encode(cp)
	if err != nil {
		return err
	}
	if len(data) > checkpointMax {
		return fmt.Errorf("checkpoint payload of %d bytes over its bound", len(data))
	}
	next := r.seq + 1
	pl, err := r.writePayload(filepath.Join("state", "checkpoints", strconv.FormatInt(next, 10)+".json"), data)
	if err != nil {
		return err
	}
	r.crash("checkpoint.payload")
	if rotate {
		if err := r.j.rotate(); err != nil {
			return err
		}
		r.crash("checkpoint.rotated")
	}
	rec := &Record{Op: "checkpoint", CompletedThrough: r.seq, Payload: pl, Seq: next, Epoch: r.epoch, Time: r.now().Unix()}
	if err := r.j.append(rec); err != nil {
		return err
	}
	r.seq = next
	r.visible = next
	if rotate {
		r.oldest = r.j.oldestSeq()
	} else if r.oldest == 0 {
		r.oldest = next
	}
	r.crash("checkpoint.record")
	// The previous checkpoint payloads go only after the new record is
	// durable; released claims' payloads go at each checkpoint.
	r.pruneFiles(next)
	return nil
}

// pruneFiles removes checkpoint payloads older than keep and claim
// payloads of claims no longer active or remembered.
func (r *Route) pruneFiles(keep int64) {
	if ents, err := os.ReadDir(r.path("state", "checkpoints")); err == nil {
		for _, e := range ents {
			n, err := strconv.ParseInt(strings.TrimSuffix(e.Name(), ".json"), 10, 64)
			if err == nil && n < keep {
				_ = os.Remove(r.path("state", "checkpoints", e.Name()))
			}
		}
	}
	live := map[string]bool{}
	for _, b := range r.boxes() {
		if t := r.activeToken(b); t != "" {
			live[t] = true
		}
	}
	for _, k := range r.readKeys {
		live[k.Token] = true
	}
	for _, k := range r.extendKeys {
		live[k.Token] = true
	}
	// Registry and hold payloads are needed only to replay records after
	// the last checkpoint; none is in flight between operations.
	for _, d := range []string{"registry", "holds"} {
		if ents, err := os.ReadDir(r.path("state", d)); err == nil {
			for _, e := range ents {
				_ = os.Remove(r.path("state", d, e.Name()))
			}
		}
	}
	if ents, err := os.ReadDir(r.path("state", "claims")); err == nil {
		for _, e := range ents {
			tok, _, _ := strings.Cut(e.Name(), "-")
			if !live[tok] {
				_ = os.Remove(r.path("state", "claims", e.Name()))
			}
		}
	}
}

// pruneKeys drops send keys past their 24 h keep and claim keys past
// their window (5.4); keys inside the window are never dropped.
func (r *Route) pruneKeys() {
	now := r.now().Unix()
	for k, v := range r.sendKeys {
		if now-v.Time > int64(keyKeepMax/time.Second) {
			delete(r.sendKeys, k)
		}
	}
	// Claim keys are kept only for open claims (4.2).
	for k, v := range r.readKeys {
		if r.activeToken(v.Box) != v.Token {
			delete(r.readKeys, k)
		}
	}
	for k, v := range r.extendKeys {
		if now-v.Time > int64(keyWindow/time.Second) || r.activeToken(v.Box) != v.Token {
			delete(r.extendKeys, k)
		}
	}
}

// boxes lists the inbox's mailboxes: real folders with a party's name.
func (r *Route) boxes() []string {
	ents, err := os.ReadDir(r.inbox)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() && inbox.NameOK(e.Name()) && e.Type()&os.ModeSymlink == 0 {
			out = append(out, e.Name())
		}
	}
	return out
}

// activeToken is a mailbox's active claim token ("" when none).
func (r *Route) activeToken(box string) string {
	b, err := readSmall(r.path(box, "claim", "token"), 256)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// logf writes one line to the daemon log.
func (d *Daemon) logf(format string, a ...any) {
	if d.cfg.Log != nil {
		d.cfg.Log(fmt.Sprintf(format, a...))
	}
}
