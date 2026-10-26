package message

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/inbox"
)

type deltaFailedWriter struct{}

func (deltaFailedWriter) Write([]byte) (int, error) {
	return 0, errors.New("injected stdout write failure")
}
func TestReviewDeltaWaitOutputFailure(t *testing.T) {
	c := newCase(t, true)
	c.send()
	c.courier()
	env := map[string]string{"HERDSMAN_INBOX": c.ic.Dir, "HERDSMAN_PREFLIGHT": c.ic.Proof, "HERDR_PANE_ID": "receiver-pane", "WATCH_REQUIRED": "0", "DEADLINE": "2", "RECHECK": "1"}
	e := &Env{Getenv: func(k string) string { return env[k] }, Stdout: deltaFailedWriter{}, Stderr: io.Discard, Client: herdr.New(c.srv.Socket), Now: func() time.Time { return c.now }, Sleep: func(d time.Duration) { c.now = c.now.Add(d) }}
	code := e.Wait([]string{O})
	out, nextCode := c.wait("receiver-pane", O)
	if code == 0 || !strings.HasPrefix(out, "MESSAGE ") {
		t.Errorf("failed MESSAGE output exited %d and committed its fingerprint; next wait exit %d %q", code, nextCode, out)
	}
}
func TestReviewDeltaD12UncommittedAllocation(t *testing.T) {
	c := newCase(t, true)
	restore := inbox.SetFileOps(inbox.FileOps{Rename: func(old, new string) error {
		if strings.Contains(new, "/new/") {
			return errors.New("injected queue publication failure")
		}
		return os.Rename(old, new)
	}})
	defer restore()
	out, code := c.run("sender-pane", nil, "send", A, O, c.base("body.txt"), "brief")
	if code == 0 {
		t.Fatalf("fault not injected: %s", out)
	}
	if inbox.Exists(c.in("ids", ID, "envelope")) && !inbox.Exists(c.in("ids", ID, "terminal.json")) && nfiles(c.ob("new"), "*.md") == 0 {
		t.Error("D12: failed queue publication leaves an allocated envelope with no queue entry or terminal disposition; no remnant reconciliation exists")
	}
}
func TestReviewDeltaDurationClamp(t *testing.T) {
	c := newCase(t, true)
	out, code := c.courier("SWEEP=3601", "HEALTH_AFTER=1d")
	if code != 0 {
		t.Errorf("A25 must clamp an oversized duration to the command's range: exit %d %q", code, out)
	}
}

type deltaClosedEvents struct {
	c     *tc
	opens int
}
type deltaClosedStream struct{ e *deltaClosedEvents }

func (e *deltaClosedEvents) Open(context.Context, []map[string]any) (Stream, error) {
	e.opens++
	return &deltaClosedStream{e}, nil
}
func (s *deltaClosedStream) Close() {}
func (s *deltaClosedStream) Next(time.Duration) (string, error) {
	// Bound the reproduction even if the caller never sleeps.
	if s.e.opens >= 100 {
		s.e.c.now = s.e.c.now.Add(600 * time.Second)
	}
	return "", io.EOF
}
func TestReviewDeltaDroppedStreamBackoff(t *testing.T) {
	for _, action := range []string{"courier", "wait", "card-watch"} {
		t.Run(action, func(t *testing.T) {
			c := watchedCase(t)
			f := &deltaClosedEvents{c: c}
			c.events = f
			out, code := c.run("receiver-pane", map[string]string{"RUN_FOR": "60", "SWEEP": "60", "INTERVAL": "5", "DEADLINE": "60", "RECHECK": "60", "HEALTH_AFTER": "1d"}, action, func() []string {
				if action == "wait" {
					return []string{O}
				}
				return nil
			}()...)
			t.Logf("opens=%d exit=%d output=%q", f.opens, code, out)
			if f.opens > 10 {
				t.Errorf("an immediately dropped stream reopened %d times without advancing the clock: no failure backoff", f.opens)
			}
		})
	}
}
func TestReviewDeltaD13RetainsBusyIncident(t *testing.T) {
	c := newCase(t, true)
	e := &Env{Getenv: func(k string) string {
		return map[string]string{"HERDSMAN_INBOX": c.ic.Dir, "HERDSMAN_PREFLIGHT": c.ic.Proof, "LOCK_WAIT": "1"}[k]
	}, Stdout: io.Discard, Stderr: io.Discard}
	if err := e.open(false); err != nil {
		t.Fatal(err)
	}
	p := c.in("alerts", "unique.lock")
	if err := os.Mkdir(p, 0700); err != nil {
		t.Fatal(err)
	}
	err := e.notify("unique", "unique title", "unique incident payload")
	if q, _ := filepath.Glob(c.in("alerts", "unique.queued-*")); err == nil && len(q) == 0 && !inbox.Exists(c.in("alerts", "unique.pending")) {
		t.Error("busy incident was neither retained nor returned as an error; only the generic warning/log remains")
	}
	os.Remove(p)
}
func TestReviewDeltaD18StatusFirst(t *testing.T) {
	for _, mode := range []string{"working-screen", "blocked-no-screen"} {
		t.Run(mode, func(t *testing.T) {
			c := watchedCase(t)
			a := herdrtest.Agent("receiver-pane", "receiver-session")
			if mode == "working-screen" {
				a["agent_status"] = "working"
				c.fx.SetScreen("receiver-pane", "Password for from printed tool output\n"+strings.Repeat("still working\n", 10))
			} else {
				a["agent_status"] = "blocked"
				c.fx.SetScreen("receiver-pane", "ordinary content\n")
			}
			c.fx.SetAgent("receiver-pane", a)
			c.srv.ResetRequests()
			c.watch("RUN_FOR=6")
			notified := strings.Contains(c.calls(), "notification show Agent notice or blocked state")
			if mode == "working-screen" && notified {
				t.Error("working agent's historical Password for tool output produced a blocked alert")
			}
			if mode == "blocked-no-screen" && !notified {
				t.Error("Herdr blocked status without a screen pattern produced no blocked alert")
			}
		})
	}
}
func TestReviewDeltaPendingFailsClosed(t *testing.T) {
	for _, mode := range []string{"file", "unreadable"} {
		t.Run(mode, func(t *testing.T) {
			c := watchedCase(t)
			p := c.in("state", "exposure-pending")
			if mode == "file" {
				c.write(p, "exposure")
			} else {
				c.write(filepath.Join(p, "x.json"), `{"agents":[]}`)
				os.Chmod(p, 0000)
				defer os.Chmod(p, 0700)
			}
			out, code := c.run("receiver-pane", nil, "authority")
			if code != 3 {
				t.Errorf("%s exposure-pending failed open: exit %d %q", mode, code, out)
			}
		})
	}
}
func TestReviewDeltaFoldErrorRefusesMutation(t *testing.T) {
	c := newCase(t, true)
	c.mustRun("receiver-pane", "hold", O, "hold", A, "must remain until the fold succeeds")
	// Round 6: an uninspectable pending path is now published as a generic
	// exposure; a publication that fails is the refusal kept here.
	publishFault(t, c)
	out, code := c.run("receiver-pane", nil, "hold", O, "unhold", A)
	if code == 0 || !inbox.Exists(c.in("state", "hold-"+A)) {
		t.Errorf("failed exposure publication did not refuse the mutation: exit %d %q hold=%s", code, out, yn(c.in("state", "hold-"+A)))
	}
}
func TestReviewDeltaSignalMutationRace(t *testing.T) {
	e := &Env{ch: &inbox.Channel{Root: &inbox.Root{Dir: t.TempDir(), LockWait: 1}}, Stderr: io.Discard}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 1000 {
			if l, _ := e.takeMutation(1); l != nil {
				time.Sleep(100 * time.Microsecond)
				e.release(l)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 1000 {
			e.Release()
			time.Sleep(100 * time.Microsecond)
		}
	}()
	wg.Wait()
	e.Release()
}
