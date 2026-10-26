// Package runcheck runs one heavy check (type check, lint, tests, a
// formatter, a build) at a time on the machine, behind a load gate, at a
// lower CPU priority, as the skill's run-check.sh does. Every agent of every
// route on the machine shares one lock file. The lock is flock(2) on an
// open file that the check inherits, so the kernel frees it when the last
// of the check's processes exits, also after a kill -9; the script's lockf
// and flock programs take the same lock, so the script and the binary
// exclude each other on one file. Inside the lock the check waits while the
// 1-minute load is at or over the limit or free memory is low. A check that
// does not run says so on a line that starts with "run-check: NOT RUN".
package runcheck

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/internal/machine"

	"github.com/arafathusayn/herdsman/internal/settings"
	"github.com/arafathusayn/herdsman/internal/shnum"
)

// MachineReading is the probe of a run: the machine's 1-minute load and free
// memory percent, or "".
var MachineReading = machine.Reading

// Exit codes besides the check's own.
const (
	ExitTempFail = 75 // the load gate gave up, or no lock came within the wait
	ExitUsage    = 64 // a usage error
	ExitNoLock   = 69 // the lock file cannot be opened or locked
	ExitMarker   = 70 // the start marker cannot be written
	ExitPublish  = 73 // the publisher cannot write its reading
)

// Config is the environment of a run, with the script's defaults filled
// in by FromEnv.
type Config struct {
	Lock     string        // HERDSMAN_LOCK, default /tmp/herdsman-checks.lock
	Reading  string        // HERDSMAN_READING, default /tmp/herdsman-load
	LockWait time.Duration // HERDSMAN_LOCK_WAIT seconds, default 5400
	GateWait time.Duration // HERDSMAN_GATE_WAIT seconds, default 600
	MinFree  int           // HERDSMAN_MIN_FREE percent, default 10
	Nice     int           // HERDSMAN_NICE, default 10
	Poll     time.Duration // HERDSMAN_GATE_POLL seconds, default 15
	// LoadLimit is HERDSMAN_LOAD_LIMIT, default the core count.
	LoadLimit string
	Probes    bool // HERDSMAN_PROBES, on unless "off"

	// PublishFor is HERDSMAN_PUBLISH_SECONDS; negative (unset) runs the
	// publisher for its hours.
	PublishFor   time.Duration
	PublishEvery time.Duration // HERDSMAN_PUBLISH_EVERY seconds, default 15

	Check    string // HERDSMAN_CHECK: "", "locked" or "gated"
	CheckIn  string // HERDSMAN_CHECK_IN
	CheckRan string // HERDSMAN_CHECK_RAN

	Now   func() time.Time
	Sleep func(time.Duration)
	// Probe reads "<1-minute load> <free memory %>" from the machine, or "".
	Probe func() string
	// Env is the environment the check runs with (HERDSMAN_CHECK and the
	// markers are set on top). nil: os.Environ().
	Env []string
}

// MaxDuration caps every duration setting: the script had no range, so
// one day (A25), and no multiplication overflows.
const MaxDuration = 24 * time.Hour

// FromEnv is Load for a caller that has checked the settings already: an
// invalid setting keeps its default.
func FromEnv(getenv func(string) string) Config {
	c, _ := Load(getenv)
	return c
}

// Load reads the settings the script reads, with its defaults, through
// the strict parser (A25): the first invalid one is the error, which the
// command prints before it exits 2 and runs nothing.
func Load(getenv func(string) string) (Config, error) {
	or := func(k, d string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return d
	}
	r := settings.Reader{Getenv: getenv}
	c := Config{
		Lock:         or("HERDSMAN_LOCK", "/tmp/herdsman-checks.lock"),
		Reading:      or("HERDSMAN_READING", "/tmp/herdsman-load"),
		LockWait:     r.Duration("HERDSMAN_LOCK_WAIT", 5400*time.Second, MaxDuration),
		GateWait:     r.Duration("HERDSMAN_GATE_WAIT", 600*time.Second, MaxDuration),
		MinFree:      int(r.Int("HERDSMAN_MIN_FREE", 10, 0, 100)),
		Nice:         int(r.Int("HERDSMAN_NICE", 10, -20, 20)),
		Poll:         r.Duration("HERDSMAN_GATE_POLL", 15*time.Second, MaxDuration),
		LoadLimit:    strings.Trim(getenv("HERDSMAN_LOAD_LIMIT"), " \t\n"),
		Probes:       or("HERDSMAN_PROBES", "on") != "off",
		PublishFor:   -1,
		PublishEvery: r.Duration("HERDSMAN_PUBLISH_EVERY", 15*time.Second, MaxDuration),
		Check:        getenv("HERDSMAN_CHECK"),
		CheckIn:      getenv("HERDSMAN_CHECK_IN"),
		CheckRan:     getenv("HERDSMAN_CHECK_RAN"),
	}
	r.Decimal("HERDSMAN_LOAD_LIMIT")
	if getenv("HERDSMAN_PUBLISH_SECONDS") != "" {
		c.PublishFor = r.Duration("HERDSMAN_PUBLISH_SECONDS", 0, MaxDuration)
	}
	// A sleep of 0 between polls would spin (A24).
	for _, d := range []struct {
		k string
		v time.Duration
	}{{"HERDSMAN_GATE_POLL", c.Poll}, {"HERDSMAN_PUBLISH_EVERY", c.PublishEvery}} {
		if d.v < time.Second && r.Err == nil {
			r.Err = &settings.Error{Var: d.k, Reason: "must be at least 1 second", Got: getenv(d.k)}
		}
	}
	return c, r.Err
}

func (c *Config) defaults() {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Sleep == nil {
		c.Sleep = time.Sleep
	}
	if c.Probe == nil {
		c.Probe = MachineReading
	}
	if c.Env == nil {
		c.Env = os.Environ()
	}
}

// probe is the machine's reading, unless the probes are off.
func (c *Config) probe() string {
	if !c.Probes {
		return ""
	}
	return c.Probe()
}

// reading is the publisher's reading while it is fresh (under a minute
// old), else the probes.
func (c *Config) reading() string {
	if st, err := os.Stat(c.Reading); err == nil && c.Now().Sub(st.ModTime()) < 60*time.Second {
		b, _ := os.ReadFile(c.Reading)
		return strings.TrimRight(string(b), "\n")
	}
	return c.probe()
}

// Publish writes the machine's reading to the reading file every
// PublishEvery until hours (or PublishFor) pass, as `run-check.sh
// --publish <hours>` does. Hours are a whole number, read as test reads
// it; a span over a day is a day.
func Publish(c Config, hours string, stderr io.Writer) int {
	c.defaults()
	if hours == "" {
		fmt.Fprintln(stderr, "usage: herdsman run-check --publish <hours>")
		return ExitUsage
	}
	// An hour count the script could not read sent it on to the check path
	// with "--publish" as the command (A26): refuse it here.
	h, ok := shnum.Test(hours)
	if !ok || h < 0 {
		fmt.Fprintf(stderr, "herdsman run-check: --publish: not a whole number of hours (got %q)\n", hours)
		return settings.ExitInvalid
	}
	span := c.PublishFor
	if span < 0 {
		span = MaxDuration
		if h < int64(MaxDuration/time.Hour) {
			span = time.Duration(h) * time.Hour
		}
	}
	end := c.Now().Add(span)
	for c.Now().Before(end) {
		if r := c.probe(); r != "" {
			// The script ignored a failed write or rename (A26): checks then
			// ran on a stale or missing reading while it went on.
			tmp := c.Reading + "." + strconv.Itoa(os.Getpid())
			err := os.WriteFile(tmp, []byte(r+"\n"), 0o644)
			if err == nil {
				err = os.Rename(tmp, c.Reading)
			}
			if err != nil {
				_ = os.Remove(tmp)
				fmt.Fprintf(stderr, "run-check: NOT PUBLISHED (cannot write the reading %s: %v)\n", c.Reading, err)
				return ExitPublish
			}
		}
		c.Sleep(c.PublishEvery)
	}
	fmt.Fprintf(stderr, "run-check: publisher stopped at %s; sandboxed checks now run without a load gate\n", c.Now().Format("15:04"))
	return 0
}

// Run runs args as one check. It returns the check's exit code, or one of
// the codes above when the check did not run.
func Run(c Config, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c.defaults()
	switch c.Check {
	case "gated":
		// A check started by a check: it already holds the lock and passed the gate.
		return c.start(args, nil, stdin, stdout, stderr, false)
	case "locked":
		// Started under the lock by the script's lockf or flock: gate, then run.
		return c.gateAndRun(args, nil, stdin, stdout, stderr)
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: herdsman run-check <command> [args]")
		return ExitUsage
	}
	base := markerBase(filepath.Dir(c.Lock))
	c.CheckIn, c.CheckRan = base+".in", base+".ran"
	f, err := os.OpenFile(c.Lock, os.O_RDWR|os.O_CREATE, 0o666)
	if err != nil {
		fmt.Fprintf(stderr, "run-check: NOT RUN (cannot open the lock %s: %v)\n", c.Lock, err)
		return ExitNoLock
	}
	defer f.Close()
	got, err := tryLock(f)
	if err != nil {
		fmt.Fprintf(stderr, "run-check: NOT RUN (cannot lock %s: %v)\n", c.Lock, err)
		return ExitNoLock
	}
	if !got {
		fmt.Fprintf(stderr, "run-check: waiting for the machine lock, held by pid %s\n", holders(f))
		deadline := c.Now().Add(c.LockWait)
		for !got {
			if !c.Now().Before(deadline) {
				fmt.Fprintf(stderr, "run-check: NOT RUN (no lock within %ds)\n", int(c.LockWait/time.Second))
				return ExitTempFail
			}
			c.Sleep(100 * time.Millisecond)
			if got, err = tryLock(f); err != nil {
				fmt.Fprintf(stderr, "run-check: NOT RUN (cannot lock %s: %v)\n", c.Lock, err)
				return ExitNoLock
			}
		}
	}
	defer os.Remove(c.CheckIn)
	defer os.Remove(c.CheckRan)
	code := c.gateAndRun(args, f, stdin, stdout, stderr)
	if !exists(c.CheckRan) {
		if exists(c.CheckIn) {
			fmt.Fprintf(stderr, "run-check: NOT RUN (exit %d; the line above says why)\n", code)
		}
	}
	return code
}

// gateAndRun waits on the load gate under the lock, marks the start and
// runs the check.
func (c *Config) gateAndRun(args []string, lock *os.File, stdin io.Reader, stdout, stderr io.Writer) int {
	if err := touch(c.CheckIn); err != nil {
		fmt.Fprintln(stderr, "run-check: NOT RUN (cannot write the start marker)")
		return ExitMarker
	}
	limit := c.LoadLimit
	if limit == "" {
		limit = strconv.Itoa(runtime.NumCPU())
	}
	start := c.Now()
	for {
		r := c.reading()
		if r == "" {
			fmt.Fprintln(stderr, "run-check: no load reading, running under the lock only")
			break
		}
		// As the script's ${r%% *} and ${r##* }: a malformed reading (even
		// whitespace only) fails the gate below, never panics.
		load, free := r, r
		if i := strings.IndexByte(r, ' '); i >= 0 {
			load, free = r[:i], r[strings.LastIndexByte(r, ' ')+1:]
		}
		if under(load, limit) && freeInt(free) >= c.MinFree {
			break
		}
		if c.Now().Sub(start) >= c.GateWait {
			fmt.Fprintf(stderr, "run-check: NOT RUN (load gate gave up after %ds: load %s, limit %s, free %s%%)\n", int64(c.GateWait/time.Second), load, limit, free)
			return ExitTempFail
		}
		c.Sleep(c.Poll)
	}
	// Mark the start before the check runs; without a marker, stop rather
	// than run a check that would read as NOT RUN.
	if err := touch(c.CheckRan); err != nil || !exists(c.CheckRan) {
		fmt.Fprintln(stderr, "run-check: NOT RUN (cannot write the start marker)")
		return ExitMarker
	}
	return c.start(args, lock, stdin, stdout, stderr, true)
}

// start runs the check as a child: at the lower priority when nice is set,
// with HERDSMAN_CHECK=gated so the checks it starts run at once, and with
// the lock file inherited so the lock lives as long as the check does.
func (c *Config) start(args []string, lock *os.File, stdin io.Reader, stdout, stderr io.Writer, nice bool) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: herdsman run-check <command> [args]")
		return ExitUsage
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.Env = append(withoutCheck(c.Env), "HERDSMAN_CHECK=gated", "HERDSMAN_CHECK_IN="+c.CheckIn, "HERDSMAN_CHECK_RAN="+c.CheckRan)
	if lock != nil {
		cmd.ExtraFiles = []*os.File{lock}
	}
	if nice {
		// The child inherits this process's niceness: lower it by Nice
		// first (the kernel caps it at its maximum), as `nice -n` does.
		if cur, err := getNice(); err == nil {
			_ = setNice(cur + c.Nice)
		}
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "run-check: %v\n", err)
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return 127
		}
		return 126
	}
	if lock != nil {
		recordHolders(lock, os.Getpid(), cmd.Process.Pid)
	}
	sigs := make(chan os.Signal, 4)
	// The terminal sends Ctrl+C to the check itself; a TERM or HUP sent to
	// this process is passed on.
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigs:
				if s != syscall.SIGINT {
					_ = cmd.Process.Signal(s)
				}
			case <-done:
				return
			}
		}
	}()
	err := cmd.Wait()
	close(done)
	return exitCode(cmd, err)
}

func exitCode(cmd *exec.Cmd, err error) int {
	if err == nil {
		return 0
	}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return cmd.ProcessState.ExitCode()
}

// withoutCheck drops the run-check variables a caller passed in.
func withoutCheck(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		switch k {
		case "HERDSMAN_CHECK", "HERDSMAN_CHECK_IN", "HERDSMAN_CHECK_RAN":
			continue
		}
		out = append(out, e)
	}
	return out
}

// under compares as awk does: the load below the limit, numerically.
func under(load, limit string) bool {
	l, err1 := strconv.ParseFloat(load, 64)
	m, err2 := strconv.ParseFloat(limit, 64)
	if err1 != nil || err2 != nil {
		return false
	}
	return l < m
}

// freeInt is ${free%.*} as a number; a value that is no number fails the gate.
func freeInt(free string) int {
	whole, _, _ := strings.Cut(free, ".")
	n, err := strconv.Atoi(whole)
	if err != nil {
		return -1
	}
	return n
}

// markerBase names the markers as mktemp does; crypto/rand.Read never
// fails (Go 1.24), so the name never does either.
func markerBase(dir string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return filepath.Join(dir, "herdsman-check."+hex.EncodeToString(b)[:6])
}

func touch(p string) error {
	if p == "" {
		return errors.New("no marker path")
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return p != "" && err == nil
}

// tryLock takes the flock without waiting; false when another holds it.
func tryLock(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return false, err
}

// recordHolders writes the pids that hold the lock into the lock file, so a
// waiter can name them (the script asks lsof, which the binary does not use).
func recordHolders(f *os.File, pids ...int) {
	var s []string
	for _, p := range pids {
		s = append(s, strconv.Itoa(p))
	}
	if f.Truncate(0) == nil {
		_, _ = f.WriteAt([]byte(strings.Join(s, " ")+"\n"), 0)
	}
}

// holders names the pids recorded in the lock file, or "?" (a lock the
// script holds records none).
func holders(f *os.File) string {
	b := make([]byte, 128)
	n, _ := f.ReadAt(b, 0)
	if s := strings.TrimSpace(string(b[:n])); s != "" {
		return s
	}
	return "?"
}
