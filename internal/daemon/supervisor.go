package daemon

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
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

var plistTmpl = template.Must(template.New("plist").Funcs(template.FuncMap{"xml": xmlText}).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{xml .Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{xml .Binary}}</string>
		<string>daemon</string>
		<string>run</string>
		<string>--private-home</string>
		<string>{{xml .PrivateHome}}</string>
	</array>
	<key>KeepAlive</key>
	<true/>
	<key>RunAtLoad</key>
	<true/>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardErrorPath</key>
	<string>{{xml .Log}}</string>
</dict>
</plist>
`))

var unitTmpl = template.Must(template.New("unit").Funcs(template.FuncMap{"arg": systemdArg}).Parse(`[Unit]
Description=herdsman daemon: the adjutant message channel

[Service]
ExecStart={{arg .Binary}} daemon run --private-home {{arg .PrivateHome}}
Restart=always
RestartSec=1

[Install]
WantedBy=default.target
`))

// xmlText escapes a value for a plist string.
func xmlText(v string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(v))
	return b.String()
}

// systemdArg quotes one ExecStart argument: a double-quoted word with
// backslash and quote escaped, and the specifier and variable signs
// doubled, so the service manager passes the path unchanged.
func systemdArg(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$")
	return `"` + r.Replace(v) + `"`
}

// Supervisor runs the system's service manager for the daemon's unit.
type Supervisor struct {
	Home        string
	PrivateHome string
	Binary      string
	Version     string
	GOOS        string
	// Run runs a supervisor program (nil: exec, bounded by the
	// operation's deadline).
	Run func(name string, args ...string) error
	// Query runs a supervisor program and returns its output (nil: exec).
	Query func(name string, args ...string) (string, error)
	// Identity reads a process's argument vector and start time (nil:
	// the system's).
	Identity func(pid int) ([]string, time.Time, error)
	// Signal signals a process (nil: kill(2)).
	Signal func(pid int, sig syscall.Signal) error
	Now    func() time.Time
	Sleep  func(time.Duration)
	// Wait bounds each lifecycle command as a whole: its manager calls
	// and its wait for hello or for the lock (default 10 s).
	Wait time.Duration

	end time.Time // the running command's deadline
}

func (s *Supervisor) goos() string {
	if s.GOOS == "" {
		return runtime.GOOS
	}
	return s.GOOS
}

// begin starts a lifecycle command's deadline.
func (s *Supervisor) begin() { s.end = s.now().Add(s.wait()) }

func (s *Supervisor) remaining() time.Duration {
	if s.end.IsZero() {
		s.begin()
	}
	return max(s.end.Sub(s.now()), 0)
}

func (s *Supervisor) exec(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.remaining())
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return string(out), nil
}

func (s *Supervisor) run(name string, args ...string) error {
	if s.Run != nil {
		return s.Run(name, args...)
	}
	_, err := s.exec(name, args...)
	return err
}

func (s *Supervisor) query(name string, args ...string) (string, error) {
	if s.Query != nil {
		return s.Query(name, args...)
	}
	return s.exec(name, args...)
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

// logPath is the daemon's log, the LaunchAgent's StandardErrorPath.
func (s *Supervisor) logPath() string { return filepath.Join(s.PrivateHome, "daemon", "daemon.log") }

// UnitText is the unit's text for this binary and private home.
func (s *Supervisor) UnitText() (string, error) {
	var b bytes.Buffer
	data := struct{ Label, Binary, PrivateHome, Log string }{launchdLabel, s.Binary, s.PrivateHome, s.logPath()}
	t := unitTmpl
	if s.goos() == "darwin" {
		t = plistTmpl
	}
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

func (s *Supervisor) domain() string  { return "gui/" + strconv.Itoa(os.Getuid()) }
func (s *Supervisor) service() string { return s.domain() + "/" + launchdLabel }

// Installed reports whether the unit file exists.
func (s *Supervisor) Installed() bool { return exists(s.UnitPath()) }

// managed is the service manager's own state of the unit: whether it is
// loaded (launchd) or active (systemd), and the pid it runs (0: none).
func (s *Supervisor) managed() (loaded bool, pid int) {
	if s.goos() == "darwin" {
		out, err := s.query("launchctl", "print", s.service())
		if err != nil {
			return false, 0
		}
		return true, fieldInt(out, "pid = ")
	}
	out, err := s.query("systemctl", "--user", "show", "-p", "ActiveState", "-p", "MainPID", systemdUnit)
	if err != nil {
		return false, 0
	}
	state := ""
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "ActiveState="); ok {
			state = v
		}
	}
	active := state == "active" || state == "activating" || state == "reloading"
	return active, fieldInt(out, "MainPID=")
}

// fieldInt is the integer after the first line that starts with key.
func fieldInt(out, key string) int {
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), key); ok {
			n, _ := strconv.Atoi(strings.TrimSpace(v))
			return n
		}
	}
	return 0
}

// manual reports the account lock's daemon when it runs outside the
// supervisor: the lock is held and the manager's pid is not its pid.
func (s *Supervisor) manual(pid int) (AccountLock, bool) {
	if !AccountLockHeld(s.Home) {
		return AccountLock{}, false
	}
	l, _ := ReadAccountLock(s.Home)
	return l, l.PID == 0 || l.PID != pid
}

// waitHello waits for the daemon's hello and returns its version.
func (s *Supervisor) waitHello() (string, error) {
	c := NewClient(SocketPath(s.PrivateHome), s.Version)
	defer c.Close()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), min(time.Second, max(s.remaining(), time.Millisecond)))
		h, err := c.Hello(ctx)
		cancel()
		if err == nil {
			return h.DaemonVersion, nil
		}
		if s.remaining() == 0 {
			return "", fmt.Errorf("no hello from the daemon within %s", s.wait())
		}
		s.sleep(200 * time.Millisecond)
	}
}

// waitFree waits until the account lock is free.
func (s *Supervisor) waitFree() error {
	for AccountLockHeld(s.Home) {
		if s.remaining() == 0 {
			return fmt.Errorf("the daemon still holds the account lock after %s", s.wait())
		}
		s.sleep(200 * time.Millisecond)
	}
	return nil
}

// Install writes the unit with the binary's absolute path and starts it;
// it refuses while a daemon of this account runs outside the supervisor.
// Run again, it converges: a loaded unit is reloaded with the new text.
func (s *Supervisor) Install() (string, error) {
	s.begin()
	if !filepath.IsAbs(s.Binary) {
		return "", fmt.Errorf("the binary path %s is not absolute", s.Binary)
	}
	if err := checkPrivateHome(s.PrivateHome); err != nil {
		return "", err
	}
	loaded, pid := s.managed()
	if l, ok := s.manual(pid); ok {
		return "", &ErrRunning{Holder: l}
	}
	text, err := s.UnitText()
	if err != nil {
		return "", err
	}
	// launchd opens StandardErrorPath before the daemon runs: its folder
	// must exist, private, first.
	if err := os.Mkdir(filepath.Dir(s.logPath()), 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(s.UnitPath()), 0o755); err != nil {
		return "", err
	}
	if err := writeDurable(s.UnitPath(), []byte(text), 0o644); err != nil {
		return "", err
	}
	if loaded && s.goos() == "darwin" {
		// bootstrap refuses a loaded service: unload the old unit first.
		if err := s.run("launchctl", "bootout", s.service()); err != nil {
			return "", err
		}
		if err := s.waitFree(); err != nil {
			return "", err
		}
	}
	if err := s.start(); err != nil {
		return "", err
	}
	if loaded && s.goos() != "darwin" {
		if err := s.run("systemctl", "--user", "restart", systemdUnit); err != nil {
			return "", err
		}
	}
	return s.waitHello()
}

// Start enables and starts the unit; an already loaded unit is left
// running.
func (s *Supervisor) Start() error {
	s.begin()
	return s.start()
}

func (s *Supervisor) start() error {
	if s.goos() == "darwin" {
		if err := s.run("launchctl", "enable", s.service()); err != nil {
			return err
		}
		if loaded, _ := s.managed(); loaded {
			return nil
		}
		return s.run("launchctl", "bootstrap", s.domain(), s.UnitPath())
	}
	if err := s.run("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	return s.run("systemctl", "--user", "enable", "--now", systemdUnit)
}

// Stop stops the daemon so it stays stopped: the supervisor's automatic
// restart is disabled first and every manager error is reported; a
// daemon that runs outside the supervisor gets SIGTERM only after its
// process is checked against the account lock. It waits until the lock
// is free, all within the command's deadline.
func (s *Supervisor) Stop() error {
	s.begin()
	return s.stop()
}

func (s *Supervisor) stop() error {
	loaded, pid := s.managed()
	if loaded || s.Installed() {
		if s.goos() == "darwin" {
			if err := s.run("launchctl", "disable", s.service()); err != nil {
				return err
			}
			if loaded {
				if err := s.run("launchctl", "bootout", s.service()); err != nil {
					return err
				}
			}
		} else {
			if err := s.run("systemctl", "--user", "disable", systemdUnit); err != nil {
				return err
			}
			if err := s.run("systemctl", "--user", "stop", systemdUnit); err != nil {
				return err
			}
		}
	}
	if l, ok := s.manual(pid); ok {
		if err := s.checkIdentity(l); err != nil {
			return err
		}
		sig := s.Signal
		if sig == nil {
			sig = syscall.Kill
		}
		if err := sig(l.PID, syscall.SIGTERM); err != nil {
			return fmt.Errorf("signal pid %d: %v", l.PID, err)
		}
	}
	return s.waitFree()
}

// checkIdentity checks that the account lock's pid is the daemon that
// took the lock: a herdsman daemon run process that started at most a
// minute before the lock's start and not after it (5.7). Any mismatch
// refuses, so a stale record never signals another process.
func (s *Supervisor) checkIdentity(l AccountLock) error {
	if l.PID <= 1 {
		return fmt.Errorf("the account lock is held but names no process; inspect %s", AccountLockPath(s.Home))
	}
	id := s.Identity
	if id == nil {
		id = procIdentity
	}
	args, started, err := id(l.PID)
	if err != nil {
		return fmt.Errorf("pid %d of the account lock cannot be checked (%v); inspect %s, nothing was signalled", l.PID, err, AccountLockPath(s.Home))
	}
	runs := slices.ContainsFunc(args[min(1, len(args)):], func(a string) bool { return a == "daemon" }) && slices.Contains(args, "run")
	lock := time.Unix(l.Start, 0)
	if !runs || started.After(lock.Add(time.Second)) || lock.Sub(started) > time.Minute {
		return fmt.Errorf("pid %d does not match the account lock (not the daemon that took it at %s); inspect %s, nothing was signalled", l.PID, lock.Format(time.RFC3339), AccountLockPath(s.Home))
	}
	return nil
}

// Uninstall disables automatic start, stops the daemon, waits for the
// lock, removes the unit and reloads the supervisor.
func (s *Supervisor) Uninstall() error {
	s.begin()
	if err := s.stop(); err != nil {
		return err
	}
	if err := os.Remove(s.UnitPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	if s.goos() != "darwin" {
		return s.run("systemctl", "--user", "daemon-reload")
	}
	return nil
}

// Restart restarts the daemon without the socket API, so it works when
// the running daemon's version differs; it then checks the new daemon's
// version equals this client's.
func (s *Supervisor) Restart() (string, error) {
	s.begin()
	loaded, pid := s.managed()
	_, manual := s.manual(pid)
	switch {
	case !manual && loaded && s.goos() == "darwin":
		if err := s.run("launchctl", "kickstart", "-k", s.service()); err != nil {
			return "", err
		}
	case !manual && loaded:
		if err := s.run("systemctl", "--user", "restart", systemdUnit); err != nil {
			return "", err
		}
	default:
		if err := s.stop(); err != nil {
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
