//go:build acceptance

// Package teardown is the real-platform gate for ownership and stopping: real
// processes, real ps and lsof, real signals, a real container when an engine
// and a local image are present.
package teardown

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon"
	"github.com/arafathusayn/herdsman/internal/ledger"
	"github.com/arafathusayn/herdsman/internal/wtree"
)

const modeVar = "HERDSMAN_ACCEPT_MODE"

// TestMain lets the test binary double as the helper processes.
func TestMain(m *testing.M) {
	if mode := os.Getenv(modeVar); mode != "" {
		helper(mode)
		return
	}
	os.Exit(m.Run())
}

func helper(mode string) {
	switch mode {
	case "ignore":
		signal.Ignore(syscall.SIGTERM)
	case "serve":
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			os.Exit(3)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		if err := os.WriteFile(os.Getenv("HERDSMAN_ACCEPT_PORT"), []byte(strconv.Itoa(port)), 0o600); err != nil {
			os.Exit(3)
		}
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				go func() { <-make(chan struct{}); _ = conn }()
			}
		}()
	case "client":
		conn, err := net.Dial("tcp", "127.0.0.1:"+os.Getenv("HERDSMAN_ACCEPT_PORT"))
		if err != nil {
			os.Exit(3)
		}
		defer conn.Close()
	}
	select {}
}

type world struct {
	t       *testing.T
	service *daemon.LedgerService
	common  string
}

type started struct {
	name string
	cmd  *exec.Cmd
}

func newWorld(t *testing.T) *world {
	t.Helper()
	common, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &world{t: t, service: daemon.NewLedgerService(), common: common}
}

func (w *world) worktree(task string) (string, string) {
	w.t.Helper()
	path := filepath.Join(w.common, "wt-"+task)
	if err := os.Mkdir(path, 0o700); err != nil {
		w.t.Fatal(err)
	}
	state, err := wtree.OpenState(w.common)
	if err != nil {
		w.t.Fatal(err)
	}
	defer state.Close()
	if err := state.PutWorktree(context.Background(), wtree.Worktree{Task: task, Branch: task, Path: path}); err != nil {
		w.t.Fatal(err)
	}
	return path, ledger.NewExecution(time.Now())
}

type spawn struct {
	name, mode, dir, execution, port string
	group                            int
}

func (w *world) spawn(s spawn) *started {
	w.t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Dir = s.dir
	cmd.Env = append(os.Environ(), modeVar+"="+s.mode, "HERDSMAN_ACCEPT_PORT="+s.port)
	if s.execution != "" {
		cmd.Env = append(cmd.Env, ledger.EnvExecution+"="+s.execution)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: s.group}
	if err := cmd.Start(); err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() {
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})
	// Reap on exit, so that a stopped helper leaves no zombie for the pid
	// check; the gate reads liveness through processAlive below.
	go func() { _ = cmd.Wait() }()
	return &started{name: s.name, cmd: cmd}
}

func (w *world) record(task, execution string, process *started) {
	w.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := w.service.AddResource(context.Background(), daemon.LedgerRequest{CommonDir: w.common, Task: task},
			ledger.Record{Execution: execution, Kind: ledger.KindProcess, ID: strconv.Itoa(process.cmd.Process.Pid)}, "acceptance")
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("record %s: %v", process.name, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func alive(p *started) bool {
	return syscall.Kill(p.cmd.Process.Pid, 0) == nil
}

func waitFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
			return string(raw)
		}
		if time.Now().After(deadline) {
			t.Fatalf("no file %s", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitDown(t *testing.T, p *started) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for alive(p) {
		if time.Now().After(deadline) {
			t.Errorf("%s is still running", p.name)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestOneTasksTeardownLeavesEverythingElse tears down task A and task B at
// the same moment, over evidence that overlaps on purpose.
func TestOneTasksTeardownLeavesEverythingElse(t *testing.T) {
	w := newWorld(t)
	treeA, execA := w.worktree("task-a")
	treeB, execB := w.worktree("task-b")

	own := w.spawn(spawn{name: "own", mode: "sleep", dir: treeA, execution: execA})
	stubborn := w.spawn(spawn{name: "stubborn", mode: "ignore", dir: treeA, execution: execA})
	mixedOwn := w.spawn(spawn{name: "mixed-own", mode: "sleep", dir: treeA, execution: execA})
	mixedHuman := w.spawn(spawn{name: "mixed-human", mode: "sleep", dir: treeA, group: mixedOwn.cmd.Process.Pid})
	human := w.spawn(spawn{name: "human", mode: "sleep", dir: treeA})
	otherOwn := w.spawn(spawn{name: "other-own", mode: "sleep", dir: treeB, execution: execB})
	overlap := w.spawn(spawn{name: "overlap", mode: "sleep", dir: treeA, execution: execB})

	portFile := filepath.Join(w.common, "port")
	server := w.spawn(spawn{name: "shared-server", mode: "serve", dir: treeA, execution: execA, port: portFile})
	port := waitFile(t, portFile)
	client := w.spawn(spawn{name: "other-client", mode: "client", dir: treeB, execution: execB, port: port})

	for _, process := range []*started{own, stubborn, mixedOwn, server} {
		w.record("task-a", execA, process)
	}
	for _, process := range []*started{otherOwn, overlap} {
		w.record("task-b", execB, process)
	}

	ctx := context.Background()
	requestA := daemon.LedgerRequest{CommonDir: w.common, Task: "task-a"}
	requestB := daemon.LedgerRequest{CommonDir: w.common, Task: "task-b"}
	var planA, planB ledger.Plan
	var err error
	deadline := time.Now().Add(10 * time.Second)
	for {
		planA, err = w.service.Plan(ctx, requestA)
		if err != nil {
			t.Fatal(err)
		}
		if level(planA, server) == ledger.LevelShared || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if planB, err = w.service.Plan(ctx, requestB); err != nil {
		t.Fatal(err)
	}
	if got := level(planA, own); got != ledger.LevelExclusive {
		t.Fatalf("own process is %s", got)
	}
	if got := level(planA, server); got != ledger.LevelShared {
		t.Fatalf("server with an outside client is %s", got)
	}
	if got := level(planB, overlap); got != ledger.LevelShared {
		t.Fatalf("a process of B in A's folder is %s in B's plan", got)
	}

	var wg sync.WaitGroup
	var errA, errB error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errA = w.service.Teardown(ctx, daemon.TeardownRequest{LedgerRequest: requestA, Approved: planA, Grace: time.Second})
	}()
	go func() {
		defer wg.Done()
		_, errB = w.service.Teardown(ctx, daemon.TeardownRequest{LedgerRequest: requestB, Approved: planB, Grace: time.Second})
	}()
	wg.Wait()
	if errA != nil {
		t.Errorf("teardown of A: %v", errA)
	}
	if errB != nil {
		t.Errorf("teardown of B: %v", errB)
	}

	for _, stopped := range []*started{own, stubborn, mixedOwn, otherOwn} {
		waitDown(t, stopped)
	}
	for _, untouched := range []*started{human, mixedHuman, overlap, server, client} {
		if !alive(untouched) {
			t.Errorf("%s was stopped", untouched.name)
		}
	}
	for _, folder := range []string{treeA, treeB} {
		if _, err := os.Stat(folder); err != nil {
			t.Errorf("teardown removed %s: %v", folder, err)
		}
	}
	for _, task := range []string{"task-a", "task-b"} {
		if _, err := os.Stat(filepath.Join(w.common, "herdsman", "logs", task, "teardown.log")); err != nil {
			t.Errorf("no log for %s: %v", task, err)
		}
	}
}

func level(plan ledger.Plan, p *started) ledger.Level {
	want := strconv.Itoa(p.cmd.Process.Pid)
	for _, line := range plan.Lines {
		if line.Kind == ledger.KindProcess && line.ID == want {
			return line.Level
		}
	}
	return "none"
}

// TestAContainerIsStoppedAndKept stops a recorded container and leaves it on
// the machine, stopped.
func TestAContainerIsStoppedAndKept(t *testing.T) {
	// The suite probes the engine and the image first; a platform without them
	// declares docker absent, so nothing here may skip.
	const image = "alpine:3"
	w := newWorld(t)
	tree, execution := w.worktree("task-c")
	name := fmt.Sprintf("herdsman-accept-%d", os.Getpid())
	out, err := exec.Command("docker", "run", "-d", "--name", name, "--label", "herdsman.exec="+execution, "-v", tree+":/w", image, "sleep", "300").Output()
	if err != nil {
		t.Fatal(err)
	}
	id := string(out[:len(out)-1])
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })

	ctx := context.Background()
	request := daemon.LedgerRequest{CommonDir: w.common, Task: "task-c"}
	if _, err := w.service.AddResource(ctx, request, ledger.Record{Execution: execution, Kind: ledger.KindContainer, ID: id}, "acceptance"); err != nil {
		t.Fatal(err)
	}
	plan, err := w.service.Plan(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.service.Teardown(ctx, daemon.TeardownRequest{LedgerRequest: request, Approved: plan, Grace: time.Second}); err != nil {
		t.Fatal(err)
	}
	state, err := exec.Command("docker", "inspect", "-f", "{{.State.Status}}", id).Output()
	if err != nil {
		t.Fatalf("the container was removed: %v", err)
	}
	if got := string(state); got != "exited\n" {
		t.Fatalf("state = %q", got)
	}
}

// TestTheVerbStopsWhatItRecorded drives the real binary in a real repository:
// resource add, teardown -y, and a re-run that finds nothing left to do.
func TestTheVerbStopsWhatItRecorded(t *testing.T) {
	w := newWorld(t)
	binary := filepath.Join(w.common, "herdsman")
	build := exec.Command("go", "build", "-o", binary, "github.com/arafathusayn/herdsman/cmd/herdsman")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	repo := filepath.Join(w.common, "repo")
	worktree := filepath.Join(w.common, "wt-verb")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=a", "-c", "user.email=a@b.c"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	git(repo, "init", "-q", "-b", "main")
	git(repo, "commit", "-q", "--allow-empty", "-m", "start")
	git(repo, "worktree", "add", "-q", "-b", "verb", worktree)
	common := filepath.Join(repo, ".git")
	state, err := wtree.OpenState(common)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.PutWorktree(context.Background(), wtree.Worktree{Task: "verb", Branch: "verb", Path: worktree}); err != nil {
		t.Fatal(err)
	}
	state.Close()
	execution := ledger.NewExecution(time.Now())
	process := w.spawn(spawn{name: "server", mode: "sleep", dir: worktree, execution: execution})
	bystander := w.spawn(spawn{name: "bystander", mode: "sleep", dir: worktree})

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(binary, args...)
		cmd.Dir = worktree
		cmd.Env = append(os.Environ(), ledger.EnvExecution+"="+execution)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("herdsman %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		cmd := exec.Command(binary, "resource", "add", "--kind", "process", "--id", strconv.Itoa(process.cmd.Process.Pid))
		cmd.Dir = worktree
		cmd.Env = append(os.Environ(), ledger.EnvExecution+"="+execution)
		out, err := cmd.CombinedOutput()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("resource add: %v\n%s", err, out)
		}
		time.Sleep(50 * time.Millisecond)
	}
	out := run("teardown", "--grace", "1", "-y")
	t.Log(out)
	waitDown(t, process)
	if !alive(bystander) {
		t.Fatal("the bystander in the worktree was stopped")
	}
	if out := run("teardown", "-y"); !containsLine(out, "nothing to stop") {
		t.Fatalf("second run: %s", out)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("the worktree went away: %v", err)
	}
}

func containsLine(out, want string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}
