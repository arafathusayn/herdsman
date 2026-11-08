package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/arafathusayn/herdsman/internal/ops"
)

// OpsList is GET /v1/ops.
type OpsList struct {
	Operations []*ops.Operation `json:"operations"`
}

// ResolveRequest is POST /v1/ops/{id}/steps/{ordinal}/resolve.
type ResolveRequest struct {
	Decision string `json:"decision"`
}

func (d *Daemon) listOps(w http.ResponseWriter, _ *http.Request) *APIError {
	list, err := d.ops.List()
	if err != nil {
		return Errf("internal", "operations: %v", err)
	}
	writeJSON(w, 200, OpsList{Operations: list})
	return nil
}

func (d *Daemon) getOp(w http.ResponseWriter, r *http.Request) *APIError {
	op, err := d.ops.Get(r.PathValue("id"))
	if errors.Is(err, ops.ErrNotFound) {
		return Errf("not_found", "no operation %s", r.PathValue("id"))
	}
	if err != nil {
		return Errf("internal", "operation: %v", err)
	}
	writeJSON(w, 200, op)
	return nil
}

func (d *Daemon) resolveOp(w http.ResponseWriter, r *http.Request) *APIError {
	var q ResolveRequest
	if e := decode(r, &q); e != nil {
		return e
	}
	var dec ops.Decision
	switch q.Decision {
	case "done":
		dec = ops.ResolveDone
	case "rerun":
		dec = ops.ResolveRerun
	default:
		return Errf("bad_request", "decision must be done or rerun, got %q", q.Decision)
	}
	id := r.PathValue("id")
	n, err := strconv.Atoi(r.PathValue("ordinal"))
	if err != nil {
		return Errf("bad_request", "step must be a number, got %q", r.PathValue("ordinal"))
	}
	if err := d.ops.Resolve(id, n, dec); err != nil {
		if errors.Is(err, ops.ErrNotFound) {
			return Errf("not_found", "no operation %s", id)
		}
		return Errf("failed_precondition", "%v", err)
	}
	d.driveLater(id)
	op, err := d.ops.Get(id)
	if err != nil {
		return Errf("internal", "operation: %v", err)
	}
	writeJSON(w, 200, op)
	return nil
}

// driveLater resumes a resolved operation without holding the request.
func (d *Daemon) driveLater(id string) {
	d.mu.Lock()
	ctx := d.opsCtx
	if d.upgrading || ctx == nil {
		d.mu.Unlock()
		return
	}
	d.bg.Add(1)
	d.mu.Unlock()
	go func() {
		defer d.bg.Done()
		if _, err := d.ops.Drive(ctx, id); err != nil && !errors.Is(err, context.Canceled) {
			d.logf("operation %s: %v", id, err)
		}
		d.deliverOutbox(ctx)
	}()
}

// opsCmd is `herdsman daemon ops ...`.
func (a *AdminEnv) opsCmd(ctx context.Context, c *Client, args []string) int {
	switch {
	case len(args) == 1 && args[0] == "list":
		var l OpsList
		if _, err := c.Do(ctx, "GET", "/v1/ops", nil, &l); err != nil {
			return a.fail(err)
		}
		if a.JSON {
			a.out(l, "")
			return 0
		}
		for _, op := range l.Operations {
			printOpLine(a, op)
		}
		return 0
	case len(args) == 2 && args[0] == "show":
		var op ops.Operation
		if _, err := c.Do(ctx, "GET", "/v1/ops/"+url.PathEscape(args[1]), nil, &op); err != nil {
			return a.fail(err)
		}
		if a.JSON {
			a.out(op, "")
			return 0
		}
		printOpLine(a, &op)
		for _, s := range op.Steps {
			fmt.Fprintf(a.Stdout, "  %d %s %s key=%s retry=%s", s.Ordinal, s.State, s.Name, s.Key, s.Retry)
			if s.Detail != "" {
				fmt.Fprintf(a.Stdout, " (%s)", s.Detail)
			}
			fmt.Fprintln(a.Stdout)
		}
		return 0
	case len(args) == 4 && args[0] == "resolve":
		var op ops.Operation
		path := "/v1/ops/" + url.PathEscape(args[1]) + "/steps/" + url.PathEscape(args[2]) + "/resolve"
		if _, err := c.Do(ctx, "POST", path, ResolveRequest{Decision: args[3]}, &op); err != nil {
			return a.fail(err)
		}
		if a.JSON {
			a.out(op, "")
			return 0
		}
		printOpLine(a, &op)
		return 0
	}
	return a.usage("ops list | ops show <id> | ops resolve <id> <step> done|rerun")
}

func printOpLine(a *AdminEnv, op *ops.Operation) {
	done := 0
	for _, s := range op.Steps {
		if s.State == ops.StepDone {
			done++
		}
	}
	fmt.Fprintf(a.Stdout, "%s %s %s %d/%d", op.ID, op.State, op.Kind, done, len(op.Steps))
	if op.Reason != "" {
		fmt.Fprintf(a.Stdout, " %s", op.Reason)
	}
	fmt.Fprintln(a.Stdout)
}
