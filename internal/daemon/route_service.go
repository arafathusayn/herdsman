package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/route/routechan"
	"github.com/arafathusayn/herdsman/internal/sys"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// routeService is the daemon's RouteService. A start runs as one journaled
// operation, so a restarted daemon finishes it without a second agent.
type routeService struct {
	d *Daemon
}

// operationEffects adds the effects of a route's start to the configured ones.
func (d *Daemon) operationEffects() map[string]ops.Effect {
	teardown := d.cfg.RouteTeardown
	if teardown == nil {
		teardown = teardownRoute(&WorktreeService{HerdrSocket: d.cfg.HerdrSocket})
	}
	effects := route.Effects(route.Deps{Callers: d.callers, Channel: routechan.Run, Recovery: d.routeRecovery(), Down: d.cfg.RouteDown, Teardown: teardown})
	for name, effect := range d.cfg.Effects {
		effects[name] = effect
	}
	return effects
}

// Up plans a route's start and journals it, or picks up the start already
// journaled for the route. The start runs in the background; Get reports it.
func (s *routeService) Up(ctx context.Context, req *daemonv1.UpRequest) (*daemonv1.UpResponse, error) {
	q, err := s.request(req)
	if err != nil {
		return nil, err
	}
	starts, err := s.starts(route.UpKey(q.Repo, q.Route))
	if err != nil {
		return nil, err
	}
	state := route.ControlRunning
	if len(starts) > 0 {
		if state, err = s.controlState(q.Repo, q.Route, starts[len(starts)-1].ID); err != nil {
			return nil, err
		}
	}
	if state == route.ControlPaused || state == route.ControlStopped {
		return nil, rpc.Errorf(rpc.FailedPrecondition, "the route is %s; bring it back with: herdsman resume %s", state, q.Route)
	}
	var latest *ops.Operation
	if len(starts) > 0 && state != route.ControlDeleted {
		latest = starts[len(starts)-1]
		q.Date = route.DateOf(latest)
	}
	var plan *route.UpPlan
	if req.GetContinue() && latest != nil {
		plan, err = route.PlanOf(latest)
		if err != nil {
			return nil, rpc.Errorf(rpc.Internal, "the journaled start: %v", err)
		}
	} else if plan, err = route.PlanUp(q); err != nil {
		return nil, rpc.Errorf(rpc.InvalidArgument, "%s", err.Error())
	}
	if state == route.ControlDeleted {
		plan.Spec.Key = fmt.Sprintf("%s#%d", plan.Key, len(starts)+1)
	}
	if req.GetDryRun() {
		return &daemonv1.UpResponse{Start: &daemonv1.RouteStart{
			Route: &corev1.RouteId{Value: q.Route}, State: "planned", Folder: plan.Folder, Inbox: plan.Inbox, Workspace: plan.Workspace, Plan: plan.Text,
		}}, nil
	}
	caller, err := s.verifiedCaller(ctx, req.GetCaller())
	if err != nil {
		return nil, err
	}
	s.d.callers.Set(plan.Key, caller)
	verdicts := map[int]string{}
	for _, v := range req.GetVerdicts() {
		verdicts[int(v.GetStep())] = v.GetVerdict()
	}
	if len(verdicts) == 0 {
		verdicts = nil
	}
	op, err := s.begin(ctx, plan, latest, starts, verdicts, req.GetContinue())
	if err != nil {
		return nil, err
	}
	s.d.driveLater(op.ID)
	return &daemonv1.UpResponse{Start: startOf(op)}, nil
}

// begin decides which operation carries this call.
func (s *routeService) begin(ctx context.Context, plan *route.UpPlan, latest *ops.Operation, starts []*ops.Operation, verdicts map[int]string, resume bool) (*ops.Operation, error) {
	if latest == nil {
		if resume {
			return nil, rpc.Errorf(rpc.FailedPrecondition, "the route has no start to continue")
		}
		return s.submit(ctx, plan.Spec)
	}
	switch latest.State {
	case ops.OpDone:
		if verdicts != nil {
			return nil, rpc.Errorf(rpc.FailedPrecondition, "the route's start is done; there is nothing to settle")
		}
		return latest, nil
	case ops.OpFailed, ops.OpStale:
		next := plan.Spec
		next.Key = fmt.Sprintf("%s#%d", plan.Key, len(starts)+1)
		return s.submit(ctx, next)
	}
	if latest.Plan != plan.Spec.Plan {
		return nil, rpc.Errorf(rpc.FailedPrecondition, "the route's start in progress has a different plan; finish it with the same commands, or delete the route first")
	}
	if err := route.Continue(s.d.ops, s.d.callers, plan, latest, verdicts); err != nil {
		return nil, rpc.Errorf(rpc.FailedPrecondition, "%s", err.Error())
	}
	op, err := s.d.ops.Get(latest.ID)
	if err != nil {
		return nil, rpc.Errorf(rpc.Internal, "operation: %v", err)
	}
	return op, nil
}

func (s *routeService) submit(ctx context.Context, spec ops.Spec) (*ops.Operation, error) {
	op, err := s.d.ops.Submit(ctx, spec)
	if err != nil {
		return nil, rpc.Errorf(rpc.Internal, "journal the start: %v", err)
	}
	return op, nil
}

// request fills a plan's request from the wire and the daemon's own facts.
func (s *routeService) request(req *daemonv1.UpRequest) (route.UpRequest, error) {
	name := req.GetRoute().GetValue()
	if name == "" {
		return route.UpRequest{}, rpc.Errorf(rpc.InvalidArgument, "the request names no route")
	}
	socket := req.GetHerdrSocket()
	if socket == "" {
		socket = s.d.cfg.HerdrSocket
	}
	return route.UpRequest{
		Route: name, Repo: req.GetRepo(), Prompt: req.GetPrompt(),
		AdjutantCommand: req.GetAdjutantCommand(), OrchestratorCommand: req.GetOrchestratorCommand(),
		AdjutantKind: req.GetAdjutantKind(), OrchestratorKind: req.GetOrchestratorKind(),
		NoAdjutant: req.GetNoAdjutant(), PairManual: req.GetPairManual(), ConfigPath: req.GetConfigPath(),
		HerdrSocket: socket, PrivateHome: s.d.cfg.PrivateHome, Executable: s.d.cfg.Executable,
		Date: orToday(req.GetDate(), s.d.cfg.Now()), PolicyFiles: policyFiles(s.d.cfg.Home), Kinds: req.GetKinds(),
	}, nil
}

func orToday(date string, now time.Time) string {
	if date != "" {
		return date
	}
	return now.Format("2006-01-02")
}

// policyFiles lists the sandbox policy files that exist for this account.
func policyFiles(home string) []string {
	var out []string
	for _, rel := range []string{".codex/config.toml", ".claude/settings.json"} {
		path := filepath.Join(home, rel)
		if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() {
			out = append(out, path)
		}
	}
	return out
}

// connKey holds a request's connection in its context.
type connKey struct{}

// verifiedCaller builds the caller from what the kernel and the process table
// say about the socket's peer. Of the request it keeps only the pane, which
// herdr checks, and the confirmed checksum, which needs a verified terminal.
func (s *routeService) verifiedCaller(ctx context.Context, claimed *daemonv1.UpCaller) (route.Caller, error) {
	conn, ok := ctx.Value(connKey{}).(syscall.Conn)
	if !ok {
		return route.Caller{}, rpc.Errorf(rpc.PermissionDenied, "the request's connection has no peer to verify")
	}
	peer, err := sys.PeerCredentials(conn)
	if err != nil {
		return route.Caller{}, rpc.Errorf(rpc.PermissionDenied, "read the caller's credentials: %v", err)
	}
	if claimed.GetPid() != int64(peer.PID) {
		return route.Caller{}, rpc.Errorf(rpc.PermissionDenied, "the request claims process %d but comes from process %d", claimed.GetPid(), peer.PID)
	}
	identity, err := sys.Identify(peer.PID)
	if err != nil {
		return route.Caller{}, rpc.Errorf(rpc.PermissionDenied, "identify the caller: %v", err)
	}
	tty, harness := inspectCaller(peer.PID, systemProcFacts)
	return route.Caller{Pane: claimed.GetPane(), PID: peer.PID, Started: identity.Start, TTY: tty, HarnessAncestor: harness, Confirmed: claimed.GetConfirmedChecksum()}, nil
}

// procFacts is one process-table row.
type procFacts struct {
	ppid         int
	tty, command string
}

func systemProcFacts(pid int) (procFacts, error) {
	out, err := exec.Command("ps", "-o", "ppid=,tty=,command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return procFacts{}, err
	}
	fields := strings.Fields(string(out))
	if len(fields) < 3 {
		return procFacts{}, fmt.Errorf("process %d: short process-table row", pid)
	}
	ppid, err := strconv.Atoi(fields[0])
	if err != nil {
		return procFacts{}, err
	}
	return procFacts{ppid: ppid, tty: fields[1], command: strings.Join(fields[2:], " ")}, nil
}

// inspectCaller reads a process's controlling terminal and looks for a coding
// agent among it and its ancestors. A script harness runs under an interpreter,
// so the second word of a command line counts as well as the first.
func inspectCaller(pid int, inspect func(int) (procFacts, error)) (tty, harness string) {
	for depth := 0; pid > 1 && depth < 64; depth++ {
		facts, err := inspect(pid)
		if err != nil {
			return tty, harness
		}
		if depth == 0 && facts.tty != "" && !strings.HasPrefix(facts.tty, "?") {
			tty = "/dev/" + facts.tty
		}
		words := strings.Fields(facts.command)
		for i := 0; i < len(words) && i < 2; i++ {
			if name := filepath.Base(words[i]); slices.Contains(route.HarnessNames, name) {
				return tty, name
			}
		}
		pid = facts.ppid
	}
	return tty, harness
}

// sortStarts orders starts oldest first: by the number in the key, which each
// new start increments, then by id, which sorts by time only to the millisecond.
func sortStarts(starts []*ops.Operation) {
	number := func(op *ops.Operation) int {
		i := strings.LastIndex(op.Key, "#")
		if i < 0 {
			return 1 // the first start's key carries no number
		}
		n, _ := strconv.Atoi(op.Key[i+1:])
		return n
	}
	slices.SortFunc(starts, func(a, b *ops.Operation) int {
		return cmp.Or(cmp.Compare(number(a), number(b)), strings.Compare(a.ID, b.ID))
	})
}

// starts returns the operations of one route's start, oldest first.
func (s *routeService) starts(key string) ([]*ops.Operation, error) {
	all, err := s.d.ops.List()
	if err != nil {
		return nil, rpc.Errorf(rpc.Internal, "operations: %v", err)
	}
	var out []*ops.Operation
	for _, op := range all {
		if op.Kind == route.OperationKind && (op.Key == key || strings.HasPrefix(op.Key, key+"#")) {
			out = append(out, op)
		}
	}
	sortStarts(out)
	return out, nil
}

// Get reports the latest start of one route.
func (s *routeService) Get(_ context.Context, req *daemonv1.RouteServiceGetRequest) (*daemonv1.RouteServiceGetResponse, error) {
	name := req.GetRoute().GetValue()
	if name == "" || req.GetRepo() == "" {
		return nil, rpc.Errorf(rpc.InvalidArgument, "the request needs a route and a repository")
	}
	starts, err := s.starts(route.UpKey(req.GetRepo(), name))
	if err != nil {
		return nil, err
	}
	if len(starts) == 0 {
		return nil, rpc.Errorf(rpc.NotFound, "no start is journaled for route %s", name)
	}
	start := startOf(starts[len(starts)-1])
	if start.Control, err = s.controlState(req.GetRepo(), name, start.GetOperation()); err != nil {
		return nil, err
	}
	return &daemonv1.RouteServiceGetResponse{Start: start}, nil
}

// List reports every journaled start.
func (s *routeService) List(context.Context, *daemonv1.RouteServiceListRequest) (*daemonv1.RouteServiceListResponse, error) {
	all, err := s.d.ops.List()
	if err != nil && !errors.Is(err, ops.ErrNotFound) {
		return nil, rpc.Errorf(rpc.Internal, "operations: %v", err)
	}
	out := &daemonv1.RouteServiceListResponse{}
	for _, op := range all {
		if op.Kind == route.OperationKind {
			start := startOf(op)
			if start.Control, err = s.controlState(route.RepoOf(op), start.GetRoute().GetValue(), op.ID); err != nil {
				return nil, err
			}
			out.Starts = append(out.Starts, start)
		}
	}
	return out, nil
}

func startOf(op *ops.Operation) *daemonv1.RouteStart {
	facts := route.Describe(op)
	start := &daemonv1.RouteStart{
		Operation: op.ID, Route: &corev1.RouteId{Value: facts.Route}, State: string(op.State), Reason: op.Reason,
		Folder: facts.Folder, Inbox: facts.Inbox, Workspace: facts.Workspace, Plan: op.Plan,
	}
	for _, st := range op.Steps {
		start.Steps = append(start.Steps, &daemonv1.RouteStep{Ordinal: int32(st.Ordinal), Name: st.Name, Key: st.Key, State: string(st.State), Detail: st.Detail})
	}
	if facts.Inbox != "" {
		sum, approved, err := route.PairState(facts.Inbox)
		if err == nil {
			pair := &daemonv1.PairStatus{Checksum: sum, Approved: approved}
			if record := filepath.Join(facts.Inbox, "pair-approval.json"); approved {
				if _, err := os.Stat(record); err == nil {
					pair.Record = record
				}
			}
			start.Pair = pair
		}
	}
	return start
}
