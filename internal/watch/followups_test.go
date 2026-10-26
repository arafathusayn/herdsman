package watch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
)

// Review port-core-11 P1: the status program's own failure was dropped,
// so its output became HEALTH events and the stored flags. A program that
// exits 7, and one whose child holds its output past WaitDelay, are failed
// checks: a line, no HEALTH, the stored flags kept.
func TestFailedStatusProgramIsNotHealth(t *testing.T) {
	for _, tc := range []struct{ name, script, want string }{
		{"exit 7", "#!/bin/sh\nprintf 'machine  <-- LOAD HIGH\\n'\nexit 7\n", "failed (exit status 7)"},
		{"output held past WaitDelay", "#!/bin/sh\nprintf 'machine  <-- LOAD HIGH\\n'\nsleep 3 &\nexit 0\n", "failed (exec: WaitDelay expired before I/O complete)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			prog := filepath.Join(h.dir, "status-prog")
			h.file(prog, tc.script)
			if err := os.Chmod(prog, 0o700); err != nil {
				t.Fatal(err)
			}
			st := filepath.Join(h.dir, "status-state")
			h.file(filepath.Join(st, "health-at"), "1\n")
			h.file(filepath.Join(st, "health-flags"), "kept\n")
			out, code := h.run(Wait, "HERDSMAN_IMPLEMENTERS", "", "HERDSMAN_REVIEWERS", "none", "HERDSMAN_STATE", st,
				"HERDSMAN_STATUS_SPECS", "im-a:w9:p1:/x", "HERDSMAN_STATUS_SCRIPT", prog, "DEADLINE", "5", "POLL", "1")
			if code != 0 || strings.Contains(out, "HEALTH ") || !strings.Contains(out, "herdsman wait: the status program "+prog+" "+tc.want+"; no health check") {
				t.Errorf("exit %d %q", code, out)
			}
			if b, _ := os.ReadFile(filepath.Join(st, "health-flags")); string(b) != "kept\n" {
				t.Errorf("the stored flags changed: %q", b)
			}
		})
	}
}

// Review port-core-11 P1: the reachable error branches of the round 13
// state and snapshot code, each with ordinary files.
func TestReachableStateAndSnapshotErrors(t *testing.T) {
	t.Run("removing a folder that is not empty", func(t *testing.T) {
		st, err := openState(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(st.path("blocked-a/child"), 0o700); err != nil {
			t.Fatal(err)
		}
		st.remove("blocked-a")
		if st.err == nil {
			t.Fatal("no error from removing a folder that is not empty")
		}
		st.remove("none")
		if !strings.Contains(st.err.Error(), "blocked-a") {
			t.Errorf("the first error is not kept: %v", st.err)
		}
	})
	t.Run("the PR state folder is a file", func(t *testing.T) {
		r := newPRRig(t)
		if err := os.WriteFile(filepath.Join(r.dir, "not-folder"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, code := r.run("7", "not-folder", 0); code != 1 || !strings.Contains(out, "herdsman watch prs: cannot save state: ") {
			t.Errorf("exit %d %q", code, out)
		}
	})
	t.Run("the third failed read cannot save its count", func(t *testing.T) {
		r := newPRRig(t)
		st := filepath.Join(r.dir, "state")
		if err := os.MkdirAll(st, 0o700); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(st, "fail-7")
		if err := os.WriteFile(p, []byte("2\n"), 0o400); err != nil {
			t.Fatal(err)
		}
		r.touch("7-fail")
		out, code := r.run("7", "state", 0)
		if code != 1 || !strings.Contains(out, "PR-WATCH-ERROR PR 7 could not be read") || !strings.Contains(out, "cannot save state in "+st) {
			t.Errorf("exit %d %q", code, out)
		}
	})
	t.Run("the next snapshot is gone", func(t *testing.T) {
		dir := t.TempDir()
		old := filepath.Join(dir, "old")
		if err := os.WriteFile(old, []byte("a\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := addedSince(old, filepath.Join(dir, "absent")); err == nil {
			t.Error("no error for a missing next snapshot")
		}
	})
	for _, which := range []string{"old", "next"} {
		t.Run("a line over the scanner limit in the "+which+" snapshot", func(t *testing.T) {
			dir := t.TempDir()
			old, next := filepath.Join(dir, "old"), filepath.Join(dir, "next")
			for _, p := range []string{old, next} {
				if err := os.WriteFile(p, []byte("a\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			p := old
			if which == "next" {
				p = next
			}
			if err := os.WriteFile(p, []byte(strings.Repeat("z", 1<<20+1)+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := addedSince(old, next); err == nil {
				t.Error("no error for a line over the scanner limit")
			}
		})
	}
}

// Review port-core-11 P1: a state write that fails in the status recheck
// of a Herdr event, during the sleep, stops each watcher with exit 1.
func TestStateFailureDuringSleep(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only folder")
	}
	for _, which := range []string{"wait", "implementers", "reviewer"} {
		t.Run(which, func(t *testing.T) {
			h := newHarness(t)
			h.env["DEADLINE"], h.env["POLL"] = "1d", "1d"
			for _, p := range []string{"w9:p1", "w9:p2", "w9:p3"} {
				h.screen(p, "")
			}
			st := h.env["HERDSMAN_STATE"]
			fn, pane, marker := Wait, "w9:p1", "hash-im-a"
			switch which {
			case "implementers":
				fn = Implementers
			case "reviewer":
				fn, pane, marker = Reviewer, "w9:p3", "hash-rv-1"
			}
			out := &syncBuffer{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan int, 1)
			go func() {
				done <- fn(ctx, func(k string) (string, bool) { v, ok := h.env[k]; return v, ok }, Deps{Client: h.client, Stdout: out, Stderr: out})
			}()
			ready := false
			for i := 0; i < 500 && !ready; i++ {
				_, err := os.Stat(filepath.Join(st, marker))
				ready = err == nil && h.fx.Server().Subscribers() > 0
				if !ready {
					time.Sleep(10 * time.Millisecond)
				}
			}
			if !ready {
				t.Fatal("the first pass did not finish")
			}
			time.Sleep(100 * time.Millisecond)
			if err := os.Chmod(st, 0o500); err != nil {
				t.Fatal(err)
			}
			defer os.Chmod(st, 0o700)
			a := herdrtest.Agent(pane, "s")
			a["agent_status"] = "blocked"
			h.fx.SetAgent(pane, a)
			h.fx.Server().Emit("pane.agent_status_changed", map[string]any{"pane_id": pane, "agent_status": "blocked"})
			select {
			case code := <-done:
				if code != 1 || !strings.Contains(out.String(), "cannot save state in "+st) || !strings.Contains(out.String(), "BLOCKED ") {
					t.Errorf("exit %d %q", code, out.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the watcher did not stop")
			}
		})
	}
}
