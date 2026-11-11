package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/sdk/client"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// fakeNotices answers the notice verbs from a script of wait answers.
type fakeNotices struct {
	waits    []*daemonv1.NoticeServiceWaitResponse
	failures int
	requests []*daemonv1.NoticeServiceWaitRequest
	ack      *daemonv1.NoticeServiceAckRequest
}

func (f *fakeNotices) Wait(_ context.Context, req *daemonv1.NoticeServiceWaitRequest) (*daemonv1.NoticeServiceWaitResponse, error) {
	copied := &daemonv1.NoticeServiceWaitRequest{WaitId: req.GetWaitId(), WaitGeneration: req.GetWaitGeneration(), After: req.GetAfter()}
	f.requests = append(f.requests, copied)
	if f.failures > 0 {
		f.failures--
		return nil, rpc.Errorf(rpc.Unavailable, "busy")
	}
	res := f.waits[0]
	f.waits = f.waits[1:]
	return res, nil
}

func (f *fakeNotices) Read(_ context.Context, req *daemonv1.NoticeServiceReadRequest) (*daemonv1.NoticeServiceReadResponse, error) {
	return &daemonv1.NoticeServiceReadResponse{Notice: &daemonv1.Notice{Id: "n-1", Kind: "brief", State: "claimed"}, Content: "do it", Token: "tok",
		Claim: &daemonv1.NoticeClaim{NoticeId: "n-1", RecordPath: "/r/n-1.md"}, Next: "herdsman notice wait --after n-1"}, nil
}

func (f *fakeNotices) Ack(_ context.Context, req *daemonv1.NoticeServiceAckRequest) (*daemonv1.NoticeServiceAckResponse, error) {
	f.ack = req
	return &daemonv1.NoticeServiceAckResponse{Notice: &daemonv1.Notice{Id: req.GetId(), State: "handled"}, Next: "herdsman notice wait"}, nil
}

func withFakeNotices(t *testing.T, f *fakeNotices) {
	t.Helper()
	oldDial, oldPause := dialNotice, noticePause
	dialNotice = func() (daemonv1.NoticeService, func(), error) { return f, func() {}, nil }
	noticePause = func(int) time.Duration { return 0 }
	t.Cleanup(func() { dialNotice, noticePause = oldDial, oldPause })
}

func runNoticeArgs(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := runNotice(args, nil, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// The wait command is one command over many polls: the first asks with its
// wait id and no generation, every later poll renews with the generation it
// got, a busy daemon is retried, and the answer prints the next command.
func TestNoticeWaitPollsAndRenews(t *testing.T) {
	f := &fakeNotices{failures: 1, waits: []*daemonv1.NoticeServiceWaitResponse{
		{Answer: "none", WaitGeneration: 4},
		{Answer: "none", WaitGeneration: 4},
		{Answer: "notice", WaitGeneration: 4, Notice: &daemonv1.Notice{Id: "n-9", Kind: "continue"}, Next: "herdsman notice read n-9"},
	}}
	withFakeNotices(t, f)
	code, out, errOut := runNoticeArgs("wait", "--after", "n-1")
	if code != 0 || !strings.Contains(out, "notice n-9") || !strings.Contains(out, "next: herdsman notice read n-9") {
		t.Fatalf("wait: %d %q %q", code, out, errOut)
	}
	if len(f.requests) != 4 {
		t.Fatalf("polls: %d", len(f.requests))
	}
	id := f.requests[0].GetWaitId()
	for i, r := range f.requests {
		if r.GetWaitId() != id || r.GetAfter() != "n-1" {
			t.Fatalf("poll %d: %+v", i, r)
		}
	}
	// The failed first call is repeated as a first call; later polls renew.
	if f.requests[0].GetWaitGeneration() != 0 || f.requests[1].GetWaitGeneration() != 0 || f.requests[2].GetWaitGeneration() != 4 {
		t.Fatalf("generations: %v", f.requests)
	}
}

// A replaced wait exits without polling again, with code 3.
func TestNoticeWaitReplacedExits(t *testing.T) {
	f := &fakeNotices{waits: []*daemonv1.NoticeServiceWaitResponse{{Answer: "replaced", Reason: "a newer wait owns this execution"}}}
	withFakeNotices(t, f)
	if code, out, _ := runNoticeArgs("wait"); code != 3 || !strings.Contains(out, "replaced") || len(f.requests) != 1 {
		t.Fatalf("replaced: %d %q %d", code, out, len(f.requests))
	}
}

func TestNoticeReadAndAck(t *testing.T) {
	f := &fakeNotices{}
	withFakeNotices(t, f)
	code, out, _ := runNoticeArgs("read")
	if code != 0 || !strings.Contains(out, "token: tok") || !strings.Contains(out, "do it") || !strings.Contains(out, "next: herdsman notice wait --after n-1") {
		t.Fatalf("read: %d %q", code, out)
	}
	if code, _, errOut := runNoticeArgs("ack", "n-1", "--token", "tok"); code != 2 || !strings.Contains(errOut, "--record") {
		t.Fatalf("ack without a record: %d %q", code, errOut)
	}
	code, out, _ = runNoticeArgs("ack", "n-1", "--token", "tok", "--outcome", "completed", "--record", "shipped")
	if code != 0 || f.ack.GetRecord() != "shipped" || !strings.Contains(out, "next: herdsman notice wait") {
		t.Fatalf("ack: %d %q %+v", code, out, f.ack)
	}
}

// Outside a managed execution the verbs say what they serve.
func TestNoticeVerbsNeedAManagedExecution(t *testing.T) {
	_, _, err := client.DialNotice("", func(string) string { return "" }, nil)
	if !errors.Is(err, client.ErrNotManaged) || !strings.Contains(err.Error(), "managed") {
		t.Fatalf("dial: %v", err)
	}
	old := dialNotice
	dialNotice = func() (daemonv1.NoticeService, func(), error) { return nil, nil, client.ErrNotManaged }
	t.Cleanup(func() { dialNotice = old })
	if code, _, errOut := runNoticeArgs("read"); code != 1 || !strings.Contains(errOut, "HERDSMAN_NOTICE_SOCKET") {
		t.Fatalf("read outside: %d %q", code, errOut)
	}
}

func (f *fakeRuntime) Submit(_ context.Context, req *daemonv1.RuntimeServiceSubmitRequest) (*daemonv1.RuntimeServiceSubmitResponse, error) {
	f.submit = req
	return &daemonv1.RuntimeServiceSubmitResponse{SubmitId: "s-1", State: "typed"}, nil
}

// A typed submit is confirmed by the person: without --yes it asks, and a
// refusal types nothing.
func TestRuntimeSubmitConfirms(t *testing.T) {
	f := &fakeRuntime{}
	withFakeRuntime(t, f)
	old := confirmSubmit
	t.Cleanup(func() { confirmSubmit = old })
	asked := ""
	confirmSubmit = func(w io.Writer) bool {
		asked = submitWarning
		return false
	}
	if code, _, _ := runRuntimeArgs("submit", "e-1", "go on"); code != 2 || f.submit != nil || !strings.Contains(asked, "200 ms") {
		t.Fatalf("unconfirmed: %d %+v", code, f.submit)
	}
	code, out, _ := runRuntimeArgs("submit", "e-1", "go on", "--yes")
	if code != 0 || !f.submit.GetConfirmed() || f.submit.GetExecutionId() != "e-1" || f.submit.GetPid() == 0 || !strings.Contains(out, "typed") {
		t.Fatalf("confirmed: %d %q %+v", code, out, f.submit)
	}
}
