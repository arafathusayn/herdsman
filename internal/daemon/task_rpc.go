package daemon

import (
	"context"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// taskRPC serves the worktree calls of TaskService. The other calls have no
// server yet, so the embedded interface stays nil and only these are mounted.
type taskRPC struct {
	daemonv1.TaskService
	service *WorktreeService
}

// LocalTaskService serves the contract in the calling process.
func LocalTaskService() daemonv1.TaskService {
	return &taskRPC{service: &WorktreeService{}}
}

func refusal(err error) error { return rpc.Errorf(rpc.FailedPrecondition, "%s", err.Error()) }

// New implements TaskService.
func (t *taskRPC) New(ctx context.Context, in *daemonv1.TaskServiceNewRequest) (*daemonv1.TaskServiceNewResponse, error) {
	made, err := t.service.forCaller(in.GetEnviron()).New(ctx, WorktreeNewRequest{
		Dir: in.GetDir(), Task: in.GetTask(), Base: in.GetBase(), From: in.GetFrom(), Type: in.GetType(),
		Agent: in.GetAgent(), Prompt: in.GetPrompt(), NoSetup: in.GetNoSetup(), Clobber: in.GetClobber(),
	})
	if err != nil {
		return nil, refusal(err)
	}
	return &daemonv1.TaskServiceNewResponse{
		SchemaVersion: worktreeSchemaVersion, Task: made.Task, Branch: made.Branch, Path: made.Path, Base: made.Base,
		Port: int32(made.Port), Herdr: made.Herdr, Setup: made.Setup, Launch: made.Launch, Logs: made.Logs,
		Warnings: made.Warnings, AgentName: made.AgentName,
	}, nil
}

// Go implements TaskService.
func (t *taskRPC) Go(ctx context.Context, in *daemonv1.TaskServiceGoRequest) (*daemonv1.TaskServiceGoResponse, error) {
	found, err := t.service.forCaller(in.GetEnviron()).Go(ctx, WorktreeGoRequest{Dir: in.GetDir(), Selector: in.GetSelector(), Focus: in.GetFocus()})
	if err != nil {
		return nil, refusal(err)
	}
	return &daemonv1.TaskServiceGoResponse{
		SchemaVersion: worktreeSchemaVersion, Task: found.Task, Branch: found.Branch, Path: found.Path, Created: found.Created,
		Focused: found.Focused, PushRemote: found.PushRemote, Warnings: found.Warnings,
	}, nil
}
