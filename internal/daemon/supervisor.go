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
	"strconv"
	"strings"
	"syscall"
	"text/template"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
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
	// Identity reads a process's native identity (nil: the system's).
	Identity func(pid int) (ProcID, error)
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
	cmd := exec.CommandContext(ctx, name, args...)
	// A manager child that keeps the output pipe open cannot hold the
	// call past the deadline.
	cmd.WaitDelay = 50 * time.Millisecond
	out, err := cmd.CombinedOutput()
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
// Only a verified answer counts as not loaded (launchd: no such service;
// systemd: inactive or failed); any other failure of the query is
// returned, so no lifecycle step treats a supervised daemon as manual.
func (s *Supervisor) managed() (loaded bool, pid int, err error) {
	if s.goos() == "darwin" {
		out, err := s.query("launchctl", "print", s.service())
		if err != nil {
			if strings.Contains(out+err.Error(), "Could not find service") {
				return false, 0, nil
			}
			return false, 0, fmt.Errorf("reading the LaunchAgent's state: %v", err)
		}
		return true, fieldInt(out, "pid = "), nil
	}
	out, err := s.query("systemctl", "--user", "show", "-p", "ActiveState", "-p", "MainPID", systemdUnit)
	if err != nil {
		return false, 0, fmt.Errorf("reading the systemd unit's state: %v", err)
	}
	state := ""
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "ActiveState="); ok {
			state = v
		}
	}
	switch state {
	case "inactive", "failed":
		return false, 0, nil
	case "active", "activating", "deactivating", "reloading", "refreshing":
		return true, fieldInt(out, "MainPID="), nil
	}
	return false, 0, fmt.Errorf("reading the systemd unit's state: unknown ActiveState %q", state)
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
	loaded, pid, err := s.managed()
	if err != nil {
		return "", err
	}
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
	if st, err := os.Lstat(filepath.Dir(s.logPath())); err != nil || !st.IsDir() {
		return "", fmt.Errorf("%s is not a folder; launchd could not open the daemon log there", filepath.Dir(s.logPath()))
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
		// The state first: a failed query changes nothing.
		loaded, _, err := s.managed()
		if err != nil {
			return err
		}
		if err := s.run("launchctl", "enable", s.service()); err != nil {
			return err
		}
		if loaded {
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
	loaded, pid, err := s.managed()
	if err != nil {
		return err
	}
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

// ProcID is a process's native identity: its executable as the kernel
// reports it, its argument vector and its start time.
type ProcID struct {
	Executable string
	Args       []string
	Start      time.Time
}

// DaemonIdentity is <private home>/daemon/identity.json, written by
// `daemon run` once it holds the account lock: its own native identity,
// which stop compares before it signals (5.7).
type DaemonIdentity struct {
	PID        int    `json:"pid"`
	Executable string `json:"executable"`
	StartNS    int64  `json:"start_ns"`
}

func identityPath(ph string) string { return filepath.Join(ph, "daemon", "identity.json") }

// readDaemonIdentity reads the identity record strictly.
func readDaemonIdentity(ph string) (DaemonIdentity, bool) {
	var r DaemonIdentity
	b, err := readSmall(identityPath(ph), 4096)
	if err != nil || inbox.StrictJSON(b) != nil || strictDecode(b, &r) != nil || r.PID <= 1 || r.Executable == "" || r.StartNS <= 0 {
		return DaemonIdentity{}, false
	}
	return r, true
}

// daemonRunArgv reports whether an argument vector is the command
// herdsman daemon run: daemon, then (after its global options) run.
func daemonRunArgv(args []string) bool {
	if len(args) < 3 || args[1] != "daemon" {
		return false
	}
	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--json":
		case "--private-home":
			i++
		default:
			return args[i] == "run"
		}
	}
	return false
}

// sameExecutable reports whether a process's executable is this herdsman:
// the same path, or the same file once links are resolved (a daemon
// started through a stable link, such as a Homebrew bin link, still
// matches after an upgrade moves the link).
func sameExecutable(exe, binary string) bool {
	if exe == "" || binary == "" {
		return false
	}
	if exe == binary {
		return true
	}
	a, err1 := filepath.EvalSymlinks(exe)
	b, err2 := filepath.EvalSymlinks(binary)
	return err1 == nil && err2 == nil && a == b
}

// checkIdentity checks that the account lock's pid is the daemon that
// took the lock (5.7): the daemon's identity record names that pid, and
// the live process runs herdsman daemon run as its command, from this
// herdsman's executable (the trusted reference; the record alone is not
// trusted) and the recorded one, with exactly the recorded native start
// time, at most a minute before the lock's start and not after it. A
// missing record or any mismatch refuses, so a stale or miswritten lock
// never signals another process.
func (s *Supervisor) checkIdentity(l AccountLock) error {
	if l.PID <= 1 {
		return fmt.Errorf("the account lock is held but names no process; inspect %s", AccountLockPath(s.Home))
	}
	refuse := func(why string) error {
		return fmt.Errorf("pid %d of the account lock %s; inspect %s, nothing was signalled", l.PID, why, AccountLockPath(s.Home))
	}
	rec, ok := readDaemonIdentity(s.PrivateHome)
	if !ok {
		return refuse("cannot be checked: no valid identity record " + identityPath(s.PrivateHome))
	}
	if rec.PID != l.PID {
		return refuse(fmt.Sprintf("does not match the identity record (pid %d)", rec.PID))
	}
	id := s.Identity
	if id == nil {
		id = procIdentity
	}
	p, err := id(l.PID)
	if err != nil {
		return refuse(fmt.Sprintf("cannot be checked (%v)", err))
	}
	lock := time.Unix(l.Start, 0)
	recStart := time.Unix(0, rec.StartNS)
	switch {
	case !daemonRunArgv(p.Args):
		return refuse("does not match: not herdsman daemon run")
	case !sameExecutable(p.Executable, s.Binary):
		return refuse(fmt.Sprintf("runs %q, not this herdsman (%s); after an upgrade, stop the old daemon through its supervisor (herdsman daemon stop with the unit installed) or end that pid yourself once you have checked it", p.Executable, s.Binary))
	case p.Executable != rec.Executable:
		return refuse(fmt.Sprintf("does not match: executable %q, recorded %q", p.Executable, rec.Executable))
	case !p.Start.Equal(recStart):
		return refuse("does not match: its native start time is not the record's (another process with a reused pid)")
	case p.Start.After(lock.Add(time.Second)) || lock.Sub(p.Start) > time.Minute:
		return refuse("does not match: not the daemon that took it at " + lock.Format(time.RFC3339))
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
	loaded, pid, err := s.managed()
	if err != nil {
		return "", err
	}
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
