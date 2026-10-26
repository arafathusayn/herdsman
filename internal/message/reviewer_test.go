package message

import (
	"fmt"
	"github.com/arafathusayn/herdsman/internal/inbox"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestReviewerCourierChangedBytes(t *testing.T) {
	c := newCase(t, true)
	c.send()
	base := c.fxAgentGet()
	observations := 0
	c.srv.Handle("agent.get", func(p map[string]any) (any, string) {
		if p["target"] == "sender-pane" {
			observations++
		}
		if observations == 2 && p["target"] == "sender-pane" {
			// Another writer finishes its mutation under the queue lock while
			// courier is awaiting its live observation.
			if err := os.Mkdir(c.in("channel.lock"), 0700); err != nil {
				return nil, err.Error()
			}
			c.editJSON(c.ob("new", ID+".md"), func(m map[string]any) { m["body"] = "unverified replacement" })
			os.Remove(c.in("channel.lock"))
		}
		return base(p)
	})
	c.courier()
	t.Logf("sender observations=%d; new=%s ready=%s quarantine=%s", observations, yn(c.ob("new", ID+".md")), yn(c.ob("ready", ID+".md")), yn(c.ob("quarantine", ID+".md")))
	if yn(c.ob("ready", ID+".md")) == "yes" && c.read(c.ob("ready", ID+".md")) != c.read(c.in("ids", ID, "envelope")) {
		t.Fatal("courier delivered and logged VERIFIED for bytes different from its verified allocation copy")
	}
}

func TestReviewerAckFIFO(t *testing.T) {
	c := newCase(t, true)
	p := c.base("record.fifo")
	if err := syscall.Mkfifo(p, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() { _, code := c.run("receiver-pane", nil, "ack", O, "token", ID, p); done <- code }()
	select {
	case code := <-done:
		if code != 2 {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(100 * time.Millisecond):
		// Release the FIFO reader so the probe leaves no blocked goroutine.
		if err := os.WriteFile(p, []byte("record"), 0600); err != nil {
			t.Fatal(err)
		}
		<-done
		t.Fatal("ack blocks reading an invalid FIFO before checking Bounded; a large regular file is also read before its size check")
	}
}

func TestReviewerStatusNoticeGuards(t *testing.T) {
	for _, mode := range []string{"leaf-link", "parent-link", "duplicate-key", "wrong-type", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			c := newCase(t, true)
			data := `{"id":"private-external-value","to":"x","state":"quarantined","authority":"notice-only"}`
			p := c.ob("notices", "notice.json")
			switch mode {
			case "leaf-link":
				c.write(c.base("external.json"), data)
				if err := os.Symlink(c.base("external.json"), p); err != nil {
					t.Fatal(err)
				}
			case "parent-link":
				c.write(c.base("external", "notice.json"), data)
				if err := os.Remove(c.ob("notices")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(c.base("external"), c.ob("notices")); err != nil {
					t.Fatal(err)
				}
			case "duplicate-key":
				c.write(p, `{"id":"old","id":"private-external-value","to":"x"}`)
			case "wrong-type":
				c.write(p, `{"id":{"body":"private-external-value"}}`)
			case "oversize":
				c.write(p, `{"id":"private-external-value","extra":"`+strings.Repeat("x", 131073)+`"}`)
			}
			out, code := c.run("receiver-pane", nil, "status", O)
			if strings.Contains(out, "private-external-value") {
				t.Fatalf("status printed unguarded notice (%s), exit %d", mode, code)
			}
		})
	}
}

func TestReviewerCardAtCapacity(t *testing.T) {
	c := newCase(t, true)
	c.watchTask("ninth card")
	source := map[string]any{"pane": "task-pane", "session": map[string]any{"agent": "claude", "kind": "id", "source": "hook", "value": "task-session"}}
	for i := 0; i < 8; i++ {
		c.ic.WriteJSON(c.in("card-events", fmt.Sprintf("old-%d.json", i)), map[string]any{"version": 1, "type": "side_agent_data", "id": fmt.Sprintf("old-%d", i), "source": source, "text": "previous card"})
	}
	if out, code := c.watch(); code != 0 {
		t.Fatalf("first watch %d %s", code, out)
	}
	if nfiles(c.in("card-events"), "*.json") != 8 {
		t.Fatal("probe did not fill journal")
	}
	// Human dispositions drain the eight earlier records. The ninth card
	// remains visible throughout, so it still needs capture.
	for i := 0; i < 8; i++ {
		os.Remove(c.in("card-events", fmt.Sprintf("old-%d.json", i)))
	}
	if out, code := c.watch(); code != 0 {
		t.Fatalf("second watch %d %s", code, out)
	}
	if nfiles(c.in("card-events"), "*.json") == 0 {
		t.Fatal("card observed at the journal cap was marked visible without being journaled; it is lost after capacity returns")
	}
}

func TestReviewerSettings(t *testing.T) {
	t.Run("event-max", func(t *testing.T) {
		c := newCase(t, true)
		c.events = &scripted{c: c}
		out, code := c.wait("receiver-pane", O, "EVENT_READ_MAX=broken")
		if code != 2 || !strings.Contains(out, "EVENT_READ_MAX") {
			t.Fatalf("invalid EVENT_READ_MAX accepted: exit %d %q, wait state exists=%s", code, out, yn(c.in("state", "wait-"+O)))
		}
	})
	t.Run("duration", func(t *testing.T) {
		c := newCase(t, true)
		out, code := c.courier("SWEEP=1m")
		if code != 0 {
			t.Fatalf("A25 duration suffix refused: exit %d %q", code, out)
		}
	})
	t.Run("health-leading-zero", func(t *testing.T) {
		c := newCase(t, true)
		out, code := c.courier("HEALTH_AFTER=0180")
		if code != 0 {
			t.Fatalf("A25 decimal setting refused: exit %d %q", code, out)
		}
	})
}

func TestReviewerQuietTimeout(t *testing.T) {
	c := newCase(t, true)
	s := &scripted{c: c}
	c.events = s
	if out, code := c.courier("RUN_FOR=6", "SWEEP=60", "EVENT_READ_MAX=1"); code != 0 {
		t.Fatalf("%d %s", code, out)
	}
	if s.opens != 1 {
		t.Fatalf("six idle seconds reopened %d subscriptions and called agent.get %d times despite SWEEP=60", s.opens, c.count("^agent get"))
	}
}

func TestReviewerSignalLocks(t *testing.T) {
	e := &Env{}
	dir := t.TempDir() + "/lock"
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			e.hold(inbox.Adopt(dir))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			e.Release()
		}
	}()
	wg.Wait()
	e.Release()
}

// A stable idle fixture (no queued message to become overdue): a wait
// with RECHECK=1 runs to its 1500 s deadline, about 1500 identity rechecks,
// and ends with TICK.
func TestReviewerWaitIterations(t *testing.T) {
	c := newCase(t, true)
	env := map[string]string{"DEADLINE": "1500", "RECHECK": "1", "HEALTH_AFTER": "1d"}
	c.run("receiver-pane", map[string]string{"DEADLINE": "1", "RECHECK": "1", "HEALTH_AFTER": "1d"}, "wait", O) // record the fingerprint
	c.srv.ResetRequests()
	start := c.now
	out, code := c.run("receiver-pane", env, "wait", O)
	count := c.count("^agent get receiver-pane")
	if count < 1000 || code != 0 || out != "TICK message-wait deadline\n" {
		t.Fatalf("A24 idle wait: %d identity rechecks, elapsed %s, exit %d: %q", count, c.now.Sub(start), code, out)
	}
}

func TestReviewerWatchCoverageFIFO(t *testing.T) {
	c := newCase(t, true)
	p := c.in("watch-targets.json")
	if err := syscall.Mkfifo(p, 0600); err != nil {
		t.Fatal(err)
	}
	w := &watcher{Env: &Env{ch: &inbox.Channel{Root: &inbox.Root{Dir: c.ic.Dir}}}}
	done := make(chan bool, 1)
	go func() { done <- w.coverage() }()
	select {
	case result := <-done:
		if result {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(100 * time.Millisecond):
		if err := os.WriteFile(p, []byte("[]"), 0600); err != nil {
			t.Fatal(err)
		}
		<-done
		t.Fatal("watcher reads watch-targets.json before checking Regular, so a replacement FIFO hangs it and a large file has no read limit")
	}
}

func TestReviewerUnholdFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission denial needs a non-root owner")
	}
	c := newCase(t, true)
	c.codexTask()
	c.relayBoundary()
	c.mustRun("receiver-pane", "hold", O, "hold", "im-task", "keep held")
	state := c.in("state")
	if err := os.Chmod(state, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(state, 0700)
	out, code := c.run("receiver-pane", nil, "hold", O, "unhold", "im-task")
	if code == 0 && yn(c.in("state", "hold-im-task")) == "yes" {
		t.Fatalf("failed unlink returned success with hold still present: exit %d %q", code, out)
	}
}

func TestReviewerLiteralInboxPath(t *testing.T) {
	c := newCase(t, true)
	root := c.ic.Dir + "[x]"
	if err := os.Rename(c.ic.Dir, root); err != nil {
		t.Fatal(err)
	}
	c.ic.Dir = root
	c.ic.Proof = root + "/setup.json"
	c.ic.Prove()
	c.mustRun("sender-pane", "send", A, O, c.base("body.txt"), "brief")
	if out, code := c.courier(); code != 0 {
		t.Fatalf("courier %d %q", code, out)
	}
	if yn(c.ob("ready", ID+".md")) != "yes" {
		t.Fatalf("valid inbox with [x] in its name is not scanned; queued envelope stays in new=%s", yn(c.ob("new", ID+".md")))
	}
}

func TestReviewerEncodedAckBound(t *testing.T) {
	c := newCase(t, true)
	c.send()
	c.courier()
	tok := token(c.mustRun("receiver-pane", "read", O))
	c.write(c.base("record.txt"), strings.Repeat("\x01", 30000))
	out, code := c.run("receiver-pane", nil, "ack", O, tok, ID, c.base("record.txt"))
	if st, err := os.Stat(c.ob("ack", ID+".md.json")); err == nil && st.Size() > inbox.MaxSize {
		t.Fatalf("ack published an invalid %d-byte record (bound %d), exit %d %q", st.Size(), inbox.MaxSize, code, out)
	}
}
