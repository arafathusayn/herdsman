package message

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// The leak gate's catalog in every test: made-up model names (A28), so no
// test runs an installed harness.
var testModels = []string{"model-a", "model.b+"}

func init() { harnessModels = func() []string { return testModels } }

// A28: the gate blocks the model names the harness catalogs list, each
// literally, and no model name of its own.
func TestLeakGateCatalogModels(t *testing.T) {
	dir := t.TempDir()
	gate := func(text string, models []string) int {
		old := testModels
		testModels = models
		defer func() { testModels = old }()
		p := filepath.Join(dir, "in.md")
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		return LeakGate([]string{p}, func(string) string { return "" }, &b, &b)
	}
	for _, tc := range []struct {
		text   string
		models []string
		want   int
	}{
		{"Reviewed by the model-a model.\n", []string{"model-a"}, 1},
		{"Reviewed by the MODEL-A model.\n", []string{"model-a"}, 1},
		{"Reviewed by the model-a model.\n", nil, 0},
		{"Reviewed by the model-a model.\n", []string{"model-z", " "}, 0},
		{"Uses modelXbb today.\n", []string{"model.b+"}, 0},
		{"Uses model.b+ today.\n", []string{"model.b+"}, 1},
	} {
		if got := gate(tc.text, tc.models); got != tc.want {
			t.Errorf("%q with %v: exit %d, want %d", tc.text, tc.models, got, tc.want)
		}
	}
	// LEAK_PATTERN still adds its own pattern.
	p := filepath.Join(dir, "x.md")
	os.WriteFile(p, []byte("a plain word\n"), 0o600)
	var b strings.Builder
	if LeakGate([]string{p}, func(k string) string { return map[string]string{"LEAK_PATTERN": "plain"}[k] }, &b, &b) != 1 {
		t.Error("LEAK_PATTERN no longer applies")
	}
}

func TestJSONOutput(t *testing.T) {
	c := newCase(t, true)
	c.send()
	out, code := c.run("receiver-pane", map[string]string{"HERDSMAN_NAME": O}, "message", "--json", "status")
	m, ok := decodeObject(strings.TrimSpace(out))
	if code != 0 || !ok || strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Fatalf("status --json: %d %q", code, out)
	}
	if reg := m["registry"].(map[string]any); reg["approved"] != true {
		t.Errorf("registry %v", reg)
	}
	boxes := m["mailboxes"].([]any)
	found := false
	for _, b := range boxes {
		if b.(map[string]any)["name"] == O && raw(b.(map[string]any)["new"]) == "1" {
			found = true
		}
	}
	if !found {
		t.Errorf("mailboxes %v", boxes)
	}
	c.courier()
	out, _ = c.run("receiver-pane", map[string]string{"HERDSMAN_NAME": O, "DEADLINE": "2", "RECHECK": "1"}, "message", "wait", "--json")
	check(t, "wait --json message event", `^\{"event":"message","name":"`+O+`"\}\n$`, out)
	out, _ = c.run("receiver-pane", map[string]string{"HERDSMAN_NAME": O, "DEADLINE": "2", "RECHECK": "1"}, "message", "wait", "--json")
	check(t, "wait --json tick", `^\{"event":"tick"\}\n$`, out)
	os.Remove(c.in("state", "heartbeat"))
	out, code = c.run("receiver-pane", map[string]string{"HERDSMAN_NAME": O, "DEADLINE": "2", "RECHECK": "1"}, "message", "wait", "--json")
	check(t, "wait --json health", `^\{"event":"health","flags":\["courier-down"\]\}\n0$`, out+strconv.Itoa(code-1))
	out, _ = c.run("receiver-pane", nil, "message", "--help")
	check(t, "help lists the exit codes", "exit codes:", out)
}

// Strict settings (A25): a bad value names the variable and exits 2 before
// anything runs; leading zeros read as decimal.
func TestStrictSettings(t *testing.T) {
	c := newCase(t, true)
	for _, v := range []string{"abc", "-1", "0", "3601", " 5", "1.5"} {
		out, code := c.courier("SWEEP=" + v)
		check(t, "SWEEP="+v, `^2 herdsman message: SWEEP: invalid SWEEP, want a duration from 1s to 3600s: digits with an optional s, m, h or d \(got "`, strconv.Itoa(code)+" "+out)
	}
	check(t, "no lock taken", "^no$", yn(c.in("courier.lock")))
	if _, code := c.courier("SWEEP=060"); code != 0 {
		t.Error("a leading zero is decimal")
	}
	for k, v := range map[string]string{"RECHECK": "0", "DEADLINE": "1501"} {
		out, code := c.wait("receiver-pane", O, k+"="+v)
		check(t, k, "^2 herdsman message: "+k, strconv.Itoa(code)+" "+out)
	}
	out, code := c.watch("INTERVAL=61")
	check(t, "INTERVAL", "^2 herdsman message: INTERVAL", strconv.Itoa(code)+" "+out)
}

func FuzzEnvelope(f *testing.F) {
	f.Add([]byte(`{"version":1,"id":"m000000000001","from":"a","to":"b","from_identity":{"pane":"p","session":{"agent":"c","kind":"id","source":"h","value":"s"}},"to_identity":{"pane":"q","session":{"agent":"c","kind":"id","source":"h","value":"t"}},"time":1,"kind":"brief","reply_to":"","body":"x"}`))
	f.Add([]byte(`{"version":1,"version":1}`))
	f.Add([]byte(`not json`))
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, b []byte) {
		p := filepath.Join(dir, "m000000000001.md")
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Skip()
		}
		m, ok := inbox.Envelope("", p)
		if ok && (m == nil || inbox.StrictJSON(b) != nil || !inbox.IDOK(m["id"].(string))) {
			t.Fatalf("accepted an invalid envelope: %q", b)
		}
	})
}

func FuzzClaimAndHold(f *testing.F) {
	f.Add([]byte(`{"holds":{"a":{"reason":"r","time":1}}}`))
	f.Add([]byte(`{"holds":{"a":{"reason":1,"time":"x"}}}`))
	f.Add([]byte(`{"type":"claim","token":".claim.1"}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		if holdFormat(b) {
			if _, ok := holdOwners(b); !ok {
				t.Fatalf("hold format without owners: %q", b)
			}
		}
		if m, ok := decodeObject(string(b)); ok && m["type"] == "claim" {
			_ = token(string(b))
		}
	})
}

func FuzzRegisterRegistry(f *testing.F) {
	f.Add([]byte(`{"route-a":{"role":"adjutant","identity":{"pane":"p","session":{"agent":"c","kind":"id","source":"h","value":"s"}}}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"a":1,"a":2}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		peers, ok := inbox.ValidRegistry(b)
		if ok && len(peers) > 2 {
			t.Fatalf("a registry of %d peers", len(peers))
		}
		if o, ok := rawObject(b); ok && inbox.StrictJSON(b) == nil {
			if len(objectKeys(b)) != len(o) {
				t.Fatalf("keys %v vs %d", objectKeys(b), len(o))
			}
		}
	})
}

// Resource use (A24): a thousand waiter rechecks, courier sweeps and
// watcher passes keep the heap flat and leave no goroutine behind.
func TestLongRunningLoopsStayFlat(t *testing.T) {
	c := newCase(t, true)
	c.send()
	c.ic.WriteJSON(c.in("watch-targets.json"), []any{map[string]any{"pane": "receiver-pane", "session": map[string]any{"source": "hook", "agent": "claude", "kind": "id", "value": "receiver-session"}}})
	c.relayBoundary()
	measure := func(name string, run func()) {
		run() // warm up
		c.srv.ResetRequests()
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		g0 := runtime.NumGoroutine()
		run()
		c.srv.ResetRequests()
		runtime.GC()
		runtime.ReadMemStats(&after)
		time.Sleep(20 * time.Millisecond)
		if grow := int64(after.HeapAlloc) - int64(before.HeapAlloc); grow > 1<<20 {
			t.Errorf("%s: heap grew %d bytes", name, grow)
		}
		if g := runtime.NumGoroutine(); g > g0 {
			t.Errorf("%s: goroutines %d -> %d", name, g0, g)
		}
	}
	measure("message wait", func() {
		// DEADLINE=1500 with RECHECK=1 is 1500 rechecks on the fake clock.
		c.run("receiver-pane", map[string]string{"DEADLINE": "1500", "RECHECK": "1"}, "wait", O)
	})
	measure("message courier", func() {
		c.courier("RUN_FOR=1000", "SWEEP=1")
	})
	measure("message card-watch", func() {
		c.watch("RUN_FOR=1000")
	})
	c.codexTask()
	c.relayBoundary()
	measure("message resume-goal", func() {
		for range 1000 {
			c.run("sender-pane", nil, "resume", "im-task")
		}
	})
}
