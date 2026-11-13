package main

import (
	"context"
	"time"

	"github.com/arafathusayn/herdsman/internal/status"
	"github.com/arafathusayn/herdsman/sdk/client"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// routeExecutions reads a route's executions through RuntimeService, the
// contract every surface reads runtime state from.
func routeExecutions(ctx context.Context, sdk *client.Client, id *corev1.RouteId, repo string) ([]*daemonv1.Execution, error) {
	list, err := sdk.Runtime.ListExecutions(ctx, &daemonv1.RuntimeServiceListExecutionsRequest{Route: id, Repo: repo})
	if err != nil {
		return nil, err
	}
	return list.GetExecutions(), nil
}

// statusAgent is an execution as the status view prints it.
func statusAgent(e *daemonv1.Execution) status.Agent {
	ref, obs := e.GetRef(), e.GetObservation()
	a := status.Agent{
		Agent: e.GetAgent(), Role: e.GetRole(), Backend: ref.GetBackend(), Execution: ref.GetExecutionId(), Generation: ref.GetGeneration(),
		Address: ref.GetTerminalAddress(), Source: e.GetSource(),
		Process: obs.GetProcess(), Readiness: obs.GetReadiness(), Program: obs.GetProgram(), RawProgram: obs.GetRawProgramStatus(),
		Visibility: obs.GetVisibility(), App: obs.GetApp(), Orchestration: obs.GetOrchestration(), Actions: e.GetActions(),
	}
	if a.Address == "" {
		a.Address = ref.GetPane()
	}
	if ms := e.GetLastVerifiedAt(); ms > 0 {
		a.LastVerified = time.UnixMilli(ms)
	}
	for _, r := range e.GetAttention() {
		a.Attention = append(a.Attention, status.Attention{Reason: r.GetReason(), Detail: r.GetDetail(), Notice: r.GetNoticeId(), Gate: r.GetGate(), Line: r.GetLine()})
	}
	for _, u := range e.GetUnresolved() {
		a.Unresolved = append(a.Unresolved, status.Unresolved{Kind: u.GetKind(), ID: u.GetId(), State: u.GetState(), Detail: u.GetDetail(), Choices: u.GetChoices()})
	}
	if n := e.GetNotices(); n != nil {
		a.Notices = &status.Notices{Issued: int(n.GetIssued()), Claimed: int(n.GetClaimed()), Handled: int(n.GetHandled()), Returned: int(n.GetReturned()),
			OpenClaim: n.GetOpenClaim(), HeldGate: n.GetHeldGate()}
	}
	return a
}
