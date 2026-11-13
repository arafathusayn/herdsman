package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/status"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// status --route reads every runtime field from RuntimeService, unchanged,
// and its JSON carries them beside the route's lifecycle.
func TestRouteStatusCarriesTheRuntimeContract(t *testing.T) {
	e := &daemonv1.Execution{
		Ref:   &corev1.RuntimeRef{Backend: "herdr-core", ExecutionId: "e1", Generation: 2, TerminalAddress: "t-7"},
		Agent: "proj-harbor-orchestrator", Role: "orchestrator", Source: "record", LastVerifiedAt: 1_800_000_000_000,
		Observation: &daemonv1.ExecutionObservation{Process: "live", Readiness: "needs_attention", Program: "idle", Visibility: "verified", App: "open"},
		Attention:   []*daemonv1.AttentionReason{{Reason: "notice_waiting", NoticeId: "n-1", Line: "Run `herdsman notice read` and do what it prints"}},
		Unresolved:  []*daemonv1.UnresolvedItem{{Kind: "typed_submit", Id: "s1", State: "unresolved", Choices: []string{"keep_waiting"}}},
		Notices:     &daemonv1.NoticeSummary{Issued: 1},
		Actions:     []string{"show", "submit"},
	}
	a := statusAgent(e)
	if a.Address != "t-7" || a.Readiness != "needs_attention" || a.App != "open" || a.Attention[0].Notice != "n-1" ||
		a.Unresolved[0].Choices[0] != "keep_waiting" || a.Notices.Issued != 1 || a.LastVerified.IsZero() || len(a.Actions) != 2 {
		t.Fatalf("%+v", a)
	}
	tui := statusAgent(&daemonv1.Execution{Ref: &corev1.RuntimeRef{Backend: "herdr-tui", Pane: "w1:p2"}, Agent: "x"})
	if tui.Address != "w1:p2" || tui.Notices != nil {
		t.Fatalf("%+v", tui)
	}
	var b bytes.Buffer
	start := &daemonv1.RouteStart{Route: &corev1.RouteId{Value: "harbor"}, Backend: "herdr-core"}
	if err := routeStatusJSONWith(&b, start, []status.Agent{a}); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b.Bytes(), &doc); err != nil || doc["backend"] != "herdr-core" {
		t.Fatalf("%v %s", err, b.String())
	}
	if !strings.Contains(b.String(), `"readiness":"needs_attention"`) || !strings.Contains(b.String(), `"reason":"notice_waiting"`) {
		t.Fatalf("%s", b.String())
	}
}
