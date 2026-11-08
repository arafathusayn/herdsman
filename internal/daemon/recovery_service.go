package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/statedir"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// sweepEvery is how often expired snapshots are pruned.
var sweepEvery = time.Hour

// mutationPoll is how often a mutating request retries the export gate.
const mutationPoll = 10 * time.Millisecond

// journaledRequests take no export gate: the journal's account lock already
// holds them back, and one that waited on the gate while holding a journal
// wait would deadlock with the export.
var journaledRequests = map[string]bool{
	"POST /v1/shutdown":                         true,
	"POST /v1/ops/{id}/steps/{ordinal}/resolve": true,
}

// enterMutation admits a request that changes route or registry state, or
// gives up with the caller. A waiting export takes the gate ahead of new
// requests.
func (d *Daemon) enterMutation(ctx context.Context) (func(), *APIError) {
	gaveUp := Errf("busy", "an export is running and this request gave up waiting; retry")
	for !d.exportGate.TryRLock() {
		select {
		case <-ctx.Done():
			return nil, gaveUp
		case <-time.After(mutationPoll):
		}
	}
	if ctx.Err() != nil {
		d.exportGate.RUnlock()
		return nil, gaveUp
	}
	return d.exportGate.RUnlock, nil
}

// quiesce holds back every writer outside the journal for an export.
func (d *Daemon) quiesce() func() {
	d.exportGate.Lock()
	return d.exportGate.Unlock
}

// recoveryContext outlives the request: a client that goes away must not stop
// an undo or an export halfway.
func (d *Daemon) recoveryContext() (context.Context, *APIError) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.opsCtx == nil || d.upgrading {
		return nil, Errf("failed_precondition", "the daemon is not accepting operations now")
	}
	return d.opsCtx, nil
}

func recoveryError(err error) *APIError {
	if errors.Is(err, ops.ErrNotFound) {
		return Errf("not_found", "%v", err)
	}
	return Errf("failed_precondition", "%v", err)
}

// opsService is the daemon's OpsService.
type opsService struct{ d *Daemon }

// admit refuses a change in inspect mode and gives the change a context that
// outlives the call.
func (s *opsService) admit(ctx context.Context) (context.Context, error) {
	if s.d.cfg.Inspect {
		return nil, rpcFailure(ctx, Errf("failed_precondition", "this daemon runs in inspect mode and changes nothing"))
	}
	run, e := s.d.recoveryContext()
	if e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return run, nil
}

// Undo restores a snapshot.
func (s *opsService) Undo(ctx context.Context, q *daemonv1.UndoRequest) (*daemonv1.UndoResponse, error) {
	run, err := s.admit(ctx)
	if err != nil {
		return nil, err
	}
	res, err := s.d.recovery.Undo(run, ops.UndoRequest{Snapshot: q.GetSnapshot(), Target: q.GetTarget()})
	if err != nil {
		return nil, rpcFailure(ctx, recoveryError(err))
	}
	return &daemonv1.UndoResponse{OperationId: res.Op.ID, Target: res.Target, Original: res.Original}, nil
}

// Export copies the account to a new folder.
func (s *opsService) Export(ctx context.Context, q *daemonv1.ExportRequest) (*daemonv1.ExportResponse, error) {
	run, err := s.admit(ctx)
	if err != nil {
		return nil, err
	}
	op, err := s.d.recovery.Export(run, q.GetDest())
	if err != nil {
		return nil, rpcFailure(ctx, recoveryError(err))
	}
	return &daemonv1.ExportResponse{OperationId: op.ID, Dest: q.GetDest()}, nil
}

// Import rebuilds an account in another state directory: this daemon's own
// is in use by this daemon.
func (s *opsService) Import(ctx context.Context, q *daemonv1.ImportRequest) (*daemonv1.ImportResponse, error) {
	run, err := s.admit(ctx)
	if err != nil {
		return nil, err
	}
	target := q.GetStateDir()
	if target == "" {
		return nil, rpc.Errorf(rpc.InvalidArgument, "the request names no state directory")
	}
	if rel, err := filepath.Rel(s.d.cfg.PrivateHome, target); err == nil && filepath.IsLocal(rel) {
		return nil, rpc.Errorf(rpc.InvalidArgument, "%s is inside this daemon's own state directory", target)
	}
	if err := ops.Import(run, q.GetSource(), target); err != nil {
		return nil, rpcFailure(ctx, recoveryError(err))
	}
	return &daemonv1.ImportResponse{StateDir: target}, nil
}

// Records lists the account's records and snapshots.
func (s *opsService) Records(ctx context.Context, _ *daemonv1.RecordsRequest) (*daemonv1.RecordsResponse, error) {
	var recs []store.Record
	err := s.d.ops.Read(func(c store.Conn) error {
		var err error
		recs, err = store.AccountRecords(c)
		return err
	})
	if err != nil {
		return nil, rpcFailure(ctx, Errf("internal", "records: %v", err))
	}
	list, err := s.d.recovery.Snapshots()
	if err != nil {
		return nil, rpcFailure(ctx, Errf("internal", "snapshots: %v", err))
	}
	res := &daemonv1.RecordsResponse{Digest: store.Digest(recs)}
	for _, r := range recs {
		res.Records = append(res.Records, &daemonv1.AccountRecord{Kind: r.Kind, Id: r.ID, Fields: r.Fields})
	}
	for _, snap := range list {
		res.Snapshots = append(res.Snapshots, &daemonv1.SnapshotInfo{Id: snap.ID, Task: snap.Task, Size: snap.Size, Created: snap.Created})
	}
	return res, nil
}

// sweep prunes expired snapshots; a failure waits for the next round.
func (d *Daemon) sweep(ctx context.Context) {
	if _, err := d.recovery.Sweep(ctx); err != nil && !errors.Is(err, context.Canceled) {
		d.logf("retention: %v", err)
	}
}

// sweepLater arms the next sweep. A timer rather than a loop, so no goroutine
// waits between rounds.
func (d *Daemon) sweepLater(ctx context.Context) {
	time.AfterFunc(sweepEvery, func() {
		d.mu.Lock()
		if d.upgrading || ctx.Err() != nil {
			d.mu.Unlock()
			return
		}
		d.bg.Add(1)
		d.mu.Unlock()
		defer d.bg.Done()
		d.sweep(ctx)
		d.sweepLater(ctx)
	})
}

// ImportAccount rebuilds an account in this process, for `herdsman import`:
// its state directory must not have a daemon yet, so nothing can serve it.
// An empty state means the one this account would use.
func ImportAccount(ctx context.Context, source, state string) (string, error) {
	if state == "" {
		dir, err := statedir.Resolve("", os.Getenv)
		if err != nil {
			return "", err
		}
		state = dir.Root
	}
	if err := ops.Import(ctx, source, state); err != nil {
		return "", err
	}
	return state, nil
}
