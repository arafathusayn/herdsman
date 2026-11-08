package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Item 0a (review a-7 P2): a full hash cache evicts one entry for each
// new one, and setup bounds the proof's files below the cache, so a
// proof's own files never evict each other.

func TestHashCacheEvictsOne(t *testing.T) {
	dir := t.TempDir()
	var c hashCache
	for i := range hashCacheMax {
		if _, err := c.sum(writeFile(t, dir, fmt.Sprintf("f%d", i), "x")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.sum(writeFile(t, dir, "one-more", "y")); err != nil {
		t.Fatal(err)
	}
	if len(c.m) != hashCacheMax {
		t.Fatalf("a full cache keeps %d entries after one miss, want %d (one evicted)", len(c.m), hashCacheMax)
	}
	if _, ok := c.m[filepath.Join(dir, "one-more")]; !ok {
		t.Fatal("the new entry is not cached")
	}
}

func TestSetupBoundsRules(t *testing.T) {
	x, _, q := proofRoute(t)
	dir := t.TempDir()
	q.Rules = nil
	for i := range maxSetupRules + 1 {
		q.Rules = append(q.Rules, writeFile(t, dir, fmt.Sprintf("rule-%d.sb", i), "(allow default)\n"))
	}
	if _, e := x.r.setup(q); e == nil || e.Code != "bad_request" {
		t.Fatalf("setup with %d rules: %v, want bad_request", len(q.Rules), e)
	}
	q.Rules = q.Rules[:maxSetupRules]
	if _, e := x.r.setup(q); e != nil {
		t.Fatal(e)
	}
	for range 3 {
		if !x.r.proofValid() {
			t.Fatal("proof not valid")
		}
	}
	if len(x.r.hashes.m) != maxSetupRules+1 {
		t.Fatalf("the cache holds %d of the proof's %d files", len(x.r.hashes.m), maxSetupRules+1)
	}
}

// Item 1 (review a-2 P1s on server.go and stream.go).

// frames reads a stream's frames into a channel until it ends.
func frames(ev *EventReader) <-chan WaitFrame {
	ch := make(chan WaitFrame, 256)
	go func() {
		defer close(ch)
		for {
			f, err := ev.Next()
			if err != nil {
				return
			}
			ch <- f
		}
	}()
	return ch
}

// nextFrame is the next frame of type typ, or a failure after 5 s.
func nextFrame(t testing.TB, ch <-chan WaitFrame, typ string) Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case f, ok := <-ch:
			if !ok {
				t.Fatalf("stream ended before %s", typ)
			}
			if f.Event == typ {
				var e Event
				if err := json.Unmarshal(f.Data, &e); err != nil {
					t.Fatal(err)
				}
				return e
			}
		case <-deadline:
			t.Fatalf("no %s frame within 5 s", typ)
		}
	}
}

// subCount is the number of streams attached to a route, read on its
// actor without the operation rules (a degraded route still answers).
func subCount(rt *Route) int {
	n := make(chan int, 1)
	rt.ops <- func() { n <- len(rt.subs) }
	return <-n
}

// TestCloseDetachesDegradedRoute: a closed party call leaves no subscriber
// behind, even on a route that refuses operations.
func TestCloseDetachesDegradedRoute(t *testing.T) {
	h := newHarness(t)
	_, orch, _ := h.route("a")
	rt, _ := h.d.route("a")
	party, err := orch.c.Events(context.Background(), "a", "")
	if err != nil {
		t.Fatal(err)
	}
	nextFrame(t, frames(party), "snapshot")
	for i := 0; subCount(rt) != 1; i++ {
		if i == 500 {
			t.Fatalf("%d calls attached, want 1", subCount(rt))
		}
		time.Sleep(10 * time.Millisecond)
	}
	for range 3 {
		_ = rt.do(context.Background(), func() *APIError { panic("boom") })
	}
	party.Close()
	for i := 0; subCount(rt) != 0; i++ {
		if i == 500 {
			t.Fatalf("%d closed calls stay attached to the degraded route", subCount(rt))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestReplayReadsFilesInSteps: a cursor older than the ring replays from
// the journal files one record at a time, not as one materialized range,
// and two rotations during the replay lose nothing of the range.
func TestReplayReadsFilesInSteps(t *testing.T) {
	x := newDirect(t)
	start := x.r.visible
	for i := range 300 {
		if _, e := x.send(fmt.Sprintf("m%d", i), key()); e != nil {
			t.Fatal(e)
		}
	}
	barrier := x.r.visible
	s := newSubscriber("r", "orch")
	first, e := x.r.subscribe(s, x.r.epoch+"-"+itoa(start))
	if e != nil {
		t.Fatal(e)
	}
	if len(first) != 0 || s.replay == nil {
		t.Fatalf("subscribe materialized %d frames (replay %v)", len(first), s.replay != nil)
	}
	defer s.replay.close()
	var got []int64
	for {
		rec, err := s.replay.next()
		if err != nil {
			t.Fatal(err)
		}
		if rec == nil {
			break
		}
		if s.wants(rec) {
			got = append(got, rec.Seq)
		}
		if len(got) == 10 {
			for range 2 {
				if err := x.r.writeCheckpoint(true); err != nil {
					t.Fatal(err)
				}
				if _, e := x.send("after", key()); e != nil {
					t.Fatal(e)
				}
			}
		}
	}
	if len(got) != 300 {
		t.Fatalf("replayed %d messages, want 300", len(got))
	}
	for i, seq := range got {
		if seq <= start || seq > barrier || (i > 0 && seq <= got[i-1]) {
			t.Fatalf("record %d has seq %d (range %d..%d)", i, seq, start, barrier)
		}
	}
}

// checkStatusShape asserts the status --json object of 5.5: the daemon
// object, every mailbox with orphan, message headers without bodies and
// the pending alerts.
func checkStatusShape(t *testing.T, raw []byte, state string, orch string) {
	t.Helper()
	var st map[string]any
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	d, _ := st["daemon"].(map[string]any)
	if d == nil || d["state"] != state || d["version"] != testVersion {
		t.Fatalf("daemon object %v", st["daemon"])
	}
	if _, ok := d["heartbeat_age"].(float64); !ok {
		t.Fatalf("no heartbeat_age: %v", d)
	}
	orphan := map[string]any{}
	for _, b := range st["boxes"].([]any) {
		m := b.(map[string]any)
		orphan[m["name"].(string)] = m["orphan"]
	}
	if orphan["stray"] != true || orphan[orch] != false {
		t.Fatalf("orphan flags %v", orphan)
	}
	msgs, _ := st["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages %v", st["messages"])
	}
	m := msgs[0].(map[string]any)
	if m["to"] != orch || m["kind"] != "brief" || m["state"] != "ready" || m["box"] != orch || m["id"] == "" {
		t.Fatalf("message header %v", m)
	}
	if _, ok := m["body"]; ok || strings.Contains(string(raw), "secret-body") {
		t.Fatal("status carries a body")
	}
	alerts, _ := st["alerts"].([]any)
	if len(alerts) != 1 || alerts[0].(map[string]any)["key"] != "k1" {
		t.Fatalf("alerts %v", st["alerts"])
	}
}

func TestStatusFullShape(t *testing.T) {
	h := newHarness(t)
	adj, orch, dir := h.route("r")
	adj.send(t, "r", orch.name, "secret-body")
	_ = os.MkdirAll(filepath.Join(dir, "alerts"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "alerts", "k1.pending"), []byte(`{"key":"k1","title":"t"}`), 0o600)
	for _, s := range []string{"new", "ready", "cur"} {
		_ = os.MkdirAll(filepath.Join(dir, "stray", s), 0o700)
	}
	var raw json.RawMessage
	h.do("GET", "/v1/routes/r/status", nil, &raw)
	checkStatusShape(t, raw, "up", orch.name)
	out, _, code := h.msg(adj, "r", "--json", "status")
	if code != 0 {
		t.Fatalf("%d %s", code, out)
	}
	checkStatusShape(t, []byte(out), "up", orch.name)
	h.stop()
	out, _, code = h.msg(adj, "r", "--json", "status")
	if code != 1 {
		t.Fatalf("%d %s", code, out)
	}
	checkStatusShape(t, []byte(out), "down", orch.name)
}

// Item 2 (review a-2 P1s on daemon.go).

// TestStartUnwindsOnFailure: a Start that fails after the account lock
// frees the lock and every route it opened, early or late.
func TestStartUnwindsOnFailure(t *testing.T) {
	h := newHarness(t)
	_, _, _ = h.route("r")
	h.stop()
	// Late: the routes are open when the socket cannot be made.
	sock := SocketPath(h.ph)
	_ = os.Remove(sock)
	_ = os.MkdirAll(filepath.Join(sock, "x"), 0o700)
	if err := New(h.cfg).Start(); err == nil {
		t.Fatal("Start succeeded with a folder at the socket path")
	}
	_ = os.RemoveAll(sock)
	d := New(h.cfg)
	if err := d.Start(); err != nil {
		t.Fatalf("after a late failure: %v", err)
	}
	if _, e := d.route("r"); e != nil {
		t.Fatalf("route held after a late failure: %v", e)
	}
	d.Stop("stop")
	// Early: daemon/ is a regular file.
	dd := filepath.Join(h.ph, "daemon")
	_ = os.Rename(dd, dd+".kept")
	_ = os.WriteFile(dd, []byte("x"), 0o600)
	if err := New(h.cfg).Start(); err == nil {
		t.Fatal("Start succeeded with a file at daemon/")
	}
	_ = os.Remove(dd)
	_ = os.Rename(dd+".kept", dd)
	d = New(h.cfg)
	if err := d.Start(); err != nil {
		t.Fatalf("after an early failure: %v", err)
	}
	d.Stop("stop")
}

// TestSchedulerPublishesProofHealth: a policy file changed while the
// route is idle reaches an open stream as a health event on the next
// scheduler tick.
func TestSchedulerPublishesProofHealth(t *testing.T) {
	h := newHarness(t)
	h.stop()
	h.cfg.HeartbeatEvery = 20 * time.Millisecond
	h.start()
	_, orch, _ := h.route("r")
	ev, err := orch.c.Events(context.Background(), "r", "")
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()
	ch := frames(ev)
	nextFrame(t, ch, "snapshot")
	if err := os.WriteFile(h.rules, []byte("(deny default)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hl := nextFrame(t, ch, "health")
	if hl.Status != "held" || len(hl.Flags) != 1 || hl.Flags[0] != "setup-proof-stale" {
		t.Fatalf("health %+v", hl)
	}
}
