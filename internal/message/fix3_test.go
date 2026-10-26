package message

import (
	"runtime"
	"strconv"
	"testing"
	"time"
)

func watchedCase(t *testing.T) *tc {
	c := newCase(t, true)
	c.ic.WriteJSON(c.in("watch-targets.json"), []any{map[string]any{"pane": "receiver-pane", "session": map[string]any{"source": "hook", "agent": "claude", "kind": "id", "value": "receiver-session"}}})
	c.relayBoundary()
	return c
}

const receiverUpdate = `{"event":"pane.updated","data":{"pane":{"pane_id":"receiver-pane","title":"x"}}}`

// D17: with a pane.updated stream an unchanged target is not read again;
// an update for its pane makes the next pass read it once.
func TestD17UpdateDrivenReads(t *testing.T) {
	c := watchedCase(t)
	c.events = &scripted{c: c, steps: []string{"@sleep 20", receiverUpdate}}
	c.srv.ResetRequests()
	out, code := c.run("courier-pane", map[string]string{"RUN_FOR": "60", "INTERVAL": "5"}, "card-watch")
	if code != 0 {
		t.Fatalf("watch %d %q", code, out)
	}
	// Start (no stream yet), the first pass after subscribing, and the
	// pass after the one update: 3 reads in 60 s, not one per 5 s pass.
	check(t, "D17 reads only at start, after subscribing and after an update", "^3$", strconv.Itoa(c.count("^pane read receiver-pane")))
	// Without a stream the watcher still reads every pass (the fallback).
	c.events = nil
	c.srv.ResetRequests()
	c.watch("RUN_FOR=20", "INTERVAL=5")
	check(t, "D17 without events every pass reads", "^5$", strconv.Itoa(c.count("^pane read receiver-pane")))
}

// A24 for D17: a thousand idle event-driven passes keep the heap flat and
// leave no goroutine, and read the target only at the 300 s reconcile.
func TestD17LoopStaysFlat(t *testing.T) {
	c := watchedCase(t)
	run := func() {
		c.events = &scripted{c: c}
		c.srv.ResetRequests()
		c.run("courier-pane", map[string]string{"RUN_FOR": "5000", "INTERVAL": "5"}, "card-watch")
	}
	run()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	g0 := runtime.NumGoroutine()
	run()
	reads := c.count("^pane read receiver-pane")
	c.srv.ResetRequests()
	runtime.GC()
	runtime.ReadMemStats(&after)
	time.Sleep(20 * time.Millisecond)
	if grow := int64(after.HeapAlloc) - int64(before.HeapAlloc); grow > 1<<20 {
		t.Errorf("heap grew %d bytes", grow)
	}
	if g := runtime.NumGoroutine(); g > g0 {
		t.Errorf("goroutines %d -> %d", g0, g)
	}
	// 1000 passes over 5000 s: start, subscribe, then one per 300 s.
	if reads < 15 || reads > 22 {
		t.Errorf("1000 idle passes read the target %d times, want about 18", reads)
	}
}
