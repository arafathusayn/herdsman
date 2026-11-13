package daemon

import (
	"context"
	"errors"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// worktreeRPC serves worktree operations through the daemon-owned runtime service.
// Record operations use taskRPC and are mounted separately.
type worktreeRPC struct {
	daemonv1.TaskService
	service *WorktreeService
	d       *Daemon
}

func newTaskRPC(d *Daemon) *worktreeRPC {
	return &worktreeRPC{service: d.worktrees, d: d}
}

// LocalTaskService serves the contract in the calling process.
func LocalTaskService() daemonv1.TaskService {
	return &worktreeRPC{service: &WorktreeService{}}
}

func refusal(err error) error { return rpc.Errorf(rpc.FailedPrecondition, "%s", err.Error()) }

// New implements TaskService.
func (t *worktreeRPC) New(ctx context.Context, in *daemonv1.TaskServiceNewRequest) (*daemonv1.TaskServiceNewResponse, error) {
	if t.d == nil && in.GetAgent() != "" {
		return nil, refusal(errors.New("agent launch requires a running daemon to select and verify its backend"))
	}
	ctx, service, err := t.admit(ctx)
	if err != nil {
		return nil, err
	}
	made, err := service.forCaller(in.GetEnviron()).New(ctx, WorktreeNewRequest{
		Dir: in.GetDir(), Task: in.GetTask(), Base: in.GetBase(), From: in.GetFrom(), Type: in.GetType(),
		Agent: in.GetAgent(), Prompt: in.GetPrompt(), NoSetup: in.GetNoSetup(), Clobber: in.GetClobber(), Integrator: in.GetIntegrator(),
	})
	if err != nil {
		return nil, refusal(err)
	}
	ports := make([]int32, len(made.Ports))
	for i, port := range made.Ports {
		ports[i] = int32(port)
	}
	return &daemonv1.TaskServiceNewResponse{
		Ports: ports, SchemaVersion: worktreeSchemaVersion, Task: made.Task, Branch: made.Branch, Path: made.Path, Base: made.Base,
		Port: int32(made.Port), Herdr: made.Herdr, Setup: made.Setup, Launch: made.Launch, Logs: made.Logs,
		Warnings: made.Warnings, AgentName: made.AgentName,
	}, nil
}

// Go implements TaskService.
func (t *worktreeRPC) Go(ctx context.Context, in *daemonv1.TaskServiceGoRequest) (*daemonv1.TaskServiceGoResponse, error) {
	ctx, service, err := t.admit(ctx)
	if err != nil {
		return nil, err
	}
	found, err := service.forCaller(in.GetEnviron()).Go(ctx, WorktreeGoRequest{Dir: in.GetDir(), Selector: in.GetSelector(), Focus: in.GetFocus()})
	if err != nil {
		return nil, refusal(err)
	}
	return &daemonv1.TaskServiceGoResponse{
		SchemaVersion: worktreeSchemaVersion, Task: found.Task, Branch: found.Branch, Path: found.Path, Created: found.Created,
		Focused: found.Focused, PushRemote: found.PushRemote, Warnings: found.Warnings,
	}, nil
}

func (t *worktreeRPC) admit(ctx context.Context) (context.Context, *WorktreeService, error) {
	if t.d == nil {
		return ctx, t.service, nil
	}
	return (ledgerRPC{d: t.d, inspect: t.d.cfg.Inspect}).admit()
}
