//go:build acceptance

package clean

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/wtree"
)

func TestARestoredTreeIsKnownToTheTaskRecordsAgain(t *testing.T) {
	g := newGate(t)
	run(t, g.main, "merge", "-q", "--ff-only", "feat/a")
	report := g.clean(daemon.WorktreeCleanRequest{})
	if report.Error != "" {
		t.Fatal(report.Error)
	}
	ctx := context.Background()
	recovery := g.recovery()
	result, err := recovery.Undo(ctx, ops.UndoRequest{Snapshot: report.Snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if err := daemon.RecordRestoredTree(ctx, recovery, report.Snapshot, result.Target); err != nil {
		t.Fatal(err)
	}
	state, err := wtree.OpenState(filepath.Join(g.main, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	rows, err := state.Worktrees(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Path != g.tree || rows[0].Branch != "feat/a" {
		t.Fatalf("rows %+v", rows)
	}
}
