package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
	"google.golang.org/protobuf/proto"
)

const statusPath = "/rpc/herdsman.daemon.v1.MessageService/Status"

// fakeDaemon is the daemon's RPC surface on a Unix socket, recording the
// headers of each call.
type fakeDaemon struct {
	socket string
	hc     *http.Client

	mu   sync.Mutex
	last http.Header
	hits int
}

func (d *fakeDaemon) seen() (http.Header, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.last, d.hits
}

// tempSocket is a path short enough for a Unix socket, which t.TempDir can
// exceed.
func tempSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "d.sock")
}

func startFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	d := &fakeDaemon{socket: tempSocket(t)}
	status := rpc.Unary("herdsman.daemon.v1.MessageService", "Status",
		func(_ context.Context, req *daemonv1.StatusRequest) (*daemonv1.StatusResponse, error) {
			if req.GetRoute().GetValue() == "" {
				return nil, rpc.Errorf(rpc.InvalidArgument, "route is required")
			}
			return &daemonv1.StatusResponse{}, nil
		})
	mux := rpc.NewMux(status)
	ln, err := net.Listen("unix", d.socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		d.last, d.hits = r.Header.Clone(), d.hits+1
		d.mu.Unlock()
		mux.ServeHTTP(w, r)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", d.socket)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	d.hc = &http.Client{Transport: transport}
	return d
}

// gatewayFixture is a loopback server in front of a daemon, with one paired
// browser.
type gatewayFixture struct {
	url, device string
	srv         *Server
}

func startGateway(t *testing.T, socket string, change func(*Config)) *gatewayFixture {
	t.Helper()
	cfg := Config{Token: "tok", DaemonSocket: socket}
	if change != nil {
		change(&cfg)
	}
	srv := NewServer(nil, nil, nil, cfg)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	sec, code, _, err := srv.devices.begin("tok", "test")
	if err != nil {
		t.Fatal(err)
	}
	device, err := srv.devices.confirm(sec, code)
	if err != nil {
		t.Fatal(err)
	}
	return &gatewayFixture{url: ts.URL, device: device, srv: srv}
}

type gatewayCall struct {
	path, contentType string
	body              []byte
	header            map[string]string
}

func send(t *testing.T, hc *http.Client, base string, c gatewayCall) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+c.path, bytes.NewReader(c.body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", c.contentType)
	for k, v := range c.header {
		if strings.EqualFold(k, "host") {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	res, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, res.Header, body
}

func validStatus(t *testing.T) []byte {
	t.Helper()
	b, err := proto.Marshal(&daemonv1.StatusRequest{Route: nil})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func wireCode(t *testing.T, body []byte) string {
	t.Helper()
	var failure struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &failure); err != nil {
		t.Fatalf("not a failure body: %q", body)
	}
	return failure.Code
}

func bearerHeader(device string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + device}
}

func TestGatewayAnswersLikeTheDaemon(t *testing.T) {
	daemon := startFakeDaemon(t)
	g := startGateway(t, daemon.socket, nil)
	cases := []struct {
		name string
		call gatewayCall
	}{
		{"json success", gatewayCall{path: statusPath, contentType: rpc.ContentTypeJSON, body: []byte(`{"route":{"value":"r"}}`)}},
		{"json failure", gatewayCall{path: statusPath, contentType: rpc.ContentTypeJSON, body: []byte(`{}`)}},
		{"proto failure", gatewayCall{path: statusPath, contentType: rpc.ContentTypeProto, body: validStatus(t)}},
		{"unknown method", gatewayCall{path: "/rpc/herdsman.daemon.v1.MessageService/Nothing", contentType: rpc.ContentTypeJSON, body: []byte(`{}`)}},
		{"unsupported type", gatewayCall{path: statusPath, contentType: "text/plain", body: []byte(`x`)}},
		{"undecodable body", gatewayCall{path: statusPath, contentType: rpc.ContentTypeJSON, body: []byte(`{`)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantStatus, wantHeader, wantBody := send(t, daemon.hc, "http://herdsman", c.call)
			through := c.call
			through.header = bearerHeader(g.device)
			gotStatus, gotHeader, gotBody := send(t, http.DefaultClient, g.url, through)
			if gotStatus != wantStatus || !bytes.Equal(gotBody, wantBody) {
				t.Fatalf("gateway %d %q, daemon %d %q", gotStatus, gotBody, wantStatus, wantBody)
			}
			if gotHeader.Get("Content-Type") != wantHeader.Get("Content-Type") {
				t.Fatalf("content type %q, daemon %q", gotHeader.Get("Content-Type"), wantHeader.Get("Content-Type"))
			}
		})
	}
}

func TestGatewayAttachesTheSessionAndDropsClientCredentials(t *testing.T) {
	daemon := startFakeDaemon(t)
	g := startGateway(t, daemon.socket, nil)
	header := bearerHeader(g.device)
	for _, name := range []string{"Cookie", "X-Herdsman-Token", "X-Herdsman-Pair", "Herdsman-Pane", "Herdsman-Name", HeaderSession} {
		header[name] = "forged"
	}
	status, _, body := send(t, http.DefaultClient, g.url, gatewayCall{path: statusPath, contentType: rpc.ContentTypeJSON, body: []byte(`{"route":{"value":"r"}}`), header: header})
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	seen, _ := daemon.seen()
	for _, name := range []string{"Authorization", "Cookie", "X-Herdsman-Token", "X-Herdsman-Pair", "Herdsman-Pane", "Herdsman-Name"} {
		if v := seen.Get(name); v != "" {
			t.Errorf("the daemon received %s: %q", name, v)
		}
	}
	want := sessionID(g.device)
	if got := seen.Get(HeaderSession); got != want {
		t.Fatalf("session %q, want %q", got, want)
	}
	if strings.Contains(want, g.device) {
		t.Fatal("the session id reveals the device secret")
	}
	if seen.Get(rpc.HeaderProtocolVersion) != "" {
		t.Fatal("the gateway invented a protocol header the caller did not send")
	}
}

func TestGatewayRefusesACallWithoutASession(t *testing.T) {
	daemon := startFakeDaemon(t)
	g := startGateway(t, daemon.socket, nil)
	for name, header := range map[string]map[string]string{
		"no secret":    nil,
		"wrong secret": bearerHeader("not-a-device"),
		"link token":   bearerHeader("tok"),
	} {
		t.Run(name, func(t *testing.T) {
			status, _, body := send(t, http.DefaultClient, g.url, gatewayCall{path: statusPath, contentType: rpc.ContentTypeJSON, body: []byte(`{}`), header: header})
			if status != http.StatusUnauthorized || wireCode(t, body) != string(rpc.Unauthenticated) {
				t.Fatalf("status %d: %s", status, body)
			}
		})
	}
	if _, hits := daemon.seen(); hits != 0 {
		t.Fatalf("%d calls reached the daemon", hits)
	}
}

func TestGatewayIsLocalhostOnly(t *testing.T) {
	daemon := startFakeDaemon(t)
	g := startGateway(t, daemon.socket, func(c *Config) { c.Hosts = []string{"box.tailnet.ts.net"} })
	refused := func(t *testing.T, status int, body []byte) {
		t.Helper()
		if status != http.StatusForbidden || wireCode(t, body) != string(rpc.PermissionDenied) {
			t.Fatalf("status %d: %s", status, body)
		}
	}
	t.Run("a name that is not loopback", func(t *testing.T) {
		h := bearerHeader(g.device)
		h["Host"] = "box.tailnet.ts.net"
		status, _, body := send(t, http.DefaultClient, g.url, gatewayCall{path: statusPath, contentType: rpc.ContentTypeJSON, body: []byte(`{}`), header: h})
		refused(t, status, body)
	})
	for _, name := range []string{"X-Forwarded-For", "Forwarded", "X-Real-Ip"} {
		t.Run("through a proxy/"+name, func(t *testing.T) {
			h := bearerHeader(g.device)
			h[name] = "203.0.113.9"
			status, _, body := send(t, http.DefaultClient, g.url, gatewayCall{path: statusPath, contentType: rpc.ContentTypeJSON, body: []byte(`{}`), header: h})
			refused(t, status, body)
		})
	}
	t.Run("a listener outside loopback", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, statusPath, strings.NewReader(`{}`))
		req.Host = "127.0.0.1"
		req.Header.Set("Authorization", "Bearer "+g.device)
		req.Header.Set("Content-Type", rpc.ContentTypeJSON)
		req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.IPv4(192, 168, 1, 20), Port: 7878}))
		rec := httptest.NewRecorder()
		g.srv.Handler().ServeHTTP(rec, req)
		refused(t, rec.Code, rec.Body.Bytes())
	})
	if _, hits := daemon.seen(); hits != 0 {
		t.Fatalf("%d calls reached the daemon", hits)
	}
}

func TestListenBindsOnlyTheNamedLoopback(t *testing.T) {
	ln, _, err := Listen("127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if addr := ln.Addr().(*net.TCPAddr); !addr.IP.IsLoopback() {
		t.Fatalf("listening on %s", addr)
	}
}

func TestGatewayOriginRule(t *testing.T) {
	daemon := startFakeDaemon(t)
	g := startGateway(t, daemon.socket, func(c *Config) { c.Origins = []string{"https://app.example"} })
	cases := []struct {
		name, origin string
		want         int
	}{
		{"no origin", "", http.StatusOK},
		{"same host", g.url, http.StatusOK},
		{"the hosted app", "https://app.example", http.StatusOK},
		{"another site", "https://evil.example", http.StatusForbidden},
		{"same name, other scheme", "http://app.example", http.StatusForbidden},
		{"not a URL", "::", http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := bearerHeader(g.device)
			if c.origin != "" {
				h["Origin"] = c.origin
			}
			status, _, body := send(t, http.DefaultClient, g.url, gatewayCall{path: statusPath, contentType: rpc.ContentTypeJSON, body: []byte(`{"route":{"value":"r"}}`), header: h})
			if status != c.want {
				t.Fatalf("status %d: %s", status, body)
			}
		})
	}
}

func TestGatewayReportsADaemonThatIsDown(t *testing.T) {
	g := startGateway(t, tempSocket(t), nil)
	status, _, body := send(t, http.DefaultClient, g.url, gatewayCall{path: statusPath, contentType: rpc.ContentTypeJSON, body: []byte(`{}`), header: bearerHeader(g.device)})
	if status != http.StatusServiceUnavailable || wireCode(t, body) != string(rpc.Unavailable) {
		t.Fatalf("status %d: %s", status, body)
	}
}

func TestGatewayIsAbsentWithoutADaemonSocket(t *testing.T) {
	g := startGateway(t, "", nil)
	status, _, _ := send(t, http.DefaultClient, g.url, gatewayCall{path: statusPath, contentType: rpc.ContentTypeJSON, body: []byte(`{}`), header: bearerHeader(g.device)})
	if status == http.StatusOK || status == http.StatusUnauthorized {
		t.Fatalf("status %d", status)
	}
}

func TestGatewayNeverTunnelsAnUpgrade(t *testing.T) {
	if streamProxy != nil {
		t.Skip("this build carries the stream proxy")
	}
	daemon := startFakeDaemon(t)
	g := startGateway(t, daemon.socket, nil)
	req, err := http.NewRequest(http.MethodGet, g.url+"/rpc/herdsman.daemon.v1.EventService/Stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+g.device)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusNotImplemented || wireCode(t, body) != string(rpc.Unimplemented) {
		t.Fatalf("status %d: %s", res.StatusCode, body)
	}
	if _, hits := daemon.seen(); hits != 0 {
		t.Fatalf("%d calls reached the daemon", hits)
	}
}

func TestGatewayActivityLeavesOutTheLongPoll(t *testing.T) {
	daemon := startFakeDaemon(t)
	file := filepath.Join(t.TempDir(), "activity")
	g := startGateway(t, daemon.socket, func(c *Config) { c.ActivityFile = file })
	wait := gatewayCall{path: "/rpc/herdsman.daemon.v1.EventService/Wait", contentType: rpc.ContentTypeJSON, body: []byte(`{}`), header: bearerHeader(g.device)}
	send(t, http.DefaultClient, g.url, wait)
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("the long poll counted as the user's action: %v", err)
	}
	send(t, http.DefaultClient, g.url, gatewayCall{path: statusPath, contentType: rpc.ContentTypeJSON, body: []byte(`{"route":{"value":"r"}}`), header: bearerHeader(g.device)})
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("a call was not recorded: %v", err)
	}
}

func TestPreflightAllowsTheBindingHeaders(t *testing.T) {
	g := startGateway(t, "", nil)
	req, err := http.NewRequest(http.MethodOptions, g.url+statusPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://app.example")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	allowed := res.Header.Get("Access-Control-Allow-Headers")
	for _, name := range []string{rpc.HeaderProtocolVersion, rpc.HeaderTimeout} {
		if !strings.Contains(allowed, name) {
			t.Errorf("%s is not allowed: %s", name, allowed)
		}
	}
}

func TestAppPolicyAllowsTheStream(t *testing.T) {
	for _, source := range []string{"ws:", "wss:"} {
		if !strings.Contains(appPolicy, source) {
			t.Errorf("connect-src leaves out %s", source)
		}
	}
}

func TestSessionSecretOfARequest(t *testing.T) {
	cases := []struct {
		name          string
		authorization string
		protocols     []string
		want          string
	}{
		{"none", "", nil, ""},
		{"bearer", "Bearer abc", nil, "abc"},
		{"subprotocol", "", []string{"herdsman.json, " + sessionProtocol + "abc"}, "abc"},
		{"subprotocol on its own line", "", []string{"herdsman.json", sessionProtocol + "abc"}, "abc"},
		{"bearer first", "Bearer abc", []string{sessionProtocol + "other"}, "abc"},
		{"only other protocols", "", []string{"herdsman.json"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if c.authorization != "" {
				req.Header.Set("Authorization", c.authorization)
			}
			req.Header["Sec-Websocket-Protocol"] = c.protocols
			if got := sessionSecret(req); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// The stock client suite, pointed at the gateway through a socket whose only
// addition is what a paired browser on this machine would send.
func TestGatewayPassesDaemonConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the daemon")
	}
	socket := startRealDaemon(t)
	g := startGateway(t, socket, nil)
	front := tempSocket(t)
	ln, err := net.Listen("unix", front)
	if err != nil {
		t.Fatal(err)
	}
	handler := g.srv.Handler()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "127.0.0.1"
		r.Header.Set("Authorization", "Bearer "+g.device)
		loopback := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7878}
		handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, loopback)))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	suite := exec.CommandContext(ctx, "go", "test", "-count=1", "./daemon")
	suite.Dir = filepath.Join("..", "..", "sdk", "conformance")
	suite.Env = append(os.Environ(), "HERDSMAN_CONFORMANCE_SOCKET="+front)
	if out, err := suite.CombinedOutput(); err != nil {
		t.Fatalf("conformance through the gateway: %v\n%s", err, out)
	}
}

// startRealDaemon runs this workspace's daemon under a private home.
func startRealDaemon(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gwd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	binary := filepath.Join(dir, "herdsman")
	if out, err := exec.Command("go", "build", "-o", binary, "github.com/arafathusayn/herdsman/cmd/herdsman").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	home, private := filepath.Join(dir, "h"), filepath.Join(dir, "p")
	for _, path := range []string{home, private} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	daemon := exec.Command(binary, "daemon", "run")
	daemon.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "HERDSMAN_PRIVATE_HOME=" + private}
	var log bytes.Buffer
	daemon.Stdout, daemon.Stderr = &log, &log
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = daemon.Process.Kill()
		_ = daemon.Wait()
	})
	socket := filepath.Join(private, "run", "daemon.sock")
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if conn, err := net.Dial("unix", socket); err == nil {
			_ = conn.Close()
			return socket
		}
	}
	t.Fatalf("the daemon did not listen on its socket:\n%s", log.String())
	return ""
}
