package daemon

import (
	"bytes"
	"context"
	"errors"
	"time"

	contextrepo "github.com/arafathusayn/herdsman/internal/context"
	"github.com/arafathusayn/herdsman/internal/project"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// currentProject is the project reference that means the caller's own.
const currentProject = "@"

// projectContextService is the part of ProjectService that works on a
// project's context repo.
type projectContextService struct{}

// contextFolder finds the bound folder the caller stands in. Finding a project
// by name needs the project store, which the daemon does not open yet, so that
// form is refused instead of answered from the wrong place.
func contextFolder(scope *daemonv1.Scope) (string, error) {
	if name := scope.GetProject(); name != "" && name != currentProject {
		return "", rpc.Errorf(rpc.FailedPrecondition, "a project cannot be named yet: run this inside its bound folder")
	}
	binding, ok, err := project.FindBinding(scope.GetFolder())
	if err != nil {
		return "", contextFailure(err)
	}
	if !ok {
		return "", rpc.Errorf(rpc.FailedPrecondition, "%s is not inside a folder bound to a project", scope.GetFolder())
	}
	return binding.Folder, nil
}

// contextFailure gives a failure the code a caller can act on.
func contextFailure(err error) error {
	var invalid *contextrepo.InvalidError
	switch {
	case errors.Is(err, contextrepo.ErrInvalid), errors.Is(err, project.ErrInvalid):
		return rpc.Errorf(rpc.InvalidArgument, "%s", err)
	case errors.Is(err, contextrepo.ErrNotGitRepo), errors.Is(err, contextrepo.ErrNotContextRepo),
		errors.Is(err, contextrepo.ErrMalformed), errors.As(err, &invalid):
		return rpc.Errorf(rpc.FailedPrecondition, "%s", err)
	}
	return rpc.Errorf(rpc.Internal, "%s", err)
}

// ContextInit lays out the context repo in the bound folder.
func (*projectContextService) ContextInit(_ context.Context, req *daemonv1.ProjectServiceContextInitRequest) (*daemonv1.ProjectServiceContextInitResponse, error) {
	folder, err := contextFolder(req.GetScope())
	if err != nil {
		return nil, err
	}
	if err := contextrepo.Init(folder); err != nil {
		return nil, contextFailure(err)
	}
	return &daemonv1.ProjectServiceContextInitResponse{Folder: folder}, nil
}

// ContextCheck reports what is wrong with the history. Problems are the answer
// here, not a failure of the call.
func (*projectContextService) ContextCheck(_ context.Context, req *daemonv1.ProjectServiceContextCheckRequest) (*daemonv1.ProjectServiceContextCheckResponse, error) {
	folder, err := contextFolder(req.GetScope())
	if err != nil {
		return nil, err
	}
	report, err := contextrepo.Check(folder)
	if err != nil {
		return nil, contextFailure(err)
	}
	res := &daemonv1.ProjectServiceContextCheckResponse{Events: int32(report.Events)}
	for _, p := range report.Problems {
		res.Problems = append(res.Problems, &daemonv1.ContextProblem{File: p.File, Code: p.Code, Detail: p.Detail})
	}
	return res, nil
}

// ContextNote adds a note written by a person or an agent.
func (*projectContextService) ContextNote(_ context.Context, req *daemonv1.ProjectServiceContextNoteRequest) (*daemonv1.ProjectServiceContextNoteResponse, error) {
	folder, err := contextFolder(req.GetScope())
	if err != nil {
		return nil, err
	}
	event, err := contextrepo.Append(folder, contextrepo.Input{
		Kind: contextrepo.KindNote, Actor: req.GetActor(), Task: req.GetTask(),
		Repos: req.GetRepos(), Parents: req.GetParents(), Body: req.GetBody(),
	})
	if err != nil {
		return nil, contextFailure(err)
	}
	return &daemonv1.ProjectServiceContextNoteResponse{Event: contextEvent(event)}, nil
}

// Log reads the history in order.
func (*projectContextService) Log(_ context.Context, req *daemonv1.ProjectServiceLogRequest) (*daemonv1.ProjectServiceLogResponse, error) {
	folder, err := contextFolder(req.GetScope())
	if err != nil {
		return nil, err
	}
	query := contextrepo.Query{Task: req.GetTask()}
	if req.GetSince() > 0 {
		query.Since = time.UnixMilli(req.GetSince())
	}
	chronology, err := contextrepo.Log(folder, query)
	if err != nil {
		return nil, contextFailure(err)
	}
	var text bytes.Buffer
	_ = chronology.WriteText(&text) // a bytes.Buffer does not fail
	res := &daemonv1.ProjectServiceLogResponse{Heads: chronology.Heads, Text: text.String()}
	for _, event := range chronology.Events {
		res.Events = append(res.Events, contextEvent(event))
	}
	return res, nil
}

func contextEvent(e contextrepo.Event) *daemonv1.ContextEvent {
	return &daemonv1.ContextEvent{
		Id: e.ID, Kind: e.Kind, At: e.At.UnixMilli(), Actor: e.Actor, Task: e.Task, Repos: e.Repos,
		Parents: e.Parents, Commits: e.Commits, Extra: e.Extra, Body: e.Body,
	}
}
