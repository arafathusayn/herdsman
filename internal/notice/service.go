package notice

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arafathusayn/herdsman/internal/runtime"
)

// Defaults of section 10's limits. They are implementation defaults, as the
// spec says; a daemon may set others.
const (
	DefaultPoll          = 30 * time.Second
	DefaultRenewal       = 5 * time.Second
	DefaultHeldGrace     = 60 * time.Second
	DefaultWaitGrace     = 60 * time.Second
	DefaultRecoveryGrace = 60 * time.Second
	DefaultGenerationGap = time.Second
	// DefaultMaxWaits is the daemon's wait budget, beside the stream slots.
	DefaultMaxWaits = 128
	// StuckAfter is how many ignored recovery answers make claim_stuck.
	StuckAfter = 3
	// keepWaitRecords bounds the wait ids kept per execution.
	keepWaitRecords = 32
)

// The answers of Wait.
const (
	AnswerNotice   = "notice"
	AnswerRecovery = "recovery"
	AnswerReplaced = "replaced"
	AnswerStopped  = "stopped"
	AnswerNone     = "none"
)

// The event kinds this package raises on the runtime topic.
const (
	EventNeedsAttention = "needs_attention"
	EventNotWaiting     = "not_waiting"
	EventHeld           = "notice_held"
	EventClaimStuck     = "claim_stuck"
	EventOutcome        = "notice_outcome"
	EventReturned       = "notice_returned"
)

// Reasons of needs_attention this package raises.
const (
	AttentionNoticeWaiting = "notice_waiting"
	AttentionInterrupted   = "interrupted"
)

// AttendedLine is the fixed line a person types in the pane of an attended
// execution when a notice waits for it. It carries no task content and no
// notice id: the content is pulled by Read, which checks every gate.
const AttendedLine = "Run `herdsman notice read` and do what it prints"

// Event is something a person or the issuer must hear.
type Event struct {
	Kind, Repo, Route, Execution string
	Reason, Detail, Notice       string
	Outcome, Line                string
}

// Executions is where the service reads what is known of an execution: its
// identity, readiness, visibility and program status.
type Executions interface {
	Execution(ctx context.Context, id string) (runtime.Execution, bool, error)
	// Running lists the executions Herdsman wants running.
	Running(ctx context.Context) ([]runtime.Execution, error)
}

// Config sets a service's limits and hooks. Zero values take the defaults.
type Config struct {
	Poll, Renewal, HeldGrace, WaitGrace, RecoveryGrace, GenerationGap time.Duration
	MaxWaits                                                          int
	Now                                                               func() time.Time
	Sleep                                                             func(context.Context, time.Duration) error
	Emit                                                              func(Event)
	// Boot names this daemon run: a final-report deadline set by another
	// run is moved once.
	Boot string
	// RecordPath is where an execution keeps the processing record of a
	// notice it claimed.
	RecordPath func(Notice) string
	// Attended reports a harness kind that holds no wait: each notice for it
	// raises needs_attention for a person unless the Queued adapter can wake it.
	Attended func(harness string) bool
	// Queued identifies adapters woken by their own message queue. Queue
	// must prove the current execution and native conversation before writing.
	Queued func(harness string) bool
	Queue  func(context.Context, runtime.Execution, string) error
	// Trace receives each step of a notice once, after it is recorded: the
	// issue (the call's own time), the claim (from issue to claim) and the
	// handling (from claim to acknowledgement). A repeated call is no step.
	Trace func(step string, n Notice, start, end time.Time)
}

// The steps of a notice that Config.Trace receives.
const (
	TraceIssue  = "issue"
	TraceClaim  = "claim"
	TraceHandle = "handle"
)

func (c *Config) defaults() {
	if c.Poll == 0 {
		c.Poll = DefaultPoll
	}
	if c.Renewal == 0 {
		c.Renewal = DefaultRenewal
	}
	if c.HeldGrace == 0 {
		c.HeldGrace = DefaultHeldGrace
	}
	if c.WaitGrace == 0 {
		c.WaitGrace = DefaultWaitGrace
	}
	if c.RecoveryGrace == 0 {
		c.RecoveryGrace = DefaultRecoveryGrace
	}
	if c.GenerationGap == 0 {
		c.GenerationGap = DefaultGenerationGap
	}
	if c.MaxWaits == 0 {
		c.MaxWaits = DefaultMaxWaits
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Sleep == nil {
		c.Sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	if c.Emit == nil {
		c.Emit = func(Event) {}
	}
	if c.Trace == nil {
		c.Trace = func(string, Notice, time.Time, time.Time) {}
	}
	if c.RecordPath == nil {
		c.RecordPath = func(n Notice) string {
			if n.Folder == "" {
				return ""
			}
			return filepath.Join(n.Folder, "notices", n.ID+".md")
		}
	}
	if c.Attended == nil {
		c.Attended = func(string) bool { return false }
	}
}

// Service is the notice domain over a store. It is safe for concurrent use;
// every read-modify-write runs under one lock and in one transaction.
type Service struct {
	store Store
	execs Executions
	cfg   Config

	queueMu sync.Mutex // one external queued-wake command at a time
	mu      sync.Mutex // serializes the transactions and guards the maps
	changed chan struct{}
	stopped chan struct{}
	closed  bool
	// attached is the poll that holds each execution's wait now.
	attached map[string]*poll
	// heldUntil is when an execution last held a wait, with the renewal
	// window after a poll that answered its caller.
	heldUntil  map[string]time.Time
	notWaiting map[string]bool
	// restSince is when an execution with an open claim was last seen to
	// report something other than working.
	restSince map[string]time.Time
	started   time.Time
}

type poll struct {
	exec string
	gen  int64
	// end carries an answer forced by a newer poll of the same execution.
	end chan string
}

// New makes a service.
func New(store Store, execs Executions, cfg Config) *Service {
	cfg.defaults()
	return &Service{store: store, execs: execs, cfg: cfg, changed: make(chan struct{}), stopped: make(chan struct{}), attached: map[string]*poll{},
		heldUntil: map[string]time.Time{}, notWaiting: map[string]bool{}, restSince: map[string]time.Time{}, started: cfg.Now()}
}

func (s *Service) now() time.Time { return s.cfg.Now() }

// Close ends every wait with an answer that tells its command to connect
// again, as the event wait does at a daemon stop.
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.stopped)
	}
}

// Changed tells every wait to look again: a gate changed.
func (s *Service) Changed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.broadcast()
}

func (s *Service) broadcast() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *Service) watch() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}

// tx runs fn under the lock in one transaction and wakes the waits after a
// change.
func (s *Service) tx(fn func(Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.store.Tx(fn)
	if err == nil {
		s.broadcast()
	}
	return err
}

// --- gates ---

// identityOK is the exact target check of the dispatch contract, plus the
// launch half of the first-dispatch gate: registered, a status since
// registration (readiness), and nothing lost.
func identityOK(e runtime.Execution, n Notice) string {
	switch {
	case e.ID != n.Execution || e.Generation != n.Generation || e.EndpointID != n.Endpoint:
		return "the caller is not the notice's execution, generation and endpoint"
	case e.Backend != runtime.BackendCore || e.Desired != runtime.DesiredRunning:
		return "the execution is not a running herdr-core execution"
	case e.Evidence.PID <= 0 || e.Evidence.Start == "":
		return "the execution has not registered"
	case e.Observation.Readiness != runtime.ReadinessReady:
		return "the execution is not ready: " + string(e.Observation.Readiness)
	}
	switch e.Observation.Process {
	case runtime.ProcessLost, runtime.ProcessReplaced, runtime.ProcessFinished:
		return "the execution's process is " + string(e.Observation.Process)
	}
	return ""
}

func visibilityOK(e runtime.Execution) bool {
	v := e.Observation.Visibility
	return v == runtime.VisibilityPending || v == runtime.VisibilityVerified
}

// gate checks every gate of one notice for one execution, at issue (claim
// false: the order rule is a claim's) or at claim. It returns the refusal.
func (s *Service) gate(tx Tx, e runtime.Execution, n Notice, claim bool) *Refusal {
	if why := identityOK(e, n); why != "" {
		return &Refusal{Gate: GateIdentity, Detail: why}
	}
	if e.Repo != n.Repo || e.Route != n.Route {
		return &Refusal{Gate: GateIdentity, Detail: "the execution is not a member of the notice's route"}
	}
	if !visibilityOK(e) {
		return &Refusal{Gate: GateVisibility, Detail: "visibility is " + string(e.Observation.Visibility)}
	}
	rs, _, err := tx.Route(n.Repo, n.Route)
	if err != nil {
		return &Refusal{Gate: GateIdentity, Detail: err.Error()}
	}
	if !n.Ordinary() {
		switch {
		case !rs.Stopping:
			return &Refusal{Gate: GateNotStopping, Detail: "the route is not stopping"}
		case rs.StopOp != n.Issuer:
			return &Refusal{Gate: GateStopOperation, Detail: "the notice is not the current stop's"}
		case !n.Deadline.IsZero() && !s.now().Before(n.Deadline):
			return &Refusal{Gate: GateDeadline, Detail: "the report's deadline has passed"}
		}
		return nil
	}
	a, ok, err := tx.Approval(n.Execution)
	if err != nil || !ok {
		return &Refusal{Gate: GateApproval, Detail: "the execution is not in an approved pair"}
	}
	if a.PolicyDigest != e.PolicyDigest {
		return &Refusal{Gate: GatePolicy, Detail: "the policy changed since the pair was approved"}
	}
	if rs.Stopping {
		return &Refusal{Gate: GateStopping, Detail: "the route is stopping"}
	}
	if rs.Held {
		return &Refusal{Gate: GateHeld, Detail: "the route is held"}
	}
	if !claim {
		return nil
	}
	list, err := tx.Notices(n.Execution)
	if err != nil {
		return &Refusal{Gate: GateOrder, Detail: err.Error()}
	}
	for _, o := range list {
		if o.ID == n.ID || !o.Ordinary() {
			continue
		}
		if o.Open() {
			return &Refusal{Gate: GateOrder, Detail: "notice " + o.ID + " is claimed and not acknowledged"}
		}
		if o.State == StateIssued && o.Seq < n.Seq {
			return &Refusal{Gate: GateOrder, Detail: "notice " + o.ID + " was issued first"}
		}
	}
	return nil
}

func (s *Service) execution(ctx context.Context, id string) (runtime.Execution, error) {
	e, ok, err := s.execs.Execution(ctx, id)
	if err != nil {
		return runtime.Execution{}, err
	}
	if !ok {
		return runtime.Execution{}, &Refusal{Gate: GateIdentity, Detail: "no execution " + id + " is recorded"}
	}
	return e, nil
}

// --- issuing ---

// IssueRequest is one ordinary notice to issue.
type IssueRequest struct {
	// Key makes the issue idempotent: a repeat returns the notice issued.
	Key       string
	Kind      string
	Execution string
	Content   string
	// Issuer is the operation that issues it.
	Issuer string
	// Previous is a returned notice's record this one carries on.
	Previous string
}

// Issue records an ordinary notice for an execution's current generation,
// after every ordinary gate but the order rule. A repeated key returns the
// notice it issued, whatever its state now.
func (s *Service) Issue(ctx context.Context, req IssueRequest) (Notice, error) {
	rule, ok := RuleOf(req.Kind)
	if !ok || req.Kind == KindFinalReport {
		return Notice{}, fmt.Errorf("notice: %q is not a kind an operation issues this way", req.Kind)
	}
	_ = rule
	if req.Key == "" || len(req.Content) == 0 || len(req.Content) > MaxContent {
		return Notice{}, errors.New("notice: a notice needs a key and content of at most 64 KiB")
	}
	start := s.now()
	e, err := s.execution(ctx, req.Execution)
	if err != nil {
		return Notice{}, err
	}
	var out Notice
	issued := false
	err = s.tx(func(tx Tx) error {
		if n, found, err := tx.NoticeByKey(req.Key); err != nil || found {
			out = n
			return err
		}
		n := s.draft(e, req.Key, req.Kind, req.Content, req.Issuer)
		n.Previous = req.Previous
		if rs, _, err := tx.Route(e.Repo, e.Route); err == nil {
			n.Folder = rs.Folder
		}
		if r := s.gate(tx, e, n, false); r != nil {
			return r
		}
		seq, err := tx.NextSeq()
		if err != nil {
			return err
		}
		n.Seq = seq
		out, issued = n, true
		return tx.PutNotice(n)
	})
	if issued && err == nil {
		s.cfg.Trace(TraceIssue, out, start, s.now())
	}
	return out, err
}

func (s *Service) draft(e runtime.Execution, key, kind, content, issuer string) Notice {
	return Notice{SchemaVersion: SchemaVersion, ID: NewID(), Key: key, Kind: kind, Repo: e.Repo, Route: e.Route, Agent: e.Agent,
		Execution: e.ID, Generation: e.Generation, Endpoint: e.EndpointID, Issuer: issuer, IssuedAt: s.now(),
		Content: content, Digest: Digest(content), State: StateIssued}
}

// FinalReportText is the final-report notice's fixed text: no task content.
// It names an open claim, so that the report says where that work stands.
func FinalReportText(open string) string {
	t := "The route is stopping. Write your report now and start no new work. Send it as your route's report message, then acknowledge this notice."
	if open != "" {
		t += " You hold an open claim on notice " + open + "; say in the report where that work stands, and acknowledge that notice too."
	}
	return t
}

// Report outcomes of a stop (spec section 10).
const (
	ReportReported    = "reported"
	ReportTimeout     = "report_timeout"
	ReportUnavailable = "report_unavailable"
	ReportNotReached  = "not_reached"
)

// FinalRequest asks one execution for its report.
type FinalRequest struct {
	Key       string
	Execution string
	StopOp    string
	Grace     time.Duration
	// HoldsWaitKind: the execution's harness keeps its wait, so one that
	// holds none cannot be reached.
	HoldsWait bool
}

// IssueFinalReport issues the final-report notice to one execution as one
// step of the stop operation, after its five checks. A refusal's gate is the
// report_unavailable reason. A repeated key returns the notice issued.
func (s *Service) IssueFinalReport(ctx context.Context, req FinalRequest) (Notice, error) {
	if req.Key == "" || req.StopOp == "" {
		return Notice{}, errors.New("notice: a final report needs a key and its stop operation")
	}
	start := s.now()
	e, err := s.execution(ctx, req.Execution)
	if err != nil {
		return Notice{}, &Refusal{Gate: "identity_unknown", Detail: err.Error()}
	}
	var out Notice
	issued := false
	err = s.tx(func(tx Tx) error {
		if n, found, err := tx.NoticeByKey(req.Key); err != nil || found {
			out = n
			return err
		}
		list, err := tx.Notices(e.ID)
		if err != nil {
			return err
		}
		open := ""
		for _, o := range list {
			if o.Open() && o.Ordinary() {
				open = o.ID
			}
		}
		n := s.draft(e, req.Key, KindFinalReport, FinalReportText(open), req.StopOp)
		n.Deadline, n.Boot = s.now().Add(req.Grace), s.cfg.Boot
		if rs, _, err := tx.Route(e.Repo, e.Route); err == nil {
			n.Folder = rs.Folder
		}
		if r := s.gate(tx, e, n, false); r != nil {
			switch r.Gate {
			case GateIdentity:
				r.Gate = "identity_unknown"
			case GateVisibility:
				r.Gate = string(e.Observation.Visibility)
			}
			return r
		}
		if req.HoldsWait && !s.holdingLocked(e.ID) {
			return &Refusal{Gate: EventNotWaiting, Detail: "the execution holds no wait"}
		}
		seq, err := tx.NextSeq()
		if err != nil {
			return err
		}
		n.Seq = seq
		out, issued = n, true
		return tx.PutNotice(n)
	})
	if issued && err == nil {
		s.cfg.Trace(TraceIssue, out, start, s.now())
	}
	return out, err
}

// ConfirmDeadlines moves, once, the deadline of each final-report notice of a
// route that another daemon run set: no agent could reach the daemon while it
// was down. It returns the route's final-report notices.
func (s *Service) ConfirmDeadlines(repo, route string) ([]Notice, error) {
	var out []Notice
	err := s.tx(func(tx Tx) error {
		rs, _, err := tx.Route(repo, route)
		if err != nil {
			return err
		}
		list, err := tx.RouteNotices(repo, route)
		if err != nil {
			return err
		}
		for _, n := range list {
			if n.Kind != KindFinalReport || n.Issuer != rs.StopOp {
				continue
			}
			if n.Boot != s.cfg.Boot && !n.DeadlineMoved && n.State != StateHandled {
				n.Deadline, n.DeadlineMoved, n.Boot = s.now().Add(rs.Grace), true, s.cfg.Boot
				if err := tx.PutNotice(n); err != nil {
					return err
				}
			}
			out = append(out, n)
		}
		return nil
	})
	return out, err
}

// --- route state, approvals ---

// Hold places (held) or lifts a route's hold. Lifting it makes the notices
// it held claimable, and they are announced in the order they were issued.
func (s *Service) Hold(repo, route string, held bool, by string) error {
	return s.tx(func(tx Tx) error {
		rs, _, err := tx.Route(repo, route)
		if err != nil {
			return err
		}
		rs.Repo, rs.Route, rs.Held, rs.HeldBy = repo, route, held, by
		if !held {
			rs.HeldBy = ""
		}
		return tx.PutRoute(rs)
	})
}

// Stopping puts a route in the stopping phase for one stop operation. The
// phase is not lifted for anything; only a resume from the stop clears it.
func (s *Service) Stopping(repo, route, op string, grace time.Duration) error {
	return s.tx(func(tx Tx) error {
		rs, _, err := tx.Route(repo, route)
		if err != nil {
			return err
		}
		if rs.Stopping && rs.StopOp == op {
			return nil
		}
		rs.Repo, rs.Route, rs.Stopping, rs.StopOp, rs.Grace = repo, route, true, op, grace
		return tx.PutRoute(rs)
	})
}

// Restart clears the stopping phase and the hold of a route that a resume
// launches again.
func (s *Service) Restart(repo, route string) error {
	return s.tx(func(tx Tx) error {
		rs, _, err := tx.Route(repo, route)
		if err != nil {
			return err
		}
		return tx.PutRoute(RouteState{Repo: repo, Route: route, Folder: rs.Folder})
	})
}

// RouteState reads a route's state.
func (s *Service) RouteState(repo, route string) (RouteState, error) {
	var out RouteState
	err := s.quiet(func(tx Tx) error {
		var err error
		out, _, err = tx.Route(repo, route)
		return err
	})
	return out, err
}

// quiet runs fn under the lock in one transaction and wakes no wait: for
// reads, and for bookkeeping that changes no answer.
func (s *Service) quiet(fn func(Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.Tx(fn)
}

// Approve records an execution's place in an approved pair, under the policy
// it was launched with, and the route folder where records are kept.
func (s *Service) Approve(e runtime.Execution, folder string) error {
	return s.tx(func(tx Tx) error {
		if folder != "" {
			rs, _, err := tx.Route(e.Repo, e.Route)
			if err != nil {
				return err
			}
			if rs.Folder != folder {
				rs.Repo, rs.Route, rs.Folder = e.Repo, e.Route, folder
				if err := tx.PutRoute(rs); err != nil {
					return err
				}
			}
		}
		return tx.PutApproval(Approval{Execution: e.ID, Repo: e.Repo, Route: e.Route, PolicyDigest: e.PolicyDigest, At: s.now()})
	})
}

// Withdraw removes an execution's approval: its ordinary notices are held.
func (s *Service) Withdraw(execution string) error {
	return s.tx(func(tx Tx) error { return tx.DeleteApproval(execution) })
}

// --- reading an execution's notices ---

// List returns an execution's notices in issue order.
func (s *Service) List(execution string) ([]Notice, error) {
	var out []Notice
	err := s.quiet(func(tx Tx) error {
		var err error
		out, err = tx.Notices(execution)
		return err
	})
	return out, err
}

// RouteList returns a route's notices in issue order.
func (s *Service) RouteList(repo, route string) ([]Notice, error) {
	var out []Notice
	err := s.quiet(func(tx Tx) error {
		var err error
		out, err = tx.RouteNotices(repo, route)
		return err
	})
	return out, err
}

// OpenClaim returns an execution's open ordinary claim. The daemon tells a
// turn under way by it, not by the program's status (spec section 12).
func (s *Service) OpenClaim(execution string) (Notice, bool, error) {
	list, err := s.List(execution)
	for _, n := range list {
		if n.Open() && n.Ordinary() {
			return n, true, err
		}
	}
	return Notice{}, false, err
}

// HasIssued reports an ordinary notice issued and not claimed.
func (s *Service) HasIssued(execution string) (bool, error) {
	list, err := s.List(execution)
	for _, n := range list {
		if n.State == StateIssued && n.Ordinary() {
			return true, err
		}
	}
	return false, err
}

// MarkRecoveryDue makes an execution's open claim due for recovery: after a
// pause that interrupted it (resumed true), or by a person's order.
func (s *Service) MarkRecoveryDue(execution string, resumed bool) (bool, error) {
	found := false
	err := s.tx(func(tx Tx) error {
		list, err := tx.Notices(execution)
		if err != nil {
			return err
		}
		for _, n := range list {
			if n.Open() {
				found = true
				n.RecoveryDue = true
				n.ResumedSince = n.ResumedSince || resumed
				if err := tx.PutNotice(n); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return found, err
}

// ReturnExecution returns every notice of an execution that ended or was
// replaced before it was handled. A returned notice is not served again.
func (s *Service) ReturnExecution(execution, reason string) ([]Notice, error) {
	var out []Notice
	err := s.tx(func(tx Tx) error {
		list, err := tx.Notices(execution)
		if err != nil {
			return err
		}
		for _, n := range list {
			if n.State != StateIssued && n.State != StateClaimed {
				continue
			}
			n.State, n.ReturnedAt, n.ReturnReason = StateReturned, s.now(), reason
			if err := tx.PutNotice(n); err != nil {
				return err
			}
			out = append(out, n)
		}
		return nil
	})
	for _, n := range out {
		s.cfg.Emit(Event{Kind: EventReturned, Repo: n.Repo, Route: n.Route, Execution: n.Execution, Notice: n.ID, Reason: reason})
	}
	return out, err
}

// Reissue gives a replacement execution a new notice for each ordinary notice
// its predecessor left returned, with the old processing record attached. A
// final report is never reissued.
func (s *Service) Reissue(ctx context.Context, from string, to string, issuer string) ([]Notice, error) {
	old, err := s.List(from)
	if err != nil {
		return nil, err
	}
	var out []Notice
	for _, n := range old {
		if n.State != StateReturned || !n.Ordinary() {
			continue
		}
		prev := "notice " + n.ID
		if n.RecordPath != "" {
			prev += ", processing record " + n.RecordPath
		}
		content := n.Content
		if n.Token != "" {
			prev = "This carries on notice " + n.ID + ", which your predecessor claimed and did not acknowledge; read " + prevRecord(n) + " before you act, and do not repeat work it records."
		}
		got, err := s.Issue(ctx, IssueRequest{Key: "reissue:" + n.ID + ":" + to, Kind: n.Kind, Execution: to, Content: content, Issuer: issuer, Previous: prev})
		if err != nil {
			return out, err
		}
		out = append(out, got)
	}
	return out, nil
}

// readContent includes the predecessor's processing record without reducing
// the documented content limit or recursively copying earlier payloads.
func readContent(n Notice) string {
	if n.Previous != "" {
		return n.Previous + "\n\n" + n.Content
	}
	return n.Content
}

func prevRecord(n Notice) string {
	if n.RecordPath == "" {
		return "its processing record"
	}
	return n.RecordPath
}

// --- the worker's calls ---

// WaitRequest is one poll of a `herdsman notice wait` command.
type WaitRequest struct {
	WaitID     string
	Generation int64
	After      string
}

// WaitAnswer is a poll's answer.
type WaitAnswer struct {
	Answer     string
	Generation int64
	Notice     Notice
	// MayContinue is false for a recovery answer under a hold or while
	// stopping (never given) and for a final report's neighbour claim.
	MayContinue bool
	Reason      string
	Next        string
}

// Read and Ack answers say the next command to run.
func nextRead(id string) string { return "herdsman notice read " + id }

const nextWait = "herdsman notice wait"

func nextWaitAfter(id string) string { return "herdsman notice wait --after " + id }

// own settles which command owns an execution's wait (spec section 10, "Wait
// limits"): a first call asks for the wait and is given the next generation,
// at least GenerationGap after the last; a renewal never makes an owner; a
// lower generation, or an id with no record that renews, is replaced every
// time; a repeated first call is answered from its record.
func (s *Service) own(ctx context.Context, exec string, req WaitRequest) (int64, bool, error) {
	for {
		var gen int64
		var replaced bool
		var sleep time.Duration
		err := s.quiet(func(tx Tx) error {
			recs, err := tx.Waits(exec)
			if err != nil {
				return err
			}
			var owner WaitRecord
			var mine *WaitRecord
			for i, r := range recs {
				if r.Generation > owner.Generation {
					owner = r
				}
				if r.WaitID == req.WaitID {
					mine = &recs[i]
				}
			}
			if req.Generation != 0 {
				if mine == nil || mine.Generation != req.Generation {
					replaced = true
					return nil
				}
				gen, replaced = mine.Generation, mine.Generation < owner.Generation
				return nil
			}
			if mine != nil {
				gen, replaced = mine.Generation, mine.Generation < owner.Generation
				return nil
			}
			if !owner.Created.IsZero() {
				if left := owner.Created.Add(s.cfg.GenerationGap).Sub(s.now()); left > 0 {
					sleep = left
					return nil
				}
			}
			gen = owner.Generation + 1
			if err := tx.PutWait(WaitRecord{Execution: exec, WaitID: req.WaitID, Generation: gen, Created: s.now()}); err != nil {
				return err
			}
			return nil
		})
		if err != nil || sleep == 0 {
			return gen, replaced, err
		}
		if err := s.cfg.Sleep(ctx, sleep); err != nil {
			return 0, false, err
		}
	}
}

// KeepWaitRecords is how many wait ids a store keeps per execution, the
// newest by generation. An id pruned that way renews as replaced, which is
// right: its generation is lower than the owner's.
const KeepWaitRecords = keepWaitRecords

var errReplaced = errors.New("notice: replaced")

// attach makes p the execution's attached poll, ending an older one: a
// lower generation is replaced, the same one (a renewal that arrived before
// the old poll noticed its caller left) ends empty.
func (s *Service) attach(p *poll) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return &Refusal{Gate: "stopping", Detail: "the daemon is stopping; connect again"}
	}
	if old := s.attached[p.exec]; old != nil {
		if old.gen < p.gen {
			old.end <- AnswerReplaced
		} else if old.gen > p.gen {
			return errReplaced
		} else {
			old.end <- AnswerNone
		}
		delete(s.attached, p.exec)
	}
	if len(s.attached) >= s.cfg.MaxWaits {
		return ErrBusy
	}
	s.attached[p.exec] = p
	s.notWaiting[p.exec] = false
	return nil
}

// detach frees the poll's slot. A poll that answered its caller keeps the
// execution's place for the renewal window, empty or not: a wait command
// exits on an answer and the agent starts the next one after it reads, so
// the execution still holds its wait in between. A lost caller, a replaced
// poll and a stopped execution keep nothing.
func (s *Service) detach(p *poll, kept bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attached[p.exec] == p {
		delete(s.attached, p.exec)
	}
	until := s.now()
	if kept {
		until = until.Add(s.cfg.Renewal)
	}
	if until.After(s.heldUntil[p.exec]) {
		s.heldUntil[p.exec] = until
	}
}

// Holding reports whether an execution holds a wait: a poll attached now, or
// within the renewal window after one that answered its caller.
func (s *Service) Holding(exec string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.holdingLocked(exec)
}

func (s *Service) holdingLocked(exec string) bool {
	if s.attached[exec] != nil {
		return true
	}
	return s.now().Before(s.heldUntil[exec])
}

// Waits is how many waits are attached now.
func (s *Service) Waits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.attached)
}

// MaxWaits is the wait budget.
func (s *Service) MaxWaits() int { return s.cfg.MaxWaits }

// Wait answers one poll of the caller's wait. The caller is an execution the
// daemon identified by process. It announces only what would pass its claim;
// otherwise the poll stays attached until a gate changes or it times out.
func (s *Service) Wait(ctx context.Context, caller runtime.Execution, req WaitRequest) (WaitAnswer, error) {
	if req.WaitID == "" || len(req.WaitID) > 64 {
		return WaitAnswer{}, errors.New("notice: a wait needs a wait id of at most 64 characters")
	}
	gen, replaced, err := s.own(ctx, caller.ID, req)
	if err != nil {
		return WaitAnswer{}, err
	}
	if replaced {
		return WaitAnswer{Answer: AnswerReplaced, Generation: gen, Reason: "a newer wait owns this execution", Next: ""}, nil
	}
	p := &poll{exec: caller.ID, gen: gen, end: make(chan string, 1)}
	if err := s.attach(p); errors.Is(err, errReplaced) {
		return WaitAnswer{Answer: AnswerReplaced, Generation: gen, Reason: "a newer wait owns this execution"}, nil
	} else if err != nil {
		return WaitAnswer{}, err
	}
	kept := true
	defer func() { s.detach(p, kept) }()
	timer := time.NewTimer(s.cfg.Poll)
	defer timer.Stop()
	expired := false
	for {
		changed := s.watch()
		ans, ready, err := s.next(ctx, caller.ID, req.After, gen, expired)
		if err != nil {
			return WaitAnswer{}, err
		}
		if ready {
			kept = ans.Answer != AnswerReplaced && ans.Answer != AnswerStopped
			return ans, nil
		}
		if expired {
			return WaitAnswer{Answer: AnswerNone, Generation: gen, Next: nextWait}, nil
		}
		select {
		case <-changed:
		case <-timer.C:
			expired = true
		case forced := <-p.end:
			return WaitAnswer{Answer: forced, Generation: gen, Reason: "another wait of this execution took its place"}, nil
		case <-s.stopped:
			return WaitAnswer{Answer: AnswerNone, Generation: gen, Reason: "the daemon is stopping; connect again", Next: nextWait}, nil
		case <-ctx.Done():
			kept = false
			return WaitAnswer{}, ctx.Err()
		}
	}
}

// next looks at an execution's notices for the poll: a final report first
// while stopping, nothing under a hold, an open claim as a recovery answer
// (the first at once, later ones at the end of a poll), else the first issued
// notice when it would pass its claim.
func (s *Service) next(ctx context.Context, exec, after string, gen int64, expired bool) (WaitAnswer, bool, error) {
	e, err := s.execution(ctx, exec)
	if err != nil {
		return WaitAnswer{}, false, err
	}
	if e.Desired != runtime.DesiredRunning {
		return WaitAnswer{Answer: AnswerStopped, Generation: gen, Reason: "the execution was stopped"}, true, nil
	}
	var out WaitAnswer
	var stuck *Event
	ready := false
	err = s.quiet(func(tx Tx) error {
		list, err := tx.Notices(exec)
		if err != nil {
			return err
		}
		rs, _, err := tx.Route(e.Repo, e.Route)
		if err != nil {
			return err
		}
		for _, n := range list {
			if n.Kind == KindFinalReport && n.State == StateIssued && s.gate(tx, e, n, true) == nil {
				out, ready = WaitAnswer{Answer: AnswerNotice, Generation: gen, Notice: n, MayContinue: true, Next: nextRead(n.ID)}, true
				return nil
			}
		}
		if rs.Stopping || rs.Held {
			return nil // no recovery answer while the work may not go on
		}
		for _, n := range list {
			if !n.Open() || !n.Ordinary() {
				continue
			}
			if after == n.ID && !n.RecoveryDue {
				return nil
			}
			if n.RecoveryAnswers > 0 && !expired {
				return nil // a later recovery answer comes at the end of a poll
			}
			n.RecoveryAnswers++
			isStuck := n.RecoveryAnswers >= StuckAfter && !n.StuckReported
			if isStuck {
				n.StuckReported = true
			}
			if err := tx.PutNotice(n); err != nil {
				return err
			}
			if isStuck {
				stuck = &Event{Kind: EventClaimStuck, Repo: n.Repo, Route: n.Route, Execution: exec, Notice: n.ID,
					Detail: fmt.Sprintf("the claim was offered %d times with no read and no acknowledgement", n.RecoveryAnswers)}
			}
			out, ready = WaitAnswer{Answer: AnswerRecovery, Generation: gen, Notice: n, MayContinue: true, Next: nextRead(n.ID)}, true
			return nil
		}
		for _, n := range list {
			if n.State != StateIssued || !n.Ordinary() {
				continue
			}
			if s.gate(tx, e, n, true) == nil {
				out, ready = WaitAnswer{Answer: AnswerNotice, Generation: gen, Notice: n, MayContinue: true, Next: nextRead(n.ID)}, true
			}
			return nil // the order rule: a held notice keeps the later ones behind it
		}
		return nil
	})
	if stuck != nil {
		s.cfg.Emit(*stuck)
	}
	return out, ready, err
}

// ReadAnswer is Read's answer.
type ReadAnswer struct {
	Notice        Notice
	Content       string
	Token         string
	ClaimedBefore bool
	MayContinue   bool
	Refused       string
	Detail        string
	Next          string
}

// Read claims a notice of the caller, or returns its claim again. Every gate
// is checked in the transaction that makes the claim; a refusal leaves the
// notice issued and tells the caller to wait again.
func (s *Service) Read(ctx context.Context, caller runtime.Execution, id string) (ReadAnswer, error) {
	e, err := s.execution(ctx, caller.ID)
	if err != nil {
		return ReadAnswer{}, err
	}
	var out ReadAnswer
	claimed := false
	err = s.tx(func(tx Tx) error {
		list, err := tx.Notices(e.ID)
		if err != nil {
			return err
		}
		var target *Notice
		if id != "" {
			for i := range list {
				if list[i].ID == id {
					target = &list[i]
				}
			}
			if target == nil {
				return ErrUnknown
			}
		} else {
			// A stop report preempts ordinary work, including an open claim.
			for i := range list {
				if list[i].Kind == KindFinalReport && (list[i].State == StateIssued || list[i].Open()) {
					target = &list[i]
					break
				}
			}
			if target == nil {
				for i := range list {
					if list[i].Open() && list[i].Ordinary() {
						target = &list[i]
						break
					}
				}
			}
			if target == nil {
				for i := range list {
					if list[i].State == StateIssued && list[i].Ordinary() {
						target = &list[i]
						break
					}
				}
			}
			if target == nil {
				out = ReadAnswer{Next: s.nextInstruction(e), Detail: "no notice waits for this execution"}
				return nil
			}
		}
		n := *target
		switch n.State {
		case StateHandled:
			out = ReadAnswer{Notice: n, Next: s.nextInstruction(e), Detail: "handled with outcome " + n.Outcome + "; there is nothing to do"}
			return nil
		case StateReturned:
			out = ReadAnswer{Notice: n, Refused: GateReturned, Next: s.nextInstruction(e), Detail: "returned: " + n.ReturnReason}
			return nil
		case StateClaimed:
			// A repeat read: the same token and content, marked as
			// claimed before. It is not permission to do the work again.
			n.RecoveryAnswers, n.RecoveryDue = 0, false
			if err := tx.PutNotice(n); err != nil {
				return err
			}
			refused := s.gate(tx, e, n, true)
			may := refused == nil
			out = ReadAnswer{Notice: n, Content: readContent(n), Token: n.Token, ClaimedBefore: true, MayContinue: may}
			if may {
				out.Next = "read your processing record " + n.RecordPath + ", go on from where it stops or close the claim with: herdsman notice ack " + n.ID + " --token " + n.Token + " --outcome interrupted --record <how far it went>"
			} else {
				out.Next = s.nextInstruction(e)
				if s.cfg.Queued == nil || !s.cfg.Queued(e.HarnessKind) {
					out.Next = nextWaitAfter(n.ID)
				}
				out.Refused, out.Detail = refused.Gate, refused.Detail
			}
			return nil
		}
		if r := s.gate(tx, e, n, true); r != nil {
			out = ReadAnswer{Notice: n, Refused: r.Gate, Detail: r.Detail, Next: s.nextInstruction(e)}
			return nil
		}
		n.State, n.Token, n.ClaimedAt = StateClaimed, randomHex(16), s.now()
		n.RecordPath = s.cfg.RecordPath(n)
		n.HeldSince, n.HeldGate, n.HeldReported = time.Time{}, "", false
		if err := tx.PutNotice(n); err != nil {
			return err
		}
		claimed = true
		out = ReadAnswer{Notice: n, Content: readContent(n), Token: n.Token, MayContinue: true,
			Next: "start `" + nextWaitAfter(n.ID) + "` in the background before the work, keep notes in " + n.RecordPath +
				", then run: herdsman notice ack " + n.ID + " --token " + n.Token + " --outcome completed --record <what you did>"}
		if s.cfg.Queued != nil && s.cfg.Queued(e.HarnessKind) {
			out.Next = "keep notes in " + n.RecordPath + ", then run: herdsman notice ack " + n.ID + " --token " + n.Token + " --outcome completed --record <what you did>"
		}
		return nil
	})
	if claimed && err == nil {
		s.cfg.Trace(TraceClaim, out.Notice, out.Notice.IssuedAt, out.Notice.ClaimedAt)
	}
	return out, err
}

// AckAnswer is Ack's answer.
type AckAnswer struct {
	Notice   Notice
	Repeated bool
	Next     string
}

// Ack closes the caller's claim with an outcome and a processing record. It
// passes no gate: it records what happened, under a hold and while stopping
// too. An outcome other than completed goes back to the issuer.
func (s *Service) Ack(ctx context.Context, caller runtime.Execution, id, token, outcome, record string) (AckAnswer, error) {
	if !ValidOutcome(outcome) {
		return AckAnswer{}, fmt.Errorf("notice: outcome %q is not completed, interrupted or abandoned", outcome)
	}
	if strings.TrimSpace(record) == "" || len(record) > MaxRecord {
		return AckAnswer{}, ErrRecord
	}
	var out AckAnswer
	var continuation Notice

	err := s.tx(func(tx Tx) error {
		n, ok, err := tx.NoticeByID(id)
		if err != nil {
			return err
		}
		if !ok || n.Execution != caller.ID || n.Generation != caller.Generation {
			return ErrUnknown
		}
		switch n.State {
		case StateHandled:
			if n.Token != token {
				return ErrToken
			}
			if n.Outcome != outcome {
				return ErrOutcome
			}
			out = AckAnswer{Notice: n, Repeated: true, Next: s.nextInstruction(caller)}
			return nil
		case StateClaimed:
		default:
			return ErrNotClaim
		}
		if n.Token != token {
			return ErrToken
		}
		n.State, n.Outcome, n.Record, n.HandledAt = StateHandled, outcome, record, s.now()
		n.RecoveryDue, n.RecoveryAnswers = false, 0
		if err := tx.PutNotice(n); err != nil {
			return err
		}
		out = AckAnswer{Notice: n, Next: s.nextInstruction(caller)}
		if outcome != OutcomeCompleted {
			if r, _ := RuleOf(n.Kind); r.FollowUp == FollowReissue {
				// The continuation is part of the acknowledgement transaction.
				// It can remain issued while held; Read applies all claim gates.
				// Keeping the prior record separate preserves both size limits.
				f := s.draft(caller, "after:"+n.ID, n.Kind, n.Content, n.Issuer)
				f.Folder = n.Folder
				f.Previous = "This carries on notice " + n.ID + ", acknowledged " + outcome +
					". Continue from this processing record; do not repeat completed work:\n" + n.Record
				seq, err := tx.NextSeq()
				if err != nil {
					return err
				}
				f.Seq = seq
				continuation = f
				if err := tx.PutNotice(f); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	if continuation.ID != "" {
		s.cfg.Trace(TraceIssue, continuation, continuation.IssuedAt, s.now())
	}
	n := out.Notice
	if !out.Repeated {
		s.cfg.Trace(TraceHandle, n, n.ClaimedAt, n.HandledAt)
	}
	if !out.Repeated && outcome != OutcomeCompleted {
		s.cfg.Emit(Event{Kind: EventOutcome, Repo: n.Repo, Route: n.Route, Execution: n.Execution, Notice: n.ID, Outcome: outcome, Reason: n.Kind})
	}
	return out, err
}

// --- the periodic look ---

// Tick looks at every running execution once: notices held past the grace
// period, waits not held, open claims whose turn ended with no
// acknowledgement, and notices that wait for a person on an attended
// execution. It raises each event once per episode.
func (s *Service) Tick(ctx context.Context) error {
	execs, err := s.execs.Running(ctx)
	if err != nil {
		return err
	}
	sort.Slice(execs, func(i, j int) bool { return execs[i].ID < execs[j].ID })
	var events []Event
	now := s.now()
	wake := false
	err = s.quiet(func(tx Tx) error {
		defer func() {
			if wake {
				s.broadcast()
			}
		}()
		for _, e := range execs {
			if e.Backend != runtime.BackendCore || e.Evidence.PID <= 0 {
				continue
			}
			list, err := tx.Notices(e.ID)
			if err != nil {
				return err
			}
			attended := s.cfg.Attended(e.HarnessKind)
			var open *Notice
			for i := range list {
				if list[i].Open() && list[i].Ordinary() {
					open = &list[i]
				}
			}
			// An open claim whose turn ended: recovery is due after the
			// grace period (a program that is not working).
			if open != nil {
				if e.Observation.Program == runtime.ProgramWorking {
					delete(s.restSince, e.ID)
				} else if since, ok := s.restSince[e.ID]; !ok {
					s.restSince[e.ID] = now
				} else if now.Sub(since) >= s.cfg.RecoveryGrace && !open.RecoveryDue && (!attended || s.cfg.Queued != nil && s.cfg.Queued(e.HarnessKind)) {
					open.RecoveryDue, wake = true, true
					if err := tx.PutNotice(*open); err != nil {
						return err
					}
				}
				if attended && e.Observation.Program == runtime.ProgramIdle && !open.Interrupted {
					open.Interrupted = true
					if err := tx.PutNotice(*open); err != nil {
						return err
					}
					events = append(events, Event{Kind: EventNeedsAttention, Repo: e.Repo, Route: e.Route, Execution: e.ID, Notice: open.ID, Reason: AttentionInterrupted,
						Detail: "the harness was interrupted with a claim open; a person types the line in its pane", Line: AttendedLine})
				}
			} else {
				delete(s.restSince, e.ID)
			}
			// The first notice in order that waits: held, or announced to a
			// person for an attended execution.
			for i := range list {
				n := list[i]
				if n.State != StateIssued {
					continue
				}
				r := s.gate(tx, e, n, true)
				changed := false
				switch {
				case r == nil && attended && (s.cfg.Queued == nil || !s.cfg.Queued(e.HarnessKind)) && !n.Attention:
					n.Attention, changed = true, true
					events = append(events, Event{Kind: EventNeedsAttention, Repo: e.Repo, Route: e.Route, Execution: e.ID, Notice: n.ID, Reason: AttentionNoticeWaiting,
						Detail: "a notice waits for this attended execution; a person types the line in its pane", Line: AttendedLine})
				case r == nil && !n.HeldSince.IsZero():
					n.HeldSince, n.HeldGate, n.HeldReported, changed, wake = time.Time{}, "", false, true, true
				case r != nil && r.Gate != GateOrder:
					if n.HeldSince.IsZero() || n.HeldGate != r.Gate {
						n.HeldSince, n.HeldGate, n.HeldReported, changed = now, r.Gate, false, true
					} else if !n.HeldReported && now.Sub(n.HeldSince) >= s.cfg.HeldGrace {
						n.HeldReported, changed = true, true
						events = append(events, Event{Kind: EventHeld, Repo: e.Repo, Route: e.Route, Execution: e.ID, Notice: n.ID, Reason: r.Gate, Detail: r.Detail})
					}
				}
				if changed {
					if err := tx.PutNotice(n); err != nil {
						return err
					}
				}
				if n.Ordinary() {
					break
				}
			}
			// A wait-holding execution that holds no wait.
			if !attended && e.Observation.Readiness == runtime.ReadinessReady {
				if e.Observation.Program == runtime.ProgramWorking && open == nil {
					// In a turn with no wait: the count starts when it ends.
					if until := now; until.After(s.heldUntil[e.ID]) {
						s.heldUntil[e.ID] = until
					}
				}
				last := s.heldUntil[e.ID]
				if last.IsZero() {
					last = s.started
					s.heldUntil[e.ID] = last
				}
				if s.attached[e.ID] == nil && now.Sub(last) >= s.cfg.WaitGrace && !s.notWaiting[e.ID] {
					s.notWaiting[e.ID] = true
					events = append(events, Event{Kind: EventNotWaiting, Repo: e.Repo, Route: e.Route, Execution: e.ID,
						Detail: "the agent holds no `herdsman notice wait`; its notices stay issued until a person submits its start line again"})
				}
			}
		}
		return nil
	})
	for _, ev := range events {
		s.cfg.Emit(ev)
	}
	if err == nil && s.cfg.Queue != nil {
		for _, e := range execs {
			if s.cfg.Queued != nil && s.cfg.Queued(e.HarnessKind) {
				if queueErr := s.QueueWake(ctx, e.ID); queueErr != nil {
					err = errors.Join(err, queueErr)
				}
			}
		}
	}
	return err
}

// NotWaiting reports whether an execution was reported not_waiting and holds
// no wait since.
func (s *Service) NotWaiting(exec string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notWaiting[exec] && !s.holdingLocked(exec)
}

// CheckSubmit applies the four dispatch rules to a person's typed submit for
// an execution: approval and policy, exact identity, visibility, and a route
// that is neither held nor stopping (spec section 10, "Dispatch contract").
// The submit's own rules (if, a first status, one outstanding) are the
// caller's.
func (s *Service) CheckSubmit(ctx context.Context, execution string) error {
	e, err := s.execution(ctx, execution)
	if err != nil {
		return err
	}
	probe := Notice{Kind: KindDispatch, Repo: e.Repo, Route: e.Route, Execution: e.ID, Generation: e.Generation, Endpoint: e.EndpointID}
	return s.quiet(func(tx Tx) error {
		if r := s.gate(tx, e, probe, false); r != nil {
			return r
		}
		return nil
	})
}

// ByKey returns the notice an operation step issued under key.
func (s *Service) ByKey(key string) (Notice, bool, error) {
	var out Notice
	found := false
	err := s.quiet(func(tx Tx) error {
		var err error
		out, found, err = tx.NoticeByKey(key)
		return err
	})
	return out, found, err
}

// Emit raises an event of the runtime topic for a caller that has no
// emitter of its own, such as a route's pause or resume step.
func (s *Service) Emit(ev Event) { s.cfg.Emit(ev) }

// A queued adapter ends its turn; holding a poll cannot wake that harness.
func (s *Service) nextInstruction(e runtime.Execution) string {
	if s.cfg.Queued != nil && s.cfg.Queued(e.HarnessKind) {
		return "end your turn; the harness queue will wake you when another notice is claimable"
	}
	return nextWait
}
