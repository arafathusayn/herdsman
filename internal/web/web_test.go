package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/route"
)

const routeMD = "# Route t\n\nOrchestrator: this session, pane w1:p1 (tab w1:t1).\n\n" +
	"## Task table\n\n| Task | Agent | Pane |\n| --- | --- | --- |\n" +
	"| auth | im-auth | w1:p2 |\n\n## Run state\n\n- 10:00 started\n"

// fixture is a fake herdr session with an orchestrator and one implementer,
// a route folder that names them, and a server in front of it.
type fixture struct {
	herdr    *herdrtest.Server
	hub      *Hub
	queue    *Queue
	srv      *Server
	http     *httptest.Server
	routeDir string
	focused  atomic.Value // string: the tab herdr reports focused
	inflight atomic.Int32
	maxIn    atomic.Int32
	codes    chan string // pairing codes the terminal would show
	authed   map[string]string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{herdr: herdrtest.Start(t), codes: make(chan string, 16)}
	f.focused.Store("w1:t1")
	proj := t.TempDir()
	f.routeDir = filepath.Join(proj, ".herdsman", "t-2026-10-24")
	if err := os.MkdirAll(f.routeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.routeDir, route.RouteFile), []byte(routeMD), 0o644); err != nil {
		t.Fatal(err)
	}
	f.herdr.Handle("session.snapshot", func(map[string]any) (any, string) {
		return map[string]any{"snapshot": map[string]any{
			"version": "0.9.4", "protocol": 22, "focused_tab_id": f.focused.Load(),
			"panes": []map[string]any{
				{"pane_id": "w1:p1", "tab_id": "w1:t1", "workspace_id": "w1", "cwd": proj, "agent_status": "idle"},
				{"pane_id": "w1:p2", "tab_id": "w1:t2", "workspace_id": "w1", "cwd": proj, "agent_status": "working"},
			},
			"agents": []map[string]any{
				{"pane_id": "w1:p1", "tab_id": "w1:t1", "workspace_id": "w1", "agent_status": "idle"},
				{"pane_id": "w1:p2", "tab_id": "w1:t2", "workspace_id": "w1", "name": "im-auth", "agent_status": "working"},
			},
		}}, ""
	})
	// Every mutation moves herdr's focus to the implementer's tab, the way a
	// prompt can, and counts how many run at once.
	mutate := func(map[string]any) (any, string) {
		n := f.inflight.Add(1)
		for {
			m := f.maxIn.Load()
			if n <= m || f.maxIn.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		f.focused.Store("w1:t2")
		f.inflight.Add(-1)
		return map[string]any{"type": "ok"}, ""
	}
	for _, m := range []string{"agent.prompt", "pane.send_keys", "pane.send_text", "pane.close", "pane.split", "agent.start", "pane.rename", "agent.rename", "pane.focus"} {
		f.herdr.Handle(m, mutate)
	}
	f.herdr.Handle("tab.focus", func(p map[string]any) (any, string) {
		f.focused.Store(p["tab_id"])
		return map[string]any{"type": "ok"}, ""
	})
	f.herdr.Handle("pane.read", func(p map[string]any) (any, string) {
		text := "line 1\nline 2"
		if p["source"] == "visible" {
			text = "screen"
		} else if p["format"] == "ansi" {
			text = "\x1b[32mline 1\x1b[0m\nline 2"
		}
		return map[string]any{"read": map[string]any{"pane_id": p["pane_id"], "text": text}}, ""
	})

	client := herdr.New(f.herdr.Socket)
	f.hub = NewHub(client, herdr.Pong{Version: "0.9.4", Protocol: 22}, nil)
	f.hub.refresh(context.Background())
	f.queue = NewQueue(client, f.hub)
	f.queue.now = func() time.Time { return time.Date(2026, 10, 24, 11, 30, 0, 0, time.Local) }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go f.queue.Run(ctx)

	f.http = httptest.NewUnstartedServer(nil)
	port := f.http.Listener.Addr().(*net.TCPAddr).Port
	f.srv = NewServer(client, f.hub, f.queue, Config{Token: "secret-token", Port: port, OnPair: func(n PairNotice) {
		f.codes <- n.Code
	}})
	f.srv.assets = fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>herdsman</title>")}}
	f.http.Config.Handler = f.srv.Handler()
	f.http.Start()
	t.Cleanup(f.http.Close)
	f.authed = map[string]string{"Authorization": "Bearer " + f.pairDevice(t)}
	return f
}

// pairDevice runs the pairing flow and returns the paired browser's device
// secret, its bearer token.
func (f *fixture) pairDevice(t *testing.T) string {
	t.Helper()
	res := f.request(t, http.MethodPost, "/api/session", "", map[string]string{"X-Herdsman-Token": "secret-token"})
	var st pairState
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil || res.StatusCode != 200 || st.Pair == "" {
		t.Fatalf("session: status %d, %+v, %v", res.StatusCode, st, err)
	}
	code := <-f.codes
	res = f.request(t, http.MethodPost, "/api/pair", `{"code":"`+code+`"}`, map[string]string{"X-Herdsman-Pair": st.Pair})
	var done pairState
	if err := json.NewDecoder(res.Body).Decode(&done); err != nil || res.StatusCode != 200 || done.Device == "" {
		t.Fatalf("pair: status %d, %+v, %v", res.StatusCode, done, err)
	}
	return done.Device
}

func (f *fixture) request(t *testing.T, method, path, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, f.http.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
		} else {
			req.Header.Set(k, v)
		}
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func (f *fixture) act(t *testing.T, a Action) (int, Result) {
	t.Helper()
	b, _ := json.Marshal(a)
	res := f.request(t, http.MethodPost, "/api/actions", string(b), f.authed)
	var r Result
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, r
}

func TestGuardAndAuth(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name   string
		method string
		path   string
		hdr    map[string]string
		origin string
		want   int
	}{
		{name: "state without token", method: "GET", path: "/api/state", want: 401},
		{name: "state with wrong token", method: "GET", path: "/api/state", hdr: map[string]string{"X-Herdsman-Token": "nope"}, want: 401},
		{name: "token alone is not access", method: "GET", path: "/api/state", hdr: map[string]string{"X-Herdsman-Token": "secret-token"}, want: 401},
		{name: "paired browser", method: "GET", path: "/api/state", hdr: f.authed, want: 200},
		{name: "rebinding host", method: "GET", path: "/api/state", hdr: map[string]string{"Authorization": f.authed["Authorization"], "Host": "evil.example:80"}, want: 403},
		{name: "wrong bearer", method: "GET", path: "/api/state", hdr: map[string]string{"Authorization": "Bearer nope"}, want: 401},
		{name: "a cookie is not access", method: "GET", path: "/api/state", hdr: map[string]string{"Cookie": "herdsman=" + strings.TrimPrefix(f.authed["Authorization"], "Bearer ")}, want: 401},
		{name: "page is public", method: "GET", path: "/", want: 200},
		{name: "session with wrong token", method: "POST", path: "/api/session", hdr: map[string]string{"X-Herdsman-Token": "nope"}, want: 401},
		{name: "action from another origin without a secret", method: "POST", path: "/api/actions", hdr: map[string]string{"Origin": "http://evil.example"}, want: 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if res := f.request(t, tc.method, tc.path, "{}", tc.hdr); res.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.want)
			}
		})
	}
}

// With --host or --allow-host, the server also answers to those names, on
// any port (a proxy may front it), and to no other name.
func TestGuardAllowsTheNamedHosts(t *testing.T) {
	f := newFixture(t)
	srv := NewServer(nil, f.hub, f.queue, Config{Token: "secret-token", Port: 7878, Hosts: []string{"192.168.1.20", "Herdsman.Example.com"}})
	h := srv.Handler()
	for host, want := range map[string]int{
		"192.168.1.20:7878": 401, "127.0.0.1:7878": 401, "[::1]:7878": 401, "192.168.1.20:443": 401,
		"herdsman.example.com": 401, "192.168.1.21:7878": 403, "evil.example": 403,
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %s: status %d, want %d", host, rec.Code, want)
		}
	}
}

// Any origin may call the API: a hosted app reaches this server across
// origins, and every call needs a secret only a paired browser holds.
func TestCORSAndPrivateNetworkPreflight(t *testing.T) {
	f := newFixture(t)
	res := f.request(t, http.MethodOptions, "/api/actions", "", map[string]string{
		"Origin": "https://herdsman.example.com", "Access-Control-Request-Method": "POST",
		"Access-Control-Request-Headers": "authorization,content-type", "Access-Control-Request-Private-Network": "true",
	})
	h := res.Header
	if res.StatusCode != http.StatusNoContent || h.Get("Access-Control-Allow-Origin") != "*" ||
		!strings.Contains(h.Get("Access-Control-Allow-Headers"), "Authorization") ||
		h.Get("Access-Control-Allow-Private-Network") != "true" {
		t.Fatalf("preflight: %d %v", res.StatusCode, h)
	}
	res = f.request(t, http.MethodGet, "/api/pane/w1:p2?lines=5", "", map[string]string{"Origin": "https://herdsman.example.com", "Authorization": f.authed["Authorization"]})
	if res.StatusCode != 200 || res.Header.Get("Access-Control-Allow-Origin") != "*" || !strings.Contains(res.Header.Get("Access-Control-Expose-Headers"), "ETag") {
		t.Fatalf("cross-origin read: %d %v", res.StatusCode, res.Header)
	}
}

// Pairing hands its secrets to the browser in the response body, never as
// cookies, and never the code.
func TestPairingSecretsAreNotCookies(t *testing.T) {
	f := newFixture(t)
	res := f.request(t, http.MethodPost, "/api/session", "", map[string]string{"X-Herdsman-Token": "secret-token"})
	if len(res.Cookies()) > 0 {
		t.Fatalf("cookies set: %v", res.Cookies())
	}
	body := readBody(t, res)
	if strings.Contains(body, <-f.codes) {
		t.Fatal("the pairing code reached the browser")
	}
	if !strings.Contains(body, `"pair":"`) {
		t.Fatalf("no pairing secret in %s", body)
	}
}

// A paired browser that opens a link with a wrong or old token stays paired,
// so a link from another page or from history cannot make it drop its secret.
func TestPairedBrowserStaysPairedOnAWrongToken(t *testing.T) {
	f := newFixture(t)
	hdr := map[string]string{"X-Herdsman-Token": "0123456789abcdef", "Authorization": f.authed["Authorization"]}
	var st pairState
	if res := f.request(t, http.MethodPost, "/api/session", "", hdr); res.StatusCode != 200 || json.NewDecoder(res.Body).Decode(&st) != nil || st.Status != "paired" {
		t.Fatalf("paired browser with a wrong token: %d %+v", res.StatusCode, st)
	}
	if res := f.request(t, http.MethodPost, "/api/session", "", map[string]string{"X-Herdsman-Token": "0123456789abcdef"}); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unpaired browser with a wrong token: %d", res.StatusCode)
	}
}

func TestPairingRejectsWrongCodesThenEnds(t *testing.T) {
	f := newFixture(t)
	res := f.request(t, http.MethodPost, "/api/session", "", map[string]string{"X-Herdsman-Token": "secret-token"})
	var st0 pairState
	_ = json.NewDecoder(res.Body).Decode(&st0)
	code := <-f.codes
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	hdr := map[string]string{"X-Herdsman-Pair": st0.Pair}
	for i := 1; i <= pairAttempts; i++ {
		res := f.request(t, http.MethodPost, "/api/pair", `{"code":"`+wrong+`"}`, hdr)
		var st pairState
		_ = json.NewDecoder(res.Body).Decode(&st)
		if res.StatusCode != http.StatusForbidden {
			t.Fatalf("attempt %d: status %d", i, res.StatusCode)
		}
		if i < pairAttempts && (st.Status != "pairing" || st.Left != pairAttempts-i) {
			t.Fatalf("attempt %d: %+v", i, st)
		}
		if i == pairAttempts && st.Status != "none" {
			t.Fatalf("last attempt: %+v, want the request ended", st)
		}
	}
	// The right code no longer works: the request is gone.
	if res := f.request(t, http.MethodPost, "/api/pair", `{"code":"`+code+`"}`, hdr); res.StatusCode != http.StatusForbidden {
		t.Fatalf("right code after lockout: status %d", res.StatusCode)
	}
}

func TestPairingExpires(t *testing.T) {
	d := newDevices()
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }
	sec, code, _, err := d.begin("Safari on iPhone")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(pairTTL + time.Second)
	if _, err := d.confirm(sec, code); err == nil {
		t.Fatal("an expired request paired")
	}
}

func TestPairingLimitsGuessing(t *testing.T) {
	d := newDevices()
	for range maxFailures / pairAttempts {
		sec, code, _, err := d.begin("x")
		if err != nil {
			t.Fatal(err)
		}
		bad := "999999"
		if code == bad {
			bad = "888888"
		}
		for range pairAttempts {
			_, _ = d.confirm(sec, bad)
		}
	}
	if _, _, _, err := d.begin("x"); err == nil {
		t.Fatalf("pairing still opens after %d wrong codes", maxFailures)
	}
}

func TestDescribeDevice(t *testing.T) {
	for ua, want := range map[string]string{
		"Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Mobile/15E148 Safari/604.1": "Safari on iPhone",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36":                   "Chrome on macOS",
		"Mozilla/5.0 (Linux; Android 16) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0 Mobile Safari/537.36":                                "Chrome on Android",
		"curl/8.0": "A browser on an unknown system",
	} {
		if got := describeDevice(ua); got != want {
			t.Errorf("describeDevice(%q) = %q, want %q", ua, got, want)
		}
	}
}

func readBody(t *testing.T, res *http.Response) string {
	t.Helper()
	var b strings.Builder
	if _, err := io.Copy(&b, res.Body); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestStateMarksRouteLive(t *testing.T) {
	f := newFixture(t)
	st := f.hub.Current()
	if len(st.Routes) != 1 || !st.Routes[0].Live || st.Routes[0].OrchestratorPane != "w1:p1" {
		t.Fatalf("routes = %+v", st.Routes)
	}
}

func TestPromptRestoresFocusAndLogsToRoute(t *testing.T) {
	f := newFixture(t)
	code, r := f.act(t, Action{Type: "prompt", Pane: "w1:p2", Text: "run the tests\nand report"})
	if code != 200 || !r.OK {
		t.Fatalf("status %d, result %+v", code, r)
	}
	if got := f.herdr.Methods(); !slices.Equal(got, []string{"agent.prompt", "tab.focus"}) {
		t.Fatalf("methods = %v, want the prompt then the focus restore", got)
	}
	if got := f.focused.Load(); got != "w1:t1" {
		t.Fatalf("focused tab = %v, want w1:t1 restored", got)
	}
	b, _ := os.ReadFile(filepath.Join(f.routeDir, route.RouteFile))
	want := "- 11:30 web: prompted im-auth (w1:p2): \"run the tests and report\"\n"
	if !strings.HasSuffix(string(b), want) {
		t.Fatalf("route file ends %q, want %q", string(b)[len(b)-len(want)-10:], want)
	}
}

func TestRiskyActionsNeedForce(t *testing.T) {
	f := newFixture(t)
	code, r := f.act(t, Action{Type: "close", Pane: "w1:p2"})
	if code != http.StatusConflict || r.Code != "needs_force" {
		t.Fatalf("close: status %d, result %+v; want 409 needs_force", code, r)
	}
	// The trash button deletes the pane, and the words say so.
	if !strings.Contains(r.Error, "confirm to delete") {
		t.Errorf("confirm text %q does not say delete", r.Error)
	}
	if m := f.herdr.Methods(); len(m) != 0 {
		t.Fatalf("herdr got %v before force", m)
	}
	if code, r := f.act(t, Action{Type: "close", Pane: "w1:p2", Force: true}); code != 200 || !r.OK || !strings.HasPrefix(r.Summary, "deleted pane") {
		t.Fatalf("forced close: %d %+v", code, r)
	}
	// Keys go straight through, to the orchestrator as to any agent.
	for _, pane := range []string{"w1:p1", "w1:p2"} {
		if code, r := f.act(t, Action{Type: "keys", Pane: pane, Keys: []string{"esc"}}); code != 200 || !r.OK {
			t.Fatalf("keys to %s: %d %+v", pane, code, r)
		}
	}
}

func TestInvalidActions(t *testing.T) {
	f := newFixture(t)
	for _, a := range []Action{
		{Type: "prompt", Pane: "w1:p2", Text: "   "},
		{Type: "start", Pane: "w1:p2", Name: "Bad Name", Kind: "claude"},
		{Type: "start", Pane: "w1:p2", Name: "ok", Kind: ""},
		{Type: "split", Pane: "w1:p2", Direction: "left"},
		{Type: "explode", Pane: "w1:p2"},
		{Type: "prompt", Text: "no pane"},
		{Type: "input", Pane: "w1:p2"},
		{Type: "input", Pane: "w1:p2", Keys: []string{"enter"}, Text: "x"},
		{Type: "scroll", Pane: "w1:p2"},
	} {
		if code, r := f.act(t, a); code != http.StatusBadRequest || r.Code != "invalid_action" {
			t.Errorf("%+v: status %d, result %+v; want 400 invalid_action", a, code, r)
		}
	}
	res := f.request(t, http.MethodPost, "/api/actions", `{"type":"prompt","pane":"w1:p2","text":"x","sneaky":1}`, f.authed)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field: status %d", res.StatusCode)
	}
}

func TestStartAgentPassesKindAndArgs(t *testing.T) {
	f := newFixture(t)
	code, r := f.act(t, Action{Type: "start", Pane: "w1:p2", Name: "im-new", Kind: "any-harness", Args: []string{"--model", "m 1"}})
	if code != 200 || !r.OK {
		t.Fatalf("%d %+v", code, r)
	}
	for _, req := range f.herdr.Requests() {
		if req.Method == "agent.start" {
			if req.Params["kind"] != "any-harness" || req.Params["name"] != "im-new" {
				t.Fatalf("params = %v", req.Params)
			}
			if args, _ := req.Params["args"].([]any); len(args) != 2 || args[1] != "m 1" {
				t.Fatalf("args = %v", req.Params["args"])
			}
			return
		}
	}
	t.Fatal("no agent.start request")
}

// Browser tabs can send actions at once; herdr must still see one mutation at a time.
func TestActionsRunOneAtATime(t *testing.T) {
	f := newFixture(t)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			b, _ := json.Marshal(Action{Type: "text", Pane: "w1:p2", Text: strings.Repeat("x", i+1)})
			req, _ := http.NewRequest(http.MethodPost, f.http.URL+"/api/actions", strings.NewReader(string(b)))
			req.Header.Set("Origin", f.http.URL)
			req.Header.Set("Authorization", f.authed["Authorization"])
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			_ = res.Body.Close()
		})
	}
	wg.Wait()
	if n := f.maxIn.Load(); n != 1 {
		t.Fatalf("up to %d mutations ran at once, want 1", n)
	}
	sent := 0
	for _, m := range f.herdr.Methods() {
		if m == "pane.send_text" {
			sent++
		}
	}
	if sent != 16 {
		t.Fatalf("send_text calls = %d, want 16", sent)
	}
}

func TestStreamSendsStateFirst(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.http.URL+"/api/stream", nil)
	req.Header.Set("Authorization", f.authed["Authorization"])
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	if !sc.Scan() || sc.Text() != "event: state" {
		t.Fatalf("first line = %q", sc.Text())
	}
	if !sc.Scan() || !strings.Contains(sc.Text(), `"im-auth"`) {
		t.Fatalf("state line = %q", sc.Text())
	}
	f.hub.Log(Entry{At: time.Now(), Source: "web", Kind: "prompt", Text: "hello"})
	for sc.Scan() {
		if sc.Text() == "event: entry" {
			return
		}
	}
	t.Fatal("no live entry event")
}

func TestPaneUsesETag(t *testing.T) {
	f := newFixture(t)
	res := f.request(t, http.MethodGet, "/api/pane/w1:p2?lines=50", "", f.authed)
	etag := res.Header.Get("ETag")
	if res.StatusCode != 200 || etag == "" {
		t.Fatalf("status %d etag %q", res.StatusCode, etag)
	}
	res = f.request(t, http.MethodGet, "/api/pane/w1:p2?lines=50", "", map[string]string{"Authorization": f.authed["Authorization"], "If-None-Match": etag})
	if res.StatusCode != http.StatusNotModified {
		t.Fatalf("second read = %d, want 304", res.StatusCode)
	}
}

func TestPaneReadsColorOnRequest(t *testing.T) {
	f := newFixture(t)
	for q, want := range map[string]string{
		"":                              "line 1\nline 2",
		"&format=ansi":                  "\x1b[32mline 1\x1b[0m\nline 2",
		"&format=ansi&source=visible":   "screen",
		"&format=ansi&source=elsewhere": "\x1b[32mline 1\x1b[0m\nline 2",
	} {
		res := f.request(t, http.MethodGet, "/api/pane/w1:p2?lines=50"+q, "", f.authed)
		var read herdr.PaneRead
		if err := json.NewDecoder(res.Body).Decode(&read); err != nil || read.Text != want {
			t.Errorf("format %q: text %q, %v", q, read.Text, err)
		}
	}
}

// Terminal typing reaches the pane, keeps the user's focus, and never lands
// in the activity stream or the route log, since it can hold a password.
func TestTerminalInputIsNotLogged(t *testing.T) {
	f := newFixture(t)
	before, _ := os.ReadFile(filepath.Join(f.routeDir, route.RouteFile))
	for _, a := range []Action{
		{Type: "input", Pane: "w1:p2", Text: "hunter2"},
		{Type: "input", Pane: "w1:p2", Keys: []string{"enter", "ctrl+c"}},
	} {
		if code, r := f.act(t, a); code != 200 || !r.OK {
			t.Fatalf("%+v: %d %+v", a, code, r)
		}
	}
	want := []string{"pane.send_text", "tab.focus", "pane.send_keys", "tab.focus"}
	if got := f.herdr.Methods(); !slices.Equal(got, want) {
		t.Fatalf("methods = %v, want %v", got, want)
	}
	for _, req := range f.herdr.Requests() {
		if req.Method == "pane.send_text" && req.Params["text"] != "hunter2" {
			t.Fatalf("text = %v", req.Params["text"])
		}
	}
	after, _ := os.ReadFile(filepath.Join(f.routeDir, route.RouteFile))
	if string(after) != string(before) {
		t.Fatalf("route log changed:\n%s", after[len(before):])
	}
	ch, _, entries := f.hub.subscribe()
	f.hub.unsubscribe(ch)
	if len(entries) != 0 {
		t.Fatalf("activity got %+v", entries)
	}
}

// The wheel scrolls herdr's scrollback where a pane has one, goes to an
// agent's UI as mouse wheel events, and leaves any other pane alone.
func TestScrollPicksTheWay(t *testing.T) {
	f := newFixture(t)
	panes := map[string]map[string]any{
		"w1:p1": {"scroll": map[string]any{"offset_from_bottom": 3, "max_offset_from_bottom": 270, "viewport_rows": 39}},
		"w1:p2": {"agent": "claude", "scroll": map[string]any{"offset_from_bottom": 0, "max_offset_from_bottom": 0, "viewport_rows": 39}},
		"w1:p3": {"scroll": map[string]any{"offset_from_bottom": 0, "max_offset_from_bottom": 0, "viewport_rows": 39}},
	}
	f.herdr.Handle("pane.get", func(p map[string]any) (any, string) {
		pane := map[string]any{"pane_id": p["pane_id"], "agent_status": "idle"}
		maps.Copy(pane, panes[p["pane_id"].(string)])
		return map[string]any{"type": "pane_info", "pane": pane}, ""
	})
	f.herdr.Handle("pane.scroll", func(map[string]any) (any, string) {
		return map[string]any{"type": "ok"}, ""
	})
	cases := []struct {
		a      Action
		method string
		param  string
		want   any
	}{
		{Action{Pane: "w1:p1", Lines: 5}, "pane.scroll", "offset_from_bottom", float64(8)},
		{Action{Pane: "w1:p1", Lines: 1000}, "pane.scroll", "offset_from_bottom", float64(270)},
		{Action{Pane: "w1:p1", Lines: -100}, "pane.scroll", "offset_from_bottom", float64(0)},
		{Action{Pane: "w1:p2", Lines: -3, Col: 20, Row: 10}, "pane.send_text", "text", strings.Repeat("\x1b[<65;20;10M", 3)},
		{Action{Pane: "w1:p2", Lines: 50}, "pane.send_text", "text", strings.Repeat("\x1b[<64;1;1M", maxWheel)},
		{Action{Pane: "w1:p3", Lines: 5}, "", "", nil},
	}
	for _, tc := range cases {
		tc.a.Type = "scroll"
		n := len(f.herdr.Requests())
		if code, r := f.act(t, tc.a); code != 200 || !r.OK {
			t.Fatalf("%+v: %d %+v", tc.a, code, r)
		}
		var got []herdrtest.Request
		for _, req := range f.herdr.Requests()[n:] {
			if req.Method == "pane.scroll" || req.Method == "pane.send_text" {
				got = append(got, req)
			}
		}
		if tc.method == "" {
			if len(got) != 0 {
				t.Errorf("%+v: sent %v, want nothing", tc.a, got)
			}
			continue
		}
		if len(got) != 1 || got[0].Method != tc.method || got[0].Params[tc.param] != tc.want {
			t.Errorf("%+v: sent %v, want %s %s=%q", tc.a, got, tc.method, tc.param, tc.want)
		}
	}

	// A scroll past any real step is refused, never negated into a panic, and
	// the queue keeps serving.
	for _, lines := range []int{math.MinInt, math.MaxInt, -maxScroll - 1} {
		if _, r := f.act(t, Action{Type: "scroll", Pane: "w1:p2", Lines: lines}); r.OK || r.Code != "invalid_action" {
			t.Fatalf("lines %d: %+v", lines, r)
		}
	}
	if _, r := f.act(t, Action{Type: "scroll", Pane: "w1:p2", Lines: -1}); !r.OK {
		t.Fatalf("queue after a refused scroll: %+v", r)
	}

	// Typing into a pane left scrolled back jumps to the bottom first, once.
	if code, r := f.act(t, Action{Type: "scroll", Pane: "w1:p1", Lines: 4}); code != 200 || !r.OK {
		t.Fatalf("scroll: %d %+v", code, r)
	}
	n := len(f.herdr.Requests())
	for range 2 {
		if code, r := f.act(t, Action{Type: "input", Pane: "w1:p1", Text: "x"}); code != 200 || !r.OK {
			t.Fatalf("input: %d %+v", code, r)
		}
	}
	var got []string
	for _, req := range f.herdr.Requests()[n:] {
		switch req.Method {
		case "pane.scroll":
			got = append(got, fmt.Sprintf("scroll %v", req.Params["offset_from_bottom"]))
		case "pane.send_text":
			got = append(got, "text")
		}
	}
	if want := []string{"scroll 0", "text", "text"}; !slices.Equal(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

func TestRouteFileIsConfinedToKnownRoutes(t *testing.T) {
	f := newFixture(t)
	if res := f.request(t, http.MethodGet, "/api/route/file?route="+routeID(f.routeDir, "t-2026-10-24")+"&path=00-route.md", "", f.authed); res.StatusCode != 200 {
		t.Fatalf("own file = %d", res.StatusCode)
	}
	for _, q := range []string{"route=" + routeID(f.routeDir, "t-2026-10-24") + "&path=../../etc/passwd", "route=other&path=00-route.md"} {
		if res := f.request(t, http.MethodGet, "/api/route/file?"+q, "", f.authed); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", q, res.StatusCode)
		}
	}
}

func TestStaticServesIndexWithCSP(t *testing.T) {
	f := newFixture(t)
	res := f.request(t, http.MethodGet, "/some/client/route", "", nil)
	if res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatalf("status %d, CSP %q", res.StatusCode, res.Header.Get("Content-Security-Policy"))
	}
}

func TestIsLiveIgnoresReusedPaneIDs(t *testing.T) {
	panes := map[string]bool{"w1:p1": true, "w1:p2": true}
	agents := map[string]string{"w1:p2": "im-new"}
	claimed := map[string]bool{}
	newer := &route.Route{OrchestratorPane: "w1:p1", Tasks: []route.Task{{Agent: "im-new", Pane: "w1:p2"}}}
	older := &route.Route{OrchestratorPane: "w1:p1", Tasks: []route.Task{{Agent: "im-old", Pane: "w1:p2"}}}
	if !isLive(newer, panes, agents, claimed) {
		t.Fatal("newer route not live")
	}
	if isLive(older, panes, agents, claimed) {
		t.Fatal("older route that reuses the same pane ids counted as live")
	}
	// Routes reuse agent names as well: an old row for the same agent in the same pane is not live.
	same := &route.Route{OrchestratorPane: "w1:p9", Tasks: []route.Task{{Agent: "im-new", Pane: "w1:p2"}}}
	if isLive(same, panes, agents, claimed) {
		t.Fatal("older route that reuses the pane id and the agent name counted as live")
	}
}
