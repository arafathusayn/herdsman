package daemon

import (
	"context"
	"encoding/json"
	"syscall"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	"github.com/arafathusayn/herdsman/internal/sys"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// submitStore keeps a person's typed submits in the account store.
type submitStore struct{ d *Daemon }

// PutSubmit implements core.SubmitStore.
func (s submitStore) PutSubmit(r core.SubmitRecord) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return s.d.ops.Read(func(c store.Conn) error {
		return store.PutTypedSubmit(c, store.TypedSubmit{ID: r.ID, Execution: r.Execution, State: r.State, Doc: string(b), Created: r.Created.UnixMilli(), Updated: r.Updated.UnixMilli()})
	})
}

// Submits implements core.SubmitStore.
func (s submitStore) Submits(execution string) ([]core.SubmitRecord, error) {
	var rows []store.TypedSubmit
	if err := s.d.ops.Read(func(c store.Conn) error {
		var err error
		rows, err = store.TypedSubmits(c, execution)
		return err
	}); err != nil {
		return nil, err
	}
	out := make([]core.SubmitRecord, 0, len(rows))
	for _, r := range rows {
		var rec core.SubmitRecord
		if err := json.Unmarshal([]byte(r.Doc), &rec); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// peerPID is the process at the other end of the request's socket.
func peerPID(ctx context.Context) (int, error) {
	conn, ok := ctx.Value(connKey{}).(syscall.Conn)
	if !ok {
		return 0, rpc.Errorf(rpc.PermissionDenied, "the request's connection has no peer to verify")
	}
	peer, err := sys.PeerCredentials(conn)
	if err != nil {
		return 0, rpc.Errorf(rpc.PermissionDenied, "read the caller's credentials: %v", err)
	}
	return peer.PID, nil
}

// submitter is the daemon's typed-submit service on core's socket.
func (c *coreRuntime) submitter() *core.Submitter {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.submits == nil {
		c.submits = &core.Submitter{Store: submitStore{c.d}, Records: c.records, Notices: c.d.noticeSvc(), Procs: c.cfg.Procs,
			Dial: func(ctx context.Context) (core.Sender, func(), error) {
				cl, err := hc.Dial(ctx, c.cfg.Socket, hc.Options{})
				if err != nil {
					return nil, nil, err
				}
				return cl, func() { _ = cl.Close() }, nil
			},
			// A typed submit reaches the core its execution's route runs on.
			DialFor: func(ctx context.Context, e runtime.Execution) (core.Sender, func(), error) {
				cl, err := hc.Dial(ctx, c.endpointOf(e.Repo, e.Route), hc.Options{ConnectTimeout: core.DefaultConnect})
				if err != nil {
					return nil, nil, err
				}
				return cl, func() { _ = cl.Close() }, nil
			}}
	}
	return c.submits
}

// Submit implements RuntimeService.Submit: one person's explicit typed
// submit to one execution. The caller is checked as up checks a person's
// terminal: the socket peer is the process the request names, it descends
// from no registered execution, runs under no coding agent and was typed in
// a shell, not started as a core terminal's command.
func (s *runtimeService) Submit(ctx context.Context, req *daemonv1.RuntimeServiceSubmitRequest) (*daemonv1.RuntimeServiceSubmitResponse, error) {
	c := s.d.coreRT()
	if c == nil {
		return nil, rpc.Errorf(rpc.Unavailable, "this daemon runs no herdr-core executions")
	}
	if !req.GetConfirmed() {
		return nil, rpc.Errorf(rpc.FailedPrecondition, "a typed submit needs the person's confirmation: %s", core.SubmitConfirmation)
	}
	pid := req.GetPid()
	if pid == 0 && fromBrowser(ctx) {
		// A paired browser through herdsman web cannot name a process: the
		// web server's own, the socket's peer, is checked as a person's
		// terminal, after the person confirmed in the browser.
		peer, err := peerPID(ctx)
		if err != nil {
			return nil, err
		}
		pid = int64(peer)
	}
	caller, err := (&routeService{d: s.d}).verifiedCaller(ctx, &daemonv1.UpCaller{Pid: pid})
	if err != nil {
		return nil, err
	}
	if caller.Execution != "" {
		return nil, rpc.Errorf(rpc.PermissionDenied, "a typed submit is a person's action, and this request comes from execution %s, an agent", caller.Execution)
	}
	person := c.cfg.Person
	if person == nil {
		person = route.VerifyPerson
	}
	if err := person(caller); err != nil {
		return nil, rpc.Errorf(rpc.PermissionDenied, "a typed submit is a person's action typed in a terminal: %v", err)
	}
	started := s.d.now()
	rec, err := c.submitter().Submit(ctx, core.SubmitRequest{Execution: req.GetExecutionId(), Text: req.GetText(), Again: req.GetAgain()})
	if err == nil {
		s.d.traceSubmit(started, rec)
	}
	if err != nil {
		switch core.CodeOf(err) {
		case core.CodeSubmitOutstanding, core.CodeSubmitNoStatus, core.CodeSubmitState, core.CodeRouteStopping, core.CodeSubmitGate:
			return nil, rpc.Errorf(rpc.FailedPrecondition, "%v", err)
		case core.CodeIdentityNotProven:
			return nil, rpc.Errorf(rpc.NotFound, "%v", err)
		}
		return nil, rpc.Errorf(rpc.Internal, "%v", err)
	}
	if rec.State == core.SubmitUnresolved {
		s.d.publishRuntime(&daemonv1.RuntimeEvent{Kind: "submit_unresolved", ExecutionId: rec.Execution, Detail: rec.Detail})
	}
	return &daemonv1.RuntimeServiceSubmitResponse{SubmitId: rec.ID, State: rec.State, Detail: rec.Detail}, nil
}
