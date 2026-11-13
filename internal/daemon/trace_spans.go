package daemon

import (
	"context"
	"encoding/json"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	"github.com/arafathusayn/herdsman/internal/trace"
)

// The launch steps of a route on herdr-core whose time the route's trace
// records, one span each (spec section 8). A pause and a stop are control
// verbs, not launch steps.
var coreLaunchEffects = []string{"core.preflight", "core.spawn", "core.register", "core.visibility", "core.ready", "core.attach", "core.enroll"}

// tracedLaunch records each run of a launch step as a span of the route's
// trace. The trace is evidence, never a gate: a span that cannot be written
// is logged and the step's own result stands.
type tracedLaunch struct {
	inner ops.Effect
	d     *Daemon
}

// Apply implements ops.Effect.
func (t tracedLaunch) Apply(ctx context.Context, s ops.Step) error {
	start := t.d.now()
	err := t.inner.Apply(ctx, s)
	var data struct {
		Folder string `json:"folder"`
		Name   string `json:"name"`
	}
	if json.Unmarshal([]byte(s.Spec), &data) != nil || data.Folder == "" {
		return err
	}
	exit := 0
	if err != nil {
		exit = 1
	}
	t.d.span(data.Folder, trace.Span{Kind: trace.KindLaunch, Name: s.Effect, Start: start, DurationMS: spanMS(t.d.now().Sub(start)), Exit: &exit, Args: data.Name})
	return err
}

// Probe implements ops.Effect.
func (t tracedLaunch) Probe(ctx context.Context, s ops.Step) (ops.Verdict, string, error) {
	return t.inner.Probe(ctx, s)
}

// traceLaunches wraps the launch steps among effects.
func (d *Daemon) traceLaunches(effects map[string]ops.Effect) {
	for _, name := range coreLaunchEffects {
		if e, ok := effects[name]; ok {
			effects[name] = tracedLaunch{inner: e, d: d}
		}
	}
}

// traceNotice records a notice's issue, claim or handling as a span of its
// route's trace. The span names the notice, its kind and its agent, never
// its content.
func (d *Daemon) traceNotice(step string, n notice.Notice, start, end time.Time) {
	if n.Folder == "" || start.IsZero() || end.Before(start) {
		return
	}
	span := trace.Span{Kind: trace.KindNotice, Name: step + " " + n.Kind, Start: start, DurationMS: spanMS(end.Sub(start)), Args: n.ID + " " + n.Agent}
	if step == notice.TraceHandle {
		span.Args += " " + n.Outcome
	}
	d.span(n.Folder, span)
}

// traceSubmit records a typed submit as a span of its route's trace: from the
// person's request to the state it reached. The span names the submit, its
// execution and its state, never the text typed.
func (d *Daemon) traceSubmit(start time.Time, rec core.SubmitRecord) {
	folder := d.executionFolder(rec.Execution)
	if folder == "" {
		return
	}
	span := trace.Span{Kind: trace.KindSubmit, Name: "submit " + rec.State, Start: start, DurationMS: spanMS(d.now().Sub(start)), Args: rec.ID + " " + rec.Execution}
	// Typed is a success and a refusal or a lost answer a failure; a submit
	// still under way has no exit yet.
	switch rec.State {
	case core.SubmitTyped:
		span.Exit = new(int)
	case core.SubmitRejectedBeforeWrite, core.SubmitUnresolved:
		one := 1
		span.Exit = &one
	}
	d.span(folder, span)
}

// executionFolder is the route folder of an execution, as its notices know it.
func (d *Daemon) executionFolder(execution string) string {
	svc := d.noticeSvc()
	if svc == nil {
		return ""
	}
	var row store.Execution
	found := false
	if err := d.ops.Read(func(c store.Conn) error {
		var err error
		row, found, err = store.ExecutionByID(c, execution)
		return err
	}); err != nil || !found {
		return ""
	}
	rs, err := svc.RouteState(row.Repo, row.Route)
	if err != nil {
		return ""
	}
	return rs.Folder
}

// span appends one span to a route folder's trace.
func (d *Daemon) span(folder string, s trace.Span) {
	if s.DurationMS < 0 {
		s.DurationMS = 0
	}
	if err := trace.Append(folder, s); err != nil {
		d.logf("trace: %v", err)
	}
}

func spanMS(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
