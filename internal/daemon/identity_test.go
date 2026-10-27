package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// identityCase is a supervisor whose stop checks pid 4242 against a
// record and a native identity the test supplies.
func identityCase(t *testing.T, binary string, rec ProcID, live ProcID) (*Supervisor, AccountLock) {
	t.Helper()
	ph := t.TempDir()
	if err := os.Mkdir(filepath.Join(ph, "daemon"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeIdentity(t, ph, 4242, rec.Executable, rec.Start)
	s := &Supervisor{PrivateHome: ph, Binary: binary, Identity: func(int) (ProcID, error) { return live, nil }}
	return s, AccountLock{PID: 4242, Start: rec.Start.Truncate(time.Second).Add(time.Second).Unix()}
}

// TestStaleNativeStartRefused: a process whose native start differs from
// the record's by any amount (a reused pid 500 ms later) is refused.
func TestStaleNativeStartRefused(t *testing.T) {
	exe := "/opt/herdsman"
	at := time.Now().Truncate(time.Second).Add(-300 * time.Millisecond)
	for _, d := range []time.Duration{500 * time.Millisecond, time.Microsecond, -time.Microsecond} {
		s, l := identityCase(t, exe, ProcID{Executable: exe, Start: at}, ProcID{Executable: exe, Args: []string{exe, "daemon", "run"}, Start: at.Add(d)})
		if err := s.checkIdentity(l); err == nil || !strings.Contains(err.Error(), "start") {
			t.Fatalf("start %v off: %v", d, err)
		}
	}
	s, l := identityCase(t, exe, ProcID{Executable: exe, Start: at}, ProcID{Executable: exe, Args: []string{exe, "daemon", "run"}, Start: at})
	if err := s.checkIdentity(l); err != nil {
		t.Fatalf("the recorded daemon: %v", err)
	}
}

// TestForgedInterpreterRecordRefused: a record and a live process that
// agree on another program (an interpreter running a script named
// daemon) are refused: the executable must be this herdsman.
func TestForgedInterpreterRecordRefused(t *testing.T) {
	at := time.Now().Truncate(time.Second)
	py := "/usr/bin/python3"
	s, l := identityCase(t, "/opt/herdsman", ProcID{Executable: py, Start: at}, ProcID{Executable: py, Args: []string{py, "daemon", "run"}, Start: at})
	if err := s.checkIdentity(l); err == nil || !strings.Contains(err.Error(), "not this herdsman") {
		t.Fatalf("got %v", err)
	}
}

// TestStopAfterUpgrade: the daemon matches through the stable path it was
// started by (a Homebrew link) even after the link moves to a new
// version; a daemon whose kernel path is the old version's own file is
// refused with what to do instead.
func TestStopAfterUpgrade(t *testing.T) {
	root := t.TempDir()
	for _, v := range []string{"1.0.0", "1.0.1"} {
		_ = os.MkdirAll(filepath.Join(root, "Cellar", v, "bin"), 0o755)
		_ = os.WriteFile(filepath.Join(root, "Cellar", v, "bin", "herdsman"), []byte(v), 0o755)
	}
	_ = os.MkdirAll(filepath.Join(root, "bin"), 0o755)
	link := filepath.Join(root, "bin", "herdsman")
	_ = os.Symlink(filepath.Join(root, "Cellar", "1.0.1", "bin", "herdsman"), link) // upgraded
	old := filepath.Join(root, "Cellar", "1.0.0", "bin", "herdsman")
	at := time.Now().Truncate(time.Second)
	run := []string{"herdsman", "daemon", "run"}
	s, l := identityCase(t, link, ProcID{Executable: link, Start: at}, ProcID{Executable: link, Args: run, Start: at})
	if err := s.checkIdentity(l); err != nil {
		t.Fatalf("started through the stable link: %v", err)
	}
	s, l = identityCase(t, link, ProcID{Executable: old, Start: at}, ProcID{Executable: old, Args: run, Start: at})
	err := s.checkIdentity(l)
	if err == nil || !strings.Contains(err.Error(), "not this herdsman") || !strings.Contains(err.Error(), "herdsman daemon stop") {
		t.Fatalf("old version's own path: %v", err)
	}
	// The resolved path of the current binary matches too.
	cur := filepath.Join(root, "Cellar", "1.0.1", "bin", "herdsman")
	s, l = identityCase(t, link, ProcID{Executable: cur, Start: at}, ProcID{Executable: cur, Args: run, Start: at})
	if err := s.checkIdentity(l); err != nil {
		t.Fatalf("resolved path: %v", err)
	}
}

// TestQueryErrorBeforeStartMutation: start reads the LaunchAgent's state
// before it enables anything; a failed query changes nothing.
func TestQueryErrorBeforeStartMutation(t *testing.T) {
	_, f, s := newSup(t, "darwin")
	f.calls = nil
	s.Query = func(string, ...string) (string, error) { return "", errors.New("manager access denied") }
	if err := s.Start(); err == nil || !strings.Contains(err.Error(), "manager access denied") {
		t.Fatalf("got %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("a change before the state was known: %v", f.calls)
	}
}

// TestRecordExecutableMustMatch: this herdsman running as the lock's pid
// is still refused when the record names another executable (the record
// belongs to another daemon).
func TestRecordExecutableMustMatch(t *testing.T) {
	at := time.Now().Truncate(time.Second)
	exe := "/opt/herdsman"
	s, l := identityCase(t, exe, ProcID{Executable: "/other/herdsman", Start: at}, ProcID{Executable: exe, Args: []string{exe, "daemon", "run"}, Start: at})
	if err := s.checkIdentity(l); err == nil || !strings.Contains(err.Error(), "recorded") {
		t.Fatalf("got %v", err)
	}
}
