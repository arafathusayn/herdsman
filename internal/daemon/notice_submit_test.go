package daemon

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	"github.com/arafathusayn/herdsman/sdk/client"
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

// A web action that types is a person's typed submit too: a paired browser
// through herdsman web names no process, so the web server, the socket's
// peer, is the process checked as a person's terminal; the same call outside
// the web is refused for naming no process, and every submit rule applies.
func TestBrowserSubmitChecksTheWebServerAsThePerson(t *testing.T) {
	r := newWorkerRig(t, NoticeConfig{})
	var checked []int
	r.h.d.coreRT().cfg.Person = func(c route.Caller) error { checked = append(checked, c.PID); return nil }
	ctx := testCtxD(t)
	req := &daemonv1.RuntimeServiceSubmitRequest{ExecutionId: r.execs[0].ID, Text: "go on", Confirmed: true}
	if _, err := routeClient(t, r.h).Runtime.Submit(ctx, req); codeOf(err) != rpc.PermissionDenied || !strings.Contains(err.Error(), "claims process 0") {
		t.Fatalf("no process outside the web: %v", err)
	}
	web, err := client.Dial(client.Options{Socket: SocketPath(r.h.ph), Header: http.Header{"Herdsman-Session": {"s-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(web.Close)
	r.w.Procs.Put(os.Getpid(), 1, "herdsman", "web")
	if _, err := web.Runtime.Submit(ctx, req); codeOf(err) != rpc.FailedPrecondition || !strings.Contains(err.Error(), core.CodeSubmitGate) {
		t.Fatalf("browser, unapproved: %v", err)
	}
	if len(checked) != 1 || checked[0] != os.Getpid() {
		t.Fatalf("person checks %v", checked)
	}
	req.Confirmed = false
	if _, err := web.Runtime.Submit(ctx, req); codeOf(err) != rpc.FailedPrecondition || !strings.Contains(err.Error(), "confirmation") {
		t.Fatalf("unconfirmed from the browser: %v", err)
	}
	if n := r.w.Core.Calls("terminal.send"); n != 0 {
		t.Fatalf("typed %d times", n)
	}
}
