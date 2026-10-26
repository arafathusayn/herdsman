package daemon

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeSupervisor records supervisor calls and acts on the harness's
// daemon as launchctl or systemctl would, with their loaded state: a
// bootstrap of a loaded service fails as launchctl's does.
type fakeSupervisor struct {
	h      *harness
	calls  []string
	loaded bool
	fail   map[string]error
}

func (f *fakeSupervisor) run(name string, args ...string) error {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	for sub, err := range f.fail {
		if strings.Contains(call, sub) {
			return err
		}
	}
	switch {
	case strings.Contains(call, "bootout") || strings.HasSuffix(call, "stop "+systemdUnit):
		if !f.loaded && strings.Contains(call, "bootout") {
			return errors.New("Boot-out failed: 3: No such process")
		}
		f.loaded = false
		f.h.stop()
	case strings.Contains(call, "bootstrap"):
		if f.loaded {
			return errors.New("Bootstrap failed: 5: Input/output error")
		}
		f.loaded = true
		if f.h.cancel == nil {
			f.h.start()
		}
	case strings.Contains(call, "enable --now"):
		f.loaded = true
		if f.h.cancel == nil {
			f.h.start()
		}
	case strings.Contains(call, "kickstart") || strings.Contains(call, "restart "+systemdUnit):
		f.h.restart()
	}
	return nil
}

func (f *fakeSupervisor) query(name string, args ...string) (string, error) {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	pid := 0
	if f.h.cancel != nil {
		pid = os.Getpid()
	}
	if strings.Contains(call, "launchctl print") {
		if !f.loaded {
			return "", errors.New("Could not find service")
		}
		return fmt.Sprintf("gui/501/ai.herdsman.daemon = {\n\tpid = %d\n}\n", pid), nil
	}
	if !f.loaded {
		return "ActiveState=inactive\nMainPID=0\n", nil
	}
	return fmt.Sprintf("ActiveState=active\nMainPID=%d\n", pid), nil
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
		Run: f.run, Query: f.query, Wait: 3 * time.Second,
		Identity: func(int) ([]string, time.Time, error) {
			return nil, time.Time{}, errors.New("no identity in this test")
		},
		Signal: func(int, syscall.Signal) error {
			t.Error("signalled without an identity check")
			return nil
		}}
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
	s.Query = func(string, ...string) (string, error) {
		return fmt.Sprintf("ActiveState=active\nMainPID=%d\n", os.Getpid()), nil
	}
	s.Wait = 300 * time.Millisecond
	if err := s.Stop(); err == nil || !strings.Contains(err.Error(), "still holds the account lock") {
		t.Fatalf("got %v", err)
	}
}

// TestInstallMakesLogFolderFirst: launchd opens StandardErrorPath before
// the daemon runs, so a fresh private home's daemon folder exists, mode
// 700, when bootstrap runs.
func TestInstallMakesLogFolderFirst(t *testing.T) {
	h, f, s := newSup(t, "darwin")
	h.stop()
	_ = os.RemoveAll(filepath.Join(h.ph, "daemon"))
	run := f.run
	s.Run = func(name string, args ...string) error {
		if strings.Contains(strings.Join(args, " "), "bootstrap") {
			st, err := os.Stat(filepath.Join(h.ph, "daemon"))
			if err != nil || st.Mode().Perm() != 0o700 {
				t.Errorf("no private log folder at bootstrap: %v", err)
			}
		}
		return run(name, args...)
	}
	if _, err := s.Install(); err != nil {
		t.Fatal(err)
	}
}

// TestUnitTextEscapes: legal special characters in the paths reach the
// service manager unchanged: XML-escaped in the plist, quoted with the
// specifier and variable signs doubled in the systemd unit.
func TestUnitTextEscapes(t *testing.T) {
	bin := `/opt/a&b <c>/her dsman%h$HOME"q\x`
	ph := `/Users/u/p&q home`
	s := &Supervisor{Home: "/h", PrivateHome: ph, Binary: bin, GOOS: "darwin"}
	text, err := s.UnitText()
	if err != nil {
		t.Fatal(err)
	}
	var plist struct {
		Dict struct {
			Strings []string `xml:"string"`
			Array   struct {
				Strings []string `xml:"string"`
			} `xml:"array"`
		} `xml:"dict"`
	}
	if err := xml.Unmarshal([]byte(text), &plist); err != nil {
		t.Fatalf("the plist is not valid XML: %v\n%s", err, text)
	}
	if args := plist.Dict.Array.Strings; len(args) != 5 || args[0] != bin || args[1] != "daemon" || args[2] != "run" || args[3] != "--private-home" || args[4] != ph {
		t.Fatalf("arguments %q", args)
	}
	if logs := plist.Dict.Strings; logs[len(logs)-1] != ph+"/daemon/daemon.log" {
		t.Fatalf("strings %q", logs)
	}
	s.GOOS = "linux"
	text, _ = s.UnitText()
	want := `ExecStart="/opt/a&b <c>/her dsman%%h$$HOME\"q\\x" daemon run --private-home "/Users/u/p&q home"`
	if !strings.Contains(text, want+"\n") {
		t.Fatalf("unit:\n%s\nwant %s", text, want)
	}
}

// TestInstallConvergesWhenLoaded: install run again with the agent loaded
// unloads it first (launchctl refuses a second bootstrap) and ends with
// the daemon answering; on Linux the running unit is restarted.
func TestInstallConvergesWhenLoaded(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			h, f, s := newSup(t, goos)
			h.stop()
			if _, err := s.Install(); err != nil {
				t.Fatal(err)
			}
			f.calls = nil
			if v, err := s.Install(); err != nil || v != testVersion {
				t.Fatalf("second install: %q %v (calls %v)", v, err, f.calls)
			}
			if goos == "darwin" && (f.index("bootout") < 0 || f.index("bootout") > f.index("bootstrap")) {
				t.Fatalf("calls %v", f.calls)
			}
			if goos == "linux" && f.index("restart "+systemdUnit) < 0 {
				t.Fatalf("calls %v", f.calls)
			}
			// start on a loaded agent is not an error either.
			if err := s.Start(); err != nil {
				t.Fatalf("start when loaded: %v", err)
			}
		})
	}
}

// TestStaleUnitFileIsNotSupervision: an unloaded unit file beside a
// daemon started by hand does not pass for the supervisor: install
// refuses, and stop takes the manual path with its identity check.
func TestStaleUnitFileIsNotSupervision(t *testing.T) {
	h, _, s := newSup(t, "darwin")
	_ = os.MkdirAll(filepath.Dir(s.UnitPath()), 0o755)
	_ = os.WriteFile(s.UnitPath(), []byte("old"), 0o644)
	if _, err := s.Install(); err == nil || !strings.Contains(err.Error(), "another daemon runs") {
		t.Fatalf("install: %v", err)
	}
	if err := s.Stop(); err == nil || !strings.Contains(err.Error(), "cannot be checked") {
		t.Fatalf("stop: %v", err)
	}
	if !AccountLockHeld(h.home) {
		t.Fatal("stopped without the identity check")
	}
}

// TestManualStopChecksIdentity: a daemon started by hand is signalled
// only when the lock's pid runs herdsman daemon run and started just
// before the lock; any mismatch refuses and signals nothing.
func TestManualStopChecksIdentity(t *testing.T) {
	h, f, s := newSup(t, "linux")
	l, _ := ReadAccountLock(h.home)
	lockAt := time.Unix(l.Start, 0)
	signalled := 0
	s.Signal = func(pid int, sig syscall.Signal) error {
		signalled++
		if pid != l.PID || sig != syscall.SIGTERM {
			t.Errorf("signal %d to %d", sig, pid)
		}
		h.stop()
		return nil
	}
	for _, c := range []struct {
		args []string
		at   time.Time
	}{
		{[]string{"/usr/bin/vim", "notes"}, lockAt},
		{[]string{"/x/herdsman", "daemon", "run"}, lockAt.Add(5 * time.Second)},
		{[]string{"/x/herdsman", "daemon", "run"}, lockAt.Add(-2 * time.Minute)},
		{[]string{"/x/herdsman", "message", "run"}, lockAt},
	} {
		s.Identity = func(int) ([]string, time.Time, error) { return c.args, c.at, nil }
		if err := s.Stop(); err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("%v at %v: %v", c.args, c.at, err)
		}
	}
	if signalled != 0 || len(f.calls) == 0 {
		t.Fatalf("signalled %d", signalled)
	}
	s.Identity = func(int) ([]string, time.Time, error) {
		return []string{"/x/herdsman", "daemon", "run", "--private-home", h.ph}, lockAt.Add(-300 * time.Millisecond), nil
	}
	if err := s.Stop(); err != nil || signalled != 1 || AccountLockHeld(h.home) {
		t.Fatalf("matching daemon: %v, signalled %d", err, signalled)
	}
}

// TestStopReportsManagerErrors: a failed disable, bootout or stop fails
// the command, so stop or uninstall never reports success while
// automatic restart may still be on; uninstall keeps the unit then.
func TestStopReportsManagerErrors(t *testing.T) {
	for _, c := range []struct{ goos, fail string }{{"darwin", "launchctl disable"}, {"darwin", "bootout"}, {"linux", "--user disable"}, {"linux", "--user stop"}} {
		t.Run(c.goos+" "+c.fail, func(t *testing.T) {
			h, f, s := newSup(t, c.goos)
			h.stop()
			if _, err := s.Install(); err != nil {
				t.Fatal(err)
			}
			f.fail = map[string]error{c.fail: errors.New("manager refused")}
			if err := s.Stop(); err == nil || !strings.Contains(err.Error(), "manager refused") {
				t.Fatalf("stop: %v", err)
			}
			if err := s.Uninstall(); err == nil || !s.Installed() {
				t.Fatalf("uninstall: %v", err)
			}
		})
	}
}

// TestLifecycleDeadline: a manager call that hangs is ended at the
// command's deadline, which covers the calls and the wait together.
func TestLifecycleDeadline(t *testing.T) {
	bin := t.TempDir()
	for _, name := range []string{"launchctl", "systemctl"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexec /bin/sleep 30\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	h, _, s := newSup(t, "darwin")
	h.stop()
	s.Run, s.Query = nil, nil
	s.Wait = 500 * time.Millisecond
	_ = os.MkdirAll(filepath.Dir(s.UnitPath()), 0o755)
	_ = os.WriteFile(s.UnitPath(), []byte("unit"), 0o644)
	start := time.Now()
	err := s.Stop()
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("stop: %v after %v", err, time.Since(start))
	}
}

// TestSupervisorUserDomain: every launchctl call targets the user's GUI
// domain, never the system domain (no root).
func TestSupervisorUserDomain(t *testing.T) {
	h, f, s := newSup(t, "darwin")
	h.stop()
	if _, err := s.Install(); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	want := "gui/" + strconv.Itoa(os.Getuid())
	for _, c := range f.calls {
		if strings.HasPrefix(c, "launchctl ") && !strings.Contains(c, want) {
			t.Fatalf("call outside %s: %s", want, c)
		}
	}
}
