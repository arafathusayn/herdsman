package daemon

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"text/template"
	"time"
)

// The supervisor units (3.1): a user LaunchAgent with KeepAlive on macOS,
// a user systemd unit with Restart=always on Linux.
const (
	launchdLabel = "ai.herdsman.daemon"
	systemdUnit  = "herdsman-daemon.service"
)

var plistTmpl = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{.Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{.Binary}}</string>
		<string>daemon</string>
		<string>run</string>
		<string>--private-home</string>
		<string>{{.PrivateHome}}</string>
	</array>
	<key>KeepAlive</key>
	<true/>
	<key>RunAtLoad</key>
	<true/>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardErrorPath</key>
	<string>{{.Log}}</string>
</dict>
</plist>
`))

var unitTmpl = template.Must(template.New("unit").Parse(`[Unit]
Description=herdsman daemon: the adjutant message channel

[Service]
ExecStart={{.Binary}} daemon run --private-home {{.PrivateHome}}
Restart=always
RestartSec=1

[Install]
WantedBy=default.target
`))

// Supervisor runs the system's service manager for the daemon's unit.
type Supervisor struct {
	Home        string
	PrivateHome string
	Binary      string
	Version     string
	GOOS        string
	// Run runs a supervisor program (nil: exec).
	Run   func(name string, args ...string) error
	Now   func() time.Time
	Sleep func(time.Duration)
	// Wait bounds the waits for hello and for the lock (default 10 s).
	Wait time.Duration
}

func (s *Supervisor) goos() string {
	if s.GOOS == "" {
		return runtime.GOOS
	}
	return s.GOOS
}

func (s *Supervisor) run(name string, args ...string) error {
	if s.Run != nil {
		return s.Run(name, args...)
	}
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return nil
}

func (s *Supervisor) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s *Supervisor) sleep(d time.Duration) {
	if s.Sleep == nil {
		time.Sleep(d)
		return
	}
	s.Sleep(d)
}

func (s *Supervisor) wait() time.Duration {
	if s.Wait == 0 {
		return 10 * time.Second
	}
	return s.Wait
}

// UnitPath is the unit file's path.
func (s *Supervisor) UnitPath() string {
	if s.goos() == "darwin" {
		return filepath.Join(s.Home, "Library", "LaunchAgents", launchdLabel+".plist")
	}
	return filepath.Join(s.Home, ".config", "systemd", "user", systemdUnit)
}

// UnitText is the unit's text for this binary and private home.
func (s *Supervisor) UnitText() (string, error) {
	var b bytes.Buffer
	data := struct{ Label, Binary, PrivateHome, Log string }{launchdLabel, s.Binary, s.PrivateHome, filepath.Join(s.PrivateHome, "daemon", "daemon.log")}
	t := unitTmpl
	if s.goos() == "darwin" {
		t = plistTmpl
	}
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

func (s *Supervisor) domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

// Installed reports whether the unit file exists.
func (s *Supervisor) Installed() bool { return exists(s.UnitPath()) }

// waitHello waits for the daemon's hello and returns its version.
func (s *Supervisor) waitHello() (string, error) {
	end := s.now().Add(s.wait())
	c := NewClient(SocketPath(s.PrivateHome), s.Version)
	defer c.Close()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		h, err := c.Hello(ctx)
		cancel()
		if err == nil {
			return h.DaemonVersion, nil
		}
		if !s.now().Before(end) {
			return "", fmt.Errorf("no hello from the daemon within %s", s.wait())
		}
		s.sleep(200 * time.Millisecond)
	}
}

// waitFree waits until the account lock is free.
func (s *Supervisor) waitFree() error {
	end := s.now().Add(s.wait())
	for AccountLockHeld(s.Home) {
		if !s.now().Before(end) {
			return fmt.Errorf("the daemon still holds the account lock after %s", s.wait())
		}
		s.sleep(200 * time.Millisecond)
	}
	return nil
}

// Install writes the unit with the binary's absolute path and starts it;
// it refuses while a daemon of this account runs outside the supervisor.
func (s *Supervisor) Install() (string, error) {
	if !filepath.IsAbs(s.Binary) {
		return "", fmt.Errorf("the binary path %s is not absolute", s.Binary)
	}
	if err := checkPrivateHome(s.PrivateHome); err != nil {
		return "", err
	}
	if AccountLockHeld(s.Home) && !s.Installed() {
		l, _ := ReadAccountLock(s.Home)
		return "", &ErrRunning{Holder: l}
	}
	text, err := s.UnitText()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(s.UnitPath()), 0o755); err != nil {
		return "", err
	}
	if err := writeDurable(s.UnitPath(), []byte(text), 0o644); err != nil {
		return "", err
	}
	if err := s.Start(); err != nil {
		return "", err
	}
	return s.waitHello()
}

// Start enables and starts the unit.
func (s *Supervisor) Start() error {
	if s.goos() == "darwin" {
		_ = s.run("launchctl", "enable", s.domain()+"/"+launchdLabel)
		return s.run("launchctl", "bootstrap", s.domain(), s.UnitPath())
	}
	if err := s.run("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	return s.run("systemctl", "--user", "enable", "--now", systemdUnit)
}

// Stop stops the daemon so it stays stopped: the supervisor's automatic
// restart is disabled first; a daemon started by hand gets SIGTERM after
// its lock and process are checked. It waits until the lock is free.
func (s *Supervisor) Stop() error {
	if s.Installed() {
		if s.goos() == "darwin" {
			_ = s.run("launchctl", "disable", s.domain()+"/"+launchdLabel)
			_ = s.run("launchctl", "bootout", s.domain()+"/"+launchdLabel)
		} else {
			_ = s.run("systemctl", "--user", "disable", systemdUnit)
			_ = s.run("systemctl", "--user", "stop", systemdUnit)
		}
	} else if AccountLockHeld(s.Home) {
		l, ok := ReadAccountLock(s.Home)
		if !ok || l.PID <= 1 {
			return fmt.Errorf("the account lock is held but names no process; inspect %s", AccountLockPath(s.Home))
		}
		if err := syscall.Kill(l.PID, syscall.SIGTERM); err != nil {
			return fmt.Errorf("signal pid %d: %v", l.PID, err)
		}
	}
	return s.waitFree()
}

// Uninstall disables automatic start, stops the daemon, waits for the
// lock, removes the unit and reloads the supervisor.
func (s *Supervisor) Uninstall() error {
	if err := s.Stop(); err != nil {
		return err
	}
	if err := os.Remove(s.UnitPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	if s.goos() != "darwin" {
		_ = s.run("systemctl", "--user", "daemon-reload")
	}
	return nil
}

// Restart restarts the daemon without the socket API, so it works when
// the running daemon's version differs; it then checks the new daemon's
// version equals this client's.
func (s *Supervisor) Restart() (string, error) {
	switch {
	case s.Installed() && s.goos() == "darwin":
		if err := s.run("launchctl", "kickstart", "-k", s.domain()+"/"+launchdLabel); err != nil {
			return "", err
		}
	case s.Installed():
		if err := s.run("systemctl", "--user", "restart", systemdUnit); err != nil {
			return "", err
		}
	default:
		if err := s.Stop(); err != nil {
			return "", err
		}
		cmd := exec.Command(s.Binary, "daemon", "run", "--private-home", s.PrivateHome)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return "", err
		}
		_ = cmd.Process.Release()
	}
	v, err := s.waitHello()
	if err != nil {
		return "", err
	}
	if v != s.Version {
		return v, fmt.Errorf("the restarted daemon reports %s, not this client's %s; install one herdsman on PATH", v, s.Version)
	}
	return v, nil
}
