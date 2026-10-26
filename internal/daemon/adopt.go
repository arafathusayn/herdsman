package daemon

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// AdoptRequest is POST /v1/routes/{route}/adopt.
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

func (d *Daemon) adoptRoute(w http.ResponseWriter, r *http.Request) *APIError {
	name := r.PathValue("route")
	var q AdoptRequest
	if e := decode(r, &q); e != nil {
		return e
	}
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
	d.mu.Lock()
	_, served := d.routes[name]
	d.mu.Unlock()
	if served {
		return Errf("route_exists", "route %s is already served", name)
	}
	res, e := d.adopt(r.Context(), name, q.Inbox, q.ConfirmPairSHA256)
	if e != nil {
		return e
	}
	writeJSON(w, 200, res)
	return nil
}

// adopt is the resumable adoption transaction (12.2): every step is
// idempotent, so a crash anywhere is finished by running it again.
func (d *Daemon) adopt(ctx context.Context, name, dir, confirm string) (AdoptResult, *APIError) {
	if e := d.checkAdoptInbox(dir); e != nil {
		return AdoptResult{}, e
	}
	marker := filepath.Join(dir, "state", "adopting.json")
	m := adoptMarker{Phase: "locks", Started: d.now().Unix(), Epoch: randomHex(16), Route: name, Confirm: confirm}
	resuming := false
	if b, err := readSmall(marker, 4096); err == nil {
		var old adoptMarker
		if strictDecode(b, &old) == nil && len(old.Epoch) == 32 {
			m, resuming = old, true
			if confirm == "" {
				confirm = m.Confirm
			}
		}
	}
	if o, ok := ReadOwner(dir); ok && !resuming {
		return AdoptResult{}, Errf("route_exists", "inbox already owned by the daemon (epoch %s)", o.Epoch)
	}
	// a. the four lock folders, each with the daemon's owner record.
	var taken []*inbox.Lock
	for _, l := range adoptLocks {
		lk, err := retakeDaemonLock(filepath.Join(dir, l), m.Epoch)
		if err != nil {
			for _, t := range taken {
				if !resuming {
					releaseLock(t)
				}
			}
			return AdoptResult{}, Errf("adopt_locked", "%v", err)
		}
		taken = append(taken, lk)
	}
	d.crash("adopt.locks")
	if !resuming {
		// Step 2.3: no partial write in flight, and the pair the human
		// confirmed.
		if partial := partialWrites(dir); partial != "" {
			for _, t := range taken {
				releaseLock(t)
			}
			return AdoptResult{}, Errf("adopt_locked", "a file-mode write is in flight (%s); retry when it ends", partial)
		}
		rt := &inbox.Root{Dir: dir}
		if _, ok := rt.Registry(); !ok {
			for _, t := range taken {
				releaseLock(t)
			}
			return AdoptResult{}, Errf("checksum_mismatch", "the registry is not an approved pair with no pending change")
		}
		ab, _ := os.ReadFile(filepath.Join(dir, "peers.approved"))
		if sha256Hex(ab) != confirm {
			for _, t := range taken {
				releaseLock(t)
			}
			return AdoptResult{}, Errf("checksum_mismatch", "confirm_pair_sha256 does not match peers.approved")
		}
		// Step 2.4: the pending list first, then the marker.
		pl := d.readPending()
		if !slices.ContainsFunc(pl.Inboxes, func(e pendingEntry) bool { return e.Inbox == dir }) {
			pl.Inboxes = append(pl.Inboxes, pendingEntry{Inbox: dir, Route: name, Started: m.Started})
			if err := d.writePending(pl); err != nil {
				return AdoptResult{}, Errf("internal", "pending list: %v", err)
			}
		}
		d.crash("adopt.pending")
		if err := mkdirs(dir, "state"); err != nil {
			return AdoptResult{}, Errf("internal", "%v", err)
		}
		m.Phase = "invalidate"
		if err := writeMarker(marker, m); err != nil {
			return AdoptResult{}, Errf("internal", "marker: %v", err)
		}
		d.crash("adopt.marker")
	}
	// b. invalidate the old proof (whatever the phase says).
	legacy := exists(filepath.Join(dir, "state", "legacy", "setup-v1.json"))
	if _, owned := ReadOwner(dir); !owned && exists(filepath.Join(dir, "setup.json")) {
		if err := mkdirs(dir, "state/legacy"); err != nil {
			return AdoptResult{}, Errf("internal", "%v", err)
		}
		dst := filepath.Join(dir, "state", "legacy", "setup-v1.json")
		for n := 2; exists(dst); n++ {
			dst = filepath.Join(dir, "state", "legacy", "setup-v1."+strconv.Itoa(n)+".json")
		}
		if err := renameDurable(filepath.Join(dir, "setup.json"), dst); err != nil {
			return AdoptResult{}, Errf("internal", "moving the old proof: %v", err)
		}
		legacy = true
	}
	if m.Phase == "invalidate" || m.Phase == "locks" {
		m.Phase = "drain"
		if err := writeMarker(marker, m); err != nil {
			return AdoptResult{}, Errf("internal", "marker: %v", err)
		}
	}
	d.crash("adopt.invalidated")
	// c. drain old file-mode processes of this inbox.
	if busy := d.drain(ctx, dir); len(busy) > 0 {
		return AdoptResult{}, Errf("adopt_busy", "file-mode processes still use this inbox: %s; end them, then run adopt again", strings.Join(busy, "; "))
	}
	m.Phase = "publish"
	if err := writeMarker(marker, m); err != nil {
		return AdoptResult{}, Errf("internal", "marker: %v", err)
	}
	// d. publish: owner.json, the route lock (open), routes.json, the
	// journal's first checkpoint and the replay (open).
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
	// Credentials are issued at adoption, file by default (12.2 step 3).
	if pb, ok := rt.registryBytes("peers"); ok && len(rt.creds) == 0 {
		if _, e := rt.approve(ApproveRequest{SHA256: sha256Hex(pb), Interaction: "adopt"}); e != nil {
			d.logf("route %s: credentials at adoption: %s", name, e.Message)
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
	_ = d.writePending(pl)
	releaseLock(inbox.Adopt(filepath.Join(dir, inbox.MutationLock)))
	started = true
	rt.start()
	res := AdoptResult{Route: name, Epoch: rt.epoch, LastMessageID: fmt.Sprintf("m%012d", rt.lastID), LegacyProof: legacy}
	d.mu.Lock()
	delete(d.held, name)
	d.routes[name] = rt
	views := make([]*subscriber, 0, len(d.views))
	for s := range d.views {
		views = append(views, s)
	}
	d.mu.Unlock()
	for _, s := range views {
		d.attachView(rt, s)
	}
	return res, nil
}

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

// resumeAdoptions runs the transaction for every pending adoption (4.4
// step 0): those on the daemon's pending list and every registered route
// with a marker.
func (d *Daemon) resumeAdoptions() {
	pl := d.readPending()
	seen := map[string]bool{}
	todo := append([]pendingEntry{}, pl.Inboxes...)
	if rf, err := ReadRoutes(d.cfg.PrivateHome); err == nil {
		for n, e := range rf.Routes {
			if pendingAdoption(e.Inbox) {
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
			if _, ok := daemonLockOwner(filepath.Join(e.Inbox, inbox.MutationLock)); ok {
				releaseLock(inbox.Adopt(filepath.Join(e.Inbox, inbox.MutationLock)))
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
		_, err := d.adopt(context.Background(), e.Route, e.Inbox, "")
		d.regMu.Unlock()
		if err != nil {
			d.logf("route %s: adoption still incomplete: %s", e.Route, err.Message)
		}
	}
}

// Proc is one process of the user, for the adoption drain.
type Proc struct {
	PID     int
	Command string
	// Env is the environment, or nil when it cannot be read (then the
	// process counts as matching: fail closed).
	Env []string
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

// drain waits up to DrainWait while another process runs a file-mode
// channel command on this inbox, and returns those that remain.
func (d *Daemon) drain(ctx context.Context, dir string) []string {
	tries := int(d.cfg.DrainWait/time.Second) + 1
	for try := 1; ; try++ {
		var busy []string
		procs, err := d.procs()
		if err != nil {
			return []string{"the process list cannot be read: " + err.Error()}
		}
		for _, p := range procs {
			if p.PID == os.Getpid() || !channelCommand(p.Command) {
				continue
			}
			match := p.Env == nil || slices.Contains(p.Env, "HERDSMAN_INBOX="+dir)
			if match {
				busy = append(busy, fmt.Sprintf("pid %d %s", p.PID, clip(p.Command, 120)))
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

// systemProcs lists the user's processes with their environments: /proc
// on Linux, ps -E on macOS.
func systemProcs() ([]Proc, error) {
	if runtime.GOOS == "linux" {
		return procfsProcs()
	}
	out, err := exec.Command("ps", "-ww", "-E", "-U", strconv.Itoa(os.Getuid()), "-o", "pid=,command=").Output()
	if err != nil {
		return nil, err
	}
	var procs []Proc
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		p := Proc{PID: pid, Command: strings.Join(f[1:], " ")}
		for _, x := range f[1:] {
			if strings.Contains(x, "=") {
				p.Env = append(p.Env, x)
			}
		}
		procs = append(procs, p)
	}
	return procs, nil
}

func procfsProcs() ([]Proc, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var procs []Proc
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		st, err := os.Stat("/proc/" + e.Name())
		if s, ok := st.Sys().(*syscall.Stat_t); err != nil || !ok || int(s.Uid) != os.Getuid() {
			continue
		}
		cmd, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil {
			continue
		}
		p := Proc{PID: pid, Command: strings.TrimSpace(strings.ReplaceAll(string(cmd), "\x00", " "))}
		if env, err := os.ReadFile("/proc/" + e.Name() + "/environ"); err == nil {
			p.Env = strings.Split(strings.TrimRight(string(env), "\x00"), "\x00")
		}
		procs = append(procs, p)
	}
	return procs, nil
}

// crash is the daemon-level crash hook (tests only).
func (d *Daemon) crash(point string) {
	if d.cfg.Crash != nil {
		d.cfg.Crash(point)
	}
}
