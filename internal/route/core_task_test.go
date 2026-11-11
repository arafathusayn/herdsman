package route_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

// A task agent on core is a core execution started through the launch
// transaction; its prompt is a task notice and nothing is typed.
func TestCoreTaskLaunchIssuesItsPromptAsANotice(t *testing.T) {
	r := newCoreRig(t)
	r.w.OpenApp()
	ctx := context.Background()
	wt := filepath.Join(r.repo, "wt-fix-login")
	if err := os.MkdirAll(wt, 0o700); err != nil {
		t.Fatal(err)
	}
	q := route.TaskLaunch{Repo: r.repo, Path: wt, Task: "fix-login", Branch: "feat/fix-login", Command: []string{"claude"},
		Prompt: "fix the login redirect", StateDir: t.TempDir(), Core: r.request().Core}
	cp := r.w.NewProvider(r.w.Core.Socket)
	got, err := route.LaunchTask(ctx, cp, q)
	if err != nil {
		t.Fatal(err)
	}
	if got.Waiting != "" {
		t.Fatalf("the task waits on %s", got.Waiting)
	}
	e := got.Execution
	if e.Backend != runtime.BackendCore || e.Agent != "fix-login" || e.Role != "task-agent" || e.Evidence.PID == 0 {
		t.Fatalf("the task's execution: %+v", e)
	}
	n, ok, err := r.w.Notices.ByKey("task:task:" + r.repo + ":fix-login")
	if err != nil || !ok || n.ID != got.Notice || n.Kind != notice.KindTask || n.Execution != e.ID {
		t.Fatalf("the task notice: %+v %v %v", n, ok, err)
	}
	env := r.w.ExecEnv("task:" + r.repo + ":fix-login")
	for _, want := range []string{"HERDSMAN_TASK=fix-login", "HERDSMAN_BRANCH=feat/fix-login", "HERDSMAN_WORKTREE_PATH=" + wt} {
		if !slices.Contains(env, want) {
			t.Errorf("the harness's environment lacks %s: %v", want, env)
		}
	}
	started := false
	for _, ev := range r.w.EventsOf(notice.EventNeedsAttention) {
		if ev.Reason == core.AttentionStartLine && ev.Execution == e.ID {
			started = true
		}
	}
	if !started {
		t.Error("no needs_attention start_line for the task agent")
	}
	if len(got.Steps) == 0 || got.Steps[0].Reason != core.AttentionStartLine {
		t.Errorf("the person steps: %+v", got.Steps)
	}
	// A retry is the same launch: one execution, one notice.
	again, err := route.LaunchTask(ctx, cp, q)
	if err != nil {
		t.Fatal(err)
	}
	if again.Execution.ID != e.ID || again.Notice != got.Notice {
		t.Errorf("a retry launched again: %s/%s, was %s/%s", again.Execution.ID, again.Notice, e.ID, got.Notice)
	}
	if n := r.w.Core.Calls("terminal.send") + r.w.Core.Calls("terminal.keys"); n != 0 {
		t.Errorf("the task launch typed %d times", n)
	}
}
