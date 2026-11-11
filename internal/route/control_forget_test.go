package route_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/route"
)

// memRegistry is the daemon's routes.json as a map of route to inbox.
type memRegistry struct {
	mu     sync.Mutex
	routes map[string]string
	// forgotten records, per call, whether the inbox was already gone.
	forgotten []bool
}

func (m *memRegistry) Registered(name, inbox string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.routes[name] == inbox
}

func (m *memRegistry) Forget(_ context.Context, name, inbox string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err := os.Stat(inbox)
	m.forgotten = append(m.forgotten, errors.Is(err, os.ErrNotExist))
	if m.routes[name] == inbox {
		delete(m.routes, name)
	}
	return nil
}

// A core route with an adjutant: its start journals the folder step before
// the inbox, and the verbs still find the inbox. A pause holds each party's
// channel, and a delete removes the inbox and forgets the channel.
func TestCoreVerbsFindTheRouteInbox(t *testing.T) {
	r := newCoreRig(t)
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@e", "commit", "-q", "--allow-empty", "-m", "base"}} {
		if out, err := exec.Command("git", append([]string{"-C", r.repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	r.recovery = &pipeline{repo: r.repo}
	r.down = func(context.Context, string, string) error { r.downs++; return nil }
	reg := &memRegistry{routes: map[string]string{}}
	r.channels = reg
	r.w.OpenApp()
	e, closeDB := r.engine()
	defer closeDB()
	p := r.plan(r.request())
	if p.Inbox == "" {
		t.Fatal("a route with an adjutant has no inbox")
	}
	reg.routes["harbor"] = p.Inbox
	op := drive(t, e, p.Spec)
	r.attestShell(p.Key)
	for i := 0; op.State != ops.OpDone; i++ {
		if i > 8 || op.State == ops.OpFailed {
			t.Fatalf("start: %s %q", op.State, lastError(op))
		}
		if err := route.Continue(e, r.callers, p, op, nil); err != nil {
			t.Fatal(err)
		}
		var err error
		if op, err = e.Drive(context.Background(), op.ID); err != nil {
			t.Fatal(err)
		}
	}
	start, err := e.Get(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	pause, err := route.PlanControl(context.Background(), route.ControlRequest{Verb: route.VerbPause, Start: start, Generation: 1, Date: "2026-11-11"})
	if err != nil {
		t.Fatal(err)
	}
	if got := drive(t, e, pause.Spec); got.State != ops.OpDone {
		t.Fatalf("pause: %s %q", got.State, lastError(got))
	}
	for party := range r.channel.parties {
		if !r.channel.Held("harbor", party) {
			t.Errorf("the pause did not hold %s's channel:\n%s", party, pause.Text)
		}
	}
	del, err := route.PlanControl(context.Background(), route.ControlRequest{Verb: route.VerbDelete, Start: start, Generation: 2, Force: true, Date: "2026-11-11"})
	if err != nil {
		t.Fatal(err)
	}
	if got := drive(t, e, del.Spec); got.State != ops.OpDone {
		t.Fatalf("delete: %s %q", got.State, lastError(got))
	}
	if _, err := os.Stat(p.Inbox); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the inbox survived the delete: %v", err)
	}
	if reg.Registered("harbor", p.Inbox) || len(reg.forgotten) != 1 || !reg.forgotten[0] {
		t.Errorf("the channel: registered %v, forget calls %v\n%s", reg.Registered("harbor", p.Inbox), reg.forgotten, del.Text)
	}
}

// A delete leaves no channel behind: once the route's inbox is removed, the
// daemon forgets the route registered at it, on the TUI as on core; a route
// of the same name at another inbox is someone else's and is kept.
func TestDeleteForgetsTheRouteChannel(t *testing.T) {
	r, _ := recoverable(t)
	reg := &memRegistry{routes: map[string]string{}}
	r.channels = reg
	p, start, e, done := deleted(t, r)
	defer done()
	reg.routes["harbor"] = p.Inbox
	reg.routes["other"] = p.Inbox + "-elsewhere"
	c := control(t, start, route.VerbDelete, 2)
	if !strings.Contains(c.Text, "forget the route's daemon channel") {
		t.Fatalf("the delete's plan has no forget step:\n%s", c.Text)
	}
	op := drive(t, e, c.Spec)
	if op.State != ops.OpDone {
		t.Fatalf("%s: %s", op.State, op.Reason)
	}
	if reg.Registered("harbor", p.Inbox) {
		t.Fatal("the deleted route is still registered")
	}
	if len(reg.forgotten) != 1 || !reg.forgotten[0] {
		t.Fatalf("forget calls (inbox gone at each): %v", reg.forgotten)
	}
	if _, ok := reg.routes["other"]; !ok {
		t.Fatal("another route's channel was forgotten")
	}
	// A rerun of the step finds nothing left to forget.
	if again := drive(t, e, c.Spec); again.State != ops.OpDone || len(reg.forgotten) != 1 {
		t.Fatalf("a rerun: %s, %d forget calls", again.State, len(reg.forgotten))
	}
}
