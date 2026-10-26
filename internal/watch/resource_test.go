package watch

import (
	"context"
	"io"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// A24: a long-running loop keeps memory flat and leaves no goroutine
// behind: 1000 polls against the fakes, then the live heap after GC grows
// by under 1 MiB and the goroutine count returns to its start.
func checkLoop(t *testing.T, name string, polls int, run func(sleep func(time.Duration) bool)) {
	t.Helper()
	heap := func() uint64 {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.HeapAlloc
	}
	// One warm-up pass fills the caches a first call allocates.
	n := 0
	run(func(time.Duration) bool { n++; return n < 2 })
	g0, h0 := runtime.NumGoroutine(), heap()
	n = 0
	run(func(time.Duration) bool { n++; return n < polls })
	if n != polls {
		t.Fatalf("%s: %d polls, want %d (it returned early)", name, n, polls)
	}
	h1 := heap()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > g0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if g := runtime.NumGoroutine(); g > g0 {
		t.Errorf("%s: %d goroutines after, %d before", name, g, g0)
	}
	if h1 > h0 && h1-h0 >= 1<<20 {
		t.Errorf("%s: live heap grew by %d bytes over %d polls", name, h1-h0, polls)
	}
}

func TestLoopsKeepMemoryFlat(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	h := newHarness(t)
	h.screen("w9:p1", "• Working (1s • esc to interrupt)")
	clock := time.Now()
	env := func(extra map[string]string) Env {
		return func(k string) (string, bool) {
			if v, ok := extra[k]; ok {
				return v, true
			}
			v, ok := h.env[k]
			return v, ok
		}
	}
	deps := func(sleep func(time.Duration) bool) Deps {
		return Deps{Client: h.client, Now: func() time.Time { return clock }, Stdout: io.Discard, Stderr: io.Discard,
			Sleep: func(d time.Duration) bool { clock = clock.Add(d); h.fx.Server().ForgetRequests(); return sleep(d) }}
	}
	// The waiter: panes with nothing on them raise no event, so it polls.
	quiet := map[string]string{"HERDSMAN_IMPLEMENTERS": "im-q", "HERDSMAN_PANE_im_q": "w9:p7", "HERDSMAN_REVIEWERS": "rv-q:w9:p8",
		"DEADLINE": "1d", "POLL": "1"}
	checkLoop(t, "wait", 1000, func(sleep func(time.Duration) bool) {
		Wait(context.Background(), env(quiet), deps(sleep))
	})
	checkLoop(t, "watch implementers", 1000, func(sleep func(time.Duration) bool) {
		Implementers(context.Background(), env(nil), deps(sleep))
	})
	checkLoop(t, "watch reviewer", 1000, func(sleep func(time.Duration) bool) {
		Reviewer(context.Background(), env(nil), deps(sleep))
	})
	r := newPRRig(t)
	r.fixture("7-view.json", view("OPEN", "abcd1234ffff"))
	prEnv := map[string]string{"HERDSMAN_PRS": "7", "HERDSMAN_REPO": "org/repo", "HERDSMAN_PR_STATE": filepath.Join(r.dir, "state"),
		"HERDSMAN_PR_DEADLINE": "1d", "HERDSMAN_PR_INTERVAL": "1"}
	r.gh.err = io.Discard
	checkLoop(t, "watch prs", 1000, func(sleep func(time.Duration) bool) {
		d := deps(sleep)
		d.Gh = func(ctx context.Context, e []string, args ...string) ([]byte, error) {
			b, err := r.gh.run(ctx, e, args...)
			r.gh.calls = nil // the fake's own log is not the watcher's memory
			return b, err
		}
		PRs(context.Background(), func(k string) (string, bool) { v, ok := prEnv[k]; return v, ok }, d)
	})
}
