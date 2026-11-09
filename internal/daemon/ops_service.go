package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/arafathusayn/herdsman/internal/ops"
)

// OpsList is the operation journal.
type OpsList struct {
	Operations []*ops.Operation `json:"operations"`
}

// ResolveRequest is what happened to a step.
type ResolveRequest struct {
	Decision string `json:"decision"`
}

func (d *Daemon) listOps() ([]*ops.Operation, *APIError) {
	list, err := d.ops.List()
	if err != nil {
		return nil, Errf("internal", "operations: %v", err)
	}
	return list, nil
}

func (d *Daemon) getOp(id string) (*ops.Operation, *APIError) {
	op, err := d.ops.Get(id)
	if errors.Is(err, ops.ErrNotFound) {
		return nil, Errf("not_found", "no operation %s", id)
	}
	if err != nil {
		return nil, Errf("internal", "operation: %v", err)
	}
	return op, nil
}

func (d *Daemon) resolveOp(id string, step int, q ResolveRequest) (*ops.Operation, *APIError) {
	var dec ops.Decision
	switch q.Decision {
	case "done":
		dec = ops.ResolveDone
	case "rerun":
		dec = ops.ResolveRerun
	default:
		return nil, Errf("bad_request", "decision must be done or rerun, got %q", q.Decision)
	}
	if err := d.ops.Resolve(id, step, dec); err != nil {
		if errors.Is(err, ops.ErrNotFound) {
			return nil, Errf("not_found", "no operation %s", id)
		}
		return nil, Errf("failed_precondition", "%v", err)
	}
	d.driveLater(id)
	op, err := d.ops.Get(id)
	if err != nil {
		return nil, Errf("internal", "operation: %v", err)
	}
	return op, nil
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
		l, err := c.ListOperations(ctx)
		if err != nil {
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
		op, err := c.GetOperation(ctx, args[1])
		if err != nil {
			return a.fail(err)
		}
		if a.JSON {
			a.out(op, "")
			return 0
		}
		printOpLine(a, op)
		for _, s := range op.Steps {
			fmt.Fprintf(a.Stdout, "  %d %s %s key=%s retry=%s", s.Ordinal, s.State, s.Name, s.Key, s.Retry)
			if s.Detail != "" {
				fmt.Fprintf(a.Stdout, " (%s)", s.Detail)
			}
			fmt.Fprintln(a.Stdout)
		}
		return 0
	case len(args) == 4 && args[0] == "resolve":
		op, err := c.ResolveOperation(ctx, args[1], args[2], ResolveRequest{Decision: args[3]})
		if err != nil {
			return a.fail(err)
		}
		if a.JSON {
			a.out(op, "")
			return 0
		}
		printOpLine(a, op)
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
