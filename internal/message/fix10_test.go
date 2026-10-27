package message

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Review 4 coverage of the round 2 delta: each caller of a refused
// exposure fold stops, retirement errors stop the courier, queued-alert
// folding edges, and failed health output.

// publishFault leaves a waiting exposure that cannot be published: a
// pending record, and a state folder that takes no new file.
func publishFault(t *testing.T, c *tc) {
	t.Helper()
	os.MkdirAll(c.in("state", "exposure-pending"), 0o700)
	c.write(c.in("state", "exposure-pending", "1.json"), `{"agents":[],"time":1}`+"\n")
	chmodT(t, c.in("state"), 0o500)
}

func TestR4FoldRefusalStopsEveryCaller(t *testing.T) {
	t.Run("display", func(t *testing.T) {
		c := newCase(t, true)
		e := openEnv(t, c, nil)
		publishFault(t, c)
		called := false
		if err := e.display(func(context.Context) error { called = true; return nil }); err == nil || called {
			t.Errorf("display went on after a refused fold: %v %v", err, called)
		}
	})
	t.Run("resume", func(t *testing.T) {
		c := newCase(t, true)
		c.codexTask()
		c.allowResume()
		c.relayBoundary()
		var buf bytes.Buffer
		e := &Env{Getenv: func(k string) string {
			return map[string]string{"HERDSMAN_INBOX": c.ic.Dir, "HERDSMAN_PREFLIGHT": c.ic.Proof, "HERDR_PANE_ID": "sender-pane"}[k]
		}, Stdout: &buf, Stderr: &buf, Client: herdr.New(c.srv.Socket)}
		err := e.resumeRun([]string{"im-task"}, func() { publishFault(t, c) })
		e.releaseAll()
		if msg, ok := inbox.IsFatal(err); !ok || !strings.Contains(msg, "exposure") {
			t.Errorf("resume went on after a refused fold: %v", err)
		}
		if c.prompts() != 0 {
			t.Error("resume sent input after a refused fold")
		}
	})
	t.Run("side-agent send", func(t *testing.T) {
		c := newCase(t, true)
		c.relayBoundary()
		publishFault(t, c)
		c.write(c.base("body.txt"), "card\n")
		out, code := c.run("sender-pane", nil, "send", A, O, c.base("body.txt"), "side_agent_data")
		if code != 2 || !strings.Contains(out, "exposure") {
			t.Errorf("side-agent send after a refused fold: %d %q", code, out)
		}
	})
}

// A retirement that cannot seed stops the courier's sweep with exit 2.
func TestR4RetirementErrorStopsCourier(t *testing.T) {
	c := newCase(t, true)
	chmodT(t, c.in("ids"), 0o300)
	out, code := c.courier()
	if code != 2 || !strings.Contains(out, "cannot list") {
		t.Errorf("courier with unlistable ids/: %d %q", code, out)
	}
}

// Queued alert folding: a queued name that is not a file is left alone; a
// pending record that is a link refuses the fold and is reported.
func TestR4FoldQueuedEdges(t *testing.T) {
	c := newCase(t, true)
	var errOut bytes.Buffer
	e := openEnv(t, c, nil)
	e.Stderr = &errOut
	os.Mkdir(c.in("alerts", "k.queued-dir"), 0o700)
	e.retryAlerts()
	if !inbox.Exists(c.in("alerts", "k.queued-dir")) || errOut.Len() != 0 {
		t.Errorf("a queued folder was touched: %q", errOut.String())
	}
	c.write(c.in("alerts", "j.queued-1"), `{"title":"t","body":"b"}`+"\n")
	os.Symlink(c.base("elsewhere"), c.in("alerts", "j.pending"))
	e.retryAlerts()
	if !strings.Contains(errOut.String(), "HEALTH alert retry failed") || !inbox.Exists(c.in("alerts", "j.queued-1")) {
		t.Errorf("a linked pending record did not refuse the fold: %q", errOut.String())
	}
}

// A health report that cannot be written is an error and is not recorded:
// the next wait reports it again.
func TestR4WaitHealthOutputFailure(t *testing.T) {
	for _, mode := range []string{"flags", "pending", "notice"} {
		t.Run(mode, func(t *testing.T) {
			c := newCase(t, true)
			me, pane, want := O, "receiver-pane", ""
			switch mode {
			case "flags":
				c.courier()
				old := c.now.Add(-24 * time.Hour)
				os.Chtimes(c.in("state", "heartbeat"), old, old) // the courier stopped a day ago
				want = "HEALTH courier-down"
			case "pending":
				c.courier()
				c.send()
				old := c.now.Add(-time.Hour)
				os.Chtimes(c.ob("new", ID+".md"), old, old) // older than a sweep
				want = "HEALTH pending delivery"
			case "notice":
				c.courier()
				me, pane = A, "sender-pane"
				c.ic.WriteJSON(c.in(A, "notices", ID+".md.json"), map[string]any{"id": ID, "to": O, "state": "quarantined", "authority": "notice-only"})
				want = "HEALTH sender notice"
			}
			env := map[string]string{"HERDSMAN_INBOX": c.ic.Dir, "HERDSMAN_PREFLIGHT": c.ic.Proof, "HERDR_PANE_ID": pane, "WATCH_REQUIRED": "0", "DEADLINE": "2", "RECHECK": "1"}
			e := &Env{Getenv: func(k string) string { return env[k] }, Stdout: deltaFailedWriter{}, Stderr: io.Discard, Client: herdr.New(c.srv.Socket), Now: func() time.Time { return c.now }, Sleep: func(d time.Duration) { c.now = c.now.Add(d) }}
			code := e.Wait([]string{me})
			var next bytes.Buffer
			e2 := &Env{Getenv: func(k string) string { return env[k] }, Stdout: &next, Stderr: io.Discard, Client: herdr.New(c.srv.Socket), Now: func() time.Time { return c.now }, Sleep: func(d time.Duration) { c.now = c.now.Add(d) }}
			e2.Wait([]string{me})
			out := next.String()
			if code == 0 || !strings.Contains(out, want) {
				t.Errorf("failed %s output exited %d; next wait %q", mode, code, out)
			}
		})
	}
}
