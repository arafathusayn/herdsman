package watch

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Review head-2 P1 (A26): the scripts dropped every failed state write, so
// a watcher with no saved state printed the same events on every run and
// exited 0. A failed state write now prints the pass's events, then the
// "cannot save state" error, exits 1, and prints no TICK.
func TestStateThatCannotBeSavedStopsTheWatchers(t *testing.T) {
	h := newHarness(t)
	h.file(filepath.Join(h.env["HERDSMAN_REPORTS"], "a.md"), "STATUS: complete\n")
	h.file(filepath.Join(h.env["HERDSMAN_REPORTS"], "review-a.md"), "VERDICT: pass\n")
	notDir := filepath.Join(h.dir, "state-file")
	h.file(notDir, "a regular file\n")
	for _, w := range []struct {
		name string
		fn   func(context.Context, Env, Deps) int
		who  string
	}{{"wait", Wait, "wait"}, {"implementers", Implementers, "watch implementers"}, {"reviewer", Reviewer, "watch reviewer"}} {
		t.Run(w.name+" state folder is a file", func(t *testing.T) {
			for range 2 {
				out, code := h.run(w.fn, "DEADLINE", "0", "POLL", "1", "HERDSMAN_STATE", notDir)
				if code != 1 || !strings.Contains(out, "herdsman "+w.who+": cannot save state: ") {
					t.Errorf("exit %d %q", code, out)
				}
				noMatch(t, "no event is printed", `REPORT|REVIEW|IDLE|TICK`, out)
			}
		})
		t.Run(w.name+" a state file cannot be written", func(t *testing.T) {
			st := filepath.Join(h.dir, "state-"+w.name)
			// A folder in the place of each file a first pass writes.
			for _, f := range []string{"mtime-a", "hash-im-a", "hash-rv-1", "seen-review-a.md"} {
				_ = os.MkdirAll(filepath.Join(st, f), 0o755)
			}
			for range 2 {
				out, code := h.run(w.fn, "DEADLINE", "0", "POLL", "1", "HERDSMAN_STATE", st)
				if code != 1 || !strings.Contains(out, "herdsman "+w.who+": cannot save state in "+st+": ") {
					t.Errorf("exit %d %q", code, out)
				}
				noMatch(t, "no TICK rests on unsaved state", `TICK`, out)
			}
		})
	}
}

// Review head-2 P1 (A26): a snapshot the PR watcher cannot commit (a
// folder in the place of pr-7.txt) is an error, not "TICK no PR change",
// and no next file is left behind.
func TestPRSnapshotThatCannotBeCommittedFails(t *testing.T) {
	r := newPRRig(t)
	r.fixture("7-view.json", view("OPEN", "abcd1234ffff"))
	st := filepath.Join(r.dir, "pr-state")
	_ = os.MkdirAll(filepath.Join(st, "pr-7.txt", "x"), 0o755)
	for range 2 {
		out, code := r.run("7", "pr-state", 0)
		if code != 1 || !strings.Contains(out, "herdsman watch prs: cannot save state in "+st+": ") || strings.Contains(out, "TICK") {
			t.Errorf("exit %d %q", code, out)
		}
		if _, err := os.Stat(filepath.Join(st, "next-7.txt")); err == nil {
			t.Error("next-7.txt left behind")
		}
	}
	// With the folder gone the next run takes its baseline.
	_ = os.RemoveAll(filepath.Join(st, "pr-7.txt"))
	if out, code := r.run("7", "pr-state", 0); code != 0 || !strings.Contains(out, "TICK no PR change in 0s") {
		t.Errorf("exit %d %q", code, out)
	}
	if b, _ := os.ReadFile(filepath.Join(st, "pr-7.txt")); !strings.Contains(string(b), "head abcd1234") {
		t.Errorf("baseline %q", b)
	}
}

// Review head-2 P1 (A24, A26): a prior snapshot that cannot be sorted made
// the comparison read both histories whole and report a success. Now it
// is a failed read: no event, the PR is listed as not read, the old
// snapshot is kept as it was, and three in a row end the watcher.
func TestUnsortableSnapshotIsAFailedRead(t *testing.T) {
	r := newPRRig(t)
	st := filepath.Join(r.dir, "pr-state")
	old := "state OPEN\nhead abcd1234\n" // not in byte order
	// The sort writes pr-7.txt.tmp: a folder that is not empty there makes
	// it fail on every poll (its cleanup cannot remove the folder).
	if err := os.MkdirAll(filepath.Join(st, "pr-7.txt.tmp", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st, "pr-7.txt"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	r.fixture("7-comments.json", []any{map[string]any{"id": 101, "user": map[string]any{"login": "greptile"}}})
	r.fixture("7-view.json", view("OPEN", "abcd1234ffff"))
	for i := 1; i <= 3; i++ {
		out, code := r.run("7", "pr-state", 0)
		if strings.Contains(out, "NEW") {
			t.Errorf("poll %d: an event from an unsorted snapshot: %q", i, out)
		}
		if i < 3 && (code != 0 || !strings.Contains(out, "TICK no PR change (not read: 7)")) {
			t.Errorf("poll %d: %d %q", i, code, out)
		}
		if i == 3 && (code != 1 || !strings.Contains(out, "PR-WATCH-ERROR PR 7 could not be read")) {
			t.Errorf("poll 3: %d %q", code, out)
		}
		if b, _ := os.ReadFile(filepath.Join(st, "pr-7.txt")); string(b) != old {
			t.Errorf("poll %d: the old snapshot changed: %q", i, b)
		}
	}
}

// hangingGh blocks every call until its context ends.
func hangingGh(ctx context.Context, _ []string, _ ...string) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// within runs fn and fails the test when it does not return in limit.
func within(t *testing.T, limit time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("still running after %v", limit)
	}
}

// Review head-2 P1 (A26): gh ran with no bound, so a gh that never ended
// held the watcher past its deadline. Each gh run gets what is left of the
// deadline; DEADLINE=0 still gets one whole poll.
func TestHangingGhEndsAtTheDeadline(t *testing.T) {
	for _, tc := range []struct {
		name     string
		deadline string
		extra    []string
		want     string
		code     int
	}{
		{"a hanging read", "1", nil, "TICK no PR change (not read: 7) in 1s", 0},
		{"a hanging read with DEADLINE=0", "0", nil, "TICK no PR change (not read: 7) in 0s", 0},
		{"a hanging token", "1", []string{"HERDSMAN_GH_ACCOUNT", "me"}, "PR-WATCH-ERROR no gh token for me", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"HERDSMAN_PRS": "7", "HERDSMAN_REPO": "org/repo", "HERDSMAN_PR_STATE": t.TempDir(),
				"HERDSMAN_PR_DEADLINE": tc.deadline, "HERDSMAN_PR_INTERVAL": "1"}
			for i := 0; i+1 < len(tc.extra); i += 2 {
				env[tc.extra[i]] = tc.extra[i+1]
			}
			var out strings.Builder
			var code int
			start := time.Now()
			d := Deps{Stdout: &out, Stderr: &out, Gh: hangingGh, Now: time.Now,
				Sleep: func(dd time.Duration) bool { time.Sleep(dd); return true }}
			within(t, 10*time.Second, func() {
				code = PRs(context.Background(), func(k string) (string, bool) { v, ok := env[k]; return v, ok }, d)
			})
			if code != tc.code || !strings.Contains(out.String(), tc.want) {
				t.Errorf("exit %d %q, want %d %q", code, out.String(), tc.code, tc.want)
			}
			if took := time.Since(start); took > 5*time.Second {
				t.Errorf("took %v", took)
			}
		})
	}
}

// Review head-2 P1: the real gh runners end with their context even when
// gh leaves a child behind that holds its output pipe open.
func TestGhRunnersEndWithTheirContext(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\nsleep 30 &\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	within(t, 10*time.Second, func() {
		if _, err := RunGh(io.Discard)(ctx, nil, "api"); err == nil {
			t.Error("RunGh: no error from a stopped gh")
		}
	})
	ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	within(t, 10*time.Second, func() {
		err := RunGhStream(io.Discard)(ctx, nil, func(r io.Reader) error { _, err := io.ReadAll(r); return err }, "api")
		if err == nil {
			t.Error("RunGhStream: no error from a stopped gh")
		}
	})
}

// Review head-2 P1 (A26): wait's status program ran with no bound. One
// that never ends (and leaves a child holding its output) is stopped at
// the deadline, with a line, and no HEALTH event.
func TestHangingStatusProgramEndsAtTheDeadline(t *testing.T) {
	h := newHarness(t)
	prog := filepath.Join(h.dir, "status-prog")
	h.file(prog, "#!/bin/sh\nsleep 30 &\nexec sleep 30\n")
	_ = os.Chmod(prog, 0o755)
	s := filepath.Join(h.dir, "state-h")
	h.file(filepath.Join(s, "health-at"), "1")
	var out string
	var code int
	start := time.Now()
	within(t, 10*time.Second, func() {
		out, code = h.run(Wait, "DEADLINE", "1", "POLL", "1", "HERDSMAN_STATE", s, "HERDSMAN_STATUS_SPECS", "im-a:w9:p1:/x", "HERDSMAN_STATUS_SCRIPT", prog)
	})
	if code != 0 || !strings.Contains(out, "herdsman wait: the status check did not finish in 1s; no health check") || strings.Contains(out, "HEALTH ") {
		t.Errorf("exit %d %q", code, out)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("took %v", took)
	}
	// The in-process check is bounded the same way.
	h.file(filepath.Join(s, "health-at"), "1")
	d := Deps{Client: h.client, Now: func() time.Time { return h.now }, Stdout: io.Discard, Stderr: io.Discard,
		Sleep: func(time.Duration) bool { return false },
		Status: func(ctx context.Context, w io.Writer, _ string) {
			<-ctx.Done()
			_, _ = io.WriteString(w, "machine  <-- LOAD HIGH\n")
		},
	}
	var errb strings.Builder
	d.Stderr = &errb
	env := map[string]string{"DEADLINE": "1", "POLL": "1", "HERDSMAN_STATE": s, "HERDSMAN_STATUS_SPECS": "im-a:w9:p1:/x", "HERDSMAN_IMPLEMENTERS": ""}
	within(t, 10*time.Second, func() {
		code = Wait(context.Background(), func(k string) (string, bool) { v, ok := env[k]; return v, ok }, d)
	})
	if code != 0 || !strings.Contains(errb.String(), "did not finish") {
		t.Errorf("in process: exit %d %q", code, errb.String())
	}
}

// Round 14 (PR #25 review bot P1): an event whose marker was saved is
// printed even when a later state write of the same pass fails; else the
// saved marker hides it for good once the bad file is fixed. Each watcher:
// the event shows in the failing run (exit 1), and not again after the fix.
func TestSavedEventsAreNotLost(t *testing.T) {
	h := newHarness(t)
	h.file(filepath.Join(h.env["HERDSMAN_REPORTS"], "a.md"), "STATUS: complete\n")
	h.file(filepath.Join(h.env["HERDSMAN_REPORTS"], "review-a.md"), "VERDICT: pass\n")
	for _, w := range []struct {
		name, event, broken string
		fn                  func(context.Context, Env, Deps) int
	}{
		{"wait", `^REPORT a `, "hash-im-a", Wait},
		{"implementers", `^REPORT a `, "hash-im-a", Implementers},
		{"reviewer", `^REVIEW .*review-a.md `, "hash-rv-1", Reviewer},
	} {
		t.Run(w.name, func(t *testing.T) {
			st := filepath.Join(h.dir, "saved-"+w.name)
			// A folder that is not empty where the pass writes a later file.
			_ = os.MkdirAll(filepath.Join(st, w.broken, "x"), 0o755)
			out, code := h.run(w.fn, "DEADLINE", "0", "POLL", "1", "HERDSMAN_STATE", st)
			if code != 1 || !strings.Contains(out, "cannot save state in "+st) {
				t.Errorf("broken run: exit %d %q", code, out)
			}
			match(t, "the saved event is printed before the stop", w.event, out)
			_ = os.RemoveAll(filepath.Join(st, w.broken))
			out, _ = h.run(w.fn, "DEADLINE", "0", "POLL", "1", "HERDSMAN_STATE", st)
			noMatch(t, "and not again once its marker is saved", w.event, out)
		})
	}
}

// Round 14: a PR change committed in a pass is printed even when another
// pull request's snapshot cannot be committed in the same pass.
func TestSavedPRChangesAreNotLost(t *testing.T) {
	r := newPRRig(t)
	st := filepath.Join(r.dir, "pr-state")
	r.fixture("7-view.json", view("OPEN", "abcd1234ffff"))
	r.run("7", "pr-state", 0)
	r.fixture("7-view.json", view("OPEN", "ffff0000aaaa"))
	_ = os.MkdirAll(filepath.Join(st, "pr-8.txt", "x"), 0o755)
	out, code := r.run("7 8", "pr-state", 0)
	if code != 1 || !strings.Contains(out, "herdsman watch prs: cannot save state in "+st) {
		t.Errorf("broken run: exit %d %q", code, out)
	}
	match(t, "the committed change is printed", `^PR 7 NEW head ffff0000$`, out)
	_ = os.RemoveAll(filepath.Join(st, "pr-8.txt"))
	out, _ = r.run("7 8", "pr-state", 0)
	noMatch(t, "and not again", `PR 7 NEW`, out)
}
