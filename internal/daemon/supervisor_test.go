package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSupervisor records supervisor calls and acts on the harness's
// daemon as launchctl or systemctl would.
type fakeSupervisor struct {
	h     *harness
	calls []string
}

func (f *fakeSupervisor) run(name string, args ...string) error {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	switch {
	case strings.Contains(call, "bootout") || strings.Contains(call, " stop "+systemdUnit) || strings.HasSuffix(call, "stop "+systemdUnit):
		f.h.stop()
	case strings.Contains(call, "bootstrap") || strings.Contains(call, "enable --now"):
		if f.h.cancel == nil {
			f.h.start()
		}
	case strings.Contains(call, "kickstart") || strings.Contains(call, "restart "+systemdUnit):
		f.h.restart()
	}
	return nil
}

func (f *fakeSupervisor) index(sub string) int {
	for i, c := range f.calls {
		if strings.Contains(c, sub) {
			return i
		}
	}
	return -1
}

func newSup(t *testing.T, goos string) (*harness, *fakeSupervisor, *Supervisor) {
	h := newHarness(t)
	f := &fakeSupervisor{h: h}
	home := filepath.Join(h.dir, "uhome")
	_ = os.MkdirAll(home, 0o700)
	s := &Supervisor{Home: h.home, PrivateHome: h.ph, Binary: "/usr/local/bin/herdsman", Version: testVersion, GOOS: goos,
		Run: f.run, Wait: 3 * time.Second}
	return h, f, s
}

func TestInstallWritesUnit(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			h, f, s := newSup(t, goos)
			// Installing while a daemon runs outside the supervisor is refused.
			if _, err := s.Install(); err == nil || !strings.Contains(err.Error(), "another daemon runs") {
				t.Fatalf("got %v", err)
			}
			h.stop()
			v, err := s.Install()
			if err != nil || v != testVersion {
				t.Fatalf("install: %q %v (calls %v)", v, err, f.calls)
			}
			b, _ := os.ReadFile(s.UnitPath())
			text := string(b)
			if !strings.Contains(text, "/usr/local/bin/herdsman") || !strings.Contains(text, h.ph) {
				t.Fatalf("unit:\n%s", text)
			}
			if goos == "darwin" {
				if !strings.Contains(text, "<key>KeepAlive</key>") || !strings.HasSuffix(s.UnitPath(), "Library/LaunchAgents/ai.herdsman.daemon.plist") {
					t.Fatalf("plist:\n%s", text)
				}
				if f.index("launchctl bootstrap") < 0 {
					t.Fatalf("calls %v", f.calls)
				}
			} else {
				if !strings.Contains(text, "Restart=always") || !strings.HasSuffix(s.UnitPath(), ".config/systemd/user/herdsman-daemon.service") {
					t.Fatalf("unit:\n%s", text)
				}
				if f.index("systemctl --user enable --now") < 0 {
					t.Fatalf("calls %v", f.calls)
				}
			}
		})
	}
}

func TestStopStaysStopped(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			h, f, s := newSup(t, goos)
			h.stop()
			if _, err := s.Install(); err != nil {
				t.Fatal(err)
			}
			if err := s.Stop(); err != nil {
				t.Fatalf("stop: %v (calls %v)", err, f.calls)
			}
			disable, stop := f.index(" disable "), f.index("bootout")
			if goos == "linux" {
				stop = f.index("stop " + systemdUnit)
			}
			if disable < 0 || stop < 0 || disable > stop {
				t.Fatalf("automatic restart not disabled before the stop: %v", f.calls)
			}
			if AccountLockHeld(h.home) {
				t.Fatal("the lock is still held")
			}
		})
	}
}

func TestUninstallRemovesUnit(t *testing.T) {
	h, f, s := newSup(t, "linux")
	h.stop()
	if _, err := s.Install(); err != nil {
		t.Fatal(err)
	}
	if err := s.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if exists(s.UnitPath()) || AccountLockHeld(h.home) {
		t.Fatal("unit or daemon left")
	}
	if f.calls[len(f.calls)-1] != "systemctl --user daemon-reload" {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestUninstallStaysStopped(t *testing.T) {
	h, f, s := newSup(t, "darwin")
	h.stop()
	if _, err := s.Install(); err != nil {
		t.Fatal(err)
	}
	if err := s.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if f.index("launchctl disable") < 0 || f.index("launchctl disable") > f.index("launchctl bootout") {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestRestartWaitsForHello(t *testing.T) {
	h, _, s := newSup(t, "darwin")
	h.stop()
	if _, err := s.Install(); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Restart(); err != nil || v != testVersion {
		t.Fatalf("%q %v", v, err)
	}
	s.Version = "9.9.9"
	if _, err := s.Restart(); err == nil || !strings.Contains(err.Error(), "not this client's 9.9.9") {
		t.Fatalf("unequal versions: %v", err)
	}
}

func TestStopWaitsForLock(t *testing.T) {
	h, _, s := newSup(t, "linux")
	h.stop()
	if _, err := s.Install(); err != nil {
		t.Fatal(err)
	}
	// A supervisor that does not stop the daemon: stop fails after the wait.
	s.Run = func(string, ...string) error { return nil }
	s.Wait = 300 * time.Millisecond
	if err := s.Stop(); err == nil || !strings.Contains(err.Error(), "still holds the account lock") {
		t.Fatalf("got %v", err)
	}
}
