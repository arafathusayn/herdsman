package daemon

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	"github.com/arafathusayn/herdsman/internal/sys"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
	"google.golang.org/protobuf/encoding/protojson"
)

// The worker socket (spec section 13, ADR 0034): a second daemon socket in
// the private run folder that serves NoticeService and nothing else, for the
// calling execution only. Its request limit is its own, so agent traffic never
// takes the slots route control needs, and an attached wait holds no request
// slot at all.
const (
	// maxWorkerRequests is the worker socket's requests in flight.
	maxWorkerRequests = 32
	// maxWorkerPerExecution is one execution's short calls in flight.
	maxWorkerPerExecution = 2
	// noticeTickEvery is the period of the notice service's look at every
	// running execution.
	noticeTickEvery = 5 * time.Second
)

// NoticeSocketPath is the worker socket of a private home.
func NoticeSocketPath(privateHome string) string {
	return filepath.Join(privateHome, "run", "notice.sock")
}

// NoticeConfig tunes the notice service; zero values take the defaults of
// section 10. Tests shorten the times.
type NoticeConfig struct {
	Poll, Renewal, HeldGrace, WaitGrace, RecoveryGrace, GenerationGap time.Duration
	MaxWaits                                                          int
	// TickEvery is the period of the look for held notices, waits that are
	// not held and claims whose turn ended (default 5 s); negative turns it
	// off for tests that tick by hand.
	TickEvery time.Duration
	// MaxRequests is the worker socket's requests in flight (default 32);
	// tests lower it to show that an attached wait holds no slot.
	MaxRequests int
}

// workerServer is the worker socket's listener and server.
type workerServer struct {
	ln  net.Listener
	srv *http.Server
	sem chan struct{}

	mu       sync.Mutex
	inFlight map[string]int
	// served ends when the server's Serve returns. It is not the daemon's
	// background group: that one guards the account database, which a
	// waiting agent's connection must not hold open.
	served sync.WaitGroup
}

// startNotices opens the notice service over the account store.
func (d *Daemon) startNotices() {
	c := d.cfg.Notice
	boot := randomHex(8)
	svc := notice.New(noticeStore{d}, noticeExecutions{d}, notice.Config{
		Poll: c.Poll, Renewal: c.Renewal, HeldGrace: c.HeldGrace, WaitGrace: c.WaitGrace, RecoveryGrace: c.RecoveryGrace,
		GenerationGap: c.GenerationGap, MaxWaits: c.MaxWaits, Boot: boot, Emit: d.emitNotice, Trace: d.traceNotice,
		Attended: func(kind string) bool {
			a, err := core.AdapterFor(kind)
			return err == nil && !a.Unattended()
		},
	})
	d.mu.Lock()
	d.notices = svc
	d.mu.Unlock()
	// A typed submit core held when the daemon stopped has lost its answer:
	// it is unresolved, never retried and never retired by time.
	if c := d.coreRT(); c != nil {
		if settled, err := c.submitter().Settle(); err != nil {
			d.logf("typed submits: %v", err)
		} else {
			for _, r := range settled {
				d.publishRuntime(&daemonv1.RuntimeEvent{Kind: "submit_unresolved", ExecutionId: r.Execution, Detail: r.Detail})
			}
		}
	}
}

// noticeSvc is the daemon's notice service, nil in a daemon that serves reads.
func (d *Daemon) noticeSvc() *notice.Service {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.notices
}

// tickNotices runs the notice service's periodic look until ctx ends.
func (d *Daemon) tickNotices(ctx context.Context) {
	every := d.cfg.Notice.TickEvery
	if every < 0 {
		return
	}
	if every == 0 {
		every = noticeTickEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.done:
			return
		case <-t.C:
		}
		if svc := d.noticeSvc(); svc != nil {
			if err := svc.Tick(ctx); err != nil && ctx.Err() == nil {
				d.logf("notices: %v", err)
			}
		}
	}
}

// emitNotice publishes what the notice service raised on the runtime topic.
func (d *Daemon) emitNotice(ev notice.Event) {
	d.publishRuntime(&daemonv1.RuntimeEvent{Kind: ev.Kind, Repo: ev.Repo, Route: ev.Route, ExecutionId: ev.Execution, Reason: ev.Reason,
		Detail: ev.Detail, NoticeId: ev.Notice, Outcome: ev.Outcome, Line: ev.Line})
}

// publishRuntime publishes one event of the runtime topic's contract.
func (d *Daemon) publishRuntime(ev *daemonv1.RuntimeEvent) {
	if ev.GetTime() == 0 {
		ev.Time = d.now().UnixMilli()
	}
	d.logf("runtime %s: %s %s %s %s", ev.GetKind(), ev.GetRoute(), ev.GetExecutionId(), ev.GetReason(), ev.GetDetail())
	payload, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(ev)
	if err != nil {
		return
	}
	d.hub.publish("runtime", ev.GetKind(), payload)
	d.fireRuntime(ev)
}

// listenWorker opens the worker socket.
func (d *Daemon) listenWorker() error {
	sock := NoticeSocketPath(d.cfg.PrivateHome)
	_ = os.Remove(sock) // stale: this process holds the account lock
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		_ = ln.Close()
		return err
	}
	limit := d.cfg.Notice.MaxRequests
	if limit <= 0 {
		limit = maxWorkerRequests
	}
	w := &workerServer{ln: &peerListener{Listener: ln}, sem: make(chan struct{}, limit), inFlight: map[string]int{}}
	w.srv = &http.Server{Handler: d.workerHandler(w), ReadHeaderTimeout: 10 * time.Second,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context { return context.WithValue(ctx, connKey{}, c) }}
	d.worker = w
	return nil
}

// serveWorker serves the worker socket until the daemon stops.
func (d *Daemon) serveWorker() {
	if d.worker == nil {
		return
	}
	d.worker.served.Add(1)
	go func() {
		defer d.worker.served.Done()
		_ = d.worker.srv.Serve(d.worker.ln)
	}()
}

// stopWorker ends every wait with an answer to connect again, then closes the
// worker socket.
func (d *Daemon) stopWorker() {
	if svc := d.noticeSvc(); svc != nil {
		svc.Close()
	}
	if d.worker != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = d.worker.srv.Shutdown(ctx)
		cancel()
		d.worker.served.Wait()
		_ = os.Remove(NoticeSocketPath(d.cfg.PrivateHome))
	}
}

type callerKey struct{}

// workerHandler serves NoticeService alone, under the worker socket's own
// request limit. A caller is identified by its process before anything is
// read: one that belongs to no registered execution gets nothing.
func (d *Daemon) workerHandler(w *workerServer) http.Handler {
	svc := &noticeRPC{d: d}
	mux := rpc.NewMux(
		rpc.Unary(daemonv1.NoticeServiceName, "Wait", svc.Wait),
		rpc.Unary(daemonv1.NoticeServiceName, "Read", svc.Read),
		rpc.Unary(daemonv1.NoticeServiceName, "Ack", svc.Ack),
	)
	return http.MaxBytesHandler(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if e := CheckVersion(r.Header, d.cfg.Version); e != nil {
			rpc.WriteError(rw, rpcFailure(r.Context(), e))
			return
		}
		select {
		case w.sem <- struct{}{}:
		case <-time.After(2 * time.Second):
			rpc.WriteError(rw, rpc.Errorf(rpc.Unavailable, "busy: too many notice calls in flight; retry"))
			return
		}
		released := false
		release := func() {
			if !released {
				released = true
				<-w.sem
			}
		}
		defer release()
		caller, err := d.workerCaller(r.Context())
		if err != nil {
			rpc.WriteError(rw, err)
			return
		}
		long := path.Base(r.URL.Path) == "Wait"
		if !long {
			w.mu.Lock()
			if w.inFlight[caller.ID] >= maxWorkerPerExecution {
				w.mu.Unlock()
				rpc.WriteError(rw, rpc.Errorf(rpc.Unavailable, "busy: this execution has %d notice calls in flight; retry", maxWorkerPerExecution))
				return
			}
			w.inFlight[caller.ID]++
			w.mu.Unlock()
			defer func() {
				w.mu.Lock()
				w.inFlight[caller.ID]--
				if w.inFlight[caller.ID] <= 0 {
					delete(w.inFlight, caller.ID)
				}
				w.mu.Unlock()
			}()
		}
		ctx := context.WithValue(r.Context(), callerKey{}, caller)
		ctx = context.WithValue(ctx, releaseKey{}, release)
		mux.ServeHTTP(rw, r.WithContext(ctx))
	}), maxBody)
}

// workerCaller is the execution the request's process belongs to, by the
// socket peer's pid walked up to a registered execution's process (spec
// section 6). Nothing the request carries counts.
func (d *Daemon) workerCaller(ctx context.Context) (runtime.Execution, error) {
	conn, ok := ctx.Value(connKey{}).(net.Conn)
	if !ok {
		return runtime.Execution{}, rpc.Errorf(rpc.PermissionDenied, "the request's connection has no peer to verify")
	}
	c := d.coreRT()
	if c == nil {
		return runtime.Execution{}, rpc.Errorf(rpc.Unavailable, "this daemon runs no herdr-core executions")
	}
	pid, err := c.peerPID(conn)
	if err != nil {
		return runtime.Execution{}, rpc.Errorf(rpc.PermissionDenied, "read the caller's process: %v", err)
	}
	e, ok := c.callerExecution(ctx, pid)
	if !ok {
		return runtime.Execution{}, rpc.Errorf(rpc.PermissionDenied, "process %d belongs to no registered herdr-core execution; the notice socket serves managed executions only", pid)
	}
	return e, nil
}

// peerPID is a connection's peer process: the kernel's record, or the test
// world's.
func (c *coreRuntime) peerPID(conn net.Conn) (int, error) {
	if c.cfg.Peer != nil {
		return c.cfg.Peer(conn)
	}
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return 0, errors.New("not a socket")
	}
	cred, err := sys.PeerCredentials(sc)
	if err != nil {
		return 0, err
	}
	return cred.PID, nil
}

// noticeRPC is NoticeService for the worker socket.
type noticeRPC struct{ d *Daemon }

func callerOf(ctx context.Context) runtime.Execution {
	e, _ := ctx.Value(callerKey{}).(runtime.Execution)
	return e
}

func (s *noticeRPC) service() (*notice.Service, error) {
	svc := s.d.noticeSvc()
	if svc == nil {
		return nil, rpc.Errorf(rpc.Unavailable, "this daemon serves no notices")
	}
	return svc, nil
}

func noticeFailure(err error) error {
	var r *notice.Refusal
	switch {
	case err == nil:
		return nil
	case errors.Is(err, notice.ErrBusy):
		return rpc.Errorf(rpc.Unavailable, "busy: the daemon's wait budget is taken; retry")
	case errors.Is(err, notice.ErrUnknown):
		return rpc.Errorf(rpc.NotFound, "%v", err)
	case errors.Is(err, notice.ErrRecord):
		return rpc.Errorf(rpc.InvalidArgument, "%v", err)
	case errors.Is(err, notice.ErrToken), errors.Is(err, notice.ErrOutcome), errors.Is(err, notice.ErrNotClaim):
		return rpc.Errorf(rpc.FailedPrecondition, "%v", err)
	case errors.As(err, &r):
		return rpc.Errorf(rpc.FailedPrecondition, "%v", err)
	case errors.Is(err, context.Canceled):
		return rpc.Errorf(rpc.Canceled, "%v", err)
	}
	return rpc.Errorf(rpc.Internal, "%v", err)
}

func noticeDoc(n notice.Notice) *daemonv1.Notice {
	if n.ID == "" {
		return nil
	}
	r, _ := notice.RuleOf(n.Kind)
	return &daemonv1.Notice{SchemaVersion: int32(n.SchemaVersion), Id: n.ID, Kind: n.Kind, ExecutionId: n.Execution, Generation: n.Generation,
		IssuedAt: n.IssuedAt.UnixMilli(), ContentDigest: n.Digest, State: n.State, SafeToRepeat: r.SafeToRepeat}
}

func claimDoc(n notice.Notice, may bool) *daemonv1.NoticeClaim {
	if n.ID == "" || n.ClaimedAt.IsZero() {
		return nil
	}
	return &daemonv1.NoticeClaim{NoticeId: n.ID, ClaimedAt: n.ClaimedAt.UnixMilli(), RecordPath: n.RecordPath, ResumedSince: n.ResumedSince, MayContinue: may}
}

// Wait implements NoticeService.Wait: an ordinary request until it attaches,
// then a wait slot of the notice service's own budget and no request slot.
func (s *noticeRPC) Wait(ctx context.Context, req *daemonv1.NoticeServiceWaitRequest) (*daemonv1.NoticeServiceWaitResponse, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}
	releaseContext(ctx)
	a, err := svc.Wait(ctx, callerOf(ctx), notice.WaitRequest{WaitID: req.GetWaitId(), Generation: req.GetWaitGeneration(), After: req.GetAfter()})
	if err != nil {
		return nil, noticeFailure(err)
	}
	out := &daemonv1.NoticeServiceWaitResponse{Answer: a.Answer, WaitGeneration: a.Generation, Next: a.Next, Reason: a.Reason}
	switch a.Answer {
	case notice.AnswerNotice:
		out.Notice = noticeDoc(a.Notice)
	case notice.AnswerRecovery:
		out.Notice, out.Claim = noticeDoc(a.Notice), claimDoc(a.Notice, a.MayContinue)
	}
	return out, nil
}

// Read implements NoticeService.Read.
func (s *noticeRPC) Read(ctx context.Context, req *daemonv1.NoticeServiceReadRequest) (*daemonv1.NoticeServiceReadResponse, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}
	a, err := svc.Read(ctx, callerOf(ctx), req.GetId())
	if err != nil {
		return nil, noticeFailure(err)
	}
	out := &daemonv1.NoticeServiceReadResponse{Notice: noticeDoc(a.Notice), Content: a.Content, Token: a.Token, ClaimedBefore: a.ClaimedBefore,
		Claim: claimDoc(a.Notice, a.MayContinue), Refused: a.Refused, Next: a.Next}
	if a.Notice.State == notice.StateHandled {
		out.Outcome, out.Record = a.Notice.Outcome, a.Notice.Record
	}
	if a.Refused != "" && a.Detail != "" {
		out.Next = a.Next + " (" + a.Detail + ")"
	}
	return out, nil
}

// Ack implements NoticeService.Ack.
func (s *noticeRPC) Ack(ctx context.Context, req *daemonv1.NoticeServiceAckRequest) (*daemonv1.NoticeServiceAckResponse, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}
	a, err := svc.Ack(ctx, callerOf(ctx), req.GetId(), req.GetToken(), req.GetOutcome(), req.GetRecord())
	if err != nil {
		return nil, noticeFailure(err)
	}
	return &daemonv1.NoticeServiceAckResponse{Notice: noticeDoc(a.Notice), Repeated: a.Repeated, Next: a.Next}, nil
}
