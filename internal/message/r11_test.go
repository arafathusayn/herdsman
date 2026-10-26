package message

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Round 11 (PR #32 bot, item 1): once the envelope is queued, a health
// failure is a warning, as `health || :` in message-send.sh: the courier
// is down (no heartbeat) and its alert's pending record is a link, so the
// alert cannot be written; send still prints the queued receipt, exit 0.
func TestR11SendQueuedDespiteHealthFault(t *testing.T) {
	c := newCase(t, true)
	os.Symlink(c.base("elsewhere"), c.in("alerts", "courier-down.pending"))
	out, code := c.run("sender-pane", nil, "send", A, O, c.base("body.txt"), "brief")
	if code != 0 || !strings.Contains(out, ID+" queued") {
		t.Errorf("send after enqueue with a health fault: exit %d %q", code, out)
	}
	if !inbox.Exists(c.ob("new", ID+".md")) {
		t.Fatal("the message was not queued")
	}
	if !strings.Contains(out, "HEALTH") {
		t.Errorf("the health flags were not reported: %q", out)
	}
}

// The watcher's pass runs `health || :` too: the same fault does not end it.
func TestR11WatcherPassDespiteHealthFault(t *testing.T) {
	c := watchedCase(t)
	os.Symlink(c.base("elsewhere"), c.in("alerts", "courier-down.pending"))
	if out, code := c.watch(); code != 0 {
		t.Errorf("the watcher ended on a health fault: exit %d %q", code, out)
	}
}

// Round 11 (item 2): an allow list that is a FIFO without a writer is
// refused at once (exit 2), never read and waited on.
func TestR11LeakAllowFIFO(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.md")
	os.WriteFile(in, []byte("see /Users/someone/x\n"), 0o600)
	fifo := filepath.Join(dir, "allow")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("no FIFO here:", err)
	}
	done := make(chan int, 1)
	var b strings.Builder
	go func() {
		done <- LeakGate([]string{in}, func(k string) string {
			if k == "LEAK_ALLOW_FILE" {
				return fifo
			}
			return ""
		}, &strings.Builder{}, &b)
	}()
	select {
	case code := <-done:
		if code != 2 {
			t.Errorf("a FIFO allow list: exit %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the leak gate waited on a FIFO allow list")
	}
}

// Round 11 (item 3): a setup proof that turns stale pauses allocation
// recovery too: the sweep checks the proof before any record changes, so
// an old cut allocation keeps its mark and gets no terminal record.
func TestR11StaleProofPausesRecovery(t *testing.T) {
	c := newCase(t, true)
	c.send()
	os.Remove(c.ob("new", ID+".md")) // the cut: allocated, never queued
	past := c.now.Add(-2 * time.Hour)
	os.Chtimes(c.in("ids", ID, "envelope"), past, past)
	timing := c.in("state", "timing.json")
	staled := false
	defer inbox.SetFileOps(inbox.FileOps{Rename: func(o, n string) error {
		err := os.Rename(o, n)
		if n == timing && !staled {
			staled = true
			c.editJSON(c.ic.Proof, func(m map[string]any) { m["policy_verified"] = false })
		}
		return err
	}})()
	c.courier()
	if !staled {
		t.Fatal("the courier wrote no timing record")
	}
	if inbox.Exists(c.in("ids", ID, "terminal.json")) {
		t.Error("a stale setup proof still retired an allocation")
	}
}
