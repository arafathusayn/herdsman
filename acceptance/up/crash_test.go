//go:build acceptance

package up

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// onTerminal starts up in a session of its own on a scripted terminal, and
// answers the pair question if it comes. It returns a reader of the output.
func (m *machine) onTerminal(name string, args ...string) (output func() string, stop func()) {
	t := m.t
	t.Helper()
	master, slave := openPTY(t)
	path := filepath.Join(m.home, name+".out")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	helper := exec.Command(os.Args[0])
	helper.Dir = m.repo
	helper.Env = append(m.env(), orphanEnv+"=1")
	helper.Stdin, helper.Stdout, helper.Stderr = slave, file, file
	helper.Args = append([]string{os.Args[0], m.bin, "up"}, args...)
	if err := helper.Run(); err != nil {
		t.Fatalf("helper: %v", err)
	}
	slave.Close()
	file.Close()
	done := make(chan struct{})
	var once sync.Once
	stop = func() { once.Do(func() { close(done); master.Close() }) }
	t.Cleanup(stop)
	var screen syncBuffer
	go func() { _, _ = io.Copy(&screen, master) }()
	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(20 * time.Millisecond):
			}
			if strings.Contains(screen.String(), "approve this pair?") {
				_, _ = master.WriteString("y\n")
				return
			}
		}
	}()
	return func() string { b, _ := os.ReadFile(path); return string(b) + screen.String() }, stop
}

// killDaemon stops the daemon the way a crash does, with no chance to clean up.
func (m *machine) killDaemon() {
	m.t.Helper()
	if err := m.daemon.Process.Kill(); err != nil {
		m.t.Fatal(err)
	}
	_ = m.daemon.Wait()
}

func waitFor(t *testing.T, what string, detail func() string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("%s:\n%s", what, detail())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestPreludeCrashReplaysNothing runs a prelude step that does its work and
// dies before reporting, then kills the daemon and removes the pane. Nothing
// can tell whether the work happened, so a restart must neither repeat the
// step nor call it done until a person says which.
func TestPreludeCrashReplaysNothing(t *testing.T) { preludeCrash(t, "auto") }

// TestVerdictSurvivesThePairQuestion is the same crash on a manual pair: the
// question comes after the verdict, and answering it must not end the start.
func TestVerdictSurvivesThePairQuestion(t *testing.T) { preludeCrash(t, "manual") }

func preludeCrash(t *testing.T, pair string) {
	t.Helper()
	m := setup(t)
	effect := filepath.Join(m.home, "effect")
	step := filepath.Join(m.home, "crash-step")
	script := fmt.Sprintf("#!/bin/sh\necho done >> %s\nkill -9 $PPID\n", effect)
	if err := os.WriteFile(step, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	effects := func() int {
		b, _ := os.ReadFile(effect)
		return strings.Count(string(b), "done")
	}

	var mu sync.Mutex
	var pane string
	sent := 0
	m.world.OnInput(func(id, text string) bool {
		if strings.Contains(text, "herdsman-step") && !strings.Contains(text, ".probe") {
			mu.Lock()
			defer mu.Unlock()
			pane = id
			sent++
		}
		return true
	})
	stepsSent := func() int { mu.Lock(); defer mu.Unlock(); return sent }

	first, stopFirst := m.onTerminal("first", "harbor", "-p", "ship it", "-a", step+" && claude", "-o", "codex", "--pair", pair, "--no-attach")
	waitFor(t, "the step never ran", first, func() bool { return effects() == 1 })

	m.killDaemon()
	mu.Lock()
	m.world.Destroy(pane)
	mu.Unlock()
	stopFirst()
	m.startDaemon()

	resumed, err := m.up("harbor", "--continue", "--no-attach")
	if err == nil {
		t.Fatalf("a start with an unsettled step finished:\n%s", resumed)
	}
	if !strings.Contains(resumed, "--step 1=done") {
		t.Fatalf("no way forward was offered:\n%s", resumed)
	}
	if n := stepsSent(); n != 1 {
		t.Fatalf("the step was sent %d times", n)
	}
	if n := m.world.Starts("repo-harbor-adjutant"); n != 0 {
		t.Fatalf("the adjutant started past an unsettled step: %d", n)
	}

	settled, _ := m.onTerminal("settled", "harbor", "--continue", "--step", "1=done", "--pair", pair, "--no-attach")
	waitFor(t, "the start did not finish", settled, func() bool { return strings.Contains(settled(), "route harbor is up") })
	if n := effects(); n != 1 {
		t.Fatalf("the effect happened %d times", n)
	}
	if n := stepsSent(); n != 1 {
		t.Fatalf("the verdict repeated the step: sent %d times", n)
	}
	if n := m.world.Starts("repo-harbor-adjutant"); n != 1 {
		t.Fatalf("the adjutant started %d times", n)
	}
}
