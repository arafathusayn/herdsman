package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// AdoptRequest moves a file inbox under the daemon.
type AdoptRequest struct {
	Inbox             string `json:"inbox"`
	ConfirmPairSHA256 string `json:"confirm_pair_sha256"`
}

// AdoptResult is an adoption's answer.
type AdoptResult struct {
	Route         string `json:"route"`
	Epoch         string `json:"epoch"`
	LastMessageID string `json:"last_message_id"`
	LegacyProof   bool   `json:"legacy_proof"`
}

// adoptMarker is state/adopting.json, present only while an adoption is
// incomplete (12.2).
type adoptMarker struct {
	Phase   string `json:"phase"`
	Started int64  `json:"started"`
	Epoch   string `json:"epoch"`
	Route   string `json:"route"`
	Confirm string `json:"confirm"`
}

// pendingList is <private home>/daemon/adopting.json.
type pendingList struct {
	Version int            `json:"version"`
	Inboxes []pendingEntry `json:"inboxes"`
}

type pendingEntry struct {
	Inbox   string `json:"inbox"`
	Route   string `json:"route"`
	Started int64  `json:"started"`
	// Confirm is the pair the human confirmed, kept so a start that
	// finds the entry before the marker exists can finish unattended.
	Confirm string `json:"confirm,omitempty"`
	// Epoch is the adoption's epoch (randomHex(16)), which names its
	// staging database; a resume takes it from here, never from the
	// inbox's marker.
	Epoch string `json:"epoch,omitempty"`
}

// adoptLocks are the file-mode locks adoption takes, in the scripts'
// order: the input lock, then the queue lock, then the services' locks.
var adoptLocks = []string{inbox.MutationLock, inbox.ChannelLock, "courier.lock", "watch.lock"}

// pendingAdoption reports whether an inbox's adoption is incomplete.
func pendingAdoption(dir string) bool {
	return exists(filepath.Join(dir, "state", "adopting.json"))
}

func (d *Daemon) pendingFile() string { return filepath.Join(d.daemonDir(), "adopting.json") }

func (d *Daemon) readPending() pendingList {
	pl := pendingList{Version: 1, Inboxes: []pendingEntry{}}
	if b, err := readSmall(d.pendingFile(), 1<<20); err == nil {
		_ = strictDecode(b, &pl)
	}
	return pl
}

func (d *Daemon) writePending(pl pendingList) error {
	b, _ := encode(pl)
	return writeDurable(d.pendingFile(), append(b, '\n'), 0o600)
}

// checkAdoptInbox is step 2.1: the inbox is absolute, canonical, a real
// folder owned by this user with mode 700, directly under the private home.
func (d *Daemon) checkAdoptInbox(dir string) *APIError {
	if !filepath.IsAbs(dir) {
		return Errf("bad_request", "inbox must be absolute")
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil || real != dir {
		return Errf("bad_request", "inbox must be canonical, with no links")
	}
	home, _ := filepath.EvalSymlinks(d.cfg.PrivateHome)
	if filepath.Dir(dir) != home && filepath.Dir(dir) != d.cfg.PrivateHome {
		return Errf("bad_request", "inbox must be a folder directly under the private home %s", d.cfg.PrivateHome)
	}
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
		return Errf("bad_request", "inbox must be a folder with mode 700")
	}
	if s, ok := st.Sys().(*syscall.Stat_t); !ok || int(s.Uid) != os.Getuid() {
		return Errf("bad_request", "inbox must be owned by this user")
	}
	return nil
}

func (d *Daemon) adoptRoute(ctx context.Context, name string, q AdoptRequest, res *AdoptResult) *APIError {
	if !validName(name) {
		return Errf("bad_request", "invalid route name")
	}
	d.regMu.Lock()
	defer d.regMu.Unlock()
	rf, err := ReadRoutes(d.cfg.PrivateHome)
	if err != nil {
		return Errf("internal", "%v", err)
	}
	if e, ok := rf.Routes[name]; ok && e.Inbox != q.Inbox {
		return Errf("route_exists", "route %s is registered with another inbox", name)
	}
	if e := d.nameTaken(rf, name, q.Inbox); e != nil {
		return e
	}
	d.mu.Lock()
	_, served := d.routes[name]
	d.mu.Unlock()
	if served {
		return Errf("route_exists", "route %s is already served", name)
	}
	out, e := d.adopt(ctx, name, q.Inbox, q.ConfirmPairSHA256, true)
	if e != nil {
		return e
	}
	*res = out
	return nil
}

// nameTaken refuses name with route_exists when a registered, served or
// pending route of another inbox has it, exactly or ignoring case: on a
// case-insensitive file system (the macOS default) a case twin shares the
// route database and the staging folder, and an exact name would publish
// over the other inbox's route. Only dir's own route of exactly that name
// passes (adopt's resume); dir "" (create) passes none, so every pending
// name is refused. The caller holds regMu.
func (d *Daemon) nameTaken(rf RoutesFile, name, dir string) *APIError {
	type use struct{ name, inbox, what string }
	var uses []use
	for n, e := range rf.Routes {
		uses = append(uses, use{n, e.Inbox, "registered"})
	}
	for _, e := range d.readPending().Inboxes {
		uses = append(uses, use{e.Route, e.Inbox, "being adopted"})
	}
	d.mu.Lock()
	for n, rt := range d.routes {
		uses = append(uses, use{n, rt.inbox, "served"})
	}
	d.mu.Unlock()
	for _, u := range uses {
		switch {
		case !strings.EqualFold(u.name, name) || (u.name == name && dir != "" && u.inbox == dir):
		case u.name == name:
			return Errf("route_exists", "route %s is %s for another inbox (%s)", name, u.what, u.inbox)
		default:
			return Errf("route_exists", "route %s differs from route %s only in case; on a case-insensitive file system they share a database", name, u.name)
		}
	}
	return nil
}

// adopt is the resumable adoption transaction (12.2): every step is
// idempotent, so a crash anywhere is finished by running it again. wait
// lets the drain wait up to DrainWait; a resume at start or from the
// scheduler checks once and leaves the rest to the next tick. A fresh run
// begins the adoption and releases its locks on any refusal, so the inbox
// stays as it was; a run that finds the marker resumes it and keeps its
// locks on a refusal, so the adoption stays pending.
func (d *Daemon) adopt(ctx context.Context, name, dir, confirm string, wait bool) (AdoptResult, *APIError) {
	if e := d.checkAdoptInbox(dir); e != nil {
		return AdoptResult{}, e
	}
	marker := filepath.Join(dir, "state", "adopting.json")
	if _, err := os.Lstat(marker); err == nil {
		m, e := d.resumeMarker(marker, name, dir, confirm)
		if e == nil {
			_, e = d.takeAdoptLocks(dir, m.Epoch)
		}
		if e != nil {
			return AdoptResult{}, e
		}
		return d.adoptPending(ctx, name, dir, m, wait)
	}
	if o, ok := ReadOwner(dir); ok {
		return AdoptResult{}, Errf("route_exists", "inbox already owned by the daemon (epoch %s)", o.Epoch)
	}
	m := adoptMarker{Phase: "invalidate", Started: d.now().Unix(), Epoch: randomHex(16), Route: name, Confirm: confirm}
	taken, e := d.takeAdoptLocks(dir, m.Epoch)
	if e == nil {
		e = d.beginAdoption(name, dir, marker, m)
	}
	if e != nil {
		for _, t := range taken {
			releaseLock(t)
		}
		return AdoptResult{}, e
	}
	return d.adoptPending(ctx, name, dir, m, wait)
}

// takeAdoptLocks is step a: the four lock folders, each with the daemon's
// owner record. Once owner.json is published (a resume after the route's
// first open), the locks carry the route's epoch, as its open expects.
// It returns the locks it took, a refusal's too, for a fresh run to
// release.
func (d *Daemon) takeAdoptLocks(dir, epoch string) ([]*inbox.Lock, *APIError) {
	lockEpoch := epoch
	if o, ok := ReadOwner(dir); ok {
		lockEpoch = o.Epoch
	}
	var taken []*inbox.Lock
	for _, l := range adoptLocks {
		lk, err := retakeDaemonLock(filepath.Join(dir, l), lockEpoch)
		if err != nil {
			return taken, Errf("adopt_locked", "%v", err)
		}
		taken = append(taken, lk)
	}
	// The input lock's record names the adoption, so a start that finds
	// it after the commit (a crash before the release) knows it is
	// leftover; the daemon's operations write random tokens instead.
	b, _ := encode(lockOwner{Owner: "daemon", Epoch: adoptLockPrefix + epoch, PID: os.Getpid(), Start: d.now().Unix()})
	if err := writeDurable(filepath.Join(dir, inbox.MutationLock, "owner.json"), append(b, '\n'), 0o600); err != nil {
		return taken, Errf("internal", "input lock: %v", err)
	}
	d.crash("adopt.locks")
	return taken, nil
}

// beginAdoption is a fresh run's prologue, under the locks: no partial
// write in flight, the pair the human confirmed, the import into a
// staging route database, then the daemon's record of the adoption and
// the inbox's marker. Until the marker is written nothing of the inbox
// has changed, so a refusal leaves the route unadopted.
func (d *Daemon) beginAdoption(name, dir, marker string, m adoptMarker) *APIError {
	// Step 2.3: no partial write in flight.
	if partial := partialWrites(dir); partial != "" {
		return Errf("adopt_locked", "a file-mode write is in flight (%s); retry when it ends", partial)
	}
	if e := confirmedPair(dir, m.Confirm); e != nil {
		return e
	}
	// Step 2.5 (section 5 step 3): the import into a staging route
	// database, before anything of the inbox changes.
	if e := d.importStaging(name, dir, m.Epoch); e != nil {
		return e
	}
	// Step 2.4: the pending list first, then the marker. The entry is the
	// daemon's record of this adoption (its route, confirmed pair and
	// epoch); an entry of an earlier run that never wrote its marker is
	// replaced.
	pl := d.readPending()
	pl.Inboxes = slices.DeleteFunc(pl.Inboxes, func(e pendingEntry) bool { return e.Inbox == dir })
	pl.Inboxes = append(pl.Inboxes, pendingEntry{Inbox: dir, Route: name, Started: m.Started, Confirm: m.Confirm, Epoch: m.Epoch})
	if err := d.writePending(pl); err != nil {
		return Errf("internal", "pending list: %v", err)
	}
	d.crash("adopt.pending")
	if err := mkdirs(dir, "state"); err != nil {
		return Errf("internal", "%v", err)
	}
	if err := writeMarker(marker, m); err != nil {
		return Errf("internal", "marker: %v", err)
	}
	d.crash("adopt.marker")
	return nil
}

// pendingRefusal is the answer when a pending adoption's pair is not the
// one the human confirmed: checksum_mismatch, naming the route and the
// recorded confirm, so the human knows which bytes finish it.
func pendingRefusal(why, name, confirm string) *APIError {
	return Errf("checksum_mismatch", "%s; the adoption of route %s stays pending until peers.approved is again the confirmed pair (confirm_pair_sha256 %s)", why, name, confirm)
}

// legacyFiles are file mode's records that go to state/legacy at step b:
// the old proof, and the approval and credential records (the import has
// copied the approval record into the route database), so the route's
// state/approval.json and credentials are only ever the daemon's own and
// the adoption always issues its own generation.
var legacyFiles = []struct{ src, base, ext, what string }{
	{"setup.json", "setup", ".json", "the old proof"},
	{"state/approval.json", "approval", ".json", "file mode's approval record"},
	{"state/credentials.json", "credentials", ".json", "file mode's credential records"},
	{"state/credentials", "credentials", "", "file mode's credential folder"},
}

// adoptPending is the resumable body, from the pair check to the commit,
// run under the locks by a fresh run after its prologue and by every
// resume.
func (d *Daemon) adoptPending(ctx context.Context, name, dir string, m adoptMarker, wait bool) (AdoptResult, *APIError) {
	marker := filepath.Join(dir, "state", "adopting.json")
	confirm := m.Confirm
	// The pair the human confirmed, on every run: an inbox writer can
	// change the registry while an adoption is pending.
	if e := confirmedPair(dir, confirm); e != nil {
		return AdoptResult{}, pendingRefusal(e.Message, name, confirm)
	}
	// b. invalidate the old proof (whatever the phase says).
	legacy := exists(filepath.Join(dir, "state", "legacy", "setup-v1.json"))
	if _, owned := ReadOwner(dir); !owned {
		for _, l := range legacyFiles {
			moved, err := moveLegacy(dir, filepath.FromSlash(l.src), l.base, l.ext)
			if err != nil {
				return AdoptResult{}, Errf("internal", "moving %s: %v", l.what, err)
			}
			legacy = legacy || (moved && l.base == "setup")
		}
	}
	if m.Phase == "invalidate" || m.Phase == "locks" {
		m.Phase = "drain"
		if err := writeMarker(marker, m); err != nil {
			return AdoptResult{}, Errf("internal", "marker: %v", err)
		}
	}
	d.crash("adopt.invalidated")
	// c. drain old file-mode processes of this inbox.
	if busy := d.drain(ctx, dir, wait); len(busy) > 0 {
		return AdoptResult{}, Errf("adopt_busy", "file-mode processes still use this inbox: %s; end them, then run adopt again", strings.Join(busy, "; "))
	}
	m.Phase = "publish"
	if err := writeMarker(marker, m); err != nil {
		return AdoptResult{}, Errf("internal", "marker: %v", err)
	}
	// d. publish: the imported route database, owner.json, the route
	// lock (open), routes.json, the journal's first checkpoint and the
	// replay (open).
	if e := d.publishStaging(name, m.Epoch); e != nil {
		return AdoptResult{}, e
	}
	if _, ok := ReadOwner(dir); !ok {
		o := d.newOwner(true)
		o.Epoch = m.Epoch
		b, _ := encode(o)
		if err := writeDurable(filepath.Join(dir, "state", "owner.json"), append(b, '\n'), 0o600); err != nil {
			return AdoptResult{}, Errf("internal", "owner: %v", err)
		}
	}
	d.crash("adopt.owner")
	rf, err := ReadRoutes(d.cfg.PrivateHome)
	if err != nil {
		return AdoptResult{}, Errf("internal", "%v", err)
	}
	if _, ok := rf.Routes[name]; !ok {
		repo := strings.TrimSuffix(filepath.Base(dir), "-"+name)
		rf.Routes[name] = RouteEntry{Inbox: dir, Repo: repo, HerdrSocket: d.cfg.HerdrSocket, Created: d.now().Unix()}
		if err := d.writeRoutes(rf); err != nil {
			return AdoptResult{}, Errf("internal", "routes.json: %v", err)
		}
	}
	d.crash("adopt.registered")
	rt := newRoute(d, name, dir)
	if err := rt.open(); err != nil {
		return AdoptResult{}, Errf("internal", "opening the adopted route: %v", err)
	}
	started := false
	defer func() {
		if !started {
			rt.closeFiles()
		}
	}()
	// Credentials are issued at adoption, file by default (12.2 step 3),
	// only for the pair the human confirmed (approve refuses any other).
	// A failure keeps the adoption pending (marker and list entry stay),
	// so the next run issues them before anything commits.
	if len(rt.creds) == 0 {
		if _, e := rt.approve(ApproveRequest{SHA256: confirm, Interaction: "adopt"}); e != nil {
			if e.Code == "checksum_mismatch" || confirmedPair(dir, confirm) != nil {
				return AdoptResult{}, pendingRefusal("the registry changed before the credentials were issued", name, confirm)
			}
			return AdoptResult{}, Errf("internal", "credentials at adoption: %s; the adoption stays pending", e.Message)
		}
	}
	// e. commit: the marker goes, then the pending entry, then the input
	// lock is released; the other three stay for the route's life.
	if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return AdoptResult{}, Errf("internal", "marker: %v", err)
	}
	_ = syncDir(filepath.Join(dir, "state"))
	d.crash("adopt.committed")
	pl := d.readPending()
	pl.Inboxes = slices.DeleteFunc(pl.Inboxes, func(e pendingEntry) bool { return e.Inbox == dir })
	if err := d.writePending(pl); err != nil {
		d.logf("route %s: pending list: %v; the next start clears it", name, err)
	}
	d.crash("adopt.unlisted")
	if err := releaseInputLock(dir); err != nil {
		d.logf("route %s: %v; the next start releases it", name, err)
	}
	started = true
	rt.start()
	res := AdoptResult{Route: name, Epoch: rt.epoch, LastMessageID: fmt.Sprintf("m%012d", rt.lastID), LegacyProof: legacy}
	d.mu.Lock()
	delete(d.held, name)
	d.routes[name] = rt
	d.mu.Unlock()
	d.view.attach(rt)
	return res, nil
}

// resumeMarker checks an inbox's adoption marker before a resume. Any
// writer of the inbox can make the marker, so it is only a hint: the
// resume follows the daemon's own record of the adoption in the private
// home (written before the marker), which must list this inbox as route
// name with the marker's epoch and confirmed pair; a request's pair, when
// given, must be that pair too. The epoch is randomHex(16)'s form.
func (d *Daemon) resumeMarker(marker, name, dir, confirm string) (adoptMarker, *APIError) {
	refuse := func(why string) (adoptMarker, *APIError) {
		return adoptMarker{}, Errf("bad_request", "%s is not an adoption this daemon started (%s); remove it to adopt again", marker, why)
	}
	b, err := readSmall(marker, 4096)
	if err != nil {
		return refuse(err.Error())
	}
	var m adoptMarker
	if err := strictDecode(b, &m); err != nil {
		return refuse("malformed: " + err.Error())
	}
	pl := d.readPending()
	i := slices.IndexFunc(pl.Inboxes, func(e pendingEntry) bool { return e.Inbox == dir })
	if i < 0 {
		return refuse("the daemon's pending list does not name this inbox")
	}
	e := pl.Inboxes[i]
	switch {
	case !validEpoch(e.Epoch) || e.Epoch != m.Epoch:
		return refuse("its epoch is not the pending adoption's")
	case e.Route != m.Route:
		return refuse("the pending adoption is of route " + e.Route)
	case e.Confirm != m.Confirm:
		return refuse("its confirmed pair is not the pending adoption's")
	case e.Route != name || (confirm != "" && confirm != e.Confirm):
		// The daemon's own pending adoption: the request differs, and the
		// marker must stay so the daemon can finish it.
		return adoptMarker{}, Errf("bad_request", "an adoption of route %s with confirm_pair_sha256 %s is pending for this inbox; run adopt again with that route and pair to finish it", e.Route, e.Confirm)
	}
	return m, nil
}

// moveLegacy moves file mode's file or folder src (relative to the inbox)
// to state/legacy/<base>-v1<ext>, or the first free <base>-v1.<n><ext>;
// it reports whether there was one to move.
func moveLegacy(dir, src, base, ext string) (bool, error) {
	from := filepath.Join(dir, src)
	if !exists(from) {
		return false, nil
	}
	if err := mkdirs(dir, "state/legacy"); err != nil {
		return false, err
	}
	dst := filepath.Join(dir, "state", "legacy", base+"-v1"+ext)
	for n := 2; exists(dst); n++ {
		dst = filepath.Join(dir, "state", "legacy", base+"-v1."+strconv.Itoa(n)+ext)
	}
	return true, renameDurable(from, dst)
}

// confirmedPair checks that the inbox's registry is an approved pair with
// no pending change whose peers.approved is the pair the human confirmed.
// The checksum read is bounded, follows no link and never blocks on a
// FIFO that replaced the checked file, since adoption holds regMu and the
// inbox locks.
func confirmedPair(dir, confirm string) *APIError {
	if _, ok := (&inbox.Root{Dir: dir}).Registry(); !ok {
		return Errf("checksum_mismatch", "the registry is not an approved pair with no pending change")
	}
	if pairChecked != nil {
		pairChecked(dir)
	}
	ab, ok := inbox.ReadStrict(dir, filepath.Join(dir, "peers.approved"))
	if !ok || sha256Hex(ab) != confirm {
		return Errf("checksum_mismatch", "confirm_pair_sha256 does not match peers.approved")
	}
	return nil
}

// pairChecked runs in confirmedPair between the registry check and the
// checksum read; tests swap peers.approved there.
var pairChecked func(dir string)

func writeMarker(p string, m adoptMarker) error {
	b, _ := encode(m)
	return writeDurable(p, append(b, '\n'), 0o600)
}

// partialWrites names a file-mode write in flight: an entry in a
// mailbox's tmp/ that is not a released claim's leftover.
func partialWrites(dir string) string {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if !e.IsDir() || !inbox.NameOK(e.Name()) {
			continue
		}
		tmp, _ := os.ReadDir(filepath.Join(dir, e.Name(), "tmp"))
		for _, t := range tmp {
			if !strings.HasPrefix(t.Name(), ".released.") {
				return e.Name() + "/tmp/" + t.Name()
			}
		}
	}
	return ""
}

// adoptLockPrefix marks the input lock's owner record while an adoption
// holds it.
const adoptLockPrefix = "adopt-"

// releaseInputLock releases herdr-mutation.lock when an adoption holds
// it (the daemon's operations hold it under a random token). The folder is first renamed away, so the lock is
// free at once and a leftover (an interrupted write inside it) never
// looks like another owner's lock; a lock with another owner is never
// touched.
func releaseInputLock(dir string) error {
	lk := filepath.Join(dir, inbox.MutationLock)
	if o, ok := daemonLockOwner(lk); !ok || !strings.HasPrefix(o.Epoch, adoptLockPrefix) {
		return nil
	}
	gone := filepath.Join(dir, ".released."+inbox.MutationLock+"."+randomHex(8))
	if err := os.Rename(lk, gone); err != nil {
		return fmt.Errorf("releasing the input lock: %v", err)
	}
	if err := os.RemoveAll(gone); err != nil {
		return fmt.Errorf("removing the released input lock %s: %v", gone, err)
	}
	return nil
}

// adoptionLockLeft reports an input lock left by a committed adoption (a
// crash between the commit and the release).
func adoptionLockLeft(dir string) bool {
	_, owned := ReadOwner(dir)
	l, locked := daemonLockOwner(filepath.Join(dir, inbox.MutationLock))
	return owned && locked && strings.HasPrefix(l.Epoch, adoptLockPrefix)
}

// resumeAdoptions runs the transaction for every pending adoption (4.4
// step 0): those on the daemon's pending list and every registered route
// with a marker. It never waits on a drain: a route still busy stays held
// adopting, and the scheduler tries again on its next tick, so one paused
// writer delays neither the start nor the other routes.
func (d *Daemon) resumeAdoptions() {
	pl := d.readPending()
	seen := map[string]bool{}
	todo := append([]pendingEntry{}, pl.Inboxes...)
	if rf, err := ReadRoutes(d.cfg.PrivateHome); err == nil {
		for n, e := range rf.Routes {
			// A marker, or an input lock an earlier daemon left after its
			// adoption committed (a crash before the release).
			if pendingAdoption(e.Inbox) || adoptionLockLeft(e.Inbox) {
				todo = append(todo, pendingEntry{Inbox: e.Inbox, Route: n})
			}
		}
	}
	for _, e := range todo {
		if seen[e.Inbox] {
			continue
		}
		seen[e.Inbox] = true
		if _, owned := ReadOwner(e.Inbox); owned && !pendingAdoption(e.Inbox) {
			// The adoption committed before its pending entry and the
			// input lock were cleared: finish that.
			d.regMu.Lock()
			pl := d.readPending()
			pl.Inboxes = slices.DeleteFunc(pl.Inboxes, func(x pendingEntry) bool { return x.Inbox == e.Inbox })
			_ = d.writePending(pl)
			d.regMu.Unlock()
			if err := releaseInputLock(e.Inbox); err != nil {
				d.logf("route %s: %v", e.Route, err)
			}
			d.mu.Lock()
			delete(d.held, e.Route)
			d.mu.Unlock()
			continue
		}
		d.mu.Lock()
		_, served := d.routes[e.Route]
		d.held[e.Route] = "adopting"
		d.mu.Unlock()
		if served {
			continue
		}
		d.regMu.Lock()
		_, err := d.adopt(context.Background(), e.Route, e.Inbox, e.Confirm, false)
		d.regMu.Unlock()
		if err != nil {
			d.logf("route %s: adoption still incomplete: %s", e.Route, err.Message)
		}
	}
}

// Proc is one process of the user, for the adoption drain.
type Proc struct {
	PID int
	// Exe is the executable's path, when the system reports it.
	Exe string
	// Args is the argument vector; when nil, Command is the command line.
	Args    []string
	Command string
	// Env is the environment, or nil when it cannot be read (then the
	// process counts as matching: fail closed).
	Env []string
	// Unreadable: the process's arguments cannot be read, so whether it
	// runs a channel command is unknown; it counts as busy (fail closed).
	Unreadable bool
}

func (p Proc) commandLine() string {
	if p.Args != nil {
		return strings.Join(p.Args, " ")
	}
	return p.Command
}

// label names a busy process for adopt_busy and the log: its pid, the
// executable's base name and the channel action, never the other
// arguments (a claim token) or the environment.
func (p Proc) label() string {
	if p.Unreadable {
		return fmt.Sprintf("pid %d (arguments unreadable)", p.PID)
	}
	f := p.Args
	if f == nil {
		f = strings.Fields(p.Command)
	}
	parts := []string{fmt.Sprintf("pid %d", p.PID)}
	for i, a := range f {
		base := filepath.Base(a)
		switch {
		case i == 0:
			parts = append(parts, base)
		case a == "message" && i+1 < len(f):
			return strings.Join(append(parts, "message", clip(f[i+1], 32)), " ")
		case strings.HasSuffix(base, ".sh"):
			return strings.Join(append(parts, base), " ")
		}
	}
	return strings.Join(parts, " ")
}

// channelCommand reports whether a command line runs a file-mode channel
// command: herdsman message … of any version, or a channel script.
func channelCommand(cmd string) bool {
	if strings.Contains(cmd, "herdsman message") || strings.Contains(cmd, "herdsman-message") {
		return true
	}
	for _, s := range []string{"message.sh", "message-", "card-watch.sh", "resume-goal-when-idle.sh"} {
		if strings.Contains(cmd, s) {
			return true
		}
	}
	return false
}

// drain returns the other processes that run a file-mode channel command
// on this inbox; with wait it checks again each second up to DrainWait
// while any remain.
func (d *Daemon) drain(ctx context.Context, dir string, wait bool) []string {
	tries := 1
	if wait {
		tries = int(d.cfg.DrainWait/time.Second) + 1
	}
	for try := 1; ; try++ {
		var busy []string
		procs, err := d.procs()
		if err != nil {
			return []string{"the process list cannot be read: " + err.Error()}
		}
		for _, p := range procs {
			if p.PID == os.Getpid() {
				continue
			}
			if p.Unreadable || (channelCommand(p.commandLine()) && (p.Env == nil || slices.Contains(p.Env, "HERDSMAN_INBOX="+dir))) {
				busy = append(busy, p.label())
			}
		}
		if len(busy) == 0 || try >= tries || ctx.Err() != nil {
			return busy
		}
		d.sleep(time.Second)
	}
}

func (d *Daemon) procs() ([]Proc, error) {
	if d.cfg.Procs != nil {
		return d.cfg.Procs()
	}
	return systemProcs()
}

// crash is the daemon-level crash hook (tests only).
func (d *Daemon) crash(point string) {
	if d.cfg.Crash != nil {
		d.cfg.Crash(point)
	}
}
