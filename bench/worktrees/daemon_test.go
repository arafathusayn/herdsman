package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTheDaemonCasesAreTheVerbsThatDialIt(t *testing.T) {
	b := &Bench{Fixtures: &Fixtures{}, Tools: Tools{Herdsman: "h", Wt: "w"}, ComposeReady: true}
	var got []string
	for _, c := range b.Cases() {
		if c.Daemon {
			got = append(got, c.ID)
		}
	}
	if want := []string{CaseLedgerScan, CaseTeardown, CaseTeardownCompose}; !slices.Equal(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
}

func fastCase(id string, daemon bool) Case {
	return Case{ID: id, Kind: KindBudget, BudgetMS: 1e6, Daemon: daemon}
}

func fastMeasure(events *[]string) measureFunc {
	return func(string, Case, int) (time.Duration, error) {
		*events = append(*events, "measure")
		return time.Millisecond, nil
	}
}

func TestADaemonSurroundsItsCaseOnly(t *testing.T) {
	var events []string
	b := &Bench{daemon: func() (func() error, error) {
		events = append(events, "start")
		return func() error { events = append(events, "stop"); return nil }, nil
	}}
	if r := b.runWithDaemon(fastCase("plain", false), 2, fastMeasure(&events)); r.Verdict == VerdictUnavailable {
		t.Fatal(r.Reason)
	}
	if slices.Contains(events, "start") {
		t.Fatalf("a daemon for a plain case: %v", events)
	}
	events = nil
	if r := b.runWithDaemon(fastCase("d", true), 2, fastMeasure(&events)); r.Verdict == VerdictUnavailable {
		t.Fatal(r.Reason)
	}
	if events[0] != "start" || events[len(events)-1] != "stop" {
		t.Fatalf("%v", events)
	}
}

func TestADaemonFaultMakesTheCaseUnavailable(t *testing.T) {
	var events []string
	cases := map[string]func() (func() error, error){
		"start": func() (func() error, error) { return nil, errors.New("no socket") },
		"stop":  func() (func() error, error) { return func() error { return errors.New("hung") }, nil },
	}
	for name, daemon := range cases {
		b := &Bench{daemon: daemon}
		if r := b.runWithDaemon(fastCase("d", true), 2, fastMeasure(&events)); r.Verdict != VerdictUnavailable || !strings.Contains(r.Reason, "daemon") {
			t.Errorf("%s: %+v", name, r)
		}
	}
}

func fakeDaemon(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "daemon")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// testFolder makes the state folder and answers on its socket.
func testFolder(t *testing.T) string {
	t.Helper()
	root, private, err := daemonFolder()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Mkdir(filepath.Join(private, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(private, "run", "daemon.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return private
}

func TestADaemonStartsAndStops(t *testing.T) {
	stop, err := startDaemon(fakeDaemon(t, "exec sleep 600"), os.Environ(), t.TempDir(), testFolder(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

func TestADaemonThatExitsIsReported(t *testing.T) {
	_, err := startDaemon(fakeDaemon(t, "echo cannot open the database; exit 3"), os.Environ(), t.TempDir(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "cannot open the database") {
		t.Fatalf("%v", err)
	}
}

func TestAMissingDaemonBinaryIsReported(t *testing.T) {
	if _, err := startDaemon(filepath.Join(t.TempDir(), "none"), os.Environ(), t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("started")
	}
}

func TestADaemonThatNeverAnswersIsReported(t *testing.T) {
	old := daemonStartLimit
	t.Cleanup(func() { daemonStartLimit = old })
	daemonStartLimit = 100 * time.Millisecond
	if _, err := startDaemon(fakeDaemon(t, "exec sleep 600"), os.Environ(), t.TempDir(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("%v", err)
	}
}

func TestADaemonThatIgnoresStopIsKilledAndReported(t *testing.T) {
	old := daemonStopLimit
	t.Cleanup(func() { daemonStopLimit = old })
	daemonStopLimit = 100 * time.Millisecond
	stop, err := startDaemon(fakeDaemon(t, "trap '' TERM; while :; do sleep 1; done"), os.Environ(), t.TempDir(), testFolder(t))
	if err != nil {
		t.Fatal(err)
	}
	// The shell needs a moment to set the trap before the signal arrives.
	time.Sleep(200 * time.Millisecond)
	if err := stop(); err == nil || !strings.Contains(err.Error(), "did not stop") {
		t.Fatalf("%v", err)
	}
}
