package daemon

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/hooks"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/trace"
	"github.com/arafathusayn/herdsman/internal/wtree"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	tracev1 "github.com/arafathusayn/herdsman/sdk/trace/v1"
)

func collectedKinds(t *testing.T, folder string) (map[string]int, trace.Trace) {
	t.Helper()
	got, err := trace.Collect(trace.Sources{Route: folder})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, span := range got.Spans {
		kinds[span.Kind]++
	}
	return kinds, got
}

// A real route start, its agents moved by what herdr reports, and every
// producer driven through the path production uses.
func TestEveryProducerRecordsInARealRoutesTrace(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	repo, _ := upNoAdjutant(t, h, c)
	ctx := context.Background()
	got, err := c.Route.Get(ctx, &daemonv1.RouteServiceGetRequest{Route: &corev1.RouteId{Value: "harbor"}, Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	folder := got.GetStart().GetFolder()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	orchestrator := filepath.Base(repo) + "-harbor-orchestrator"
	var pane, workspace string
	agents, err := herdr.New(routeSocketOf(t, h, repo)).AgentList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range agents {
		if agent.Name != nil && *agent.Name == orchestrator {
			pane, workspace = agent.PaneID, agent.WorkspaceID
		}
	}
	if pane == "" {
		t.Fatalf("no orchestrator in %+v", agents)
	}
	reviewer := herdrtest.Agent("reviewer-pane", "reviewer-session")
	reviewer["name"], reviewer["workspace_id"], reviewer["agent"] = "reviewer-one", workspace, "claude"
	h.fx.SetAgent("reviewer-pane", reviewer)
	h.fx.Edit(pane, "agent_status", "working")
	h.fx.Edit("reviewer-pane", "agent_status", "working")
	h.d.observeTurns(ctx)
	h.fx.Edit(pane, "agent_status", "idle")
	h.fx.SetAgent("reviewer-pane", nil)
	h.d.observeTurns(ctx)

	t.Setenv(routeFolderEnv, folder)
	repository, err := (&WorktreeService{}).open(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.close()
	runner := repository.runner(wtree.Vars{Values: map[string]string{"task": "a"}}, repo)
	if _, err := runner.Run(ctx, "pre-start", hooks.Plan{{{Command: hooks.Command{Name: "warm"}, Rendered: "true"}}}); err != nil {
		t.Fatal(err)
	}
	exit := int32(0)
	if _, err := NewTraceService(false).AddSpan(ctx, &tracev1.AddSpanRequest{Route: folder, Span: &tracev1.Span{Kind: trace.KindCheck, Name: "make", Start: time.Now().Format(time.RFC3339Nano), Exit: &exit}}); err != nil {
		t.Fatal(err)
	}

	kinds, collected := collectedKinds(t, folder)
	for _, kind := range []string{trace.KindTurn, trace.KindReview, trace.KindCommand, trace.KindCheck, trace.KindHook, trace.KindRoute} {
		if kinds[kind] == 0 {
			t.Errorf("no %s span in %v", kind, kinds)
		}
	}
	if kinds[trace.KindTurn] != 1 || kinds[trace.KindReview] != 1 || collected.Skipped != 0 {
		t.Errorf("%v, skipped %d", kinds, collected.Skipped)
	}
}

func routeSocketOf(t *testing.T, h *harness, repo string) string {
	t.Helper()
	return route.SocketOf(latestStart(t, h, repo))
}

func namedAgent(pane, name, workspace string, status herdr.Status, session string) herdr.AgentRecord {
	kind := "claude"
	return herdr.AgentRecord{Agent: herdr.Agent{PaneID: pane, Name: &name, WorkspaceID: workspace, Status: status, Agent: &kind}, AgentSession: json.RawMessage(session)}
}

func TestATurnIsRecordedOnceWithTheHarnessUsage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	folder := t.TempDir()
	now := time.Now().UTC()
	line := `{"type":"assistant","timestamp":"` + now.Format(time.RFC3339Nano) + `","message":{"id":"m1","usage":{"input_tokens":3,"output_tokens":4}}}` + "\n"
	transcript := filepath.Join(home, ".claude", "projects", "slug", "sess.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	session := `{"agent":"claude","kind":"id","value":"sess"}`
	d := &Daemon{}
	lead := namedAgent("p1", "o", "w", "idle", session)
	working := namedAgent("p2", "worker", "w", herdrWorking, session)
	d.turns.working = map[string]observedTurn{"r\x00p2": {since: now.Add(-time.Minute), name: "worker", agent: "claude", session: json.RawMessage(session)}}
	d.observeRoute("r", "o", folder, []herdr.AgentRecord{lead, working})
	if kinds, _ := collectedKinds(t, folder); kinds[trace.KindTurn] != 0 {
		t.Fatalf("a turn still running was recorded: %v", kinds)
	}
	d.turns.working["r\x00p2"] = observedTurn{since: now.Add(-time.Minute), name: "worker", agent: "claude", session: json.RawMessage(session)}
	done := namedAgent("p2", "worker", "w", "idle", session)
	d.observeRoute("r", "o", folder, []herdr.AgentRecord{lead, done})
	d.observeRoute("r", "o", folder, []herdr.AgentRecord{lead, done})
	kinds, got := collectedKinds(t, folder)
	if kinds[trace.KindTurn] != 1 {
		t.Fatalf("%v", kinds)
	}
	for _, span := range got.Spans {
		if span.Kind == trace.KindTurn && (span.Tokens == nil || *span.Tokens != 7 || span.CostMicros != nil) {
			t.Fatalf("%+v", span)
		}
	}
}

func TestAgentsOutsideTheRouteAreIgnored(t *testing.T) {
	d := &Daemon{}
	d.observeRoute("r", "o", t.TempDir(), []herdr.AgentRecord{namedAgent("p", "x", "other", herdrWorking, "null")})
	if len(d.turns.working) != 0 {
		t.Fatalf("%v", d.turns.working)
	}
	lead := namedAgent("p0", "o", "w", "idle", "null")
	unnamed := namedAgent("p1", "y", "w", herdrWorking, "null")
	unnamed.Name = nil
	d.observeRoute("r", "o", t.TempDir(), []herdr.AgentRecord{lead, unnamed})
	if len(d.turns.working) != 0 {
		t.Fatalf("%v", d.turns.working)
	}
}

func TestObservingWithoutAJournalDoesNothing(t *testing.T) {
	(&Daemon{}).observeTurns(context.Background())
}

func TestWatchTurnsStopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	(&Daemon{}).watchTurns(ctx)
}

func TestATurnThatCannotBeWrittenIsOnlyLogged(t *testing.T) {
	folder := t.TempDir()
	if err := os.WriteFile(filepath.Join(folder, "trace"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	(&Daemon{}).recordTurn("r", folder, observedTurn{since: time.Now(), name: "n"}, time.Now())
}

func TestTurnKindReadsTheRole(t *testing.T) {
	if turnKind("reviewer-sol") != trace.KindReview || turnKind("Repo-Reviewer") != trace.KindReview || turnKind("implementer") != trace.KindTurn {
		t.Fatal("wrong kind")
	}
}
