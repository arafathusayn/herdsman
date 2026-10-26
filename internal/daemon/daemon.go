package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
)

// Config is a daemon's settings. Tests inject the clock, the crash hook,
// the Herdr socket and the process lister.
type Config struct {
	// PrivateHome is the folder no sandboxed agent can reach; the daemon's
	// files and the inboxes live under it.
	PrivateHome string
	// Home is the user's home folder, where the account lock lives.
	Home string
	// Version is the daemon's build version (5.6).
	Version string
	// Executable is the installed herdsman binary (setup proof, upgrade
	// check).
	Executable string
	// HerdrSocket is the default Herdr socket of new routes.
	HerdrSocket string
	// LockWait is the input lock's tries (default 6).
	LockWait int
	// HeartbeatEvery is the scheduler's period (default 60 s).
	HeartbeatEvery time.Duration

	Now   func() time.Time
	Sleep func(time.Duration)
	Log   func(string)
	// Crash is a test hook called at injected crash points.
	Crash func(point string)
	// Keychain stores keychain credentials (nil: the macOS security tool).
	Keychain Keychain
	// Procs lists the user's processes for the adoption drain (nil: the
	// system's).
	Procs func() ([]Proc, error)
	// DrainWait bounds the adoption drain (default 60 s).
	DrainWait time.Duration
}

// Daemon is one running daemon.
type Daemon struct {
	cfg Config

	mu        sync.Mutex
	regMu     sync.Mutex // serializes routes.json changes
	routes    map[string]*Route
	held      map[string]string // route to why it is not served
	credIndex map[string]credRef
	views     map[*subscriber]struct{}
	streams   map[string]int // per party key, "view", "all"
	sem       chan struct{}
	upgrading bool
	started   time.Time
	acct      *os.File
	ln        net.Listener
	srv       *http.Server
	done      chan struct{}
	stopOnce  sync.Once
}

type credRef struct{ route, name string }

// Paths of the daemon's own files.
func (d *Daemon) daemonDir() string  { return filepath.Join(d.cfg.PrivateHome, "daemon") }
func (d *Daemon) runDir() string     { return filepath.Join(d.cfg.PrivateHome, "run") }
func (d *Daemon) routesFile() string { return filepath.Join(d.daemonDir(), "routes.json") }

// SocketPath is the daemon socket of a private home (3.3).
func SocketPath(privateHome string) string { return filepath.Join(privateHome, "run", "daemon.sock") }

// AccountLockPath is the per-account singleton lock (3.4, 3.6).
func AccountLockPath(home string) string { return filepath.Join(home, ".herdsman-daemon.lock") }

// New makes a daemon with defaults filled in.
func New(cfg Config) *Daemon {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = time.Sleep
	}
	if cfg.LockWait == 0 {
		cfg.LockWait = 6
	}
	if cfg.HeartbeatEvery == 0 {
		cfg.HeartbeatEvery = 60 * time.Second
	}
	if cfg.DrainWait == 0 {
		cfg.DrainWait = 60 * time.Second
	}
	return &Daemon{cfg: cfg, routes: map[string]*Route{}, held: map[string]string{}, credIndex: map[string]credRef{},
		views: map[*subscriber]struct{}{}, streams: map[string]int{}, sem: make(chan struct{}, 32), done: make(chan struct{})}
}

func (d *Daemon) now() time.Time                    { return d.cfg.Now() }
func (d *Daemon) sleep(t time.Duration)             { d.cfg.Sleep(t) }
func (d *Daemon) herdr(socket string) *herdr.Client { return herdr.New(socket) }

// AccountLock is the content of the account lock file (5.7).
type AccountLock struct {
	PID         int    `json:"pid"`
	Version     string `json:"version"`
	Start       int64  `json:"start"`
	PrivateHome string `json:"private_home"`
	Socket      string `json:"socket"`
}

// ErrRunning is a second daemon of this account (3.6).
type ErrRunning struct{ Holder AccountLock }

func (e *ErrRunning) Error() string {
	return fmt.Sprintf("another daemon runs for this user (pid %d, version %s, socket %s); stop it with herdsman daemon stop, or use it",
		e.Holder.PID, e.Holder.Version, e.Holder.Socket)
}

// ReadAccountLock reads the account lock's content (informative only).
func ReadAccountLock(home string) (AccountLock, bool) {
	b, err := readSmall(AccountLockPath(home), 4096)
	if err != nil {
		return AccountLock{}, false
	}
	var l AccountLock
	if json.Unmarshal(b, &l) != nil || l.PID <= 0 {
		return AccountLock{}, false
	}
	return l, true
}

// AccountLockHeld reports whether some process holds the account lock.
func AccountLockHeld(home string) bool {
	f, err := os.OpenFile(AccountLockPath(home), os.O_RDONLY, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return true
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// takeAccountLock takes the per-account singleton (3.6).
func (d *Daemon) takeAccountLock() error {
	p := AccountLockPath(d.cfg.Home)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		l, _ := ReadAccountLock(d.cfg.Home)
		return &ErrRunning{Holder: l}
	}
	b, _ := encode(AccountLock{PID: os.Getpid(), Version: d.cfg.Version, Start: d.now().Unix(), PrivateHome: d.cfg.PrivateHome, Socket: SocketPath(d.cfg.PrivateHome)})
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt(append(b, '\n'), 0)
		_ = f.Sync()
	}
	d.acct = f
	return nil
}

// RouteEntry is one route of routes.json (5.7).
type RouteEntry struct {
	Inbox       string `json:"inbox"`
	Repo        string `json:"repo"`
	HerdrSocket string `json:"herdr_socket"`
	Created     int64  `json:"created"`
}

// RoutesFile is routes.json.
type RoutesFile struct {
	Version int                   `json:"version"`
	Routes  map[string]RouteEntry `json:"routes"`
}

// ReadRoutes reads a private home's routes.json (empty when absent).
func ReadRoutes(privateHome string) (RoutesFile, error) {
	rf := RoutesFile{Version: 1, Routes: map[string]RouteEntry{}}
	b, err := readSmall(filepath.Join(privateHome, "daemon", "routes.json"), 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return rf, nil
	}
	if err != nil {
		return rf, err
	}
	if err := strictDecode(b, &rf); err != nil || rf.Version != 1 {
		return rf, fmt.Errorf("invalid routes.json: %v", err)
	}
	if rf.Routes == nil {
		rf.Routes = map[string]RouteEntry{}
	}
	return rf, nil
}

func (d *Daemon) writeRoutes(rf RoutesFile) error {
	b, _ := encode(rf)
	return writeDurable(d.routesFile(), append(b, '\n'), 0o600)
}

// DaemonInfo is daemon.json (3.4).
type DaemonInfo struct {
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
	PID      int    `json:"pid"`
	Start    int64  `json:"start"`
	Socket   string `json:"socket"`
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// validName checks a route or repository name: a path segment.
func validName(s string) bool { return nameRe.MatchString(s) && !strings.Contains(s, "..") }

// checkPrivateHome creates (mode 700) and checks the private home.
func checkPrivateHome(p string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("private home %s must be absolute", p)
	}
	if err := os.MkdirAll(p, 0o700); err != nil {
		return err
	}
	st, err := os.Lstat(p)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("private home %s is not a folder", p)
	}
	if st.Mode().Perm() != 0o700 {
		return fmt.Errorf("private home %s must have mode 700", p)
	}
	if s, ok := st.Sys().(*syscall.Stat_t); ok && int(s.Uid) != os.Getuid() {
		return fmt.Errorf("private home %s must be owned by this user", p)
	}
	if len(SocketPath(p)) > 103 {
		return fmt.Errorf("socket path %s is longer than the 104-byte limit; use a shorter private home", SocketPath(p))
	}
	return nil
}

// Start takes the singleton, opens every route and listens on the socket.
// Serve runs until Stop.
func (d *Daemon) Start() error {
	if err := checkPrivateHome(d.cfg.PrivateHome); err != nil {
		return err
	}
	if err := d.takeAccountLock(); err != nil {
		return err
	}
	for _, dir := range []string{d.daemonDir(), d.runDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		_ = os.Chmod(dir, 0o700)
	}
	d.started = d.now()
	b, _ := encode(DaemonInfo{Version: d.cfg.Version, Protocol: Protocol, PID: os.Getpid(), Start: d.started.Unix(), Socket: SocketPath(d.cfg.PrivateHome)})
	if err := writeDurable(filepath.Join(d.daemonDir(), "daemon.json"), append(b, '\n'), 0o600); err != nil {
		return err
	}
	d.touchHeartbeat()
	d.resumeAdoptions()
	rf, err := ReadRoutes(d.cfg.PrivateHome)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(rf.Routes))
	for n := range rf.Routes {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		d.mu.Lock()
		_, served := d.routes[n]
		d.mu.Unlock()
		if !served {
			d.openRoute(n, rf.Routes[n])
		}
	}
	sock := SocketPath(d.cfg.PrivateHome)
	_ = os.Remove(sock) // stale: this process holds the account lock
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		_ = ln.Close()
		return err
	}
	d.ln = &peerListener{Listener: ln}
	d.srv = &http.Server{Handler: d.handler(), ReadHeaderTimeout: 10 * time.Second}
	return nil
}

// Serve answers requests until Stop; it runs the scheduler too.
func (d *Daemon) Serve(ctx context.Context) error {
	go d.scheduler(ctx)
	errc := make(chan error, 1)
	go func() { errc <- d.srv.Serve(d.ln) }()
	select {
	case err := <-errc:
		d.Stop("stop")
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		d.Stop("stop")
		<-errc
		return nil
	case <-d.done:
		<-errc
		return nil
	}
}

// Stop drains: shutdown frames on every stream, the routes stop, the
// socket closes, the account lock is freed.
func (d *Daemon) Stop(reason string) {
	d.stopOnce.Do(func() {
		d.mu.Lock()
		d.upgrading = true
		routes := make([]*Route, 0, len(d.routes))
		for _, r := range d.routes {
			routes = append(routes, r)
		}
		for s := range d.views {
			s.push(frame{ev: &Event{Type: "shutdown", Reason: reason}})
			s.close()
		}
		d.mu.Unlock()
		for _, r := range routes {
			_ = r.do(context.Background(), func() *APIError {
				for s := range r.subs {
					s.push(frame{ev: &Event{Type: "shutdown", Reason: reason}})
					s.close()
				}
				return nil
			})
			r.stop()
		}
		if d.srv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = d.srv.Shutdown(ctx)
			cancel()
		}
		_ = os.Remove(SocketPath(d.cfg.PrivateHome))
		if d.acct != nil {
			_ = d.acct.Close()
		}
		close(d.done)
	})
}

// Done is closed when the daemon has stopped.
func (d *Daemon) Done() <-chan struct{} { return d.done }

// openRoute opens and starts a registered route; a failure holds it
// (health shows why) and the scheduler retries.
func (d *Daemon) openRoute(name string, e RouteEntry) {
	if pendingAdoption(e.Inbox) {
		d.mu.Lock()
		d.held[name] = "adopting"
		d.mu.Unlock()
		return
	}
	r := newRoute(d, name, e.Inbox)
	if err := r.open(); err != nil {
		d.logf("route %s held: %v", name, err)
		d.mu.Lock()
		d.held[name] = err.Error()
		d.mu.Unlock()
		return
	}
	r.start()
	d.mu.Lock()
	delete(d.held, name)
	d.routes[name] = r
	views := make([]*subscriber, 0, len(d.views))
	for s := range d.views {
		views = append(views, s)
	}
	d.mu.Unlock()
	for _, s := range views {
		d.attachView(r, s)
	}
}

// route returns a served route.
func (d *Daemon) route(name string) (*Route, *APIError) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r, ok := d.routes[name]; ok {
		return r, nil
	}
	if why, ok := d.held[name]; ok {
		return nil, Errf("route_unowned", "route %s is not served yet: %s", name, why)
	}
	return nil, Errf("unknown_route", "route %s is not registered", name)
}

// routeEntry is a route's routes.json entry.
func (d *Daemon) routeEntry(name string) (RouteEntry, bool) {
	rf, err := ReadRoutes(d.cfg.PrivateHome)
	if err != nil {
		return RouteEntry{}, false
	}
	e, ok := rf.Routes[name]
	return e, ok
}

// herdrFor is the Herdr client of a route.
func (d *Daemon) herdrFor(name string) *herdr.Client {
	e, _ := d.routeEntry(name)
	sock := e.HerdrSocket
	if sock == "" {
		sock = d.cfg.HerdrSocket
	}
	return d.herdr(sock)
}

// indexCredentials replaces a route's entries in the credential index.
func (d *Daemon) indexCredentials(route string, creds map[string]credential) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, v := range d.credIndex {
		if v.route == route {
			delete(d.credIndex, k)
		}
	}
	for n, c := range creds {
		if c.SHA256 != "" {
			d.credIndex[c.SHA256] = credRef{route: route, name: n}
		}
	}
}

// touchHeartbeat touches daemon/heartbeat (3.4).
func (d *Daemon) touchHeartbeat() {
	p := filepath.Join(d.daemonDir(), "heartbeat")
	now := d.now()
	if err := os.Chtimes(p, now, now); err != nil {
		_ = os.WriteFile(p, []byte(strconv.FormatInt(now.Unix(), 10)+"\n"), 0o600)
		_ = os.Chtimes(p, now, now)
	}
}

// exeStamp is the executable's size, mtime and inode (5.6 upgrade check).
func exeStamp(p string) string {
	st, err := os.Stat(p)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d:%d", st.Size(), st.ModTime().UnixNano(), inode(st))
}

// scheduler is the daemon's one timer (3.2): the heartbeat, the
// executable check and the retry of held routes, every HeartbeatEvery.
func (d *Daemon) scheduler(ctx context.Context) {
	stamp := exeStamp(d.cfg.Executable)
	t := time.NewTimer(d.cfg.HeartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.done:
			return
		case <-t.C:
		}
		d.touchHeartbeat()
		if s := exeStamp(d.cfg.Executable); stamp != "" && s != stamp {
			d.logf("the executable changed; stopping for the supervisor to start the new binary")
			go d.Stop("upgrade")
			return
		}
		d.retryHeld()
		t.Reset(d.cfg.HeartbeatEvery)
	}
}

// retryHeld retries routes that could not be opened.
func (d *Daemon) retryHeld() {
	d.resumeAdoptions()
	d.mu.Lock()
	var names []string
	for n := range d.held {
		names = append(names, n)
	}
	d.mu.Unlock()
	sort.Strings(names)
	for _, n := range names {
		d.mu.Lock()
		_, served := d.routes[n]
		d.mu.Unlock()
		if e, ok := d.routeEntry(n); ok && !served {
			d.openRoute(n, e)
		}
	}
}

// herdrSocketPath is the default Herdr session's socket.
func herdrSocketPath() (string, error) { return herdr.SocketPath("") }
