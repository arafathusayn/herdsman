package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/runtime"
)

// A task launch on core is admitted when the daemon runs core, starts the
// agent as a core execution in the account store, and issues the prompt as a
// task notice; nothing is typed.
func TestCoreTaskLaunchThroughTheDaemon(t *testing.T) {
	h, w := newCoreHarness(t)
	ctx := context.Background()
	w.OpenApp()
	h.setDefaultBackend(runtime.BackendCore)
	repo := filepath.Join(h.ph, "proj")
	wt := filepath.Join(h.ph, "proj-fix-login")
	for _, dir := range []string{repo, wt} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.d.taskBackend(ctx, repo); err != nil {
		t.Fatalf("a core task launch was refused: %v", err)
	}
	if !h.d.taskOnCore(ctx, repo) {
		t.Fatal("the task does not run on core")
	}
	got, err := h.d.coreTask(ctx, route.TaskLaunch{Repo: repo, Path: wt, Task: "fix-login", Branch: "feat/fix-login", Command: []string{"claude"}, Prompt: "fix it"})
	if err != nil {
		t.Fatal(err)
	}
	execs, err := h.d.coreRT().records.List(ctx, repo, "")
	if err != nil || len(execs) != 1 || execs[0].ID != got.Execution.ID || execs[0].Role != "task-agent" {
		t.Fatalf("the account store's records: %+v %v", execs, err)
	}
	n, ok, err := h.d.noticeSvc().ByKey("task:" + repo + ":fix-login")
	if err != nil || !ok || n.Kind != notice.KindTask || n.ID != got.Notice {
		t.Fatalf("the task notice: %+v %v %v", n, ok, err)
	}
	if c := w.Core.Calls("terminal.send") + w.Core.Calls("terminal.keys"); c != 0 {
		t.Errorf("the task launch typed %d times", c)
	}
}

func TestCoreTaskCommandPreservesQuotingAndPrelude(t *testing.T) {
	s, repo := shareRepo(t, "")
	s.OnCore = func(context.Context, string) bool { return true }
	var got route.TaskLaunch
	s.CoreTask = func(_ context.Context, q route.TaskLaunch) (route.TaskStarted, error) {
		got = q
		return route.TaskStarted{}, nil
	}
	_, err := s.New(context.Background(), WorktreeNewRequest{Dir: repo, Task: "quoted", Base: "main", NoSetup: true,
		Agent: `printf '%s' 'prepare this' && EXAMPLE='two words' codex --no-daemon -c 'model="example model"'`})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex", "--no-daemon", "-c", `model="example model"`}
	if !slices.Equal(got.Command, want) || got.Env["EXAMPLE"] != "two words" || len(got.Prelude) != 1 || got.Prelude[0].Command.Argv[2] != "prepare this" {
		t.Fatalf("task command lost its structure: %+v", got)
	}
}
