package runcheck

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The C cases of the skill's run-tests.sh. The cases that need two
// processes run this test binary as the run-check command (helperMode).

func TestMain(m *testing.M) {
	switch os.Getenv("RUNCHECK_HELPER") {
	case "run":
		os.Exit(Run(FromEnv(os.Getenv), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	case "nice":
		n, _ := getNice()
		fmt.Println(n)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// helper returns a command that runs this binary as `herdsman run-check args...`.
func helper(t *testing.T, env []string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(append(withoutCheck(os.Environ()), "RUNCHECK_HELPER=run"), env...)
	return cmd
}

type fixture struct {
	dir     string
	lock    string
	reading string
	env     []string
}

func newFixture(t *testing.T) fixture {
	dir := t.TempDir()
	f := fixture{dir: dir, lock: filepath.Join(dir, "checks.lock"), reading: filepath.Join(dir, "load")}
	f.env = []string{"HERDSMAN_LOCK=" + f.lock, "HERDSMAN_READING=" + f.reading, "HERDSMAN_PROBES=off", "HERDSMAN_GATE_POLL=1", "HERDSMAN_LOAD_LIMIT=4", "HERDSMAN_NICE=10"}
	return f
}

func (f fixture) setReading(t *testing.T, s string) {
	t.Helper()
	if err := os.WriteFile(f.reading, []byte(s+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// config is an in-process run with a fake clock that each sleep moves on.
func (f fixture) config() (Config, *time.Time) {
	clock := time.Now()
	c := FromEnv(func(k string) string {
		for _, e := range f.env {
			if kk, v, _ := strings.Cut(e, "="); kk == k {
				return v
			}
		}
		return ""
	})
	c.Nice = 0
	c.Now = func() time.Time { return clock }
	c.Sleep = func(d time.Duration) { clock = clock.Add(d) }
	c.Env = withoutCheck(os.Environ())
	return c, &clock
}

func runIn(c Config, args ...string) (string, string, int) {
	var out, errb bytes.Buffer
	code := Run(c, args, nil, &out, &errb)
	return out.String(), errb.String(), code
}

func match(t *testing.T, name, re, s string) {
	t.Helper()
	if !regexp.MustCompile("(?m)" + re).MatchString(s) {
		t.Errorf("%s: want /%s/ in %q", name, re, s)
	}
}

func noMatch(t *testing.T, name, re, s string) {
	t.Helper()
	if regexp.MustCompile("(?m)" + re).MatchString(s) {
		t.Errorf("%s: unexpected /%s/ in %q", name, re, s)
	}
}

func TestExitStatusAndUsage(t *testing.T) {
	f := newFixture(t)
	f.setReading(t, "0.5 50")
	c, _ := f.config()
	if _, _, code := runIn(c, "sh", "-c", "exit 3"); code != 3 {
		t.Errorf("C1 exit status of the check: %d", code)
	}
	if _, _, code := runIn(c, "sh", "-c", "exit 4"); code != 4 {
		t.Errorf("Z3 exit status kept: %d", code)
	}
	_, errs, code := runIn(c)
	if code != ExitUsage || !strings.Contains(errs, "usage: herdsman run-check") {
		t.Errorf("C1c usage error without a command: %d %q", code, errs)
	}
	_, errs, code = runIn(c, filepath.Join(f.dir, "no-such-program"))
	if code != 127 || !strings.Contains(errs, "run-check:") {
		t.Errorf("a missing program: %d %q", code, errs)
	}
	_, _, code = runIn(c, f.dir)
	if code != 126 {
		t.Errorf("a folder as the program: %d", code)
	}
	_, _, code = runIn(c, "sh", "-c", "kill -TERM $$")
	if code != 128+int(syscall.SIGTERM) {
		t.Errorf("a check killed by a signal: %d", code)
	}
	// A TERM sent to run-check is passed on to the check (the sleep lets
	// run-check set its handler first).
	_, _, code = runIn(c, "sh", "-c", "sleep 1; kill -TERM $PPID; exec sleep 10")
	if code != 128+int(syscall.SIGTERM) {
		t.Errorf("a TERM passed on: %d", code)
	}
}

func TestNiceLowersThePriority(t *testing.T) {
	f := newFixture(t)
	f.setReading(t, "0.5 50")
	cur, err := getNice()
	if err != nil {
		t.Fatal(err)
	}
	max := 19
	if runtime.GOOS == "darwin" {
		max = 20
	}
	want := min(cur+10, max)
	// The check is this binary in "nice" mode, started through a shell that
	// sets the mode over the inherited "run".
	cmd := helper(t, f.env, "sh", "-c", `RUNCHECK_HELPER=nice "$0"`, os.Args[0])
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != strconv.Itoa(want) {
		t.Errorf("C1b lowers the priority by 10: got %s, want %d", got, want)
	}
}

func TestNestedCheckRunsAtOnce(t *testing.T) {
	f := newFixture(t)
	f.setReading(t, "0.5 50")
	cmd := helper(t, append(f.env, "HERDSMAN_LOCK_WAIT=5"), os.Args[0], "echo", "nested")
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "nested" {
		t.Errorf("C2 a nested check runs at once: %v %q", err, out)
	}
}

func TestSecondCheckWaitsForTheFirst(t *testing.T) {
	f := newFixture(t)
	f.setReading(t, "0.5 50")
	order := filepath.Join(f.dir, "order.log")
	first := helper(t, f.env, "sh", "-c", "sleep 2; echo first >> '"+order+"'")
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	out, _ := helper(t, f.env, "sh", "-c", "echo second >> '"+order+"'").CombinedOutput()
	_ = first.Wait()
	b, _ := os.ReadFile(order)
	if got := strings.Join(strings.Fields(string(b)), " "); got != "first second" {
		t.Errorf("C3 the second check waits for the first: %q", got)
	}
	match(t, "C3b and says whose lock it waits for", `waiting for the machine lock, held by pid [0-9]+ [0-9]+`, string(out))
}

func TestNoLockWithinTheWait(t *testing.T) {
	f := newFixture(t)
	f.setReading(t, "0.5 50")
	first := helper(t, f.env, "sleep", "3")
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	defer first.Wait()
	time.Sleep(500 * time.Millisecond)
	cmd := helper(t, append(f.env, "HERDSMAN_LOCK_WAIT=1"), "echo", "ran")
	out, err := cmd.CombinedOutput()
	match(t, "C4 no lock within the wait: NOT RUN", `run-check: NOT RUN \(no lock within 1s\)`, string(out))
	noMatch(t, "C4b the check did not run", `^ran$`, string(out))
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != ExitTempFail {
		t.Errorf("C4c exit 75: %v", err)
	}
}

func TestLoadGate(t *testing.T) {
	f := newFixture(t)
	f.env = append(f.env, "HERDSMAN_GATE_WAIT=1")
	f.setReading(t, "9.0 50")
	c, _ := f.config()
	out, errs, code := runIn(c, "echo", "ran")
	match(t, "C5 load over the limit: the gate gives up", `NOT RUN \(load gate gave up after 1s: load 9.0, limit 4, free 50%\)`, errs)
	match(t, "C5 and the outer line names the exit", `NOT RUN \(exit 75; the line above says why\)`, errs)
	noMatch(t, "C5b the check did not run", `ran`, out)
	if code != ExitTempFail {
		t.Errorf("C5c exit 75: %d", code)
	}
	f.setReading(t, "0.5 5")
	c, _ = f.config()
	_, errs, _ = runIn(c, "echo", "ran")
	match(t, "C6 low free memory: the gate gives up", `NOT RUN \(load gate gave up`, errs)
	f.setReading(t, "0.5 x")
	c, _ = f.config()
	_, errs, _ = runIn(c, "echo", "ran")
	match(t, "C6b (Go) a reading with no number fails the gate", `NOT RUN \(load gate gave up`, errs)
	// R2 finding 7: a reading of whitespace only is no panic; the gate fails it.
	f.setReading(t, " \t\n")
	c, _ = f.config()
	out, errs, code = runIn(c, "echo", "ran")
	match(t, "(Go) a whitespace-only reading fails the gate", `NOT RUN \(load gate gave up after 1s: load , limit 4, free \t%\)`, errs)
	noMatch(t, "(Go) and the check did not run", `ran`, out)
	if code != ExitTempFail {
		t.Errorf("(Go) a whitespace-only reading: exit %d", code)
	}
	f.setReading(t, "x 50")
	c, _ = f.config()
	_, errs, _ = runIn(c, "echo", "ran")
	match(t, "C6c (Go) a load with no number fails the gate", `NOT RUN \(load gate gave up`, errs)

	// The gate waits for a reading that falls under the limit.
	f.setReading(t, "9.0 50")
	c, clock := f.config()
	c.GateWait = time.Hour
	polls := 0
	c.Sleep = func(d time.Duration) {
		*clock = clock.Add(d)
		if polls++; polls == 2 {
			f.setReading(t, "0.5 50")
			_ = os.Chtimes(f.reading, *clock, *clock)
		}
	}
	out, errs, code = runIn(c, "echo", "ran")
	if code != 0 || out != "ran\n" || errs != "" {
		t.Errorf("C5d (Go) the gate lets the check run once the load falls: %d %q %q", code, out, errs)
	}

	f.env = append(f.env, "HERDSMAN_LOAD_LIMIT=")
	f.setReading(t, "0.5 50")
	c, _ = f.config()
	c.LoadLimit = ""
	if out, _, _ := runIn(c, "echo", "ran"); out != "ran\n" {
		t.Errorf("C5e (Go) the core count is the default limit: %q", out)
	}
}

func TestUnderTheLimits(t *testing.T) {
	f := newFixture(t)
	f.setReading(t, "0.5 50")
	c, _ := f.config()
	out, errs, code := runIn(c, "echo", "ran")
	if out != "ran\n" || code != 0 {
		t.Errorf("C7 under the limits the check runs: %q %d", out, code)
	}
	noMatch(t, "C7b without a gate warning", `run-check:`, errs)
	os.Remove(f.reading)
	out, errs, _ = runIn(c, "echo", "ran")
	match(t, "C8 no reading: runs under the lock only and says so", `no load reading, running under the lock only`, errs)
	match(t, "C8b and ran", `ran$`, out)
	left, _ := filepath.Glob(filepath.Join(f.dir, "herdsman-check.*"))
	if len(left) != 0 {
		t.Errorf("C9 no start markers left behind: %v", left)
	}
	// A stale reading (a minute old) is not read; the probes are.
	f.setReading(t, "9.0 50")
	old := time.Now().Add(-2 * time.Minute)
	_ = os.Chtimes(f.reading, old, old)
	c, _ = f.config()
	c.Probes = true
	c.Probe = func() string { return "0.1 80" }
	if out, errs, _ := runIn(c, "echo", "ran"); out != "ran\n" || errs != "" {
		t.Errorf("C7c (Go) a stale reading gives way to the probe: %q %q", out, errs)
	}
}

func TestRunningCheckKeepsTheLockAfterItsParentDies(t *testing.T) {
	f := newFixture(t)
	f.setReading(t, "0.5 50")
	log := filepath.Join(f.dir, "orphan.log")
	outer := helper(t, f.env, "sh", "-c", "sleep 2; echo first >> '"+log+"'")
	if err := outer.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	_ = outer.Process.Kill()
	_ = outer.Wait()
	if out, err := helper(t, f.env, "sh", "-c", "echo second >> '"+log+"'").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	b, _ := os.ReadFile(log)
	if got := strings.Join(strings.Fields(string(b)), " "); got != "first second" {
		t.Errorf("C10 a running check keeps the lock after its run-check process dies: %q", got)
	}
}

func TestRealProbesFeedTheGate(t *testing.T) {
	f := newFixture(t)
	c, _ := f.config()
	c.Probes, c.Probe = true, nil
	c.LoadLimit, c.MinFree, c.GateWait = "10000", 1, 5*time.Second
	out, errs, _ := runIn(c, "echo", "ran")
	if out != "ran\n" {
		t.Errorf("C10b the machine's own reading lets a check run: %q %q", out, errs)
	}
	noMatch(t, "C10c with a reading, not under the lock only", `run-check:`, errs)
}

func TestPublisher(t *testing.T) {
	f := newFixture(t)
	c, _ := f.config()
	c.Probes = true
	c.Probe = MachineReading
	c.PublishSeconds = "2"
	c.PublishEvery = time.Second
	var errb bytes.Buffer
	if code := Publish(c, "1", &errb); code != 0 {
		t.Fatalf("exit %d", code)
	}
	b, _ := os.ReadFile(f.reading)
	match(t, "C11 the publisher writes a reading", `^[0-9.]+ [0-9.]+$`, strings.TrimSpace(string(b)))
	match(t, "C11b and stops by itself", `publisher stopped at [0-9]{2}:[0-9]{2}; sandboxed checks now run without a load gate`, errb.String())

	for _, tc := range []struct{ hours, secs string }{{"", ""}, {"x", ""}, {"1", "y"}} {
		c.PublishSeconds = tc.secs
		errb.Reset()
		if code := Publish(c, tc.hours, &errb); code != ExitUsage || !strings.Contains(errb.String(), "usage: herdsman run-check --publish <hours>") {
			t.Errorf("C11c (Go) --publish %q %q: %d %q", tc.hours, tc.secs, code, errb.String())
		}
	}
	// An hour count with no seconds setting, and a probe with nothing to say.
	c.PublishSeconds = ""
	c.Probe = func() string { return "" }
	clock := time.Now()
	c.Now = func() time.Time { return clock }
	c.Sleep = func(d time.Duration) { clock = clock.Add(30 * time.Minute) }
	errb.Reset()
	if code := Publish(c, "1", &errb); code != 0 {
		t.Errorf("publish for an hour: %d", code)
	}
}

func TestMarkersAndLockErrors(t *testing.T) {
	f := newFixture(t)
	f.setReading(t, "0.5 50")
	// A lock that cannot be opened.
	c, _ := f.config()
	c.Lock = filepath.Join(f.dir, "no-such-dir", "x.lock")
	if _, errs, code := runIn(c, "echo", "x"); code != ExitNoLock || !strings.Contains(errs, "NOT RUN (cannot open the lock") {
		t.Errorf("an unopenable lock: %d %q", code, errs)
	}
	// A lock folder where the start marker cannot be written.
	if os.Geteuid() != 0 {
		ro := filepath.Join(f.dir, "ro")
		_ = os.Mkdir(ro, 0o755)
		lock := filepath.Join(ro, "checks.lock")
		_ = os.WriteFile(lock, nil, 0o666)
		_ = os.Chmod(ro, 0o555)
		defer os.Chmod(ro, 0o755)
		c.Lock = lock
		if _, errs, code := runIn(c, "echo", "x"); code != ExitMarker || !strings.Contains(errs, "NOT RUN (cannot write the start marker)") {
			t.Errorf("an unwritable marker: %d %q", code, errs)
		}
	}
	// Started under the lock by the script (HERDSMAN_CHECK=locked): gate, then run.
	c, _ = f.config()
	c.Check = "locked"
	c.CheckIn, c.CheckRan = filepath.Join(f.dir, "m.in"), filepath.Join(f.dir, "m.ran")
	if out, _, code := runIn(c, "echo", "ran"); code != 0 || out != "ran\n" || !exists(c.CheckRan) {
		t.Errorf("a run under the script's lock: %d %q", code, out)
	}
	c.CheckIn = ""
	if _, _, code := runIn(c, "echo", "ran"); code != ExitMarker {
		t.Errorf("no marker path: %d", code)
	}
	c, _ = f.config()
	c.Check = "gated"
	if _, _, code := runIn(c); code != ExitUsage {
		t.Errorf("a gated run without a command: %d", code)
	}
}

// The script and the binary take the same lock: either one holding it makes
// the other wait.
func TestScriptAndBinaryExcludeEachOther(t *testing.T) {
	script, _ := filepath.Abs("../../skills/herdsman/scripts/run-check.sh")
	if _, err := os.Stat(script); err != nil {
		t.Skip("no run-check.sh")
	}
	_, lockf := exec.LookPath("lockf")
	_, flock := exec.LookPath("flock")
	if lockf != nil && flock != nil {
		t.Skip("neither lockf nor flock")
	}
	f := newFixture(t)
	f.setReading(t, "0.5 50")
	// The script holds it.
	holder := exec.Command("/bin/bash", script, "sleep", "3")
	holder.Env = append(withoutCheck(os.Environ()), f.env...)
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	out, err := helper(t, append(f.env, "HERDSMAN_LOCK_WAIT=1"), "echo", "ran").CombinedOutput()
	_ = holder.Wait()
	match(t, "X1 the binary waits for a lock the script holds", `NOT RUN \(no lock within 1s\)`, string(out))
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != ExitTempFail {
		t.Errorf("X1b exit 75: %v", err)
	}
	// The binary holds it.
	bin := helper(t, f.env, "sleep", "3")
	if err := bin.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	cmd := exec.Command("/bin/bash", script, "echo", "ran")
	cmd.Env = append(withoutCheck(os.Environ()), append(f.env, "HERDSMAN_LOCK_WAIT=1")...)
	out, _ = cmd.CombinedOutput()
	_ = bin.Wait()
	match(t, "X2 the script waits for a lock the binary holds", `NOT RUN \(no lock within 1s\)`, string(out))
	noMatch(t, "X2b and does not run", `^ran$`, string(out))
}

func TestFromEnvDefaults(t *testing.T) {
	c := FromEnv(func(k string) string {
		if k == "HERDSMAN_LOCK_WAIT" || k == "HERDSMAN_NICE" {
			return "x"
		}
		return ""
	})
	if c.Lock != "/tmp/herdsman-checks.lock" || c.Reading != "/tmp/herdsman-load" || c.LockWait != 5400*time.Second ||
		c.GateWait != 600*time.Second || c.MinFree != 10 || c.Nice != 10 || c.Poll != 15*time.Second || !c.Probes || c.PublishEvery != 15*time.Second {
		t.Fatalf("defaults = %+v", c)
	}
}

func TestTryLockOnAClosedFile(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "lock"))
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if got, err := tryLock(f); got || err == nil {
		t.Errorf("a closed file locks: %v %v", got, err)
	}
}

// R3 sweep (b): the numbers run-check.sh compares with [ -ge ] or reads in
// $(( )) are read as bash reads them, and GATE_WAIT is printed as written.
func TestShellNumbers(t *testing.T) {
	f := newFixture(t)
	f.env = append(f.env, "HERDSMAN_GATE_WAIT= 1")
	f.setReading(t, "9.0 50")
	c, _ := f.config()
	_, errs, code := runIn(c, "echo", "ran")
	match(t, "a padded GATE_WAIT gives up, printed as written", `NOT RUN \(load gate gave up after  1s: load 9.0`, errs)
	if code != ExitTempFail {
		t.Errorf("exit %d", code)
	}
	// GATE_WAIT that test refuses: the gate never gives up (here the load
	// falls after a few polls).
	f = newFixture(t)
	f.env = append(f.env, "HERDSMAN_GATE_WAIT=x")
	f.setReading(t, "9.0 50")
	c, _ = f.config()
	polls := 0
	c.Sleep = func(time.Duration) {
		if polls++; polls == 5 {
			f.setReading(t, "0.5 50")
		}
	}
	out, _, code := run(c, "echo", "ran")
	if code != 0 || !strings.Contains(out, "ran") || polls < 5 {
		t.Errorf("GATE_WAIT=x: %d %q after %d polls", code, out, polls)
	}
	// MIN_FREE that test refuses: no reading is ever good enough.
	f = newFixture(t)
	f.env = append(f.env, "HERDSMAN_MIN_FREE=x", "HERDSMAN_GATE_WAIT=1")
	f.setReading(t, "0.5 50")
	c, _ = f.config()
	_, errs, _ = runIn(c, "echo", "ran")
	match(t, "MIN_FREE=x fails the gate", `NOT RUN \(load gate gave up`, errs)
	f = newFixture(t)
	f.env = append(f.env, "HERDSMAN_MIN_FREE= 060", "HERDSMAN_GATE_WAIT=1")
	f.setReading(t, "0.5 50")
	c, _ = f.config()
	_, errs, _ = runIn(c, "echo", "ran")
	match(t, "MIN_FREE= 060 is 60 (decimal in test)", `NOT RUN \(load gate gave up`, errs)
	// $(( )): blanks, octal and hexadecimal.
	for _, tc := range []struct{ hours, secs string }{{"1", " 0"}, {"1", "00"}, {"1", "0x0"}} {
		c.PublishSeconds = tc.secs
		var errb bytes.Buffer
		if code := Publish(c, tc.hours, &errb); code != 0 {
			t.Errorf("--publish %q with %q seconds: %d %q", tc.hours, tc.secs, code, errb.String())
		}
	}
	c.PublishSeconds = ""
	clock := time.Now()
	end := clock
	c.Now = func() time.Time { return clock }
	c.Sleep = func(d time.Duration) { clock = clock.Add(d) }
	c.Probe = func() string { return "" }
	var errb bytes.Buffer
	if code := Publish(c, " 01 ", &errb); code != 0 || clock.Sub(end) < time.Hour || clock.Sub(end) > time.Hour+time.Minute {
		t.Errorf("--publish \" 01 \": %d, ran %v", code, clock.Sub(end))
	}
}

// run is runIn with the config's own clock and sleep.
func run(c Config, args ...string) (string, string, int) {
	var out, errb bytes.Buffer
	code := Run(c, args, nil, &out, &errb)
	return out.String(), errb.String(), code
}

func TestGateWaitTextWithoutTheEnvironment(t *testing.T) {
	c := Config{GateWait: 2 * time.Second}
	if c.gateWaitText() != "2" {
		t.Errorf("%q", c.gateWaitText())
	}
	if testSecs("9223372036854775807") != math.MaxInt64 {
		t.Error("a GATE_WAIT past what a duration holds")
	}
}
