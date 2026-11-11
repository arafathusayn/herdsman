package daemon

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	"github.com/arafathusayn/herdsman/internal/runtime/core/coretest"
	"github.com/arafathusayn/herdsman/sdk/client"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// workerRig is a daemon on a fake core with a route's two executions
// registered, whose worker-socket peer a test chooses.
type workerRig struct {
	h     *harness
	w     *coretest.World
	peer  *atomic.Int64
	execs []runtime.Execution
	sock  string
}

func newWorkerRig(t *testing.T, notices NoticeConfig) *workerRig {
	t.Helper()
	w := coretest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w.AutoWrap(ctx)
	peer := &atomic.Int64{}
	h := newAccountHarnessWith(t, func(dir string, cfg *Config) {
		cfg.Core = CoreConfig{Socket: w.Core.Socket, App: coretest.AppRunner{W: w}, AppHome: w.AppHome, Procs: w.Procs,
			// The launch socket's peer is the world's; the worker socket's
			// is the test's once it names one.
			Peer: func(c net.Conn) (int, error) {
				if p := peer.Load(); p != 0 {
					return int(p), nil
				}
				return w.Peer(c)
			},
			Environ:  []string{"PATH=/usr/bin:/bin", "HOME=" + filepath.Join(dir, "h"), "LANG=C"},
			Timeouts: core.Timeouts{Register: 5 * time.Second, Ready: 5 * time.Second, Visibility: 300 * time.Millisecond, Poll: 10 * time.Millisecond}, NoReconcile: true}
		notices.TickEvery = -1
		cfg.Notice = notices
	})
	if err := os.MkdirAll(filepath.Join(h.home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.home, ".claude", "settings.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := routeClient(t, h)
	repo := filepath.Join(h.ph, "proj")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Route.Up(context.Background(), coreUp(repo)); err != nil {
		t.Fatal(err)
	}
	waitHalt(t, c, repo)
	w.OpenApp()
	for range 2 { // both start lines
		req := coreUp(repo)
		req.Continue = true
		if _, err := c.Route.Up(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		waitHalt(t, c, repo)
	}
	var rows []store.Execution
	if err := h.d.ops.Read(func(c store.Conn) error {
		var err error
		rows, err = store.Executions(c, repo, "harbor")
		return err
	}); err != nil || len(rows) != 2 {
		t.Fatalf("records: %v %d", err, len(rows))
	}
	r := &workerRig{h: h, w: w, peer: peer, sock: NoticeSocketPath(h.ph)}
	for _, row := range rows {
		e, ok, err := noticeExecutions{h.d}.Execution(context.Background(), row.ID)
		if err != nil || !ok {
			t.Fatalf("execution %s: %v", row.ID, err)
		}
		r.execs = append(r.execs, e)
	}
	return r
}

// as dials the worker socket as the process pid.
func (r *workerRig) as(t *testing.T, pid int) daemonv1.NoticeService {
	t.Helper()
	r.peer.Store(int64(pid))
	svc, closeIdle, err := client.DialNotice(r.sock, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeIdle)
	return svc
}

// The worker socket: who can call what. A process outside every registered
// execution gets nothing; an execution reads only its own notices and cannot
// tell another's exist; the socket serves NoticeService and nothing else; its
// file is the owner's alone.
func TestWorkerSocketServesTheCallingExecutionOnly(t *testing.T) {
	r := newWorkerRig(t, NoticeConfig{Poll: 100 * time.Millisecond})
	ctx := testCtxD(t)
	info, err := os.Stat(r.sock)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode: %v %v", info, err)
	}

	// Not an execution: refused before anything is read.
	stranger := r.as(t, os.Getpid())
	if _, err := stranger.Read(ctx, &daemonv1.NoticeServiceReadRequest{}); codeOf(err) != rpc.PermissionDenied {
		t.Fatalf("stranger read: %v", err)
	}
	if _, err := stranger.Wait(ctx, &daemonv1.NoticeServiceWaitRequest{WaitId: "w-1"}); codeOf(err) != rpc.PermissionDenied {
		t.Fatalf("stranger wait: %v", err)
	}

	a, b := r.execs[0], r.execs[1]
	svc := r.h.d.noticeSvc()
	for _, e := range r.execs {
		if err := svc.Approve(e, t.TempDir()); err != nil {
			t.Fatal(err)
		}
	}
	nb, err := svc.Issue(ctx, notice.IssueRequest{Key: "t:b", Kind: notice.KindDispatch, Execution: b.ID, Content: "b's work", Issuer: "op-1"})
	if err != nil {
		t.Fatalf("issue to b: %v", err)
	}

	// A process below a's harness is a; b's notice is unknown to it.
	child := r.w.Procs.Add(int(a.Evidence.PID), "sh", "-c")
	asA := r.as(t, child)
	if _, err := asA.Read(ctx, &daemonv1.NoticeServiceReadRequest{Id: nb.ID}); codeOf(err) != rpc.NotFound {
		t.Fatalf("a read b's notice: %v", err)
	}
	if _, err := asA.Ack(ctx, &daemonv1.NoticeServiceAckRequest{Id: nb.ID, Token: "x", Outcome: "completed", Record: "r"}); err == nil {
		t.Fatal("a acknowledged b's notice")
	}
	if res, err := asA.Read(ctx, &daemonv1.NoticeServiceReadRequest{}); err != nil || res.GetNotice() != nil {
		t.Fatalf("a's own read: %+v %v", res, err)
	}

	// b reads and acknowledges its own.
	asB := r.as(t, int(b.Evidence.PID))
	got, err := asB.Read(ctx, &daemonv1.NoticeServiceReadRequest{})
	if err != nil || got.GetNotice().GetId() != nb.ID || got.GetToken() == "" || got.GetContent() != "b's work" {
		t.Fatalf("b's read: %+v %v", got, err)
	}
	if _, err := asB.Ack(ctx, &daemonv1.NoticeServiceAckRequest{Id: nb.ID, Token: "wrong", Outcome: "completed", Record: "done"}); codeOf(err) != rpc.FailedPrecondition {
		t.Fatalf("wrong token: %v", err)
	}
	if res, err := asB.Ack(ctx, &daemonv1.NoticeServiceAckRequest{Id: nb.ID, Token: got.GetToken(), Outcome: "completed", Record: "done"}); err != nil || res.GetNotice().GetState() != notice.StateHandled {
		t.Fatalf("b's ack: %+v %v", res, err)
	}

	// Nothing of the main socket is served here.
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", r.sock)
	}}}
	t.Cleanup(hc.CloseIdleConnections)
	for _, path := range []string{"/herdsman.daemon.v1.RouteService/Up", "/herdsman.daemon.v1.RuntimeService/Submit", "/herdsman.daemon.v1.DaemonService/Status"} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://notice"+path, bytes.NewReader([]byte("{}")))
		req.Header.Set("Content-Type", "application/json")
		res, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s on the notice socket: %d", path, res.StatusCode)
		}
	}
}

// Wait capacity on the socket: an attached wait gives its request slot back,
// so with one slot in all, a wait held by one execution still lets another
// execution read and acknowledge.
func TestWorkerSocketWaitHoldsNoRequestSlot(t *testing.T) {
	r := newWorkerRig(t, NoticeConfig{Poll: 3 * time.Second, MaxRequests: 1})
	ctx := testCtxD(t)
	a, b := r.execs[0], r.execs[1]
	svc := r.h.d.noticeSvc()
	for _, e := range r.execs {
		if err := svc.Approve(e, t.TempDir()); err != nil {
			t.Fatal(err)
		}
	}
	asA := r.as(t, int(a.Evidence.PID))
	waited := make(chan error, 1)
	go func() {
		_, err := asA.Wait(ctx, &daemonv1.NoticeServiceWaitRequest{WaitId: "w-a"})
		waited <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !svc.Holding(a.ID) {
		if time.Now().After(deadline) {
			t.Fatal("a's wait never attached")
		}
		time.Sleep(10 * time.Millisecond)
	}
	nb, err := svc.Issue(ctx, notice.IssueRequest{Key: "t:b", Kind: notice.KindDispatch, Execution: b.ID, Content: "b", Issuer: "op-1"})
	if err != nil {
		t.Fatal(err)
	}
	asB := r.as(t, int(b.Evidence.PID))
	got, err := asB.Read(ctx, &daemonv1.NoticeServiceReadRequest{Id: nb.ID})
	if err != nil || got.GetToken() == "" {
		t.Fatalf("read beside a wait: %+v %v", got, err)
	}
	if _, err := asB.Ack(ctx, &daemonv1.NoticeServiceAckRequest{Id: nb.ID, Token: got.GetToken(), Outcome: "completed", Record: "ok"}); err != nil {
		t.Fatalf("ack beside a wait: %v", err)
	}
	if err := <-waited; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("a's wait: %v", err)
	}
}

// The daemon's own launch sets the worker socket in each execution's
// environment, and no automated path typed into a terminal.
func TestWorkerSocketReachesTheHarness(t *testing.T) {
	r := newWorkerRig(t, NoticeConfig{})
	for _, e := range r.execs {
		env := r.w.ExecEnv(e.LaunchIntent)
		if !strings.Contains(strings.Join(env, "\n"), "HERDSMAN_NOTICE_SOCKET="+r.sock) {
			t.Errorf("%s: no notice socket in %v", e.Agent, env)
		}
	}
	if n := r.w.Core.Calls("terminal.send"); n != 0 {
		t.Errorf("typed into core %d times", n)
	}
	// The doctor says, before anything more starts, that the route's Codex
	// orchestrator needs a person.
	doc, err := (&runtimeService{d: r.h.d}).Doctor(context.Background(), &daemonv1.RuntimeServiceDoctorRequest{Repo: r.execs[0].Repo, Route: &corev1.RouteId{Value: "harbor"}})
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]string{}
	for _, ch := range doc.GetChecks() {
		checks[ch.GetName()] = ch.GetState() + ": " + ch.GetDetail()
	}
	if !strings.Contains(checks["harness-codex"], "need a person") || !strings.Contains(checks["notice-channel"], r.sock) {
		t.Errorf("doctor: %v", checks)
	}
	if !strings.Contains(checks["route-attendance"], "orchestrator") || strings.Contains(checks["route-attendance"], "adjutant") {
		t.Errorf("route attendance: %q", checks["route-attendance"])
	}
}

func testCtxD(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}
