package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/hooks"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/trace"
	"github.com/arafathusayn/herdsman/internal/wtree"
	tracev1 "github.com/arafathusayn/herdsman/sdk/trace/v1"
)

func routeFolderFixture(t *testing.T, repo, name string) string {
	t.Helper()
	folder := filepath.Join(repo, ".herdsman", name)
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, route.RouteFile), []byte("# route\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return folder
}

// One route, each producer driven through the path production uses, and the
// trace read back as an exporter would.
func TestEveryProducerRecordsInTheRoutesTrace(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	repo := filepath.Join(root, "repo")
	folder := routeFolderFixture(t, repo, "r-2026-01-02")
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	h := newHarness(t)
	adj, orch, _ := h.route("r")
	sent := adj.send(t, "r", orch.name, "work")
	claim := orch.read(t, "r")
	orch.ack(t, "r", claim.Claim, sent.ID)
	h.d.turns.begin("r", "review-claim")
	h.d.recordTurn("r", "reviewer-one", "review-claim")

	t.Setenv(routeFolderEnv, folder)
	service := &WorktreeService{}
	repository, err := service.open(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.close()
	runner := repository.runner(wtree.Vars{Values: map[string]string{"task": "a"}}, repo)
	if _, err := runner.Run(context.Background(), "pre-start", hooks.Plan{{{Command: hooks.Command{Name: "warm"}, Rendered: "true"}}}); err != nil {
		t.Fatal(err)
	}
	exit := int32(0)
	if _, err := NewTraceService(false).AddSpan(context.Background(), &tracev1.AddSpanRequest{Route: folder, Span: &tracev1.Span{Kind: trace.KindCheck, Name: "make check", Start: time.Now().Format(time.RFC3339Nano), Exit: &exit}}); err != nil {
		t.Fatal(err)
	}

	got, err := trace.Collect(trace.Sources{Route: folder})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, span := range got.Spans {
		kinds[span.Kind]++
	}
	for _, kind := range []string{trace.KindTurn, trace.KindReview, trace.KindCommand, trace.KindCheck, trace.KindHook, trace.KindRoute} {
		if kinds[kind] == 0 {
			t.Errorf("no %s span in %v", kind, kinds)
		}
	}
	if got.Skipped != 0 {
		t.Errorf("skipped %d", got.Skipped)
	}
}

func TestATurnLastsFromItsClaim(t *testing.T) {
	var clock turnClock
	clock.begin("r", "")
	if clock.started != nil {
		t.Fatal("an empty token began a turn")
	}
	clock.begin("r", "t")
	first := clock.started[turnKey("r", "t")]
	clock.begin("r", "t")
	if clock.started[turnKey("r", "t")] != first {
		t.Fatal("a repeated read restarted the turn")
	}
	later := first.Add(time.Hour)
	if got := clock.since("r", "t", later); got != first {
		t.Fatalf("since = %v", got)
	}
	clock.end("r", "t")
	if got := clock.since("r", "t", later); got != later {
		t.Fatalf("a forgotten claim must start now, got %v", got)
	}
}

func TestRouteFolderIsFoundByNameAndDate(t *testing.T) {
	repo := t.TempDir()
	dated := routeFolderFixture(t, repo, "alpha-2026-01-02")
	routeFolderFixture(t, repo, "alpha-beta-2026-01-03")
	plain := routeFolderFixture(t, repo, "plain")
	if got := routeFolderIn(repo, "alpha"); got != dated {
		t.Errorf("alpha = %q, want %q", got, dated)
	}
	if got := routeFolderIn(repo, "plain"); got != plain {
		t.Errorf("plain = %q, want %q", got, plain)
	}
	if got := routeFolderIn(repo, "absent"); got != "" {
		t.Errorf("absent = %q", got)
	}
}

func TestATurnWithNoRouteFolderIsSkipped(t *testing.T) {
	h := newHarness(t)
	h.route("r")
	h.d.recordTurn("r", "orch", "t")
	h.d.recordTurn("unknown", "orch", "t")
}

func TestTurnKindReadsTheRole(t *testing.T) {
	if turnKind("reviewer-sol") != trace.KindReview || turnKind("Repo-Reviewer") != trace.KindReview || turnKind("implementer") != trace.KindTurn {
		t.Fatal("wrong kind")
	}
}
