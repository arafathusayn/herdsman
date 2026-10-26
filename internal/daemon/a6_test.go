package daemon

// Review a-6 (and a-2, a-3) reproductions, kept as fail-first tests.
import (
	"context"
	"encoding/json"
	"errors"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestA6QueuedCancellation(t *testing.T) {
	x := newDirect(t)
	x.r.start()
	defer func() { x.r.stop(); x.open() }()
	started, release := make(chan struct{}), make(chan struct{})
	blockDone := make(chan *APIError, 1)
	go func() {
		blockDone <- x.r.do(context.Background(), func() *APIError { close(started); <-release; return nil })
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran := false
	e := x.r.do(ctx, func() *APIError { ran = true; return nil })
	if e == nil {
		t.Fatal("cancelled call succeeded")
	}
	close(release)
	<-blockDone
	_ = x.r.do(context.Background(), func() *APIError { return nil })
	if ran {
		t.Fatal("queued cancelled write ran after its handler returned")
	}
}
func TestA6RealAdoptionNew(t *testing.T) {
	h := newHarness(t)
	dir, sum := fileInbox(t, h)
	legacyEntry(t, dir, "m000000000003", "adj-1", "orch-1", "old new", "old new")
	legacyEntry(t, dir, "m000000000004", "adj-1", "orch-1", "original", "tampered")
	h.do("POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: sum}, nil)
	h.do("GET", "/v1/routes/old/status", nil, nil)
	for _, p := range []string{"orch-1/ready/m000000000003.md", "orch-1/quarantine/m000000000004.md", "ids/m000000000004/terminal.json", "adj-1/notices/m000000000004.md.json"} {
		if !exists(filepath.Join(dir, p)) {
			t.Errorf("adopt missing %s", p)
		}
	}
	before := intakeRecords(dir)
	h.restart()
	h.do("GET", "/v1/routes/old/status", nil, nil)
	if intakeRecords(dir) != before {
		t.Fatal("adopt intake duplicated after restart")
	}
}
func TestA6IntakeBarrier(t *testing.T) {
	x := newDirect(t)
	legacyEntry(t, x.dir, "m000000000001", "adj", "orch", "first", "tampered")
	legacyEntry(t, x.dir, "m000000000002", "adj", "orch", "second", "tampered")
	old := rename
	defer func() { rename = old }()
	rename = func(src, dst string) error {
		if strings.HasSuffix(src, "new/m000000000001.md") {
			return errors.New("injected rename failure")
		}
		return old(src, dst)
	}
	var laterRan bool
	e := x.r.runOp(func() *APIError { laterRan = true; return nil }) // make intake due explicitly below
	_ = e
	laterRan = false
	x.r.intakeDue = true
	x.r.intakeAt = time.Time{}
	before := x.r.visible
	e = x.r.runOp(func() *APIError { laterRan = true; return nil })
	if len(x.r.unfinished) == 0 {
		t.Fatal("fixture did not inject a committed unfinished intake")
	}
	if laterRan || x.r.visible > before || e == nil {
		t.Fatalf("crossed unfinished intake: laterRan=%v visible=%d -> %d error=%v", laterRan, before, x.r.visible, e)
	}
}
func TestA6ReplayDuplicate(t *testing.T) {
	x := newDirect(t)
	id := "m000000000001"
	legacyEntry(t, x.dir, id, "adj", "orch", "first", "first")
	if e := x.r.dispose("orch", id, "verified"); e != nil {
		t.Fatal(e)
	}
	// A second copy appears after delivery but before restart; verification must hold it.
	_ = os.MkdirAll(x.path("orch", "cur"), 0o700)
	if _, e := x.r.claim("orch", ClaimRequest{ReadKey: key()}); e != nil {
		t.Fatal(e)
	}
	legacyEntry(t, x.dir, id, "adj", "orch", "first", "first")
	x.kill()
	x.open()
	if !exists(x.path("orch", "new", id+".md")) || exists(x.path("orch", "ready", id+".md")) {
		t.Fatal("journal replay promoted duplicate despite an existing cur copy")
	}
}
func TestA6AckCachePathGuard(t *testing.T) {
	x := newDirect(t)
	a, e := x.send("one", key())
	if e != nil {
		t.Fatal(e)
	}
	cl, e := x.r.claim("orch", ClaimRequest{ReadKey: key()})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := x.r.ack("orch", cl.Claim, AckRequest{ID: a.ID, Record: "note"}); e != nil {
		t.Fatal(e)
	}
	box := x.path("orch")
	if !x.r.acks.valid(x.r.root(), box, a.ID) {
		t.Fatal("fixture ack invalid")
	}
	moved := filepath.Join(filepath.Dir(x.dir), "moved-mailbox")
	if e := os.Rename(box, moved); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(moved, box); e != nil {
		t.Fatal(e)
	}
	if x.r.root().AckValid(box, a.ID) {
		t.Fatal("fixture canonical guard unexpectedly accepts link")
	}
	if x.r.acks.valid(x.r.root(), box, a.ID) {
		t.Fatal("cache accepts ack through symlink that canonical AckValid rejects")
	}
}

func TestA6VerifyGuards(t *testing.T) {
	for _, which := range []string{"from_identity", "to_identity", "allocation", "reply", "live", "envelope", "duplicate"} {
		t.Run(which, func(t *testing.T) {
			h := newHarness(t)
			adj, orch, dir := h.route("r")
			h.stop()
			rt := newRoute(h.d, "r", dir)
			if err := rt.open(); err != nil {
				t.Fatal(err)
			}
			defer rt.closeFiles()
			id := "m000000000001"
			legacyEntry(t, dir, id, adj.name, orch.name, "body", "body")
			f := rt.path(orch.name, "new", id+".md")
			alloc := rt.path("ids", id, "envelope")
			b, _ := os.ReadFile(f)
			switch which {
			case "from_identity":
				b = []byte(strings.Replace(string(b), `"sess1r"`, `"wrong"`, 1))
			case "to_identity":
				b = []byte(strings.Replace(string(b), `"sess2r"`, `"wrong"`, 1))
			case "allocation":
				b = append(b, ' ')
			case "reply":
				b = []byte(strings.Replace(string(b), `"reply_to": ""`, `"reply_to": "m999999999999"`, 1))
			case "live":
				h.fx.SetAgent(adj.pane, nil)
			case "envelope":
				b = []byte(`{"id":"` + id + `"}`)
			case "duplicate":
				_ = os.WriteFile(rt.path(orch.name, "ready", id+".md"), b, 0o600)
			}
			if which != "allocation" {
				_ = os.WriteFile(alloc, b, 0o600)
			}
			_ = os.WriteFile(f, b, 0o600)
			got := rt.verifyEntry(orch.name, id)
			want := "quarantined"
			if which == "duplicate" {
				want = "held"
			}
			if got != want {
				t.Fatalf("%s got %s want %s", which, got, want)
			}
		})
	}
}

func TestA2InputLockIsExclusive(t *testing.T) {
	x := newDirect(t)
	unlock, e := x.r.inputLock()
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	again, e := x.r.inputLock()
	if e == nil {
		again()
		t.Fatal("second mutation transaction adopted the first transaction's live input lock")
	}
	if e.Code != "busy" {
		t.Fatalf("second lock = %v", e)
	}
}

func TestA2CancelledHoldKeepsMutationBoundary(t *testing.T) {
	h := newHarness(t)
	adj, orch, dir := h.route("r")
	rt, _ := h.d.route("r")
	entered := make(chan struct{})
	release := make(chan struct{})
	blockedDone := make(chan struct{})
	go func() {
		rt.do(context.Background(), func() *APIError { close(entered); <-release; return nil })
		close(blockedDone)
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("PUT", "/v1/routes/r/holds/"+orch.name, strings.NewReader(`{"reason":"pause"}`)).WithContext(ctx)
	req.SetPathValue("route", "r")
	req.SetPathValue("name", orch.name)
	req.Header.Set(HeaderPane, adj.pane)
	req.Header.Set(HeaderName, adj.name)
	req.Header.Set("Authorization", "Bearer "+adj.c.Credential)
	handlerDone := make(chan *APIError, 1)
	go func() { handlerDone <- h.d.holdOp(httptest.NewRecorder(), req, true) }()
	end := time.Now().Add(time.Second)
	for len(rt.ops) == 0 && time.Now().Before(end) {
		time.Sleep(time.Millisecond)
	}
	if len(rt.ops) == 0 {
		close(release)
		t.Fatal("hold was not queued")
	}
	cancel()
	e := <-handlerDone
	protected := exists(filepath.Join(dir, "herdr-mutation.lock"))
	close(release)
	<-blockedDone
	rt.do(context.Background(), func() *APIError { return nil })
	if e == nil {
		t.Fatal("cancelled hold unexpectedly succeeded")
	}
	if !protected && exists(filepath.Join(dir, "state", "hold-"+orch.name)) {
		t.Fatal("cancelled HTTP request freed input lock, then queued hold wrote after the lock was gone")
	}
}

func TestA6SetupLockOrder(t *testing.T) {
	h := newHarness(t)
	_, _, dir := h.route("r")
	rt, _ := h.d.route("r")
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan *APIError, 1)
	go func() {
		done <- rt.do(context.Background(), func() *APIError { close(started); <-release; return nil })
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body, _ := json.Marshal(SetupRequest{Rules: []string{h.rules}, PolicyVerified: true, Boundary: json.RawMessage(`{"version":1}`)})
	req := httptest.NewRequest("PUT", "/v1/routes/r/setup", strings.NewReader(string(body))).WithContext(ctx)
	req.SetPathValue("route", "r")
	answered := make(chan *APIError, 1)
	go func() { answered <- h.d.putSetup(httptest.NewRecorder(), req) }()
	end := time.Now().Add(time.Second)
	for len(rt.ops) == 0 && time.Now().Before(end) {
		time.Sleep(time.Millisecond)
	}
	held := exists(filepath.Join(dir, "herdr-mutation.lock"))
	cancel()
	<-answered
	close(release)
	<-done
	if !held {
		t.Fatal("setup queued on actor before holding shared input lock")
	}
}

func TestA3AdoptionDeliversLegacyNew(t *testing.T) {
	h := newHarness(t)
	dir, sum := fileInbox(t, h)
	id := "m000000000001.md"
	if err := os.Rename(filepath.Join(dir, "orch-1", "ready", id), filepath.Join(dir, "orch-1", "new", id)); err != nil {
		t.Fatal(err)
	}
	var res AdoptResult
	h.do("POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: sum}, &res)
	h.setup("old")
	orch := &party{name: "orch-1", pane: "p2"}
	orch.c = h.partyClient(dir, orch)
	cl := orch.read(t, "old")
	if len(cl.Manifest) != 2 {
		t.Fatalf("adoption stranded queued legacy message in new/: got %d messages, want 2", len(cl.Manifest))
	}
}

func TestA6IntakeLinkGuards(t *testing.T) {
	for _, which := range []string{"allocation", "reply"} {
		t.Run(which, func(t *testing.T) {
			h := newHarness(t)
			adj, orch, dir := h.route("r")
			h.stop()
			rt := newRoute(h.d, "r", dir)
			if e := rt.open(); e != nil {
				t.Fatal(e)
			}
			defer rt.closeFiles()
			id := "m000000000001"
			legacyEntry(t, dir, id, adj.name, orch.name, "body", "body")
			outside := filepath.Join(h.dir, "outside-allocation")
			if which == "allocation" {
				if e := os.Rename(rt.path("ids", id), outside); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(outside, rt.path("ids", id)); e != nil {
					t.Fatal(e)
				}
			} else {
				f := rt.path(orch.name, "new", id+".md")
				b, _ := os.ReadFile(f)
				b = []byte(strings.Replace(string(b), `"reply_to": ""`, `"reply_to": "m000000000002"`, 1))
				_ = os.WriteFile(f, b, 0o600)
				_ = os.WriteFile(rt.path("ids", id, "envelope"), b, 0o600)
				_ = os.Mkdir(outside, 0o700)
				_ = os.WriteFile(filepath.Join(outside, "envelope"), []byte("outside"), 0o600)
				if e := os.Symlink(outside, rt.path("ids", "m000000000002")); e != nil {
					t.Fatal(e)
				}
			}
			if got := rt.verifyEntry(orch.name, id); got == "verified" {
				t.Fatalf("verified %s through symlink outside inbox", which)
			}
		})
	}
}

// TestSideAgentRetryBeforeBoundary: a committed side_agent_data send
// retried with its key answers the original id as a duplicate, even when
// the boundary turned HUMAN after the commit (5.4); a new key is refused.
func TestSideAgentRetryBeforeBoundary(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	boundary := `{"version":1,"inventory_current":true,"private_home":true,"human_checked":true,"protected_domain":true,"launch_admission":true,"courier_panes":[],"lower_trust":[]}`
	h.do("PUT", "/v1/routes/r/setup", SetupRequest{Rules: []string{h.rules}, PolicyVerified: true, Boundary: json.RawMessage(boundary)}, nil)
	k := key()
	q := SendRequest{To: orch.name, Kind: "side_agent_data", Body: `{"version":1,"text":"x","source":{"pane":"p9","session":"s9"}}`, Key: k}
	var first SendResult
	if _, err := adj.c.Do(context.Background(), "POST", "/v1/routes/r/messages", q, &first); err != nil {
		t.Fatal(err)
	}
	h.fx.SetAgent("p9", herdrtest.Agent("p9", "stranger"))
	var again SendResult
	if _, err := adj.c.Do(context.Background(), "POST", "/v1/routes/r/messages", q, &again); err != nil || again.ID != first.ID || !again.Duplicate {
		t.Fatalf("retry after the boundary changed: %+v %v", again, err)
	}
	q.Key = key()
	if _, err := adj.c.Do(context.Background(), "POST", "/v1/routes/r/messages", q, nil); code(err) != "human_boundary" {
		t.Fatalf("a new side_agent_data send under HUMAN: %v", err)
	}
}

// TestRequestNullRefused: a request body is one JSON object whose members
// may be absent but never null; null never decodes to an empty value.
func TestRequestNullRefused(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	for _, body := range []string{
		`{"to":"` + orch.name + `","kind":"brief","body":null,"key":"` + key() + `"}`,
		`{"to":null,"kind":"brief","body":"x","key":"` + key() + `"}`,
		`null`,
		`[]`,
	} {
		var raw json.RawMessage = json.RawMessage(body)
		if _, err := adj.c.Do(context.Background(), "POST", "/v1/routes/r/messages", raw, nil); code(err) != "bad_request" {
			t.Errorf("%s: %v", body, err)
		}
	}
}
