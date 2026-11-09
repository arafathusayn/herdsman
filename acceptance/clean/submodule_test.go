//go:build acceptance

package clean

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon"
	"github.com/arafathusayn/herdsman/internal/ops"
)

// newSubmoduleGate lands a task whose tree holds an initialized submodule, at
// the upstream's newest of two commits.
func newSubmoduleGate(t *testing.T) (*gate, string) {
	t.Helper()
	g := newGate(t)
	upstream := filepath.Join(g.root, "upstream")
	if err := os.Mkdir(upstream, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, upstream, "init", "-q", "-b", "main")
	for _, version := range []string{"one", "two"} {
		put(t, filepath.Join(upstream, "lib.txt"), version)
		run(t, upstream, "add", ".")
		run(t, upstream, "commit", "-qm", version)
	}
	run(t, g.tree, "-c", "protocol.file.allow=always", "submodule", "add", "-q", upstream, "sub")
	run(t, g.tree, "commit", "-qm", "add the submodule")
	run(t, g.main, "merge", "-q", "--ff-only", "feat/a")
	return g, filepath.Join(g.tree, "sub")
}

func (g *gate) assertRefusedForSubmodule(report daemon.WorktreeCleanReport, reason string) {
	g.t.Helper()
	if !strings.Contains(report.Error, "sub (") || !strings.Contains(report.Error, reason) {
		g.t.Fatalf("%+v", report)
	}
	g.assertTreeKept()
}

func TestASavedSubmoduleDoesNotRefuseDeletion(t *testing.T) {
	g, _ := newSubmoduleGate(t)
	recorded := run(t, g.tree, "rev-parse", "HEAD:sub")
	report := g.clean(daemon.WorktreeCleanRequest{})
	if report.Error != "" || strings.Join(report.Submodules, ",") != "sub" || len(report.Lost) != 0 {
		t.Fatalf("%+v", report)
	}
	if _, err := os.Lstat(g.tree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the tree is still there: %v", err)
	}
	if _, err := g.recovery().Undo(context.Background(), ops.UndoRequest{Snapshot: report.Snapshot}); err != nil {
		t.Fatal(err)
	}
	if got := run(t, g.tree, "rev-parse", "HEAD:sub"); got != recorded {
		t.Fatalf("restored gitlink %s, want %s", got, recorded)
	}
}

func TestADirtySubmoduleRefusesDeletion(t *testing.T) {
	g, sub := newSubmoduleGate(t)
	put(t, filepath.Join(sub, "lib.txt"), "edited here only")
	g.assertRefusedForSubmodule(g.clean(daemon.WorktreeCleanRequest{Force: true}), "uncommitted")
}

func TestAnUnpushedSubmoduleRefusesDeletion(t *testing.T) {
	g, sub := newSubmoduleGate(t)
	put(t, filepath.Join(sub, "lib.txt"), "three")
	run(t, sub, "add", ".")
	run(t, sub, "commit", "-qm", "three")
	// Recorded, so that the commit being on no remote is the only problem.
	run(t, g.tree, "add", "sub")
	run(t, g.tree, "commit", "-qm", "move the submodule")
	run(t, g.main, "merge", "-q", "--ff-only", "feat/a")
	g.assertRefusedForSubmodule(g.clean(daemon.WorktreeCleanRequest{Force: true}), "no remote")
}

func TestAMovedSubmoduleRefusesDeletionUntilNamed(t *testing.T) {
	g, sub := newSubmoduleGate(t)
	run(t, sub, "checkout", "-q", "--detach", "HEAD~1")
	g.assertRefusedForSubmodule(g.clean(daemon.WorktreeCleanRequest{Force: true}), "moved")
	report := g.clean(daemon.WorktreeCleanRequest{Force: true, Named: []string{"sub"}})
	if report.Error != "" || strings.Join(report.Lost, ",") != "sub" {
		t.Fatalf("%+v", report)
	}
}

func TestASubmoduleHidingUntrackedFilesRefusesDeletion(t *testing.T) {
	g, sub := newSubmoduleGate(t)
	run(t, sub, "config", "status.showUntrackedFiles", "no")
	put(t, filepath.Join(sub, "only-copy.txt"), "nowhere else")
	g.assertRefusedForSubmodule(g.clean(daemon.WorktreeCleanRequest{Force: true}), "uncommitted")
}

func TestASubmoduleHidingAnEditRefusesDeletion(t *testing.T) {
	g, sub := newSubmoduleGate(t)
	run(t, sub, "update-index", "--assume-unchanged", "lib.txt")
	put(t, filepath.Join(sub, "lib.txt"), "edited and hidden")
	g.assertRefusedForSubmodule(g.clean(daemon.WorktreeCleanRequest{Force: true}), "hides")
}
