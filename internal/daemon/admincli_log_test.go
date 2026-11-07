package daemon

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestAskTimeout(t *testing.T) {
	for v, want := range map[string]time.Duration{"": 5 * time.Minute, "90": 90 * time.Second, "007": 7 * time.Second, "2m": 2 * time.Minute, "3h": 3 * time.Hour, "1d": 24 * time.Hour, "9d": 24 * time.Hour, "99999999999999999h": 24 * time.Hour} {
		if got, err := askTimeout(v); err != nil || got != want {
			t.Errorf("%q: %v %v", v, got, err)
		}
	}
}

// TestDaemonLogBounded: the daemon log keeps at most logMax bytes plus
// one previous file, however many lines are written.
func TestDaemonLogBounded(t *testing.T) {
	ph := t.TempDir()
	var echo bytes.Buffer
	l := newBoundedLog(ph, &echo)
	l.max = 4096
	line := strings.Repeat("x", 99)
	for range 1000 {
		l.Line(line)
	}
	l.Line(strings.Repeat("y", 10000)) // one line longer than the bound
	l.Close()
	ents, _ := os.ReadDir(filepath.Join(ph, "daemon"))
	if len(ents) != 2 {
		t.Fatalf("files %v", ents)
	}
	for _, e := range ents {
		st, _ := os.Stat(filepath.Join(ph, "daemon", e.Name()))
		if st.Size() > l.max || st.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %d bytes %v", e.Name(), st.Size(), st.Mode())
		}
	}
	if echo.Len() != 1000*100+10001 {
		t.Fatalf("stderr copy %d", echo.Len())
	}
	// An existing log over the bound rotates before the first new line.
	big := filepath.Join(ph, "daemon", "daemon.log")
	_ = os.WriteFile(big, bytes.Repeat([]byte("z"), 5000), 0o600)
	l = newBoundedLog(ph, io.Discard)
	l.max = 4096
	l.Line("new")
	l.Close()
	if b, _ := os.ReadFile(big); string(b) != "new\n" {
		t.Fatalf("not rotated: %d bytes", len(b))
	}
}

// TestDaemonLogFollowsStderr: when stderr is the log file (the
// LaunchAgent's StandardErrorPath), lines are not doubled onto stderr
// and fd 2 moves to the new file at each rotation.
func TestDaemonLogFollowsStderr(t *testing.T) {
	ph := t.TempDir()
	_ = os.Mkdir(filepath.Join(ph, "daemon"), 0o700)
	p := filepath.Join(ph, "daemon", "daemon.log")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := unix.Dup(2)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = f
	t.Cleanup(func() {
		os.Stderr = old
		_ = unix.Dup2(saved, 2)
		_ = unix.Close(saved)
		f.Close()
	})
	l := newBoundedLog(ph, os.Stderr)
	if !l.redirect {
		t.Fatal("stderr is the log file but was not detected")
	}
	l.max = 64
	for range 3 {
		l.Line(strings.Repeat("a", 40))
	}
	_, _ = fmt.Fprintln(f, "panic text") // the descriptor launchd gave (fd 2 there)
	l.Close()
	b, _ := os.ReadFile(p)
	if !strings.HasSuffix(string(b), "panic text\n") || len(b) > 64+11 {
		t.Fatalf("fd 2 did not follow the rotation: %q", b)
	}
}

// TestDaemonLogOpenFailures: a log that cannot be opened (no private
// home, or daemon.log a folder) drops the line from the file and keeps
// the copy on stderr.
func TestDaemonLogOpenFailures(t *testing.T) {
	var echo bytes.Buffer
	l := newBoundedLog(filepath.Join(t.TempDir(), "missing", "ph"), &echo)
	l.Line("x")
	ph := t.TempDir()
	_ = os.MkdirAll(filepath.Join(ph, "daemon", "daemon.log"), 0o700)
	l2 := newBoundedLog(ph, &echo)
	l2.Line("y")
	l.Close()
	l2.Close()
	if echo.String() != "x\ny\n" {
		t.Fatalf("%q", echo.String())
	}
}

// TestAdminDownJSON: a command whose daemon does not answer exits 75
// with the daemon_down error object.
func TestAdminDownJSON(t *testing.T) {
	h, _, sup := newSup(t, "linux")
	h.stop()
	out, _, code := adminRun(h, sup, nil, nil, false, "--json", "route", "list")
	if code != 75 || !strings.HasPrefix(out, `{"error":{"code":"daemon_down"`) {
		t.Fatalf("%d %q", code, out)
	}
}

// TestSetupLastQuestionUnanswered: input that ends at the last question
// (policy_verified) records nothing either; extra input lines are left.
func TestSetupLastQuestionUnanswered(t *testing.T) {
	h, _, sup := newSup(t, "linux")
	adminRun(h, sup, nil, nil, false, "route", "create", "--repo", "repo", "--route", "r")
	if _, errOut, code := adminRun(h, sup, strings.NewReader(strings.Repeat("y\n", 5)), nil, true, "setup", "r", "--rule", h.rules); code != 2 || !strings.Contains(errOut, "no answer") {
		t.Fatalf("%d %q", code, errOut)
	}
	if out, errOut, code := adminRun(h, sup, strings.NewReader(strings.Repeat("y\n", 9)), nil, true, "setup", "r", "--rule", h.rules); code != 0 {
		t.Fatalf("extra lines: %d %q %q", code, out, errOut)
	}
}

// TestStartupFailuresKeepLaunchdLogBounded: a daemon run whose start
// fails on its private home (a KeepAlive restart loop) opens nothing in
// that home and keeps the stderr file it was given bounded through that
// descriptor.
func TestStartupFailuresKeepLaunchdLogBounded(t *testing.T) {
	ph := t.TempDir()
	if err := os.Mkdir(filepath.Join(ph, "daemon"), 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(ph, "daemon", "daemon.log")
	if err := os.WriteFile(logPath, bytes.Repeat([]byte("x"), logMax), 0o600); err != nil {
		t.Fatal(err)
	}
	log, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	if err := os.Chmod(ph, 0o755); err != nil { // the start refuses this private home
		t.Fatal(err)
	}
	a := &AdminEnv{Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: log, Version: testVersion}
	for range 3 {
		if code := a.run(ph, t.TempDir()); code == 0 {
			t.Fatal("an invalid private home started")
		}
		a.Stderr = log
	}
	st, err := os.Stat(logPath)
	if err != nil || st.Size() > logMax || st.Size() == 0 {
		t.Fatalf("daemon.log: %v %v", st.Size(), err)
	}
	if _, err := os.Stat(logPath + ".1"); err == nil {
		t.Fatal("a refused private home was written: daemon.log.1")
	}
	// The given descriptor still writes to the file.
	_, _ = log.WriteString("after\n")
	if b, _ := os.ReadFile(logPath); !strings.HasSuffix(string(b), "after\n") || !strings.Contains(string(b), "herdsman daemon:") {
		t.Fatalf("daemon.log: %q", b)
	}
}

// TestOwnIdentityRecord: daemon run's identity record is this process's
// native identity, read back strictly; a malformed record is no record.
func TestOwnIdentityRecord(t *testing.T) {
	ph := t.TempDir()
	_ = os.Mkdir(filepath.Join(ph, "daemon"), 0o700)
	if err := writeOwnIdentity(ph); err != nil {
		t.Skipf("sandbox: %v", err)
	}
	rec, ok := readDaemonIdentity(ph)
	self, _ := procIdentity(os.Getpid())
	if !ok || rec.PID != os.Getpid() || rec.Executable != self.Executable || rec.StartNS != self.Start.UnixNano() {
		t.Fatalf("%+v %v", rec, ok)
	}
	for _, body := range []string{`{"pid":2,"pid":3,"executable":"/x","start_ns":1}`, `{"pid":2,"executable":"","start_ns":1}`, `{"pid":2,"executable":"/x","start_ns":"1"}`} {
		_ = os.WriteFile(identityPath(ph), []byte(body), 0o600)
		if _, ok := readDaemonIdentity(ph); ok {
			t.Fatalf("%s read as a record", body)
		}
	}
}

// TestDaemonRunServesUntilSignal: daemon run starts, records its own
// identity for stop, serves, and ends with exit 0 at a signal.
func TestDaemonRunServesUntilSignal(t *testing.T) {
	dir := shortDir(t)
	ph, home := filepath.Join(dir, "p"), filepath.Join(dir, "h")
	_ = os.Mkdir(ph, 0o700)
	_ = os.Mkdir(home, 0o700)
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(dir, "no-herdr.sock"))
	sig := make(chan os.Signal, 1)
	a := &AdminEnv{Stdout: io.Discard, Stderr: io.Discard, Version: testVersion, Signals: sig}
	done := make(chan int, 1)
	go func() { done <- a.run(ph, home) }()
	var rec DaemonIdentity
	ok := false
	for i := 0; i < 500 && !(ok && exists(SocketPath(ph))); i++ {
		time.Sleep(10 * time.Millisecond)
		rec, ok = readDaemonIdentity(ph)
	}
	if !exists(SocketPath(ph)) {
		t.Fatal("the daemon did not listen")
	}
	sig <- os.Interrupt
	if code := <-done; code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !ok || rec.PID != os.Getpid() || rec.Executable == "" {
		t.Fatalf("identity record %+v %v", rec, ok)
	}
}
