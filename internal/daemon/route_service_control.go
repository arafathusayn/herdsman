package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/route"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// controlCall is one lifecycle verb as the wire asks for it.
type controlCall struct {
	verb          route.Verb
	name, repo    string
	dryRun        bool
	note          string
	grace         time.Duration
	force         bool
	keepFolder    bool
	keepWorktrees bool
	teardown      bool
	caller        *daemonv1.UpCaller
}

// Pause interrupts a route's agents and holds its channel.
func (s *routeService) Pause(ctx context.Context, req *daemonv1.RouteServicePauseRequest) (*daemonv1.RouteServicePauseResponse, error) {
	res, err := s.control(ctx, controlCall{verb: route.VerbPause, name: req.GetRoute().GetValue(), repo: req.GetRepo(), dryRun: req.GetDryRun(), teardown: req.GetTeardown()})
	if err != nil {
		return nil, err
	}
	return &daemonv1.RouteServicePauseResponse{Result: res}, nil
}

// Resume continues a paused route or restarts a stopped one.
func (s *routeService) Resume(ctx context.Context, req *daemonv1.RouteServiceResumeRequest) (*daemonv1.RouteServiceResumeResponse, error) {
	res, err := s.control(ctx, controlCall{verb: route.VerbResume, name: req.GetRoute().GetValue(), repo: req.GetRepo(), dryRun: req.GetDryRun(), note: req.GetNote(), caller: req.GetCaller()})
	if err != nil {
		return nil, err
	}
	return &daemonv1.RouteServiceResumeResponse{Result: res}, nil
}

// Stop ends a route's sessions and keeps its work.
func (s *routeService) Stop(ctx context.Context, req *daemonv1.RouteServiceStopRequest) (*daemonv1.RouteServiceStopResponse, error) {
	res, err := s.control(ctx, controlCall{verb: route.VerbStop, name: req.GetRoute().GetValue(), repo: req.GetRepo(), dryRun: req.GetDryRun(), teardown: req.GetTeardown(), grace: time.Duration(req.GetGraceSeconds()) * time.Second})
	if err != nil {
		return nil, err
	}
	return &daemonv1.RouteServiceStopResponse{Result: res}, nil
}

// Delete removes a route, once a verified snapshot covers it.
func (s *routeService) Delete(ctx context.Context, req *daemonv1.RouteServiceDeleteRequest) (*daemonv1.RouteServiceDeleteResponse, error) {
	res, err := s.control(ctx, controlCall{verb: route.VerbDelete, name: req.GetRoute().GetValue(), repo: req.GetRepo(), dryRun: req.GetDryRun(), force: req.GetForce(), keepFolder: req.GetKeepFolder(), keepWorktrees: req.GetKeepWorktrees(), grace: time.Duration(req.GetGraceSeconds()) * time.Second})
	if err != nil {
		return nil, err
	}
	return &daemonv1.RouteServiceDeleteResponse{Result: res}, nil
}

// controlOps returns the verbs of one start of a route, oldest first.
func (s *routeService) controlOps(repo, name, startID string) ([]*ops.Operation, error) {
	all, err := s.d.ops.List()
	if err != nil && !errors.Is(err, ops.ErrNotFound) {
		return nil, rpc.Errorf(rpc.Internal, "operations: %v", err)
	}
	var out []*ops.Operation
	for _, op := range all {
		if !route.IsControlKind(op.Kind) {
			continue
		}
		verb := strings.TrimPrefix(op.Kind, "route.")
		if strings.HasPrefix(op.Key, route.ControlKeyPrefix(verb, repo, name, startID)) {
			out = append(out, op)
		}
	}
	slices.SortFunc(out, func(a, b *ops.Operation) int { return cmp.Compare(generationOf(a), generationOf(b)) })
	return out, nil
}

// generationOf reads a verb's number from its key. The number, not the id, is
// the order: it is assigned from the verbs already journaled.
func generationOf(op *ops.Operation) int {
	gen, _ := strconv.Atoi(op.Key[strings.LastIndex(op.Key, "#")+1:])
	return gen
}

// controlState is a route's lifecycle state.
func (s *routeService) controlState(repo, name, startID string) (string, error) {
	verbs, err := s.controlOps(repo, name, startID)
	if err != nil {
		return "", err
	}
	return route.ControlState(verbs), nil
}

// deletable refuses a delete this daemon could not undo, before anything runs
// and even for a dry run, whose plan would otherwise promise it.
func (s *routeService) deletable(call controlCall) error {
	if call.verb != route.VerbDelete {
		return nil
	}
	if s.d.cfg.Recovery == nil {
		return rpc.Errorf(rpc.FailedPrecondition, "%s", route.ErrNoRecovery.Error())
	}
	if s.d.cfg.RouteDown == nil && !call.keepWorktrees {
		return rpc.Errorf(rpc.FailedPrecondition, "this build cannot run down on the route's tasks; pass keep-worktrees to leave them")
	}
	return nil
}

func inFlight(op *ops.Operation) bool {
	return op.State != ops.OpDone && op.State != ops.OpFailed && op.State != ops.OpStale
}

// unchanged says whether a verb already holds, and refuses one that cannot
// apply to the route's state.
func unchanged(verb route.Verb, state string) (bool, error) {
	if state == route.ControlDeleted && verb != route.VerbDelete {
		return false, rpc.Errorf(rpc.FailedPrecondition, "the route is deleted")
	}
	switch verb {
	case route.VerbPause:
		if state == route.ControlStopped {
			return false, rpc.Errorf(rpc.FailedPrecondition, "the route is stopped; there is nothing to pause")
		}
		return state == route.ControlPaused, nil
	case route.VerbResume:
		return state == route.ControlRunning, nil
	case route.VerbStop:
		return state == route.ControlStopped, nil
	}
	return state == route.ControlDeleted, nil
}

func (s *routeService) control(ctx context.Context, call controlCall) (*daemonv1.RouteControl, error) {
	if call.name == "" || call.repo == "" {
		return nil, rpc.Errorf(rpc.InvalidArgument, "the request needs a route and a repository")
	}
	starts, err := s.starts(route.UpKey(call.repo, call.name))
	if err != nil {
		return nil, err
	}
	if len(starts) == 0 {
		return nil, rpc.Errorf(rpc.NotFound, "no start is journaled for route %s", call.name)
	}
	start := starts[len(starts)-1]
	if start.State == ops.OpRunning {
		return nil, rpc.Errorf(rpc.FailedPrecondition, "the route is still starting; wait for up to finish")
	}
	verbs, err := s.controlOps(call.repo, call.name, start.ID)
	if err != nil {
		return nil, err
	}
	state := route.ControlState(verbs)
	if err := s.deletable(call); err != nil {
		return nil, err
	}
	if n := len(verbs); n > 0 && inFlight(verbs[n-1]) {
		return s.finishInFlight(ctx, call, verbs[n-1], state)
	}
	same, err := unchanged(call.verb, state)
	if err != nil {
		return nil, err
	}
	if same {
		return &daemonv1.RouteControl{State: "unchanged", Control: state}, nil
	}
	plan, err := route.PlanControl(ctx, route.ControlRequest{
		Verb: call.verb, Start: start, Generation: len(verbs) + 1, From: state, Note: call.note,
		Grace: call.grace, Force: call.force, KeepFolder: call.keepFolder, KeepWorktrees: call.keepWorktrees, Teardown: call.teardown, Date: s.d.cfg.Now().Format("2006-01-02"),
	})
	if err != nil {
		return nil, rpc.Errorf(rpc.Internal, "plan the %s: %v", call.verb, err)
	}
	if call.dryRun {
		return &daemonv1.RouteControl{State: "planned", Control: state, Plan: plan.Text, Snapshot: plan.Snapshot}, nil
	}
	if err := s.admit(ctx, call, start, state); err != nil {
		return nil, err
	}
	op, err := s.d.ops.Submit(ctx, plan.Spec)
	if err != nil {
		var locked *ops.LockedError
		if errors.As(err, &locked) {
			return nil, rpc.Errorf(rpc.FailedPrecondition, "%s", err.Error())
		}
		return nil, rpc.Errorf(rpc.Internal, "journal the %s: %v", call.verb, err)
	}
	return s.driveControl(ctx, call, op)
}

// admit makes the checks only a real run needs.
func (s *routeService) admit(ctx context.Context, call controlCall, start *ops.Operation, state string) error {
	if call.verb == route.VerbDelete && !call.force {
		running, err := route.RunningAgents(ctx, start)
		if err != nil {
			return rpc.Errorf(rpc.Unavailable, "%s", err.Error())
		}
		if len(running) > 0 {
			return rpc.Errorf(rpc.FailedPrecondition, "agents still run: %s; stop the route first, or pass force", strings.Join(running, ", "))
		}
	}
	if call.verb == route.VerbResume && state == route.ControlStopped {
		return s.attestResume(ctx, call)
	}
	return nil
}

// attestResume records who asked, because a restart approves the pair again.
func (s *routeService) attestResume(ctx context.Context, call controlCall) error {
	caller, err := s.verifiedCaller(ctx, call.caller)
	if err != nil {
		return err
	}
	s.d.callers.Set(route.UpKey(call.repo, call.name), caller)
	return nil
}

// finishInFlight picks up the route's unfinished verb. Only the same verb may
// do that; a different one waits until a person settles the first.
func (s *routeService) finishInFlight(ctx context.Context, call controlCall, op *ops.Operation, state string) (*daemonv1.RouteControl, error) {
	if op.Kind != route.KindOf(call.verb) {
		return nil, rpc.Errorf(rpc.FailedPrecondition, "operation %s (%s) has not finished: %s", op.ID, op.Kind, op.Reason)
	}
	if call.dryRun {
		return &daemonv1.RouteControl{Operation: op.ID, State: string(op.State), Reason: op.Reason, Control: state, Plan: op.Plan, Snapshot: route.SnapshotOf(op)}, nil
	}
	if call.verb == route.VerbResume {
		if err := s.attestResume(ctx, call); err != nil {
			return nil, err
		}
		if err := route.ResolveHalted(s.d.ops, op); err != nil {
			return nil, rpc.Errorf(rpc.FailedPrecondition, "%s", err.Error())
		}
	}
	return s.driveControl(ctx, call, op)
}

// driveControl runs the verb. It outlives the caller's connection, so a client
// that goes away cannot leave the route half way through a step.
func (s *routeService) driveControl(ctx context.Context, call controlCall, op *ops.Operation) (*daemonv1.RouteControl, error) {
	done, err := s.d.ops.Drive(context.WithoutCancel(ctx), op.ID)
	if errors.Is(err, ops.ErrBusy) {
		if done, err = s.d.ops.Get(op.ID); err != nil {
			return nil, rpc.Errorf(rpc.Internal, "operation: %v", err)
		}
		return s.controlResult(call, done)
	}
	if done == nil {
		return nil, rpc.Errorf(rpc.Internal, "%s: %v", call.verb, err)
	}
	if err != nil && done.Reason == "" {
		done.Reason = fmt.Sprintf("%v", err)
	}
	return s.controlResult(call, done)
}

func (s *routeService) controlResult(call controlCall, op *ops.Operation) (*daemonv1.RouteControl, error) {
	verbs, err := s.controlOps(call.repo, call.name, startOfVerb(op))
	if err != nil {
		return nil, err
	}
	state := route.ControlState(verbs)
	return &daemonv1.RouteControl{Operation: op.ID, State: string(op.State), Reason: op.Reason, Control: state, Plan: op.Plan, Snapshot: route.SnapshotOf(op)}, nil
}

// startOfVerb reads the start a verb acts on from its key.
func startOfVerb(op *ops.Operation) string {
	key := op.Key[strings.LastIndex(op.Key, "@")+1:]
	return key[:strings.LastIndex(key, "#")]
}

// teardownRoute stops the task processes of a route folder through the
// worktree service. The command showed its plan and was confirmed, so what the
// service plans is approved.
func teardownRoute(s *WorktreeService) func(context.Context, string, string) error {
	return func(ctx context.Context, repo, folder string) error {
		plans, err := s.TeardownPlan(ctx, WorktreeTeardownPlanRequest{Dir: repo, Route: folder})
		if err != nil {
			return err
		}
		if len(plans.Plans) == 0 {
			return nil
		}
		done, err := s.Teardown(ctx, WorktreeTeardownRequest{Dir: repo, Plans: plans.Plans})
		if err != nil {
			return err
		}
		var failed []error
		for _, report := range done.Reports {
			if report.Error != "" {
				failed = append(failed, fmt.Errorf("%s: %s", report.Task, report.Error))
			}
		}
		return errors.Join(failed...)
	}
}
