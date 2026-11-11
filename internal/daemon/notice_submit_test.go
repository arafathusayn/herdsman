package daemon

import (
	"os"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// A typed submit is a person's: it needs the confirmation, and a request
// whose process descends from an execution is refused as an agent's, with
// nothing typed. A person's shell outside every execution is let through to
// the submit rules.
func TestSubmitIsAPersonsAction(t *testing.T) {
	r := newWorkerRig(t, NoticeConfig{})
	r.h.d.coreRT().cfg.Person = func(route.Caller) error { return nil }
	c := routeClient(t, r.h)
	ctx := testCtxD(t)
	target := r.execs[0]
	req := &daemonv1.RuntimeServiceSubmitRequest{ExecutionId: target.ID, Text: "go on", Pid: int64(os.Getpid())}
	if _, err := c.Runtime.Submit(ctx, req); codeOf(err) != rpc.FailedPrecondition {
		t.Fatalf("unconfirmed: %v", err)
	}
	req.Confirmed = true
	// From inside an execution: refused as an agent.
	r.w.Procs.Put(os.Getpid(), int(r.execs[1].Evidence.PID), "herdsman", "runtime")
	if _, err := c.Runtime.Submit(ctx, req); codeOf(err) != rpc.PermissionDenied || !strings.Contains(err.Error(), "agent") {
		t.Fatalf("agent caller: %v", err)
	}
	if n := r.w.Core.Calls("terminal.send"); n != 0 {
		t.Fatalf("typed %d times", n)
	}
	// A person's shell: the submit rules apply (the pair is not approved).
	r.w.Procs.Put(os.Getpid(), 1, "zsh")
	if _, err := c.Runtime.Submit(ctx, req); codeOf(err) != rpc.FailedPrecondition || !strings.Contains(err.Error(), core.CodeSubmitGate) {
		t.Fatalf("person, unapproved: %v", err)
	}
	if err := r.h.d.noticeSvc().Approve(target, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	res, err := c.Runtime.Submit(ctx, req)
	if err != nil || res.GetState() != core.SubmitTyped {
		t.Fatalf("person: %+v %v", res, err)
	}
	if n := r.w.Core.Calls("terminal.send"); n != 1 {
		t.Fatalf("sends = %d", n)
	}
}
