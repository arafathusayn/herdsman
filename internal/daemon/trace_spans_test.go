package daemon

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	"github.com/arafathusayn/herdsman/internal/trace"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// readSpans reads the spans a route folder's own log keeps.
func readSpans(t *testing.T, folder string) []trace.Span {
	t.Helper()
	f, err := os.Open(filepath.Join(folder, "trace", "spans.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []trace.Span
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		var s trace.Span
		if err := json.Unmarshal(scan.Bytes(), &s); err != nil {
			t.Fatalf("%s: %v", scan.Text(), err)
		}
		out = append(out, s)
	}
	return out
}

func spansOf(spans []trace.Span, kind string) []trace.Span {
	var out []trace.Span
	for _, s := range spans {
		if s.Kind == kind {
			out = append(out, s)
		}
	}
	return out
}

// Traces: a route on herdr-core records its launch steps, each notice's
// issue, claim and handling, and each typed submit as spans of the route's
// own trace, which the trace command reads. No span carries a notice's
// content or the text typed.
func TestCoreStepsAreSpansOfTheRoutesTrace(t *testing.T) {
	r := newWorkerRig(t, NoticeConfig{})
	c := routeClient(t, r.h)
	ctx := testCtxD(t)
	start := waitHalt(t, c, filepath.Join(r.h.ph, "proj"))
	folder := start.GetFolder()
	if folder == "" {
		t.Fatalf("no folder: %+v", start)
	}

	// Launch: every step of both agents, the visibility wait as a failure.
	launch := spansOf(readSpans(t, folder), trace.KindLaunch)
	names := map[string]int{}
	for _, s := range launch {
		names[s.Name]++
		if s.Exit == nil || s.Args == "" {
			t.Errorf("launch span without exit or agent: %+v", s)
		}
	}
	for _, step := range []string{"core.preflight", "core.spawn", "core.register", "core.visibility", "core.ready"} {
		if names[step] == 0 {
			t.Errorf("no %s span in %v", step, names)
		}
	}
	if !slices.ContainsFunc(launch, func(s trace.Span) bool { return s.Name == "core.visibility" && *s.Exit == 1 }) {
		t.Errorf("the visibility wait is not a failed step: %+v", launch)
	}

	// Notices: issue, claim and handling of one notice.
	target := r.execs[0]
	svc := r.h.d.noticeSvc()
	if err := svc.Approve(target, folder); err != nil {
		t.Fatal(err)
	}
	n, err := svc.Issue(ctx, notice.IssueRequest{Key: "trace-1", Kind: notice.KindBrief, Execution: target.ID, Content: "secret brief", Issuer: "op-test"})
	if err != nil {
		t.Fatal(err)
	}
	read, err := svc.Read(ctx, target, n.ID)
	if err != nil || read.Token == "" {
		t.Fatalf("claim: %+v %v", read, err)
	}
	if _, err := svc.Ack(ctx, target, n.ID, read.Token, notice.OutcomeCompleted, "done"); err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, s := range spansOf(readSpans(t, folder), trace.KindNotice) {
		if strings.Contains(s.Args, n.ID) {
			steps = append(steps, s.Name)
		}
	}
	if !slices.Equal(steps, []string{"issue brief", "claim brief", "handle brief"}) {
		t.Errorf("notice spans %v", steps)
	}

	// A typed submit.
	r.h.d.coreRT().cfg.Person = func(route.Caller) error { return nil }
	r.w.Procs.Put(os.Getpid(), 1, "zsh")
	res, err := c.Runtime.Submit(ctx, &daemonv1.RuntimeServiceSubmitRequest{ExecutionId: target.ID, Text: "go on now", Pid: int64(os.Getpid()), Confirmed: true})
	if err != nil || res.GetState() != core.SubmitTyped {
		t.Fatalf("submit: %+v %v", res, err)
	}
	submits := spansOf(readSpans(t, folder), trace.KindSubmit)
	if len(submits) != 1 || submits[0].Name != "submit typed" || submits[0].Exit == nil || *submits[0].Exit != 0 || !strings.Contains(submits[0].Args, res.GetSubmitId()) {
		t.Errorf("submit spans %+v", submits)
	}

	raw, err := os.ReadFile(filepath.Join(folder, "trace", "spans.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret brief", "go on now"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the trace carries %q", secret)
		}
	}
	// The trace command reads them under the route's root span.
	got, err := trace.Collect(trace.Sources{Route: folder})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, s := range got.Spans {
		kinds[s.Kind] = true
	}
	for _, k := range []string{trace.KindRoute, trace.KindLaunch, trace.KindNotice, trace.KindSubmit} {
		if !kinds[k] {
			t.Errorf("the trace has no %s span: %v", k, kinds)
		}
	}
}
