package herdr_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
)

func fixture(t *testing.T) (*herdrtest.Server, *herdrtest.Fixture, *herdr.Client) {
	t.Helper()
	srv := herdrtest.Start(t)
	return srv, herdrtest.Serve(srv), herdr.New(srv.Socket)
}

func TestAgentGetByPaneAndName(t *testing.T) {
	_, fx, c := fixture(t)
	a := herdrtest.Agent("w1:p1", "sess-1")
	a["name"] = "im-a"
	fx.SetAgent("w1:p1", a)
	ctx := context.Background()
	for _, target := range []string{"w1:p1", "im-a"} {
		got, err := c.AgentGet(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		if got.PaneID != "w1:p1" || got.Status != "idle" || *got.TerminalID != "term-w1:p1" || got.Revision.String() != "1" ||
			!strings.Contains(string(got.AgentSession), `"sess-1"`) || !strings.Contains(string(got.Raw), `"interactive_ready":true`) {
			t.Fatalf("%s: got %+v", target, got)
		}
	}
	_, err := c.AgentGet(ctx, "nobody")
	if he, ok := errors.AsType[*herdr.Error](err); !ok || he.Code != "agent_not_found" {
		t.Fatalf("err = %v", err)
	}
}

func TestAgentGetRejectsAnEmptyAnswer(t *testing.T) {
	srv := herdrtest.Start(t)
	srv.Handle("agent.get", func(map[string]any) (any, string) { return map[string]any{"agent": nil}, "" })
	if _, err := herdr.New(srv.Socket).AgentGet(context.Background(), "x"); err == nil {
		t.Fatal("no error for a missing agent")
	}
	srv.Handle("agent.get", func(map[string]any) (any, string) { return map[string]any{"agent": []int{1}}, "" })
	if _, err := herdr.New(srv.Socket).AgentGet(context.Background(), "x"); err == nil {
		t.Fatal("no error for a malformed agent")
	}
}

func TestAgentListAndPaneList(t *testing.T) {
	_, fx, c := fixture(t)
	fx.SetAgent("b", herdrtest.Agent("b", "s2"))
	fx.SetAgent("a", herdrtest.Agent("a", "s1"))
	ctx := context.Background()
	list, err := c.AgentList(ctx)
	if err != nil || len(list) != 2 || list[0].PaneID != "a" {
		t.Fatalf("list = %+v, err = %v", list, err)
	}
	panes, err := c.PaneList(ctx)
	if err != nil || len(panes) != 2 || panes[1].ID != "b" || panes[1].TabID != "fixture-tab" {
		t.Fatalf("panes = %+v, err = %v", panes, err)
	}
	fx.Fail("agent.list", "temporary_failure")
	if _, err := c.AgentList(ctx); err == nil {
		t.Fatal("no error")
	}
}

func TestAgentListRejectsABadRecord(t *testing.T) {
	srv := herdrtest.Start(t)
	srv.Handle("agent.list", func(map[string]any) (any, string) { return map[string]any{"agents": []any{"x"}}, "" })
	if _, err := herdr.New(srv.Socket).AgentList(context.Background()); err == nil {
		t.Fatal("no error")
	}
}

func TestReadTextSendsSourceAndLines(t *testing.T) {
	srv, fx, c := fixture(t)
	fx.SetScreen("w1:p1", "hello")
	ctx := context.Background()
	got, err := c.ReadText(ctx, "w1:p1", herdr.SourceVisible, 60)
	if err != nil || got != "hello" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := c.ReadText(ctx, "w1:p1", herdr.SourceRecentUnwrapped, 0); err != nil {
		t.Fatal(err)
	}
	reqs := srv.Requests()
	if reqs[0].Params["source"] != "visible" || reqs[0].Params["lines"] != float64(60) {
		t.Fatalf("params = %v", reqs[0].Params)
	}
	if reqs[1].Params["source"] != "recent_unwrapped" || reqs[1].Params["lines"] != nil {
		t.Fatalf("params = %v", reqs[1].Params)
	}
}

func TestPromptFocusMetadataNotification(t *testing.T) {
	srv, fx, c := fixture(t)
	ctx := context.Background()
	if err := c.AgentPrompt(ctx, "w1:p1", "/compact"); err != nil {
		t.Fatal(err)
	}
	if err := c.TabFocus(ctx, "w1:t2"); err != nil {
		t.Fatal(err)
	}
	one := "1"
	if err := c.ReportMetadata(ctx, herdr.Metadata{PaneID: "w1:p1", Source: "herdsman-message-count", Tokens: map[string]*string{"messages": &one, "old": nil}}); err != nil {
		t.Fatal(err)
	}
	if err := c.ReportMetadata(ctx, herdr.Metadata{PaneID: "w1:p1", Source: "s"}); err != nil {
		t.Fatal(err)
	}
	shown, err := c.ShowNotification(ctx, herdr.Notification{Title: "t", Body: "b", Sound: "request"})
	if err != nil || !shown {
		t.Fatalf("shown = %v, %v", shown, err)
	}
	fx.Shown(false)
	if shown, _ := c.ShowNotification(ctx, herdr.Notification{Title: "t"}); shown {
		t.Fatal("shown with notifications off")
	}
	r := srv.Requests()
	if r[0].Params["target"] != "w1:p1" || r[0].Params["text"] != "/compact" || r[1].Params["tab_id"] != "w1:t2" {
		t.Fatalf("requests = %v", r)
	}
	tok := r[2].Params["tokens"].(map[string]any)
	if tok["messages"] != "1" || tok["old"] != nil || r[3].Params["tokens"] != nil {
		t.Fatalf("metadata = %v", r[2].Params)
	}
	if r[4].Params["sound"] != "request" || r[4].Params["body"] != "b" || r[5].Params["body"] != nil {
		t.Fatalf("notification = %v", r[4].Params)
	}
}

func TestWaitAgentStatus(t *testing.T) {
	srv, fx, c := fixture(t)
	fx.SetAgent("w1:p1", herdrtest.Agent("w1:p1", "s"))
	ctx := context.Background()
	// Already there.
	if s, err := c.WaitAgentStatus(ctx, "w1:p1", []herdr.Status{"idle"}, time.Second); err != nil || s != "idle" {
		t.Fatalf("s = %q, err = %v", s, err)
	}
	// A change announced by an event.
	fx.Edit("w1:p1", "agent_status", "working")
	go func() {
		for srv.Subscribers() == 0 {
			time.Sleep(time.Millisecond)
		}
		// Events faster than the waiter reads keep one wake-up.
		for range 5 {
			srv.Emit("pane.agent_status_changed", map[string]any{"pane_id": "w1:p1", "agent_status": "working"})
		}
		fx.Edit("w1:p1", "agent_status", "done")
		srv.Emit("pane.agent_status_changed", map[string]any{"pane_id": "w1:p1", "agent_status": "done"})
	}()
	if s, err := c.WaitAgentStatus(ctx, "w1:p1", []herdr.Status{"idle", "done"}, 5*time.Second); err != nil || s != "done" {
		t.Fatalf("s = %q, err = %v", s, err)
	}
	// No change: the timeout.
	fx.Edit("w1:p1", "agent_status", "working")
	if _, err := c.WaitAgentStatus(ctx, "w1:p1", []herdr.Status{"idle"}, 50*time.Millisecond); !errors.Is(err, herdr.ErrWaitTimeout) {
		t.Fatalf("err = %v, want timeout", err)
	}
	// The caller's context ends first.
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := c.WaitAgentStatus(cctx, "w1:p1", []herdr.Status{"idle"}, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the context's", err)
	}
	// An unknown agent.
	if _, err := c.WaitAgentStatus(ctx, "nobody", []herdr.Status{"idle"}, time.Second); err == nil {
		t.Fatal("no error")
	}
}

func TestWaitAgentStatusFailures(t *testing.T) {
	srv, fx, c := fixture(t)
	fx.SetAgent("w1:p1", herdrtest.Agent("w1:p1", "s"))
	fx.Edit("w1:p1", "agent_status", "working")
	ctx := context.Background()
	// The subscription is refused.
	srv.Handle("events.subscribe", func(map[string]any) (any, string) { return nil, "bad_subscription" })
	if _, err := c.WaitAgentStatus(ctx, "w1:p1", []herdr.Status{"idle"}, time.Second); err == nil || errors.Is(err, herdr.ErrWaitTimeout) {
		t.Fatalf("err = %v", err)
	}
	// The agent disappears during the wait (a fresh server: the refused
	// subscription above may still count as a subscriber).
	srv, fx, c = fixture(t)
	fx.SetAgent("w1:p1", herdrtest.Agent("w1:p1", "s"))
	fx.Edit("w1:p1", "agent_status", "working")
	go func() {
		for srv.Subscribers() == 0 {
			time.Sleep(time.Millisecond)
		}
		fx.SetAgent("w1:p1", nil)
		srv.Emit("pane.agent_status_changed", map[string]any{"pane_id": "w1:p1"})
	}()
	if _, err := c.WaitAgentStatus(ctx, "w1:p1", []herdr.Status{"idle"}, 5*time.Second); err == nil || errors.Is(err, herdr.ErrWaitTimeout) {
		t.Fatalf("err = %v", err)
	}
}

// R2 finding 3: the timeout covers the whole wait, the first read too. A
// first answer that comes after the timeout is a timeout, never a success.
func TestWaitAgentStatusTimesTheFirstRead(t *testing.T) {
	srv, _, c := fixture(t)
	srv.Handle("agent.get", func(map[string]any) (any, string) {
		time.Sleep(300 * time.Millisecond)
		a := herdrtest.Agent("w1:p1", "s")
		return map[string]any{"type": "agent_info", "agent": a}, ""
	})
	start := time.Now()
	_, err := c.WaitAgentStatus(context.Background(), "w1:p1", []herdr.Status{"idle"}, 10*time.Millisecond)
	if !errors.Is(err, herdr.ErrWaitTimeout) {
		t.Errorf("err = %v, want the timeout", err)
	}
	if d := time.Since(start); d > 250*time.Millisecond {
		t.Errorf("the wait took %v, past its timeout", d)
	}
}

// R3 finding 3: a caller's cancellation ends the wait at once, during the
// first read and during a later one, whatever the server does after.
func TestWaitAgentStatusCancelledDuringARead(t *testing.T) {
	for _, later := range []bool{false, true} {
		for _, timeout := range []time.Duration{0, time.Second} {
			srv, _, c := fixture(t)
			received := make(chan struct{}, 4)
			var reads int
			var mu sync.Mutex
			srv.Handle("agent.get", func(map[string]any) (any, string) {
				mu.Lock()
				reads++
				n := reads
				mu.Unlock()
				a := herdrtest.Agent("w1:p1", "s")
				if later && n == 1 {
					a["agent_status"] = "working"
					return map[string]any{"type": "agent_info", "agent": a}, ""
				}
				received <- struct{}{}
				time.Sleep(300 * time.Millisecond)
				return map[string]any{"type": "agent_info", "agent": a}, "" // idle, after the cancellation
			})
			ctx, cancel := context.WithCancel(context.Background())
			go func() {
				if later {
					for srv.Subscribers() == 0 {
						time.Sleep(time.Millisecond)
					}
					srv.Emit("pane.agent_status_changed", map[string]any{"pane_id": "w1:p1"})
				}
				<-received
				cancel()
			}()
			start := time.Now()
			s, err := c.WaitAgentStatus(ctx, "w1:p1", []herdr.Status{"idle"}, timeout)
			if !errors.Is(err, context.Canceled) || s != "" {
				t.Errorf("later=%v timeout=%v: %q %v, want the cancellation", later, timeout, s, err)
			}
			if d := time.Since(start); d > 250*time.Millisecond {
				t.Errorf("later=%v timeout=%v: took %v", later, timeout, d)
			}
			cancel()
		}
	}
}

// R3 sweep (c): Call ends when its context is cancelled, not at the
// connection deadline.
func TestCallEndsOnCancel(t *testing.T) {
	srv, _, c := fixture(t)
	received := make(chan struct{}, 1)
	srv.Handle("pane.list", func(map[string]any) (any, string) {
		received <- struct{}{}
		time.Sleep(300 * time.Millisecond)
		return map[string]any{"panes": []any{}}, ""
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-received; cancel() }()
	start := time.Now()
	if _, err := c.PaneList(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want the cancellation", err)
	}
	if d := time.Since(start); d > 250*time.Millisecond {
		t.Errorf("took %v", d)
	}
}
