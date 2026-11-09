package wtree

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/gitrun"
)

// equivalentOnMain puts the branch's change on main as a different commit.
func (r *cleanRig) equivalentOnMain() {
	write(r.t, filepath.Join(r.main, "other.txt"), "main moved on")
	rigGit(r.t, r.main, "add", ".")
	rigGit(r.t, r.main, "commit", "-qm", "other")
	rigGit(r.t, r.main, "cherry-pick", r.head)
}

func TestArchiveOnlySnapshotsAndRemovesNothing(t *testing.T) {
	r := newCleanRig(t)
	write(t, filepath.Join(r.tree, "unsaved.txt"), "x")
	r.deps.PreClean = func(context.Context) error { t.Fatal("a hook ran"); return nil }
	r.req.ArchiveOnly = true
	report, err := Clean(context.Background(), r.deps, r.req)
	if err != nil {
		t.Fatal(err)
	}
	if report.Snapshot != "s1" || !report.ArchiveOnly || report.BranchDeleted {
		t.Fatalf("report %+v", report)
	}
	if !r.exists(r.tree) || !r.branchExists() {
		t.Fatal("something was removed")
	}
	if rows, _ := r.state.Worktrees(context.Background()); len(rows) != 1 {
		t.Fatalf("rows %v", rows)
	}
}

func TestDeleteBranchIsAuthorityForAPatchEquivalentBranch(t *testing.T) {
	r := newCleanRig(t)
	r.equivalentOnMain()
	r.req.Force, r.req.DeleteBranch = true, true
	report, err := Clean(context.Background(), r.deps, r.req)
	if err != nil {
		t.Fatal(err)
	}
	if !report.BranchDeleted || report.BranchReason != ProofPatchEquivalent || r.branchExists() {
		t.Fatalf("report %+v", report)
	}
}

func TestDeleteBranchAloneKeepsAnEquivalentBranchWithoutTheFlag(t *testing.T) {
	r := newCleanRig(t)
	r.equivalentOnMain()
	r.req.Force = true
	report, err := Clean(context.Background(), r.deps, r.req)
	if err != nil {
		t.Fatal(err)
	}
	if report.BranchDeleted || !r.branchExists() {
		t.Fatalf("report %+v", report)
	}
}

func TestDeleteBranchNeverDeletesRealUnintegratedWork(t *testing.T) {
	r := newCleanRig(t)
	r.req.Force, r.req.DeleteBranch = true, true
	report, err := Clean(context.Background(), r.deps, r.req)
	if err != nil {
		t.Fatal(err)
	}
	if report.BranchDeleted || !r.branchExists() {
		t.Fatalf("report %+v", report)
	}
}

func TestPatchEquivalentReportsAnUnknownTarget(t *testing.T) {
	r := newCleanRig(t)
	if _, err := PatchEquivalent(context.Background(), &gitrun.Runner{}, r.main, r.head, "absent"); err == nil {
		t.Fatal("an unknown target")
	}
}

func TestPatchEquivalentIsFalseWithNothingAhead(t *testing.T) {
	r := newCleanRig(t)
	r.land()
	equivalent, err := PatchEquivalent(context.Background(), &gitrun.Runner{}, r.main, r.head, "main")
	if err != nil || equivalent {
		t.Fatalf("%v %v", equivalent, err)
	}
}

func TestStateRemembersAMark(t *testing.T) {
	r := newCleanRig(t)
	ctx := context.Background()
	if seen, err := r.state.Marked(ctx, "x"); err != nil || seen {
		t.Fatalf("%v %v", seen, err)
	}
	if err := r.state.Mark(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	if seen, err := r.state.Marked(ctx, "x"); err != nil || !seen {
		t.Fatalf("%v %v", seen, err)
	}
}
