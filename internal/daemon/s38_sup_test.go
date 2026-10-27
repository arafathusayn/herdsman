package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// S38 F3: the supervisor's lifecycle admission and its wait for a free
// lock take the three-outcome probe: a held lock that cannot be checked
// stops nothing, installs nothing and is never reported free.

// lockHeld is the test's view of the account lock: held, or not known.
func lockHeld(home string) bool {
	held, err := accountLockState(home)
	return held || err != nil
}

// unreadableHeldLock holds the account lock under home on an open
// descriptor, then makes its file unreadable (mode 0000).
func unreadableHeldLock(t *testing.T, home string) {
	t.Helper()
	f, err := os.OpenFile(AccountLockPath(home), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"pid":42,"version":"v","start":1,"private_home":"/p","socket":"/p/s"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(AccountLockPath(home), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(AccountLockPath(home), 0o600)
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	})
	if g, err := os.Open(AccountLockPath(home)); err == nil {
		_ = g.Close()
		t.Skip("mode 0000 does not stop this user from opening the file")
	}
}

func s38Sup(t *testing.T, goos string) (*Supervisor, *[]string) {
	t.Helper()
	// A short base: the unit names the socket, which has a length limit.
	base, err := os.MkdirTemp("", "s38")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	home, ph := filepath.Join(base, "home"), filepath.Join(base, "ph")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	calls := &[]string{}
	s := &Supervisor{Home: home, PrivateHome: ph, Binary: "/usr/local/bin/herdsman", Version: testVersion, GOOS: goos,
		Run: func(name string, args ...string) error {
			*calls = append(*calls, name+" "+strings.Join(args, " "))
			return nil
		},
		Sleep: func(time.Duration) {}, Wait: 50 * time.Millisecond}
	unreadableHeldLock(t, home)
	return s, calls
}

func TestSupervisorStopUnreadableHeldLock(t *testing.T) {
	s, calls := s38Sup(t, "linux")
	err := s.Stop()
	if err == nil || !strings.Contains(err.Error(), "inspect "+AccountLockPath(s.Home)) || len(*calls) != 0 {
		t.Fatalf("Stop = %v, calls %v; want the inspection error and no call", err, *calls)
	}
	var out, errb bytes.Buffer
	code := RunDaemon(&AdminEnv{Getenv: func(string) string { return "" }, Stdout: &out, Stderr: &errb, Version: testVersion,
		Supervisor: s}, []string{"--private-home", s.PrivateHome, "stop"})
	if code == 0 || strings.Contains(out.String(), "stopped") || !strings.Contains(errb.String(), "inspect ") {
		t.Fatalf("daemon stop: exit %d, stdout %q, stderr %q", code, out.String(), errb.String())
	}
}

func TestSupervisorInstallUnreadableHeldLock(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			s, calls := s38Sup(t, goos)
			_, err := s.Install()
			if err == nil || !strings.Contains(err.Error(), "inspect "+AccountLockPath(s.Home)) {
				t.Fatalf("Install = %v; want the inspection error", err)
			}
			if exists(s.UnitPath()) || len(*calls) != 0 {
				t.Fatalf("unit written %v, calls %v", exists(s.UnitPath()), *calls)
			}
		})
	}
}

func TestSupervisorWaitFreeUnreadableHeldLock(t *testing.T) {
	s, _ := s38Sup(t, "linux")
	if err := s.waitFree(); err == nil || !strings.Contains(err.Error(), "inspect "+AccountLockPath(s.Home)) {
		t.Fatalf("waitFree = %v; want the inspection error", err)
	}
}
