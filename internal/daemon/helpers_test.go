package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/inbox"
)

const testVersion = "1.0.0"

// clock is a settable test clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Unix(1_800_000_000, 0)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// shortDir is a temporary folder with a short path (socket paths are
// limited to 104 bytes on macOS), mode 700.
func shortDir(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "hd")
	if err != nil {
		t.Fatal(err)
	}
	dir, _ = filepath.EvalSymlinks(dir)
	_ = os.Chmod(dir, 0o700)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// harness is a running daemon on a socket with a fake Herdr server.
type harness struct {
	t     testing.TB
	dir   string
	ph    string
	home  string
	srv   *herdrtest.Server
	fx    *herdrtest.Fixture
	clock *clock
	// clients made by client(), for closeClients.
	clients []*Client
	d       *Daemon
	cfg     Config
	rules   string
	cancel  context.CancelFunc
	served  chan error
}

func newHarness(t testing.TB) *harness {
	t.Helper()
	dir := shortDir(t)
	h := &harness{t: t, dir: dir, ph: filepath.Join(dir, "p"), home: filepath.Join(dir, "h"), clock: newClock()}
	_ = os.MkdirAll(h.home, 0o700)
	h.srv = herdrtest.Start(t)
	h.fx = herdrtest.Serve(h.srv)
	h.rules = filepath.Join(dir, "policy.sb")
	_ = os.WriteFile(h.rules, []byte("(allow default)\n"), 0o600)
	bin := filepath.Join(dir, "herdsman-bin")
	_ = os.WriteFile(bin, []byte("binary"), 0o700)
	h.cfg = Config{PrivateHome: h.ph, Home: h.home, Version: testVersion, Executable: bin, HerdrSocket: h.srv.Socket,
		Now: h.clock.Now, Sleep: func(time.Duration) {}, Log: func(s string) { t.Log(s) }, LockWait: 2, DrainWait: time.Millisecond,
		// No file-mode processes unless a test names some: the machine's own
		// would make adoption busy.
		Procs: func() ([]Proc, error) { return nil, nil }}
	h.start()
	return h
}

func (h *harness) start() {
	h.t.Helper()
	h.d = New(h.cfg)
	if err := h.d.Start(); err != nil {
		h.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.served = make(chan error, 1)
	d := h.d
	go func() { h.served <- d.Serve(ctx) }()
	h.t.Cleanup(h.stop)
}

func (h *harness) stop() {
	if h.cancel != nil {
		h.cancel()
		<-h.served
		h.cancel = nil
	}
}

// restart stops the daemon and starts a new one on the same files.
func (h *harness) restart() {
	h.t.Helper()
	h.stop()
	h.start()
}

func (h *harness) client() *Client {
	c := NewClient(SocketPath(h.ph), testVersion)
	h.t.Cleanup(c.Close)
	h.clients = append(h.clients, c)
	return c
}

// closeClients drops every harness client's idle connections.
func (h *harness) closeClients() {
	for _, c := range h.clients {
		c.Close()
	}
}

// do sends an admin request and fails the test on an error.
func (h *harness) do(method, path string, in, out any) {
	h.t.Helper()
	if _, err := h.client().Do(context.Background(), method, path, in, out); err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
}

// party is one enrolled, approved party with its client.
type party struct {
	name, role, pane, session string
	c                         *Client
}

// route creates a route with an approved pair (adjutant adj-1 in pane p1,
// orchestrator orch-1 in pane p2), file credentials and a setup proof.
func (h *harness) route(name string) (adj, orch *party, inboxDir string) {
	h.t.Helper()
	var cr CreateResult
	h.do("POST", "/v1/routes", CreateRequest{Repo: "repo", Route: name}, &cr)
	adj = &party{name: "adj-" + name, role: "adjutant", pane: "p1-" + name, session: "sess1" + name}
	orch = &party{name: "orch-" + name, role: "orchestrator", pane: "p2-" + name, session: "sess2" + name}
	var sum string
	for _, p := range []*party{adj, orch} {
		h.fx.SetAgent(p.pane, herdrtest.Agent(p.pane, p.session))
		c := h.client()
		c.Pane = p.pane
		var res map[string]string
		if _, err := c.Do(context.Background(), "POST", "/v1/routes/"+name+"/enroll", EnrollRequest{Name: p.name, Role: p.role}, &res); err != nil {
			h.t.Fatalf("enroll %s: %v", p.name, err)
		}
		sum = res["pending_sha256"]
	}
	h.do("POST", "/v1/routes/"+name+"/pair/approve", ApproveRequest{SHA256: sum}, nil)
	h.setup(name)
	for _, p := range []*party{adj, orch} {
		p.c = h.partyClient(cr.Inbox, p)
	}
	return adj, orch, cr.Inbox
}

func (h *harness) setup(name string) {
	h.t.Helper()
	h.do("PUT", "/v1/routes/"+name+"/setup", SetupRequest{Rules: []string{h.rules}, PolicyVerified: true,
		Boundary: json.RawMessage(`{"version":1}`)}, nil)
}

func (h *harness) partyClient(dir string, p *party) *Client {
	c := h.client()
	c.Pane, c.Name = p.pane, p.name
	b, err := os.ReadFile(filepath.Join(dir, "state", "credentials", p.name))
	if err == nil {
		c.Credential = strings.TrimSpace(string(b))
	}
	return c
}

// key is a fresh 128-bit key.
func key() string { return randomHex(16) }

func (p *party) send(t testing.TB, route, to, body string) SendResult {
	t.Helper()
	var res SendResult
	if _, err := p.c.Do(context.Background(), "POST", "/v1/routes/"+route+"/messages",
		SendRequest{To: to, Kind: "brief", Body: body, Key: key()}, &res); err != nil {
		t.Fatalf("send: %v", err)
	}
	return res
}

func (p *party) read(t testing.TB, route string) ClaimResult {
	t.Helper()
	var res ClaimResult
	if _, err := p.c.Do(context.Background(), "POST", "/v1/routes/"+route+"/claims", ClaimRequest{ReadKey: key()}, &res); err != nil {
		t.Fatalf("read: %v", err)
	}
	return res
}

func (p *party) ack(t testing.TB, route, token, id string) {
	t.Helper()
	if _, err := p.c.Do(context.Background(), "POST", "/v1/routes/"+route+"/claims/"+token+"/acks", AckRequest{ID: id, Record: "handled " + id}, nil); err != nil {
		t.Fatalf("ack: %v", err)
	}
}

// code is the error code of err ("" when nil).
func code(err error) string {
	if err == nil {
		return ""
	}
	if e, ok := AsAPIError(err); ok {
		return e.Code
	}
	return "other: " + err.Error()
}

// direct is a route opened in-process without a socket, for crash tests:
// its operations run on the test goroutine.
type direct struct {
	t     testing.TB
	dir   string
	d     *Daemon
	r     *Route
	clock *clock
	crash string
}

// seedInbox makes a daemon-owned inbox with an approved pair and a setup
// proof, as create, enroll, approve and setup leave it.
func seedInbox(t testing.TB) (string, Config) {
	t.Helper()
	base := shortDir(t)
	dir := filepath.Join(base, "repo-r")
	_ = os.Mkdir(dir, 0o700)
	_ = mkdirs(dir, "state", "ids", "enrolled")
	o := Owner{Owner: "daemon", Epoch: randomHex(16), Contract: 2, Protocol: 1}
	b, _ := encode(o)
	_ = os.WriteFile(filepath.Join(dir, "state", "owner.json"), b, 0o600)
	reg := prettyJSON(obj(
		"adj", obj("role", "adjutant", "identity", identityObj(inbox.Identity{Pane: "p1", Session: inbox.Session{Agent: "claude", Kind: "id", Source: "hook", Value: "s1"}})),
		"orch", obj("role", "orchestrator", "identity", identityObj(inbox.Identity{Pane: "p2", Session: inbox.Session{Agent: "claude", Kind: "id", Source: "hook", Value: "s2"}}))))
	_ = os.WriteFile(filepath.Join(dir, "peers"), reg, 0o600)
	_ = os.WriteFile(filepath.Join(dir, "peers.approved"), reg, 0o600)
	rules := filepath.Join(base, "policy.sb")
	bin := filepath.Join(base, "bin")
	_ = os.WriteFile(rules, []byte("x"), 0o600)
	_ = os.WriteFile(bin, []byte("y"), 0o700)
	doc := SetupV2{Version: 2, Inbox: dir, PolicyVerified: true, Rules: []FileSum{{rules, sha256Hex([]byte("x"))}},
		Binary: FileSum{canon(bin), sha256Hex([]byte("y"))}, Boundary: json.RawMessage(`{"version":1}`), Probes: []Probe{}}
	_ = os.WriteFile(filepath.Join(dir, "setup.json"), prettyJSON(doc), 0o600)
	cfg := Config{PrivateHome: base, Home: base, Version: testVersion, Executable: bin, Log: func(s string) { t.Log(s) },
		Sleep: func(time.Duration) {}, LockWait: 1}
	return dir, cfg
}

func newDirect(t testing.TB) *direct {
	t.Helper()
	dir, cfg := seedInbox(t)
	x := &direct{t: t, dir: dir, clock: newClock()}
	cfg.Now = x.clock.Now
	cfg.Crash = func(p string) {
		if p == x.crash {
			panic(crashSignal{p})
		}
	}
	x.d = New(cfg)
	x.open()
	t.Cleanup(func() {
		if x.r != nil {
			x.r.closeFiles()
		}
	})
	return x
}

// open opens the route from its files (a daemon start).
func (x *direct) open() {
	x.t.Helper()
	x.r = newRoute(x.d, "r", x.dir)
	if err := x.r.open(); err != nil {
		x.t.Fatalf("open: %v", err)
	}
}

// kill ends the route as a killed process would: the files stay as they
// are, the kernel frees the route lock.
func (x *direct) kill() {
	x.r.closeFiles()
	x.r = nil
}

// at runs fn with a crash injected at point; it reports whether the crash
// happened.
func (x *direct) at(point string, fn func()) (crashed bool) {
	x.crash = point
	defer func() {
		x.crash = ""
		if p := recover(); p != nil {
			if _, ok := p.(crashSignal); !ok {
				panic(p)
			}
			crashed = true
		}
	}()
	fn()
	return false
}

func (x *direct) journal() []Record {
	x.t.Helper()
	var out []Record
	for _, n := range []string{journalPrev, journalCurrent} {
		b, err := os.ReadFile(filepath.Join(x.dir, "state", "events", n))
		if err != nil {
			continue
		}
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if l == "" {
				continue
			}
			var r Record
			if err := json.Unmarshal([]byte(l), &r); err != nil {
				x.t.Fatalf("journal line %q: %v", l, err)
			}
			out = append(out, r)
		}
	}
	return out
}

func (x *direct) count(op string) int {
	n := 0
	for _, r := range x.journal() {
		if r.Op == op {
			n++
		}
	}
	return n
}

func (x *direct) path(p ...string) string { return filepath.Join(append([]string{x.dir}, p...)...) }

// canon is a path with its links resolved, as the daemon records the
// installed binary.
func canon(p string) string {
	if c, err := filepath.EvalSymlinks(p); err == nil {
		return c
	}
	return p
}
