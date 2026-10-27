package daemon

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Item 3 (review a-2 P1s on client.go and msgcli.go).

// fakeDaemon serves a hello of this version and the handlers routes adds
// on a private home's socket.
func fakeDaemon(t *testing.T, routes func(m *http.ServeMux)) string {
	t.Helper()
	ph := filepath.Join(shortDir(t), "p")
	if err := os.MkdirAll(filepath.Join(ph, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", SocketPath(ph))
	if err != nil {
		t.Fatal(err)
	}
	m := http.NewServeMux()
	m.HandleFunc("GET /v1/hello", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, Hello{DaemonVersion: testVersion, Protocol: Protocol, MinProtocol: Protocol, Features: []string{},
			CredentialOptions: []string{"file"}, DefaultCredential: "file"})
	})
	routes(m)
	srv := &http.Server{Handler: m}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ph
}

// hang blocks a handler until its client or the server goes.
func hang(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }

// runFake runs one message command against a private home, failing the
// test when it has not finished within limit.
func runFake(t *testing.T, e *MessageEnv, env map[string]string, limit time.Duration, args ...string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	e.Getenv = func(k string) string { return env[k] }
	e.Stdout, e.Stderr, e.Version = &out, &errb, testVersion
	done := make(chan int, 1)
	go func() { done <- RunMessage(e, args) }()
	select {
	case code := <-done:
		return out.String(), errb.String(), code
	case <-time.After(limit):
		t.Fatalf("message %v did not finish within %v", args, limit)
	}
	return "", "", 0
}

func partyEnv(ph string) map[string]string {
	return map[string]string{"HERDSMAN_PRIVATE_HOME": ph, "HERDSMAN_ROUTE": "r", "HERDSMAN_NAME": "orch", "HERDR_PANE_ID": "p2"}
}

// TestWaitBacksOffAfterStreamEnds: a stream the daemon ends at once is
// reopened after the 0.2 s doubling backoff, not in a tight loop.
func TestWaitBacksOffAfterStreamEnds(t *testing.T) {
	var opens atomic.Int64
	ph := fakeDaemon(t, func(m *http.ServeMux) {
		m.HandleFunc("GET /v1/routes/r/events", func(w http.ResponseWriter, _ *http.Request) {
			opens.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
		})
	})
	var mu sync.Mutex
	var sleeps []time.Duration
	e := &MessageEnv{Sleep: func(d time.Duration) {
		mu.Lock()
		sleeps = append(sleeps, d)
		mu.Unlock()
		time.Sleep(d / 10)
	}}
	env := partyEnv(ph)
	env["HEALTH_GRACE"] = "60"
	_, _, code := runFake(t, e, env, 10*time.Second, "wait", "--deadline", "1")
	mu.Lock()
	defer mu.Unlock()
	if code != 0 || opens.Load() > 20 {
		t.Fatalf("exit %d after %d stream opens in 1 s", code, opens.Load())
	}
	if len(sleeps) < 2 || sleeps[0] != 200*time.Millisecond || sleeps[1] != 400*time.Millisecond {
		t.Fatalf("backoff %v", sleeps)
	}
}

// TestWaitGraceBoundsHandshake: a daemon that accepts but never answers
// is HEALTH daemon-unreachable after HEALTH_GRACE, not a TICK at the
// deadline.
func TestWaitGraceBoundsHandshake(t *testing.T) {
	ph := fakeDaemon(t, func(m *http.ServeMux) {
		m.HandleFunc("GET /v1/routes/r/events", hang)
	})
	env := partyEnv(ph)
	env["HEALTH_GRACE"] = "1"
	out, _, code := runFake(t, &MessageEnv{}, env, 10*time.Second, "wait", "--deadline", "30")
	if code != 1 || !strings.HasPrefix(out, "HEALTH daemon-unreachable") {
		t.Fatalf("%d %q", code, out)
	}
}

// TestKeyedCallAttemptDeadline: a send whose answer never comes is retried
// with a deadline per attempt and gives up after the 60 s window.
func TestKeyedCallAttemptDeadline(t *testing.T) {
	var tries atomic.Int64
	ph := fakeDaemon(t, func(m *http.ServeMux) {
		m.HandleFunc("POST /v1/routes/r/messages", func(w http.ResponseWriter, r *http.Request) {
			tries.Add(1)
			hang(w, r)
		})
	})
	c := newClock()
	e := &MessageEnv{AttemptTimeout: 50 * time.Millisecond, Now: c.Now, Sleep: c.Add}
	body := writeFile(t, t.TempDir(), "b.md", "hello\n")
	_, errOut, code := runFake(t, e, partyEnv(ph), 20*time.Second, "send", "adj", body, "brief")
	if code != 75 || tries.Load() < 2 {
		t.Fatalf("exit %d after %d tries: %s", code, tries.Load(), errOut)
	}
}

// TestDurationClampsLarge: a well-formed duration is clamped, never
// refused for its size or wrapped by an overflow (A25).
func TestDurationClampsLarge(t *testing.T) {
	for _, v := range []string{"999999999d", "9999999999", "99999999999h"} {
		if d, err := parseDuration(v, time.Second, 24*time.Hour); err != nil || d != 24*time.Hour {
			t.Fatalf("%s: %v %v", v, d, err)
		}
	}
}

// TestDiscoveryNamesOtherPrivateHome: with the socket of the selected
// private home down and a daemon running with another one, the client
// names that private home (3.6).
func TestDiscoveryNamesOtherPrivateHome(t *testing.T) {
	h := newHarness(t)
	other := filepath.Join(shortDir(t), "q")
	env := partyEnv(other)
	env["HOME"] = h.home
	_, errOut, code := runFake(t, &MessageEnv{}, env, 10*time.Second, "authority")
	if code != 75 || !strings.Contains(errOut, "the running daemon uses "+h.ph+"; set HERDSMAN_PRIVATE_HOME") {
		t.Fatalf("%d %q", code, errOut)
	}
}

// TestClientConcurrentFirstRequests: the version check is shared safely
// by concurrent first requests (run under -race).
func TestClientConcurrentFirstRequests(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Do(context.Background(), "GET", "/v1/health", nil, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}
