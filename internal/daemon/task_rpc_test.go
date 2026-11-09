package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/wtree"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

func taskRepo(t *testing.T) (repo string, environ []string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo = filepath.Join(root, "work", "repo")
	os.MkdirAll(repo, 0o755)
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "first"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	return repo, []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + root, "XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
		"HERDR_SOCKET_PATH=" + filepath.Join(root, "none.sock"), "CALLER_ONLY=from-caller",
	}
}

func TestNewAndGoThroughTheDaemon(t *testing.T) {
	sdk := dialHarness(t, newHarness(t))
	repo, environ := taskRepo(t)
	ctx := context.Background()
	made, err := sdk.Task.New(ctx, &daemonv1.TaskServiceNewRequest{Dir: repo, Task: "one", NoSetup: true, Environ: environ})
	if err != nil {
		t.Fatal(err)
	}
	if made.GetTask() != "one" || made.GetBranch() != "feat/one" || made.GetSchemaVersion() != worktreeSchemaVersion {
		t.Fatalf("%+v", made)
	}
	if _, err := os.Stat(filepath.Join(made.GetPath(), ".git")); err != nil {
		t.Fatal(err)
	}
	found, err := sdk.Task.Go(ctx, &daemonv1.TaskServiceGoRequest{Dir: repo, Selector: "one", Environ: environ})
	if err != nil || found.GetPath() != made.GetPath() || found.GetCreated() {
		t.Fatalf("%+v %v", found, err)
	}
}

func TestNewRunsHooksInTheCallersEnvironment(t *testing.T) {
	sdk := dialHarness(t, newHarness(t))
	repo, environ := taskRepo(t)
	config := `{"hooks":{"pre-start":"echo \"$CALLER_ONLY\" > seen"}}`
	if err := os.WriteFile(filepath.Join(filepath.Dir(repo), "herdsman.config.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	service := (&WorktreeService{}).forCaller(environ)
	if _, err := service.Hook(ctx, WorktreeHookRequest{Dir: repo, Type: "pre-start", Approve: true}); err != nil {
		t.Fatal(err)
	}
	made, err := sdk.Task.New(ctx, &daemonv1.TaskServiceNewRequest{Dir: repo, Task: "one", NoSetup: true, Environ: environ})
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(made.GetPath(), "seen")); string(data) != "from-caller\n" {
		t.Fatalf("%q", data)
	}
}

func TestNewReportsAFailureAsARefusal(t *testing.T) {
	sdk := dialHarness(t, newHarness(t))
	_, environ := taskRepo(t)
	_, err := sdk.Task.New(context.Background(), &daemonv1.TaskServiceNewRequest{Dir: "relative", Task: "one", Environ: environ})
	var failure *rpc.Error
	if !errors.As(err, &failure) || failure.Code != rpc.FailedPrecondition {
		t.Fatalf("%v", err)
	}
}

func TestLocalTaskServiceServesTheSameCalls(t *testing.T) {
	repo, environ := taskRepo(t)
	made, err := LocalTaskService().New(context.Background(), &daemonv1.TaskServiceNewRequest{Dir: repo, Task: "one", NoSetup: true, Environ: environ})
	if err != nil || made.GetBranch() != "feat/one" {
		t.Fatalf("%+v %v", made, err)
	}
}

func awaitWorktreeRow(t *testing.T, repo, task string) string {
	t.Helper()
	state, err := wtree.OpenState(filepath.Join(repo, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	deadline := time.Now().Add(20 * time.Second)
	for {
		rows, err := state.Worktrees(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Task == task {
				return row.Path
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no record of %s: %+v", task, rows)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A crash after the tree exists and before it is recorded must not strand it:
// the restarted daemon sees the tree and writes the record, once.
func TestACrashBetweenTheTreeAndItsRecordIsFinishedOnRestart(t *testing.T) {
	h := newHarness(t)
	h.stop()
	var crashed atomic.Bool
	h.cfg.OpsFault = func(point string) {
		if strings.HasPrefix(point, "step:") && strings.HasSuffix(point, ":applied") && crashed.CompareAndSwap(false, true) {
			panic("crash after the first effect")
		}
	}
	h.start()
	repo, environ := taskRepo(t)
	sdk := dialHarness(t, h)
	if _, err := sdk.Task.New(context.Background(), &daemonv1.TaskServiceNewRequest{Dir: repo, Task: "one", NoSetup: true, Environ: environ}); err == nil {
		t.Fatal("the crash did not reach the caller")
	}
	if !crashed.Load() {
		t.Fatal("no crash")
	}
	state, err := wtree.OpenState(filepath.Join(repo, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := state.Worktrees(context.Background())
	state.Close()
	if len(rows) != 0 {
		t.Fatalf("recorded before the crash: %+v", rows)
	}
	h.stop()
	h.cfg.OpsFault = nil
	h.start()
	path := awaitWorktreeRow(t, repo, "one")
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		t.Fatal(err)
	}
	listing, err := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain").Output()
	if err != nil || strings.Count(string(listing), path) != 1 {
		t.Fatalf("%s %v", listing, err)
	}
}

func TestTreeProbeTellsAbsentMatchingAndForeign(t *testing.T) {
	repo, environ := taskRepo(t)
	service := (&WorktreeService{}).forCaller(environ)
	r, err := service.open(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	path := filepath.Join(filepath.Dir(repo), "tree")
	spec := treeSpec{Main: r.main, Task: "t", Branch: "feat/t", Path: path}
	check := func(want ops.Verdict) {
		t.Helper()
		if got, _, err := probeTree(context.Background(), r, spec); err != nil || got != want {
			t.Fatalf("%v, want %v, %v", got, want, err)
		}
	}
	check(ops.Absent)
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", "-b", "feat/t", path).CombinedOutput(); err != nil {
		t.Fatalf("%s", out)
	}
	check(ops.Matches)
	spec.Branch = "feat/other"
	check(ops.Different)
	spec.Path = filepath.Join(filepath.Dir(repo), "stray")
	os.Mkdir(spec.Path, 0o755)
	check(ops.Different)
}

func TestTheCallCarriesTheIntegratorRequest(t *testing.T) {
	repo, environ := taskRepo(t)
	made, err := (&taskRPC{service: &WorktreeService{}}).New(context.Background(), &daemonv1.TaskServiceNewRequest{Dir: repo, Task: "one", Integrator: true, Environ: environ})
	if err != nil || !slices.ContainsFunc(made.GetWarnings(), func(line string) bool { return strings.Contains(line, "integrator") }) {
		t.Fatalf("%v %v", made, err)
	}
}

func TestTheProbeKnowsATasksOwnClone(t *testing.T) {
	repo, environ := taskRepo(t)
	service := (&WorktreeService{}).forCaller(environ)
	r, err := service.open(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	path := filepath.Join(t.TempDir(), "clone")
	spec := treeSpec{Main: repo, Task: "one", Branch: "feat/one", Path: path, OwnClone: true}
	check := func(want ops.Verdict) {
		t.Helper()
		if got, detail, err := probeTree(context.Background(), r, spec); err != nil || got != want {
			t.Fatalf("%s %q %v", got, detail, err)
		}
	}
	check(ops.Absent)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	check(ops.Different)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
		cmd.Env = environ
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	git("init", "-q", "-b", "other")
	check(ops.Different)
	git("symbolic-ref", "HEAD", "refs/heads/feat/one")
	check(ops.Matches)
}
