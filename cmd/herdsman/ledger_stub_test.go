package main

import (
	"context"
	"encoding/json"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// ledgerStub answers the generated client from the same table of JSON replies
// the verb tests give stubWorktree.
type ledgerStub struct{ daemonv1.LedgerService }

func (ledgerStub) ask(method string, body map[string]any, reply any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	raw, err := worktreeCall(context.Background(), method, data)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, reply)
}

func (s ledgerStub) TeardownPlans(_ context.Context, q *daemonv1.TeardownPlansRequest) (*daemonv1.TeardownPlansResponse, error) {
	var reply struct {
		Plans []json.RawMessage `json:"plans"`
		Hooks bool              `json:"hooks"`
	}
	if err := s.ask("TeardownPlan", map[string]any{"dir": q.GetDir(), "selector": q.GetSelector(), "route": q.GetRoute(), "all": q.GetAll()}, &reply); err != nil {
		return nil, err
	}
	res := &daemonv1.TeardownPlansResponse{Hooks: reply.Hooks}
	for _, plan := range reply.Plans {
		res.Plans = append(res.Plans, []byte(plan))
	}
	return res, nil
}

func (s ledgerStub) Teardown(_ context.Context, q *daemonv1.TeardownRequest) (*daemonv1.TeardownResponse, error) {
	plans := make([]json.RawMessage, 0, len(q.GetPlans()))
	for _, plan := range q.GetPlans() {
		plans = append(plans, json.RawMessage(plan))
	}
	var reply struct {
		Reports []teardownReport `json:"reports"`
	}
	if err := s.ask("Teardown", map[string]any{"dir": q.GetDir(), "plans": plans, "grace_seconds": q.GetGraceSeconds(), "include_associated": q.GetIncludeAssociated()}, &reply); err != nil {
		return nil, err
	}
	res := &daemonv1.TeardownResponse{}
	for _, report := range reply.Reports {
		wire := &daemonv1.TeardownReport{Task: report.Task, Error: report.Error}
		for _, outcome := range report.Outcomes {
			wire.Outcomes = append(wire.Outcomes, &daemonv1.TeardownOutcome{Kind: outcome.Kind, Id: outcome.ID, Result: outcome.Result})
		}
		res.Reports = append(res.Reports, wire)
	}
	return res, nil
}

func (s ledgerStub) RegisterResource(_ context.Context, q *daemonv1.RegisterResourceRequest) (*daemonv1.RegisterResourceResponse, error) {
	var reply map[string]any
	if err := s.ask("ResourceAdd", map[string]any{"dir": q.GetDir(), "task": q.GetTask(), "execution": q.GetExecution(), "kind": q.GetKind(), "id": q.GetId()}, &reply); err != nil {
		return nil, err
	}
	return &daemonv1.RegisterResourceResponse{}, nil
}

func (s ledgerStub) LedgerHealth(_ context.Context, q *daemonv1.LedgerHealthRequest) (*daemonv1.LedgerHealthResponse, error) {
	var reply struct {
		Running []struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		} `json:"running"`
	}
	if err := s.ask("LedgerHealth", map[string]any{"dir": q.GetDir()}, &reply); err != nil {
		return nil, err
	}
	res := &daemonv1.LedgerHealthResponse{}
	for _, line := range reply.Running {
		res.Running = append(res.Running, &daemonv1.ResourceLine{Kind: line.Kind, Id: line.ID})
	}
	return res, nil
}
