package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/ops"
)

// fileSentinel creates the file named by a step's spec.
type fileSentinel struct{}

func (fileSentinel) Apply(_ context.Context, s ops.Step) error {
	return os.WriteFile(s.Spec, []byte("touched"), 0o600)
}

func (fileSentinel) Probe(_ context.Context, s ops.Step) (ops.Verdict, string, error) {
	if _, err := os.Lstat(s.Spec); errors.Is(err, os.ErrNotExist) {
		return ops.Absent, "", nil
	}
	return ops.Matches, "", nil
}

// signalSentinel terminates the process whose id is the step's spec.
type signalSentinel struct{}

func (signalSentinel) Apply(_ context.Context, s ops.Step) error {
	pid, err := strconv.Atoi(s.Spec)
	if err != nil {
		return err
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

func (signalSentinel) Probe(context.Context, ops.Step) (ops.Verdict, string, error) {
	return ops.Absent, "", nil
}

func sentinelEffects() map[string]ops.Effect {
	return map[string]ops.Effect{"touch": fileSentinel{}, "signal": signalSentinel{}}
}

// leaveIntended journals an operation that stops as a crash would, right
// after its step was recorded as intended.
func leaveIntended(t *testing.T, e *ops.Engine, stop *string, s ops.Spec) {
	t.Helper()
	ctx := context.Background()
	op, err := e.Submit(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	*stop = "step:0:intended"
	defer func() {
		*stop = ""
		if p := recover(); p == nil {
			t.Fatal("the step ran past its intent")
		}
	}()
	_, _ = e.Drive(ctx, op.ID)
}

// sentinelState builds a state directory holding two intended steps.
func sentinelState(t *testing.T) (root, sentinel string, child *exec.Cmd) {
	t.Helper()
	base := shortDir(t)
	root = filepath.Join(base, "a")
	if err := os.MkdirAll(filepath.Join(root, "daemon"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel = filepath.Join(base, "sentinel")
	child = exec.Command("/bin/sleep", "120")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := make(chan struct{})
	go func() { _ = child.Wait(); close(reaped) }()
	t.Cleanup(func() { _ = child.Process.Kill(); <-reaped })
	db, err := store.Open(filepath.Join(root, "daemon", "account.db"), store.Writer, store.KindAccount)
	if err != nil {
		t.Fatal(err)
	}
	stop := ""
	e := ops.NewEngine(db, ops.Options{Effects: sentinelEffects(), Fault: func(p string) {
		if p == stop {
			panic("crash")
		}
	}})
	leaveIntended(t, e, &stop, ops.Spec{Kind: "sentinel-file", Plan: "create a file", Locks: []string{ops.TaskLock("one")},
		Steps: []ops.StepSpec{{Name: "touch sentinel", Effect: "touch", Key: "file-key", Spec: sentinel}}})
	leaveIntended(t, e, &stop, ops.Spec{Kind: "sentinel-signal", Plan: "signal a process", Locks: []string{ops.TaskLock("two")},
		Steps: []ops.StepSpec{{Name: "signal child", Effect: "signal", Key: "pid-key", Spec: strconv.Itoa(child.Process.Pid), Retry: ops.RetryIdempotent}}})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return root, sentinel, child
}

func copyState(t *testing.T, from string) string {
	t.Helper()
	to := filepath.Join(filepath.Dir(from), "b")
	err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		target := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	return to
}

// serving starts a daemon on root and stops it when the returned func is called.
func serving(t *testing.T, root string, inspect bool) (*Client, func()) {
	t.Helper()
	bin := filepath.Join(filepath.Dir(root), "bin")
	_ = os.WriteFile(bin, []byte("x"), 0o700)
	d := New(Config{PrivateHome: root, Home: root, Version: testVersion, Executable: bin, Log: func(s string) { t.Log(s) },
		Inspect: inspect, Effects: sentinelEffects()})
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- d.Serve(ctx) }()
	c := NewClient(SocketPath(root), testVersion)
	var once bool
	return c, func() {
		if once {
			return
		}
		once = true
		c.Close()
		cancel()
		if err := <-served; err != nil {
			t.Error(err)
		}
	}
}

func procExists(c *exec.Cmd) bool { return c.Process.Signal(syscall.Signal(0)) == nil }

func TestSentinelGateInspectChangesNothing(t *testing.T) {
	orig, sentinel, child := sentinelState(t)
	root := copyState(t, orig)
	ctx := context.Background()

	c, stop := serving(t, root, true)
	defer stop()
	check := func(when string) {
		t.Helper()
		time.Sleep(50 * time.Millisecond)
		if _, err := os.Lstat(sentinel); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: the sentinel file was created (%v)", when, err)
		}
		if !procExists(child) {
			t.Fatalf("%s: the sentinel process was signalled", when)
		}
	}
	check("after start")

	var st StatusDetail
	if _, err := c.Do(ctx, "GET", "/v1/status", nil, &st); err != nil {
		t.Fatal(err)
	}
	if !st.Inspect || st.Schema.Current != store.Newest(store.KindAccount) || st.Schema.Expected != st.Schema.Current || st.StateDir != root {
		t.Fatalf("status %+v", st)
	}
	var l OpsList
	if _, err := c.Do(ctx, "GET", "/v1/ops", nil, &l); err != nil {
		t.Fatal(err)
	}
	if len(l.Operations) != 2 {
		t.Fatalf("listing: %+v", l)
	}
	for _, op := range l.Operations {
		if op.State != ops.OpRunning || op.Steps[0].State != ops.StepIntended {
			t.Fatalf("inspect changed the journal: %+v", op)
		}
	}
	check("after the listing")

	_, err := c.Do(ctx, "POST", "/v1/ops/"+l.Operations[0].ID+"/steps/0/resolve", ResolveRequest{Decision: "done"}, nil)
	e, ok := AsAPIError(err)
	if !ok || e.Code != "failed_precondition" || !strings.Contains(e.Message, "inspect mode") {
		t.Fatalf("a mutating request answered %v", err)
	}
	check("after a refused change")
	stop()

	// The control: the same journal without --inspect does fire, so the
	// checks above can fail.
	c2, stop2 := serving(t, root, false)
	defer stop2()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := os.Lstat(sentinel)
		if err == nil && !procRunning(child) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recovery did not run: %v alive=%v", err, procExists(child))
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = c2
}

// procRunning tells a process that is gone or a zombie from a running one.
func procRunning(c *exec.Cmd) bool {
	if !procExists(c) {
		return false
	}
	out, err := exec.Command("/bin/ps", "-o", "stat=", "-p", strconv.Itoa(c.Process.Pid)).Output()
	return err == nil && !strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}

func TestInspectHoldsRoutesAndStaysOK(t *testing.T) {
	root := filepath.Join(shortDir(t), "s")
	if err := os.MkdirAll(filepath.Join(root, "daemon"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(root, 0o700)
	inbox := filepath.Join(root, "inbox")
	if err := os.MkdirAll(filepath.Join(inbox, "worker", "ready"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, "worker", "ready", "m000000000001.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(RoutesFile{Version: 1, Routes: map[string]RouteEntry{"alpha": {Inbox: inbox, Repo: "r", HerdrSocket: "/nonexistent.sock", Created: 5}}})
	if err := os.WriteFile(filepath.Join(root, "daemon", "routes.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	c, stop := serving(t, root, true)
	defer stop()
	ctx := context.Background()
	var h Health
	if _, err := c.Do(ctx, "GET", "/v1/health", nil, &h); err != nil {
		t.Fatal(err)
	}
	if h.Status != "ok" || len(h.Routes) != 1 || h.Routes[0].Status != heldInspect || len(h.Herdr) != 0 {
		t.Fatalf("health %+v", h)
	}
	var st StatusDetail
	if _, err := c.Do(ctx, "GET", "/v1/status", nil, &st); err != nil {
		t.Fatal(err)
	}
	if st.Counts.Routes != 1 || st.Counts.Messages != 1 || st.Counts.Tasks != 0 || len(st.Digest) != 64 {
		t.Fatalf("status %+v", st)
	}
	if _, err := os.Lstat(filepath.Join(inbox, ".route.lock")); err == nil {
		t.Fatal("inspect took the route's lock")
	}
}

func runCLI(t *testing.T, env map[string]string, signals <-chan os.Signal, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	a := &AdminEnv{Getenv: func(k string) string { return env[k] }, Stdin: strings.NewReader(""), Stdout: &out, Stderr: io.Discard,
		Version: testVersion, Executable: "/bin/true", Signals: signals}
	code := RunDaemon(a, args)
	return code, out.String()
}

func TestTwoDaemonsRunSideBySide(t *testing.T) {
	base := shortDir(t)
	roots := []string{filepath.Join(base, "one"), filepath.Join(base, "two")}
	var stops []chan os.Signal
	var done []chan int
	for _, root := range roots {
		env := map[string]string{"HERDSMAN_STATE_DIR": root}
		sig := make(chan os.Signal, 1)
		ch := make(chan int, 1)
		stops, done = append(stops, sig), append(done, ch)
		go func() {
			var out bytes.Buffer
			a := &AdminEnv{Getenv: func(k string) string { return env[k] }, Stdin: strings.NewReader(""), Stdout: &out, Stderr: io.Discard,
				Version: testVersion, Executable: "/bin/true", Signals: sig}
			ch <- RunDaemon(a, []string{"run"})
		}()
	}
	defer func() {
		for i := range stops {
			stops[i] <- syscall.SIGTERM
			if code := <-done[i]; code != 0 {
				t.Errorf("daemon %d exited %d", i, code)
			}
		}
	}()
	for _, root := range roots {
		waitFor(t, SocketPath(root))
		code, out := runCLI(t, map[string]string{"HERDSMAN_STATE_DIR": root}, nil, "--json", "status")
		if code != 0 {
			t.Fatalf("status %d: %s", code, out)
		}
		var got struct {
			StateDir       string       `json:"state_dir"`
			SupervisorFile string       `json:"supervisor_file"`
			Inspect        bool         `json:"inspect"`
			Schema         SchemaStatus `json:"schema"`
			Counts         StatusCounts `json:"counts"`
			Digest         string       `json:"digest"`
			Daemon         AccountLock  `json:"daemon"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatal(err)
		}
		if got.StateDir != root || got.Inspect || got.Schema.Current == 0 || got.Schema.Current != got.Schema.Expected ||
			got.SupervisorFile == "" || got.Digest == "" || got.Daemon.PrivateHome != root {
			t.Fatalf("status %+v", got)
		}
		if _, err := os.Lstat(filepath.Join(root, ".herdsman-daemon.lock")); err != nil {
			t.Fatalf("the lock is not inside the state directory: %v", err)
		}
	}
}

func waitFor(t *testing.T, p string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Lstat(p); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not appear", p)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOneStateDirectoryHasOneDaemon(t *testing.T) {
	base := shortDir(t)
	root := filepath.Join(base, "s")
	home := filepath.Join(base, "h")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	first := New(Config{PrivateHome: root, Home: home, Version: testVersion, Log: func(string) {}})
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	defer first.Stop("stop")
	// The same folder selected through the override has another account lock.
	second := New(Config{PrivateHome: root, Home: root, Version: testVersion, Log: func(string) {}})
	var running *ErrRunning
	if err := second.Start(); !errors.As(err, &running) || running.Holder.PID != os.Getpid() {
		t.Fatalf("second daemon on one state directory: %v", err)
	}
	if _, err := os.Lstat(SocketPath(root)); err != nil {
		t.Fatalf("the first daemon's socket: %v", err)
	}
}

func TestOverriddenStateDirRefusesTheSupervisor(t *testing.T) {
	for _, cmd := range []string{"install", "uninstall", "start", "stop", "restart"} {
		code, _ := runCLI(t, map[string]string{"HERDSMAN_STATE_DIR": "/tmp/x"}, nil, cmd)
		if code != 2 {
			t.Fatalf("%s exited %d", cmd, code)
		}
	}
}

func TestRunRefusesAnUnknownFlag(t *testing.T) {
	if code, _ := runCLI(t, map[string]string{"HERDSMAN_STATE_DIR": "/tmp/x"}, nil, "run", "--bogus"); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

var opIDRe = regexp.MustCompile(`\b[0-9A-HJKMNP-TV-Z]{26}\b`)

func TestOpsCLIGolden(t *testing.T) {
	orig, _, _ := sentinelState(t)
	root := copyState(t, orig)
	_, stop := serving(t, root, true)
	defer stop()
	env := map[string]string{"HERDSMAN_STATE_DIR": root}
	code, list := runCLI(t, env, nil, "ops", "list")
	if code != 0 {
		t.Fatalf("list %d: %s", code, list)
	}
	lines := strings.SplitAfter(strings.TrimSpace(list), "\n")
	sort.Slice(lines, func(i, j int) bool { return strings.Fields(lines[i])[2] < strings.Fields(lines[j])[2] }) // two operations may share a millisecond
	list = strings.Join([]string{strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1]), ""}, "\n")
	id := strings.Fields(lines[1])[0]
	code, show := runCLI(t, env, nil, "ops", "show", id)
	if code != 0 {
		t.Fatalf("show %d: %s", code, show)
	}
	got := opIDRe.ReplaceAllString(list+"--\n"+show, "<id>")
	want, err := os.ReadFile("testdata/ops-cli.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if code, _ := runCLI(t, env, nil, "ops"); code != 2 {
		t.Fatalf("bare ops exited %d", code)
	}
	if code, _ := runCLI(t, env, nil, "ops", "show", "missing"); code == 0 {
		t.Fatal("a missing operation succeeded")
	}
	if code, _ := runCLI(t, env, nil, "ops", "resolve", id, "0", "done"); code != ExitCode("failed_precondition") {
		t.Fatalf("resolve under inspect exited %d", code)
	}
}

func TestResolveThroughTheDaemon(t *testing.T) {
	root := filepath.Join(shortDir(t), "s")
	if err := os.MkdirAll(filepath.Join(root, "daemon"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(root, 0o700)
	db, err := store.Open(filepath.Join(root, "daemon", "account.db"), store.Writer, store.KindAccount)
	if err != nil {
		t.Fatal(err)
	}
	stopAt := ""
	e := ops.NewEngine(db, ops.Options{Effects: map[string]ops.Effect{"touch": fileSentinel{}}, Fault: func(p string) {
		if p == stopAt {
			panic("crash")
		}
	}})
	sentinel := filepath.Join(root, "made")
	leaveIntended(t, e, &stopAt, ops.Spec{Kind: "k", Plan: "p", Steps: []ops.StepSpec{{Name: "n", Effect: "touch", Key: "x", Spec: sentinel}}})
	_ = db.Close()

	// A binary that lacks the step's effect stops the operation for a person.
	bin := filepath.Join(filepath.Dir(root), "bin")
	_ = os.WriteFile(bin, []byte("x"), 0o700)
	d := New(Config{PrivateHome: root, Home: root, Version: testVersion, Executable: bin, Log: func(string) {}, Effects: map[string]ops.Effect{}})
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- d.Serve(ctx) }()
	defer func() { cancel(); <-served }()
	c := NewClient(SocketPath(root), testVersion)
	defer c.Close()
	var l OpsList
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := c.Do(ctx, "GET", "/v1/ops", nil, &l); err != nil {
			t.Fatal(err)
		}
		if len(l.Operations) == 1 && l.Operations[0].State == ops.OpStopped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not stopped: %+v", l.Operations)
		}
		time.Sleep(10 * time.Millisecond)
	}
	id := l.Operations[0].ID
	if _, err := c.Do(ctx, "POST", "/v1/ops/"+id+"/steps/0/resolve", ResolveRequest{Decision: "sideways"}, nil); err == nil {
		t.Fatal("a bad decision was accepted")
	}
	if _, err := c.Do(ctx, "POST", "/v1/ops/"+id+"/steps/x/resolve", ResolveRequest{Decision: "done"}, nil); err == nil {
		t.Fatal("a bad step was accepted")
	}
	if _, err := c.Do(ctx, "POST", "/v1/ops/missing/steps/0/resolve", ResolveRequest{Decision: "done"}, nil); err == nil {
		t.Fatal("a missing operation was accepted")
	}
	var op ops.Operation
	if _, err := c.Do(ctx, "POST", "/v1/ops/"+id+"/steps/0/resolve", ResolveRequest{Decision: "done"}, &op); err != nil {
		t.Fatal(err)
	}
	for {
		var got ops.Operation
		if _, err := c.Do(ctx, "GET", "/v1/ops/"+id, nil, &got); err != nil {
			t.Fatal(err)
		}
		if got.State == ops.OpDone {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("not resumed: %+v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
