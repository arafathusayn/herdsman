package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/statedir"
)

// sweepEvery is how often expired snapshots are pruned.
const sweepEvery = time.Hour

// ExportRequest is POST /v1/export.
type ExportRequest struct {
	Dest string `json:"dest"`
}

// ImportRequest asks for an account to be rebuilt from an export.
type ImportRequest struct {
	Source   string `json:"source"`
	StateDir string `json:"state_dir,omitempty"`
}

// ImportResponse says where the account now lives.
type ImportResponse struct {
	StateDir string `json:"state_dir"`
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

func (d *Daemon) undo(w http.ResponseWriter, r *http.Request) *APIError {
	var q ops.UndoRequest
	if e := decode(r, &q); e != nil {
		return e
	}
	ctx, e := d.recoveryContext()
	if e != nil {
		return e
	}
	res, err := d.recovery.Undo(ctx, q)
	if err != nil {
		return recoveryError(err)
	}
	writeJSON(w, 200, res)
	return nil
}

func (d *Daemon) export(w http.ResponseWriter, r *http.Request) *APIError {
	var q ExportRequest
	if e := decode(r, &q); e != nil {
		return e
	}
	ctx, e := d.recoveryContext()
	if e != nil {
		return e
	}
	op, err := d.recovery.Export(ctx, q.Dest)
	if err != nil {
		return recoveryError(err)
	}
	writeJSON(w, 200, op)
	return nil
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

// RecoveryCaller serves the undo, export and import verbs: the first two
// through the running daemon, which owns the journal, and import in this
// process, because its state directory must not have a daemon yet.
func RecoveryCaller(version string) func(context.Context, string, []byte) ([]byte, error) {
	return func(ctx context.Context, method string, request []byte) ([]byte, error) {
		switch method {
		case "Undo":
			var q ops.UndoRequest
			if err := json.Unmarshal(request, &q); err != nil {
				return nil, err
			}
			var res ops.UndoResult
			return forward(ctx, version, "/v1/undo", q, &res)
		case "Export":
			var q ExportRequest
			if err := json.Unmarshal(request, &q); err != nil {
				return nil, err
			}
			var op ops.Operation
			return forward(ctx, version, "/v1/export", q, &op)
		case "Import":
			return importAccount(ctx, request)
		}
		return nil, fmt.Errorf("recovery service: no method %q", method)
	}
}

func forward(ctx context.Context, version, path string, in, out any) ([]byte, error) {
	dir, err := statedir.Resolve("", os.Getenv)
	if err != nil {
		return nil, err
	}
	c := NewClient(SocketPath(dir.Root), version)
	defer c.Close()
	if _, err := c.Do(ctx, "POST", path, in, out); err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

func importAccount(ctx context.Context, request []byte) ([]byte, error) {
	var q ImportRequest
	if err := json.Unmarshal(request, &q); err != nil {
		return nil, err
	}
	if q.StateDir == "" {
		dir, err := statedir.Resolve("", os.Getenv)
		if err != nil {
			return nil, err
		}
		q.StateDir = dir.Root
	}
	if err := ops.Import(ctx, q.Source, q.StateDir); err != nil {
		return nil, err
	}
	return json.Marshal(ImportResponse{StateDir: q.StateDir})
}
