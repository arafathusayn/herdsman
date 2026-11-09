package ledger

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

func openTestLog(t *testing.T) (*Log, func() []map[string]any) {
	t.Helper()
	dir := t.TempDir()
	log, err := OpenLog(dir, "api")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log, func() []map[string]any {
		file, err := os.Open(filepath.Join(dir, "herdsman", "logs", "api", "teardown.log"))
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		var entries []map[string]any
		scanner := bufio.NewScanner(file)
		scanner.Buffer(nil, 1<<20)
		for scanner.Scan() {
			var entry map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
				t.Fatal(err)
			}
			entries = append(entries, entry)
		}
		return entries
	}
}

func entryOf(t *testing.T, entries []map[string]any, event string) map[string]any {
	t.Helper()
	for _, entry := range entries {
		if entry["event"] == event {
			return entry
		}
	}
	t.Fatalf("no %s entry in %v", event, entries)
	return nil
}

func TestAFailedSignalIsLoggedWithItsStatus(t *testing.T) {
	log, read := openTestLog(t)
	m := newMachine(proc(10, 1, 10))
	e := execution(m)
	e.Log = log
	e.Kill = func(pid int, signal syscall.Signal) error {
		if signal == 0 {
			return nil
		}
		return syscall.EPERM
	}
	if _, err := e.Execute(context.Background()); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("err = %v", err)
	}
	entries := read()
	action := entryOf(t, entries, "action")
	if action["result"] != "failed" || action["exit_status"] != float64(syscall.EPERM) || action["error"] == nil {
		t.Fatalf("action = %v", action)
	}
	if action["identity"] != ProcessIdentity(10, testStart).String() || action["level"] != string(LevelExclusive) || action["class"] == nil {
		t.Fatalf("action lacks the target: %v", action)
	}
	if finish := entryOf(t, entries, "finish"); finish["error"] == nil || finish["interrupted"] != false {
		t.Fatalf("finish = %v", finish)
	}
}

func TestADeliveredSignalIsLoggedAsDelivered(t *testing.T) {
	log, read := openTestLog(t)
	e := execution(newMachine(proc(10, 1, 10)))
	e.Log = log
	if _, err := e.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries := read()
	if action := entryOf(t, entries, "action"); action["result"] != "delivered" || action["exit_status"] != float64(0) {
		t.Fatalf("action = %v", action)
	}
	finish := entryOf(t, entries, "finish")
	if finish["error"] != nil {
		t.Fatalf("finish = %v", finish)
	}
	if outcomes, _ := finish["outcomes"].([]any); len(outcomes) != 1 {
		t.Fatalf("finish = %v", finish)
	}
}

func TestATargetThatLeftFirstIsLoggedAsGone(t *testing.T) {
	log, read := openTestLog(t)
	e := execution(newMachine(proc(10, 1, 10)))
	e.Log = log
	e.Kill = func(pid int, signal syscall.Signal) error { return syscall.ESRCH }
	if _, err := e.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if action := entryOf(t, read(), "action"); action["result"] != "gone" || action["error"] != nil {
		t.Fatalf("action = %v", action)
	}
}

func TestARefusalIsLoggedForItsTarget(t *testing.T) {
	log, read := openTestLog(t)
	m := newMachine(proc(10, 1, 10))
	e := execution(m)
	e.Log = log
	m.onRead = func(m *machine, _ int) {
		m.snapshot.Records = append(m.snapshot.Records, Record{Task: "b", Execution: "q", Kind: KindProcess, ID: "10", Start: testStart, Command: "server --n 10"})
	}
	var stop *StopError
	if _, err := e.Execute(context.Background()); !errors.As(err, &stop) {
		t.Fatalf("err = %v", err)
	}
	entries := read()
	refused := entryOf(t, entries, "refused")
	if refused["id"] != "10" || refused["error"] == nil || refused["level"] != string(LevelExclusive) {
		t.Fatalf("refused = %v", refused)
	}
	if slices.ContainsFunc(entries, func(entry map[string]any) bool { return entry["event"] == "action" }) {
		t.Fatalf("a refused target was signaled: %v", entries)
	}
}

func TestAnInterruptedPlanSaysSo(t *testing.T) {
	log, read := openTestLog(t)
	m := newMachine(proc(10, 1, 10))
	m.ignoreTER[10] = true
	e := execution(m)
	e.Log = log
	ctx, cancel := context.WithCancel(context.Background())
	kill := e.Kill
	e.Kill = func(pid int, signal syscall.Signal) error {
		err := kill(pid, signal)
		if signal == syscall.SIGTERM {
			cancel()
		}
		return err
	}
	if _, err := e.Execute(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if finish := entryOf(t, read(), "finish"); finish["interrupted"] != true || finish["error"] == nil {
		t.Fatalf("finish = %v", finish)
	}
}

func TestAFailedContainerStopLogsItsExitCode(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 7")
	exitErr := cmd.Run()
	log, read := openTestLog(t)
	m := newMachine()
	m.snapshot.Containers = []Container{container("c1", "running", "p", "/w")}
	recordContainer(&m.snapshot, "c1")
	e := containerExecution(m, &engine{}, "")
	e.Log = log
	e.Run = func(context.Context, string, ...string) ([]byte, error) { return nil, exitErr }
	if _, err := e.Execute(context.Background()); err == nil {
		t.Fatal("no error")
	}
	if action := entryOf(t, read(), "action"); action["result"] != "failed" || action["exit_status"] != float64(7) || action["id"] != "c1" {
		t.Fatalf("action = %v", action)
	}
}

func TestARefusedContainerIsLogged(t *testing.T) {
	log, read := openTestLog(t)
	m := newMachine()
	m.snapshot.Containers = []Container{container("c1", "running", "p", "/w")}
	recordContainer(&m.snapshot, "c1")
	x := &engine{}
	e := containerExecution(m, x, "")
	e.Log = log
	m.readFail = fmt.Errorf("no read")
	if _, err := e.Execute(context.Background()); err == nil {
		t.Fatal("no error")
	}
	if refused := entryOf(t, read(), "refused"); refused["id"] != "c1" {
		t.Fatalf("refused = %v", refused)
	}
	if len(x.calls) != 0 {
		t.Fatalf("calls = %v", x.calls)
	}
}

func TestAnUnrecognisedFailureHasNoExitStatus(t *testing.T) {
	if got := exitStatus(errors.New("x")); got != -1 {
		t.Fatalf("exitStatus = %d", got)
	}
	if got := exitStatus(nil); got != 0 {
		t.Fatalf("exitStatus = %d", got)
	}
}

// A project's own compose file is what `compose -p` would load from wherever
// the command runs, so a wholly owned project is stopped by its container ids.
func TestAnOwnedProjectIsStoppedByItsIDsInOneCall(t *testing.T) {
	m := newMachine()
	m.snapshot.Containers = []Container{container("c1", "running", "p", "/w"), container("c2", "running", "p", "/w")}
	recordContainer(&m.snapshot, "c1")
	recordContainer(&m.snapshot, "c2")
	x := &engine{}
	outcomes, err := containerExecution(m, x, "p").Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(x.calls, []string{"docker stop c1 c2"}) || len(outcomes) != 2 {
		t.Fatalf("calls = %v, outcomes = %v", x.calls, outcomes)
	}
}

func TestCreatedReadsAResourcesCreationTime(t *testing.T) {
	h := validHost()
	r := hostReader(h, "darwin")
	got, err := r.Created(context.Background(), KindVolume, "v1")
	if err != nil || !got.Equal(time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("created = %v, err = %v", got, err)
	}
	if got, err = r.Created(context.Background(), KindNetwork, "n1"); err != nil || got.IsZero() {
		t.Fatalf("created = %v, err = %v", got, err)
	}
}

func TestCreatedRefusesWhatItCannotEstablish(t *testing.T) {
	ctx := context.Background()
	noEngine := &Reader{Run: func(context.Context, string, ...string) ([]byte, error) { return nil, exec.ErrNotFound }}
	if _, err := noEngine.Created(ctx, KindVolume, "v"); err == nil {
		t.Fatal("no engine")
	}
	reply := func(text string, failure error) *Reader {
		return &Reader{Engine: "docker", Run: func(context.Context, string, ...string) ([]byte, error) { return []byte(text), failure }}
	}
	cases := map[string]*Reader{
		"failed read": reply("", errors.New("no such volume")),
		"bad output":  reply("not json", nil),
		"other name":  reply(`[{"Name":"other","CreatedAt":"2026-11-08T09:00:00Z"}]`, nil),
		"no time":     reply(`[{"Name":"v","CreatedAt":"2 hours ago"}]`, nil),
	}
	for name, r := range cases {
		if _, err := r.Created(ctx, KindVolume, "v"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestUnfinishedFindsARunThatNeverEnded(t *testing.T) {
	dir := t.TempDir()
	if got, err := Unfinished(dir, "api"); err != nil || got {
		t.Fatalf("no log: %v, %v", got, err)
	}
	log, err := OpenLog(dir, "api")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	log.plan(Plan{})
	if got, err := Unfinished(dir, "api"); err != nil || !got {
		t.Fatalf("after a plan: %v, %v", got, err)
	}
	log.finish(nil, nil)
	if got, err := Unfinished(dir, "api"); err != nil || got {
		t.Fatalf("after its end: %v, %v", got, err)
	}
	if _, err := Unfinished(dir, ".."); err == nil {
		t.Fatal("a bad name")
	}
	blocked := t.TempDir()
	if err := os.MkdirAll(filepath.Join(blocked, "herdsman", "logs", "api", "teardown.log"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Unfinished(blocked, "api"); err == nil {
		t.Fatal("a folder as the log")
	}
}
