package route

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

// The steps of a herdr-core route that issue notices or change what their
// gates read (spec sections 10 and 12). Nothing here types into a terminal.
const (
	effectCoreBrief    = "core.brief"
	effectCoreHold     = "core.hold"
	effectCoreUnhold   = "core.unhold"
	effectCoreResume   = "core.resume"
	effectCoreStopping = "core.stopping"
	effectCoreReport   = "core.final-report"
	effectCoreReports  = "core.reports"
	effectCoreRestart  = "core.restart"
)

// Statuses a core control step records for an agent, besides the pause
// outcomes of the core package.
const (
	statusRecovery      = "recovery_due"
	statusNoticeWaiting = "notice_waiting"
	statusContinue      = "continue_issued"
	statusNotWaiting    = "not_waiting"
	statusInterrupted   = "interrupted"
	statusResumePartial = "resume_partial"
)

func noticesOf(cp *core.Provider) (*notice.Service, error) {
	if cp.Notices == nil {
		return nil, errors.New("this daemon serves no notices: a herdr-core route needs them")
	}
	return cp.Notices, nil
}

// briefText is the first brief: the coordinator's role command, as a notice.
// It is the line the TUI typed; on core the agent pulls it.
func briefText(route, line string) string {
	return "Your role in route " + route + ": run this command and follow what it prints.\n\n" + line
}

// applyCoreBrief issues a coordinator's first brief once its pair is
// enrolled. On a resume after a stop it first gives the new execution what its
// predecessor left unhandled, with the old processing record.
func applyCoreBrief(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	cp, err := coreProvider(deps)
	if err != nil {
		return err
	}
	svc, err := noticesOf(cp)
	if err != nil {
		return err
	}
	rec, ok, err := cp.Records.ByIntent(ctx, d.Core.Launch.Intent)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s has no execution record for launch %s", d.Name, d.Core.Launch.Intent)
	}
	if d.Core.Reissue {
		all, err := cp.Records.List(ctx, d.Repo, d.Route)
		if err != nil {
			return err
		}
		for _, old := range all {
			if old.Agent == d.Name && old.ID != rec.ID && old.Desired == runtime.DesiredStopped {
				got, err := svc.Reissue(ctx, old.ID, rec.ID, d.Op)
				if err != nil {
					return err
				}
				for _, n := range got {
					if n.Kind == notice.KindBrief {
						return nil // the predecessor's brief carries on; no second one
					}
				}
			}
		}
	}
	_, err = cp.Deliver(ctx, runtime.Target{ExecutionID: rec.ID}, runtime.Delivery{Text: d.Core.Brief, Kind: notice.KindBrief, Key: briefKey(d), Issuer: d.Op})
	return err
}

func briefKey(d stepData) string { return "brief:" + d.Core.Launch.Intent }

func probeCoreBrief(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	cp, err := coreProvider(deps)
	if err != nil || cp.Notices == nil {
		return ops.Unknown, "no notice service", nil
	}
	if _, found, err := cp.Notices.ByKey(briefKey(d)); err != nil {
		return ops.Unknown, err.Error(), nil
	} else if found {
		return ops.Matches, "", nil
	}
	return ops.Absent, "", nil
}

// applyCoreHold persists the route's hold before any interrupt: from here no
// notice of the route can be claimed or announced.
func applyCoreHold(_ context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	cp, err := coreProvider(deps)
	if err != nil {
		return err
	}
	svc, err := noticesOf(cp)
	if err != nil {
		return err
	}
	return svc.Hold(d.Repo, d.Route, true, d.Op)
}

func probeCoreHold(_ context.Context, deps Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	cp, err := coreProvider(deps)
	if err != nil || cp.Notices == nil {
		return ops.Unknown, "no notice service", nil
	}
	rs, err := cp.Notices.RouteState(d.Repo, d.Route)
	if err != nil {
		return ops.Unknown, err.Error(), nil
	}
	if rs.Held && rs.HeldBy == d.Op {
		return ops.Matches, "", nil
	}
	return ops.Absent, "", nil
}

// applyCoreUnhold lifts the hold; the notices it held are announced in the
// order they were issued.
func applyCoreUnhold(_ context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	cp, err := coreProvider(deps)
	if err != nil {
		return err
	}
	svc, err := noticesOf(cp)
	if err != nil {
		return err
	}
	return svc.Hold(d.Repo, d.Route, false, "")
}

func probeCoreUnhold(_ context.Context, deps Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	cp, err := coreProvider(deps)
	if err != nil || cp.Notices == nil {
		return ops.Unknown, "no notice service", nil
	}
	rs, err := cp.Notices.RouteState(d.Repo, d.Route)
	if err != nil {
		return ops.Unknown, err.Error(), nil
	}
	if !rs.Held {
		return ops.Matches, "", nil
	}
	return ops.Absent, "", nil
}

// applyCoreResume restores one agent after a pause (spec section 12): an open
// claim becomes due for recovery and gets no continue prompt; an issued
// notice gets none either, since lifting the hold announces it; otherwise
// the continue prompt is issued as a notice. An execution that cannot be
// woken is reported, and the agent's row says resume_partial.
func applyCoreResume(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	cp, err := coreProvider(deps)
	if err != nil {
		return err
	}
	svc, err := noticesOf(cp)
	if err != nil {
		return err
	}
	recs, err := liveRecords(ctx, cp, d)
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		return writeRow(d, AgentRow{Name: d.Name, Status: StatusNotReached})
	}
	var statuses []string
	for _, rec := range recs {
		a, err := core.AdapterFor(rec.HarnessKind)
		if err != nil {
			return err
		}
		open, hasOpen, err := svc.OpenClaim(rec.ID)
		if err != nil {
			return err
		}
		status := ""
		switch issued, err := svc.HasIssued(rec.ID); {
		case err != nil:
			return err
		case hasOpen:
			if _, err := svc.MarkRecoveryDue(rec.ID, true); err != nil {
				return err
			}
			status = statusRecovery
		case issued:
			status = statusNoticeWaiting
		default:
			if _, err := cp.Deliver(ctx, runtime.Target{ExecutionID: rec.ID}, runtime.Delivery{Text: lastContinuePrompt, Kind: notice.KindContinue,
				Key: "continue:" + rec.ID + ":" + d.Op, Issuer: d.Op}); err != nil {
				return err
			}
			status = statusContinue
		}
		partial := ""
		switch {
		case a.HoldsWait && !svc.Holding(rec.ID):
			partial = statusNotWaiting
			svc.Emit(notice.Event{Kind: notice.EventNotWaiting, Repo: rec.Repo, Route: rec.Route, Execution: rec.ID,
				Detail: "the agent holds no wait; a person submits its start line again in its pane", Line: a.StartLine})
		case a.Attended && hasOpen && open.Interrupted:
			partial = statusInterrupted
			svc.Emit(notice.Event{Kind: notice.EventNeedsAttention, Repo: rec.Repo, Route: rec.Route, Execution: rec.ID, Reason: notice.AttentionInterrupted,
				Notice: open.ID, Detail: "the harness was interrupted; a person types the line in its pane", Line: notice.AttendedLine})
		}
		if partial != "" {
			status += ", " + partial + ", " + statusResumePartial
			svc.Emit(notice.Event{Kind: statusResumePartial, Repo: rec.Repo, Route: rec.Route, Execution: rec.ID, Reason: partial})
		}
		statuses = append(statuses, status)
	}
	return writeRow(d, AgentRow{Name: d.Name, Pane: recs[0].TerminalAddress, Status: strings.Join(statuses, "; ")})
}

// lastContinuePrompt is the continue prompt of a resume, with no task content.
const lastContinuePrompt = "The route was paused and is resumed. Go on with your work from where you stopped."

// applyCoreStopping puts the route in the stopping phase for this stop: from
// here ordinary notices and typed submits are refused, and only this stop's
// final-report notices are issued and claimed. The phase is not lifted.
func applyCoreStopping(_ context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	cp, err := coreProvider(deps)
	if err != nil {
		return err
	}
	svc, err := noticesOf(cp)
	if err != nil {
		return err
	}
	return svc.Stopping(d.Repo, d.Route, d.Op, time.Duration(d.Control.GraceMS)*time.Millisecond)
}

func probeCoreStopping(_ context.Context, deps Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	cp, err := coreProvider(deps)
	if err != nil || cp.Notices == nil {
		return ops.Unknown, "no notice service", nil
	}
	rs, err := cp.Notices.RouteState(d.Repo, d.Route)
	if err != nil {
		return ops.Unknown, err.Error(), nil
	}
	if rs.Stopping && rs.StopOp == d.Op {
		return ops.Matches, "", nil
	}
	return ops.Absent, "", nil
}

// reportRow is one agent's part of a stop's report requests.
type reportRow struct {
	Executions map[string]reportEntry `json:"executions"`
}

type reportEntry struct {
	Notice string `json:"notice,omitempty"`
	// Unavailable is the report_unavailable reason when none was issued.
	Unavailable string `json:"unavailable,omitempty"`
}

func reportPath(d stepData) string {
	return filepath.Join(controlDir(d.Folder), fmt.Sprintf("report-%d-%s.json", d.Control.Generation, d.Name))
}

func finalKey(op, exec string) string { return "final:" + op + ":" + exec }

// applyCoreReport issues the final-report notice to each running execution of
// an agent, one journaled step bound to this stop. One that cannot be asked
// is recorded as report_unavailable with the reason.
func applyCoreReport(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	cp, err := coreProvider(deps)
	if err != nil {
		return err
	}
	svc, err := noticesOf(cp)
	if err != nil {
		return err
	}
	recs, err := liveRecords(ctx, cp, d)
	if err != nil {
		return err
	}
	row := reportRow{Executions: map[string]reportEntry{}}
	for _, rec := range recs {
		a, err := core.AdapterFor(rec.HarnessKind)
		if err != nil {
			row.Executions[rec.ID] = reportEntry{Unavailable: "unqualified_harness"}
			continue
		}
		n, err := svc.IssueFinalReport(ctx, notice.FinalRequest{Key: finalKey(d.Op, rec.ID), Execution: rec.ID, StopOp: d.Op,
			Grace: time.Duration(d.Control.GraceMS) * time.Millisecond, HoldsWait: a.HoldsWait})
		if gate := notice.GateOf(err); gate != "" {
			row.Executions[rec.ID] = reportEntry{Unavailable: gate}
			continue
		}
		if err != nil {
			return err
		}
		row.Executions[rec.ID] = reportEntry{Notice: n.ID}
	}
	b, err := json.Marshal(row)
	if err != nil {
		return err
	}
	return writeFileNew(reportPath(d), b)
}

func probeReportRow(_ context.Context, _ Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	if _, err := os.Stat(reportPath(d)); err == nil {
		return ops.Matches, "", nil
	}
	return ops.Absent, "", nil
}

func reportsPath(d stepData) string {
	return filepath.Join(controlDir(d.Folder), fmt.Sprintf("reports-%d.done", d.Control.Generation))
}

// applyCoreReports waits until every asked execution has reported or its
// deadline has passed. Nothing is typed meanwhile. After a daemon restart the
// deadlines are moved once, since no agent could reach the daemon while it
// was down.
func applyCoreReports(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	cp, err := coreProvider(deps)
	if err != nil {
		return err
	}
	svc, err := noticesOf(cp)
	if err != nil {
		return err
	}
	every := deps.PollEvery
	if every == 0 {
		every = 200 * time.Millisecond
	}
	for {
		finals, err := svc.ConfirmDeadlines(d.Repo, d.Route)
		if err != nil {
			return err
		}
		waiting := 0
		for _, n := range finals {
			if n.State == notice.StateHandled || n.State == notice.StateReturned {
				continue
			}
			if time.Now().Before(n.Deadline) {
				waiting++
			}
		}
		if waiting == 0 {
			return writeFileNew(reportsPath(d), nil)
		}
		if deps.Poll != nil {
			deps.Poll()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
}

func probeReports(_ context.Context, _ Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	if _, err := os.Stat(reportsPath(d)); err == nil {
		return ops.Matches, "", nil
	}
	return ops.Absent, "", nil
}

// reportOutcome is an execution's report outcome at its termination.
func reportOutcome(svc *notice.Service, d stepData, rec runtime.Execution) (string, string) {
	if b, err := os.ReadFile(reportPath(d)); err == nil {
		var row reportRow
		if json.Unmarshal(b, &row) == nil {
			if e, ok := row.Executions[rec.ID]; ok && e.Unavailable != "" {
				return notice.ReportUnavailable, e.Unavailable
			}
		}
	}
	n, found, err := svc.ByKey(finalKey(d.Op, rec.ID))
	switch {
	case err != nil || !found:
		return notice.ReportUnavailable, "identity_unknown"
	case n.State == notice.StateHandled && !n.HandledAt.After(n.Deadline):
		return notice.ReportReported, n.Outcome + ", record " + n.RecordPath
	case n.State == notice.StateClaimed:
		return notice.ReportTimeout, "claimed and not acknowledged"
	case n.State == notice.StateHandled:
		return notice.ReportTimeout, "acknowledged after the deadline"
	}
	return notice.ReportTimeout, "unclaimed"
}

// restartCore clears a stopped route's stopping phase before a resume
// launches its agents again.
func applyCoreRestart(_ context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	cp, err := coreProvider(deps)
	if err != nil {
		return err
	}
	svc, err := noticesOf(cp)
	if err != nil {
		return err
	}
	return svc.Restart(d.Repo, d.Route)
}

func probeCoreRestart(_ context.Context, deps Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	cp, err := coreProvider(deps)
	if err != nil || cp.Notices == nil {
		return ops.Unknown, "no notice service", nil
	}
	rs, err := cp.Notices.RouteState(d.Repo, d.Route)
	if err != nil {
		return ops.Unknown, err.Error(), nil
	}
	if !rs.Stopping && !rs.Held {
		return ops.Matches, "", nil
	}
	return ops.Absent, "", nil
}

// waitBudget refuses, in preflight, a launch that would pass the daemon's
// wait budget: no agent is started that could not wait (spec section 10).
func waitBudget(ctx context.Context, cp *core.Provider, intent string) error {
	if cp.Notices == nil {
		return nil
	}
	all, err := cp.Records.List(ctx, "", "")
	if err != nil {
		return err
	}
	live := 0
	for _, e := range all {
		if e.Backend == runtime.BackendCore && e.Desired == runtime.DesiredRunning && e.LaunchIntent != intent {
			live++
		}
	}
	if budget := cp.Notices.MaxWaits(); live+1 > budget {
		return &core.Error{Code: "wait_budget", Msg: fmt.Sprintf("the daemon runs %d managed executions and its wait budget is %d: this launch could not hold its notice wait; stop a route first", live, budget)}
	}
	return nil
}
