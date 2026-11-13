package route

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/launch"
	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/profile"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

// TaskLaunch asks for one task agent on herdr-core: a harness started in the
// task's worktree as a core execution, through the same launch transaction
// as a route's coordinators (spec section 8), with its prompt as a notice.
type TaskLaunch struct {
	// Repo is the main checkout; Path the task's worktree.
	Repo, Path string
	// Task and Branch name the task; they are the agent's environment.
	Task, Branch string
	// Command is the harness's words; its first names the kind.
	Command         []string
	Env             map[string]string
	Prelude         []launch.Step
	RestrictedRoots *profile.Roots
	// Prompt is the task's first instruction, issued as a task notice.
	Prompt string
	// StateDir is where the launch's step markers go.
	StateDir string
	Core     *CoreLaunch
}

// TaskStarted is a task agent's launch on core: its execution, the notice
// that holds its prompt, and what waits on a person.
type TaskStarted struct {
	Execution runtime.Execution
	Notice    string
	// Waiting is the refusal code of a launch step that waits on a person
	// (the app's visibility, a dialog); empty when the agent is ready.
	Waiting string
	// Steps are the agent's steps that need a person, the start line first.
	Steps []core.PersonStep
}

// LaunchTask starts a task agent on herdr-core. Nothing is typed: the
// start line is a person's (needs_attention start_line), the prompt is a
// task notice the agent claims, and a step that waits on a person is
// reported rather than skipped. A retry with the same task reuses the
// execution its intent reserved.
func LaunchTask(ctx context.Context, cp *core.Provider, q TaskLaunch) (TaskStarted, error) {
	var out TaskStarted
	if q.Core == nil || len(q.Command) == 0 {
		return out, errors.New("a task launch on herdr-core needs the daemon's core launch and a command")
	}
	kind := filepath.Base(q.Command[0])
	a, err := core.AdapterFor(kind)
	if err != nil {
		return out, err
	}
	if err := a.CheckArgs(q.Command[1:]); err != nil {
		return out, err
	}
	intent, exec, err := taskAttempt(ctx, cp, q)
	if err != nil {
		return out, err
	}

	stepsDir, err := launch.StepsDir(q.StateDir, "task-"+strings.ReplaceAll(q.Task, "/", "-")+"-"+exec)
	if err != nil {
		return out, err
	}
	d := launch.CoreDescriptor{
		Intent: intent, Execution: exec, Kind: kind, Harness: append([]string(nil), q.Command...), StepsDir: stepsDir, Cwd: q.Path,
		Env:   mergeEnv(q.Env, map[string]string{"HERDSMAN_TASK": q.Task, "HERDSMAN_BRANCH": q.Branch, "HERDSMAN_WORKTREE_PATH": q.Path}),
		Login: q.Core.Login, LoginFrom: q.Core.LoginFrom, NoticeSocket: q.Core.NoticeSocket,
	}
	for i, step := range q.Prelude {
		d.Prelude = append(d.Prelude, launch.NumberedStep{N: i + 1, Step: step})
	}

	if q.RestrictedRoots != nil {
		if err := profile.CheckCoreHarnessArgs(d.Harness[1:]); err != nil {
			return out, err
		}
		d.Sandbox, err = profile.CoreSandboxConfig(kind, *q.RestrictedRoots, q.Core.NoticeSocket)
		if err != nil {
			return out, err
		}
	}

	scope := "task-" + q.Task
	l := core.Launch{
		CoreSocket: q.Core.Socket, CoreBinary: q.Core.Endpoint.Binary,
		Intent: intent, Execution: exec, Owner: q.Core.Owner, RouteID: core.UUIDOf(q.Core.Owner, q.Repo, scope), AgentID: core.UUIDOf(q.Core.Owner, q.Repo, scope, q.Task),
		Repo: q.Repo, AgentName: q.Task, Role: "task-agent", Kind: kind,
		Placement: herdrapp.PlacementIDs{Space: core.OpaqueID(q.Core.Owner, q.Repo, "tasks", "space"), Tab: core.OpaqueID("tab", exec)},
		Name:      core.CoreName(scope, "task", exec),
		Wrapper:   []string{q.Core.Wrapper, "launch", "--socket", q.Core.LaunchSocket, "--intent", intent}, Cwd: q.Path, Descriptor: d,
	}
	c, err := cp.Dial(ctx)
	if err != nil {
		return out, err
	}
	_, err = core.Preflight(ctx, c, core.PreflightInput{Kind: kind, Argv: d.Harness, Roots: slices.Compact([]string{q.Repo, q.Path})})
	c.Close()
	if err != nil {
		return out, err
	}
	if v, _, err := cp.ProbeSpawn(ctx, l); err != nil {
		return out, err
	} else if v != core.ProbeMatches {
		if _, err := cp.SpawnStep(ctx, l); err != nil {
			return out, err
		}
	}
	if v, _, err := cp.ProbeRegister(ctx, l); err != nil {
		return out, err
	} else if v != core.ProbeMatches {
		resolved, err := resolvedLaunch(ctx, cp, l)
		if err != nil {
			return out, err
		}
		if _, err := cp.RegisterStep(ctx, resolved, q.Core.Timeouts); err != nil {
			return out, err
		}
	}
	// Visibility and readiness wait on a person when they refuse: the task
	// stays launched, and the refusal is what the caller reports.
	for _, step := range []func(context.Context, core.Launch, core.Timeouts) (runtime.Execution, error){cp.VisibilityStep, cp.ReadyStep} {
		if _, err := step(ctx, l, q.Core.Timeouts); err != nil {
			code := core.CodeOf(err)
			if code == "" {
				return out, err
			}
			out.Waiting = code
			break
		}
	}
	rec, ok, err := cp.Records.ByIntent(ctx, intent)
	if err != nil {
		return out, err
	}
	if !ok {
		return out, fmt.Errorf("task %s has no execution record for launch %s", q.Task, intent)
	}
	out.Execution, out.Steps = rec, a.PersonSteps(false)
	if cp.Notices == nil {
		return out, errors.New("this daemon has no notice service")
	}
	if err := cp.Notices.Approve(rec, ""); err != nil {
		return out, err
	}
	cp.Notices.Emit(notice.Event{Kind: notice.EventNeedsAttention, Repo: rec.Repo, Route: rec.Route, Execution: rec.ID,
		Reason: core.AttentionStartLine, Line: a.StartLine, Detail: "a person submits task " + q.Task + "'s start line in its terminal"})
	if q.Prompt != "" {
		// A notice journaled under the old doubled key is the same task
		// prompt: a retry finds it there and issues nothing new.
		if n, ok, err := cp.Notices.ByKey(LegacyTaskNoticeKey(q.Repo, q.Task)); err != nil {
			return out, err
		} else if ok && n.Execution == rec.ID {
			out.Notice = n.ID
			return out, nil
		}
		key := intent
		if _, err := cp.Deliver(ctx, runtime.Target{ExecutionID: rec.ID}, runtime.Delivery{Text: q.Prompt, Kind: notice.KindTask, Key: key, Issuer: "task:" + q.Task}); err != nil {
			return out, err
		}
		if n, ok, err := cp.Notices.ByKey(key); err != nil {
			return out, err
		} else if ok {
			out.Notice = n.ID
		}
	}
	return out, nil
}

// TaskNoticeKey is the key of a task agent's prompt notice: the task's
// launch intent, so a retry of the same task issues its prompt once.
func TaskNoticeKey(repo, task string) string { return "task:" + repo + ":" + task }

// LegacyTaskNoticeKey is the doubled form earlier builds journaled
// (task:task:<repo>:<task>). It is read, never written.
func LegacyTaskNoticeKey(repo, task string) string { return "task:" + TaskNoticeKey(repo, task) }

// taskAttempt keeps retries on their reserved execution, but a stopped task
// starts a new generation with its own notice and prelude markers.
func taskAttempt(ctx context.Context, cp *core.Provider, q TaskLaunch) (string, string, error) {
	base := TaskNoticeKey(q.Repo, q.Task)
	rows, err := cp.Records.List(ctx, q.Repo, "")
	if err != nil {
		return "", "", err
	}
	var latest *runtime.Execution
	for i := range rows {
		e := &rows[i]
		if e.Backend == runtime.BackendCore && e.Repo == q.Repo && e.Route == "" && e.Agent == q.Task && e.Role == "task-agent" &&
			(e.LaunchIntent == base || strings.HasPrefix(e.LaunchIntent, base+":attempt:")) && (latest == nil || e.Generation > latest.Generation) {
			latest = e
		}
	}
	if latest == nil {
		return base, runtime.NewExecutionID(), nil
	}
	if latest.Desired == runtime.DesiredRunning {
		return latest.LaunchIntent, latest.ID, nil
	}
	procs := cp.Procs
	if procs == nil {
		procs = core.SystemProcs{}
	}
	alive, err := core.Alive(procs, latest.Evidence)
	if err != nil {
		return "", "", err
	}
	if alive {
		return "", "", errors.New("the task's previous execution is still alive; finish stopping it before starting another")
	}
	return base + ":attempt:" + strconv.FormatInt(latest.Generation+1, 10), runtime.NewExecutionID(), nil
}
