package wtree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/gitrun"
	"github.com/arafathusayn/herdsman/internal/snapshot"
)

type cleanRig struct {
	t      *testing.T
	main   string
	common string
	tree   string
	head   string
	state  *State
	req    CleanRequest
	deps   CleanDeps
	cap    int64
}

func rigGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newCleanRig makes a repository whose feature tree holds one commit, not yet
// on any remote and not merged.
func newCleanRig(t *testing.T) *cleanRig {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(root, "repo")
	if err := os.Mkdir(main, 0o755); err != nil {
		t.Fatal(err)
	}
	rigGit(t, main, "init", "-q", "-b", "main")
	write(t, filepath.Join(main, ".gitignore"), ".env\nnode_modules\nbig.bin\n")
	rigGit(t, main, "add", ".")
	rigGit(t, main, "commit", "-qm", "base")
	tree := filepath.Join(root, "tree")
	rigGit(t, main, "worktree", "add", "-q", "-b", "feat/a", tree)
	write(t, filepath.Join(tree, "a.txt"), "a")
	rigGit(t, tree, "add", ".")
	rigGit(t, tree, "commit", "-qm", "work")
	common := filepath.Join(main, ".git")
	state, err := OpenState(common)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	if err := state.PutWorktree(context.Background(), Worktree{Task: "a", Branch: "feat/a", Path: tree, CreatedAt: time.Unix(1, 0)}); err != nil {
		t.Fatal(err)
	}
	r := &cleanRig{t: t, main: main, common: common, tree: tree, state: state, head: rigGit(t, tree, "rev-parse", "HEAD")}
	r.req = CleanRequest{Common: common, Main: main, Task: "a", Branch: "feat/a", Path: tree, Head: r.head, Default: "main", Rebuildable: []string{"node_modules"}}
	dest := filepath.Join(root, "snapshots", "s1")
	r.deps = CleanDeps{
		Git: &gitrun.Runner{}, State: state,
		Snapshot: func(ctx context.Context, q SnapshotQuery) (string, *snapshot.Manifest, error) {
			m, err := snapshot.Take(ctx, snapshot.Options{Worktree: q.Path, Dest: dest, Task: q.Task, Op: "s1", Rebuildable: q.Rebuildable, FileCap: r.cap})
			return "s1", m, err
		},
		Drain: func(string) error { return nil },
	}
	return r
}

func (r *cleanRig) exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func (r *cleanRig) branchExists() bool {
	return rigGit(r.t, r.main, "for-each-ref", "refs/heads/feat/a") != ""
}

func (r *cleanRig) land() {
	rigGit(r.t, r.main, "merge", "-q", "--ff-only", "feat/a")
}

func (r *cleanRig) assertKept(err error) {
	r.t.Helper()
	if err == nil {
		r.t.Fatal("clean did not refuse")
	}
	if !r.exists(filepath.Join(r.tree, "a.txt")) || !r.branchExists() {
		r.t.Fatalf("a refused clean removed something: %v", err)
	}
}

func TestCleanRemovesAnIntegratedTreeAndKeepsItsIgnoredFiles(t *testing.T) {
	r := newCleanRig(t)
	r.land()
	write(t, filepath.Join(r.tree, ".env"), "SECRET=1")
	write(t, filepath.Join(r.tree, "node_modules/x/i.js"), "x")
	var order []string
	r.deps.Running = func(context.Context) ([]string, error) { order = append(order, "running"); return nil, nil }
	r.deps.PreClean = func(context.Context) error { order = append(order, "pre"); return nil }
	r.deps.PostClean = func(context.Context) error { order = append(order, "post"); return nil }
	var drained string
	r.deps.Drain = func(p string) error { drained = p; return nil }
	report, err := Clean(context.Background(), r.deps, r.req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "running,pre,running,post" {
		t.Fatalf("order %v", order)
	}
	if report.Snapshot != "s1" || !report.BranchDeleted || report.BranchReason != ProofAncestry {
		t.Fatalf("report %+v", report)
	}
	if r.exists(r.tree) || r.branchExists() {
		t.Fatal("tree or branch left behind")
	}
	if !strings.HasPrefix(drained, filepath.Join(r.common, "herdsman", "trash")) || !r.exists(filepath.Join(drained, ".env")) {
		t.Fatalf("trash path %q", drained)
	}
	if len(report.Rebuildable) != 1 || report.Rebuildable[0] != "node_modules" {
		t.Fatalf("rebuildable %v", report.Rebuildable)
	}
	if out := rigGit(t, r.main, "worktree", "list"); strings.Contains(out, r.tree) {
		t.Fatalf("worktree record stayed: %s", out)
	}
	rows, _ := r.state.Worktrees(context.Background())
	if len(rows) != 0 {
		t.Fatalf("state rows %v", rows)
	}
	if report.FreeBefore == 0 || report.FreeAfter == 0 {
		t.Fatalf("disk report %+v", report)
	}
}

func TestCleanStopsWhenTheSnapshotFails(t *testing.T) {
	r := newCleanRig(t)
	r.land()
	boom := errors.New("no space")
	r.deps.Snapshot = func(context.Context, SnapshotQuery) (string, *snapshot.Manifest, error) { return "", nil, boom }
	_, err := Clean(context.Background(), r.deps, r.req)
	r.assertKept(err)
	if !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
}

func TestCleanRefusesWhileTheTaskRuns(t *testing.T) {
	r := newCleanRig(t)
	r.land()
	r.deps.Running = func(context.Context) ([]string, error) { return []string{"process 5"}, nil }
	r.deps.Snapshot = func(context.Context, SnapshotQuery) (string, *snapshot.Manifest, error) {
		t.Fatal("snapshot taken for a running task")
		return "", nil, nil
	}
	_, err := Clean(context.Background(), r.deps, r.req)
	r.assertKept(err)
	if !errors.Is(err, ErrStillRunning) || !strings.Contains(err.Error(), "process 5") {
		t.Fatalf("err %v", err)
	}
}

func TestCleanRunningCheckErrorStops(t *testing.T) {
	r := newCleanRig(t)
	boom := errors.New("ps failed")
	r.deps.Running = func(context.Context) ([]string, error) { return nil, boom }
	_, err := Clean(context.Background(), r.deps, r.req)
	r.assertKept(err)
	if !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
}

func TestCleanRefusesAPathTheSnapshotDidNotCapture(t *testing.T) {
	r := newCleanRig(t)
	r.land()
	r.cap = 64
	write(t, filepath.Join(r.tree, "big.bin"), strings.Repeat("x", 128))
	_, err := Clean(context.Background(), r.deps, r.req)
	r.assertKept(err)
	var uncaptured *UncapturedError
	if !errors.As(err, &uncaptured) || len(uncaptured.Paths) != 1 || uncaptured.Paths[0] != "big.bin" {
		t.Fatalf("err %v", err)
	}
	if !strings.Contains(err.Error(), "--force-irreversible") {
		t.Fatalf("message names no way out: %v", err)
	}
}

func TestCleanForceIrreversibleNamesExactlyWhatIsLost(t *testing.T) {
	r := newCleanRig(t)
	r.land()
	r.cap = 64
	write(t, filepath.Join(r.tree, "big.bin"), strings.Repeat("x", 128))
	r.req.Named = []string{"*.bin", "nothing/here"}
	report, err := Clean(context.Background(), r.deps, r.req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(report.Lost, ",") != "big.bin" || strings.Join(report.UnmatchedNames, ",") != "nothing/here" {
		t.Fatalf("report %+v", report)
	}
}

func TestCleanRejectsANamedPathOutsideTheTree(t *testing.T) {
	r := newCleanRig(t)
	for _, bad := range []string{"/etc/passwd", "../x", "", "[bad"} {
		r.req.Named = []string{bad}
		_, err := Clean(context.Background(), r.deps, r.req)
		r.assertKept(err)
	}
}

func TestCleanRefusesAFileChangedAfterTheSnapshot(t *testing.T) {
	r := newCleanRig(t)
	r.land()
	write(t, filepath.Join(r.tree, ".env"), "OLD")
	take := r.deps.Snapshot
	r.deps.Snapshot = func(ctx context.Context, q SnapshotQuery) (string, *snapshot.Manifest, error) {
		id, m, err := take(ctx, q)
		write(t, filepath.Join(r.tree, ".env"), "NEWER VALUE")
		write(t, filepath.Join(r.tree, "late.txt"), "late")
		return id, m, err
	}
	_, err := Clean(context.Background(), r.deps, r.req)
	r.assertKept(err)
	var uncaptured *UncapturedError
	if !errors.As(err, &uncaptured) || strings.Join(uncaptured.Paths, ",") != ".env,late.txt" {
		t.Fatalf("err %v", err)
	}
}

func TestCleanRefusesASymlinkRetargetedAfterTheSnapshot(t *testing.T) {
	r := newCleanRig(t)
	r.land()
	r.req.Force = true
	link := filepath.Join(r.tree, "current")
	if err := os.Symlink("one-target", link); err != nil {
		t.Fatal(err)
	}
	take := r.deps.Snapshot
	r.deps.Snapshot = func(ctx context.Context, q SnapshotQuery) (string, *snapshot.Manifest, error) {
		id, m, err := take(ctx, q)
		if removeErr := os.Remove(link); removeErr != nil {
			t.Fatal(removeErr)
		}
		// The same length, so that only the link's identity differs.
		if linkErr := os.Symlink("two-target", link); linkErr != nil {
			t.Fatal(linkErr)
		}
		return id, m, err
	}
	_, err := Clean(context.Background(), r.deps, r.req)
	r.assertKept(err)
	var uncaptured *UncapturedError
	if !errors.As(err, &uncaptured) || strings.Join(uncaptured.Paths, ",") != "current" {
		t.Fatalf("err %v", err)
	}
}

func TestCleanRefusesAFileReplacedByALinkAfterTheSnapshot(t *testing.T) {
	r := newCleanRig(t)
	r.land()
	r.req.Force = true
	file := filepath.Join(r.tree, ".env")
	write(t, file, "0123456789")
	take := r.deps.Snapshot
	r.deps.Snapshot = func(ctx context.Context, q SnapshotQuery) (string, *snapshot.Manifest, error) {
		id, m, err := take(ctx, q)
		if removeErr := os.Remove(file); removeErr != nil {
			t.Fatal(removeErr)
		}
		if linkErr := os.Symlink("0123456789", file); linkErr != nil {
			t.Fatal(linkErr)
		}
		return id, m, err
	}
	_, err := Clean(context.Background(), r.deps, r.req)
	r.assertKept(err)
	var uncaptured *UncapturedError
	if !errors.As(err, &uncaptured) {
		t.Fatalf("err %v", err)
	}
}

func TestCleanRefusesWhatAPreCleanHookLeftBehind(t *testing.T) {
	r := newCleanRig(t)
	r.land()
	r.deps.PreClean = func(context.Context) error {
		write(t, filepath.Join(r.tree, "hook-backup.db"), "only copy")
		return nil
	}
	r.req.Force = true
	_, err := Clean(context.Background(), r.deps, r.req)
	r.assertKept(err)
	var uncaptured *UncapturedError
	if !errors.As(err, &uncaptured) || strings.Join(uncaptured.Paths, ",") != "hook-backup.db" {
		t.Fatalf("err %v", err)
	}
	if !r.exists(filepath.Join(r.tree, "hook-backup.db")) {
		t.Fatal("the hook's file was removed")
	}
}

func TestCleanRefusesWhatAPreCleanHookStarted(t *testing.T) {
	r := newCleanRig(t)
	r.land()
	started := false
	r.deps.PreClean = func(context.Context) error { started = true; return nil }
	r.deps.Running = func(context.Context) ([]string, error) {
		if started {
			return []string{"process 9"}, nil
		}
		return nil, nil
	}
	_, err := Clean(context.Background(), r.deps, r.req)
	r.assertKept(err)
	if !errors.Is(err, ErrStillRunning) {
		t.Fatalf("err %v", err)
	}
}

func TestCleanSafetyIgnoresGitDisplaySettings(t *testing.T) {
	r := newCleanRig(t)
	r.land()
	rigGit(t, r.tree, "config", "status.showUntrackedFiles", "no")
	write(t, filepath.Join(r.tree, "untracked.txt"), "x")
	_, err := Clean(context.Background(), r.deps, r.req)
	r.assertKept(err)
	if !errors.Is(err, ErrUnsafe) {
		t.Fatalf("err %v", err)
	}
}

func TestCleanSafety(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(r *cleanRig)
		force   bool
		refused bool
		deleted bool
		proof   string
	}{
		{name: "unpushed and unmerged refuses", refused: true},
		{name: "force on unpushed keeps the branch", force: true},
		{name: "dirty refuses even when landed", setup: func(r *cleanRig) { r.land(); write(r.t, filepath.Join(r.tree, "b"), "b") }, refused: true},
		{name: "force on dirty goes on", setup: func(r *cleanRig) { r.land(); write(r.t, filepath.Join(r.tree, "b"), "b") }, force: true, deleted: true, proof: ProofAncestry},
		{name: "pushed but unmerged keeps the branch", setup: func(r *cleanRig) {
			rigGit(r.t, r.main, "update-ref", "refs/remotes/origin/feat/a", r.head)
		}},
		{name: "merged pull request head", setup: func(r *cleanRig) { r.req.PullRequestHead = r.head }, deleted: true, proof: ProofPullRequest},
		{name: "landing record", setup: func(r *cleanRig) {
			r.req.Landed = true
			rigGit(r.t, r.main, "update-ref", "refs/remotes/origin/feat/a", r.head)
		}, deleted: true, proof: ProofLanding},
		{name: "a pull request at another head is no proof", setup: func(r *cleanRig) { r.req.PullRequestHead = strings.Repeat("0", 40) }, refused: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newCleanRig(t)
			if c.setup != nil {
				c.setup(r)
			}
			r.req.Force = c.force
			report, err := Clean(context.Background(), r.deps, r.req)
			if c.refused {
				r.assertKept(err)
				if !errors.Is(err, ErrUnsafe) {
					t.Fatalf("err %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if report.BranchDeleted != c.deleted || r.branchExists() == c.deleted || report.BranchReason != c.proof {
				t.Fatalf("report %+v", report)
			}
		})
	}
}

func TestCleanKeepBranch(t *testing.T) {
	r := newCleanRig(t)
	r.land()
	r.req.KeepBranch = true
	report, err := Clean(context.Background(), r.deps, r.req)
	if err != nil || report.BranchDeleted || !r.branchExists() {
		t.Fatalf("%+v %v", report, err)
	}
}

func TestCleanPreHookFailureRemovesNothing(t *testing.T) {
	r := newCleanRig(t)
	r.land()
	boom := errors.New("hook")
	r.deps.PreClean = func(context.Context) error { return boom }
	_, err := Clean(context.Background(), r.deps, r.req)
	r.assertKept(err)
	if !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
}

func TestCleanPostHookFailureIsAWarning(t *testing.T) {
	r := newCleanRig(t)
	r.land()
	r.deps.PostClean = func(context.Context) error { return errors.New("late hook") }
	report, err := Clean(context.Background(), r.deps, r.req)
	if err != nil || len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "late hook") {
		t.Fatalf("%+v %v", report, err)
	}
}

func TestCleanDrainFailureIsAWarning(t *testing.T) {
	r := newCleanRig(t)
	r.land()
	r.deps.Drain = func(string) error { return errors.New("no fork") }
	report, err := Clean(context.Background(), r.deps, r.req)
	if err != nil || r.exists(r.tree) || len(report.Warnings) != 1 {
		t.Fatalf("%+v %v", report, err)
	}
}

func TestCleanLeavesRecordsOfWhatItKept(t *testing.T) {
	r := newCleanRig(t)
	r.land()
	ctx := context.Background()
	for _, row := range []Resource{{Task: "a", Execution: "e", Kind: "volume", ID: "v1", Start: "s"}, {Task: "a", Execution: "e", Kind: "process", ID: "5", Start: "s"}} {
		if err := r.state.Register(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	report, err := Clean(ctx, r.deps, r.req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(report.Kept, ",") != "volume v1" {
		t.Fatalf("kept %v", report.Kept)
	}
	left, err := r.state.Resources(ctx, "a")
	if err != nil || len(left) != 1 || left[0].Kind != "volume" {
		t.Fatalf("%v %v", left, err)
	}
}

func TestDeleteBranchRefusesAMovedOrCheckedOutBranch(t *testing.T) {
	r := newCleanRig(t)
	ctx := context.Background()
	git := &gitrun.Runner{}
	r.land()
	proof := BranchProof{Target: "main"}
	if deleted, _, err := DeleteBranch(ctx, git, r.main, "feat/a", r.head, proof); err != nil || deleted {
		t.Fatalf("checked out: %v %v", deleted, err)
	}
	rigGit(t, r.main, "worktree", "remove", "--force", r.tree)
	stale := rigGit(t, r.main, "rev-parse", "main~1")
	if deleted, reason, err := DeleteBranch(ctx, git, r.main, "feat/a", stale, proof); err != nil || deleted || reason != "" {
		t.Fatalf("moved: %v %q %v", deleted, reason, err)
	}
	if !r.branchExists() {
		t.Fatal("a moved branch was deleted")
	}
	if deleted, _, err := DeleteBranch(ctx, git, r.main, "main", r.head, proof); err != nil || deleted {
		t.Fatalf("target: %v %v", deleted, err)
	}
	if deleted, _, err := DeleteBranch(ctx, git, r.main, "", r.head, proof); err != nil || deleted {
		t.Fatalf("detached: %v %v", deleted, err)
	}
	if deleted, reason, err := DeleteBranch(ctx, git, r.main, "feat/a", r.head, proof); err != nil || !deleted || reason != ProofAncestry {
		t.Fatalf("proven: %v %q %v", deleted, reason, err)
	}
}

func TestInventoryKeepsSubmodulesAndNestedRepositoriesUncaptured(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "sub/.git/HEAD"), "x")
	write(t, filepath.Join(root, "kept.txt"), "kept")
	info, err := os.Lstat(filepath.Join(root, "kept.txt"))
	if err != nil {
		t.Fatal(err)
	}
	m := &snapshot.Manifest{
		Files:    []snapshot.FileRecord{{Path: "kept.txt", Mode: 0o100644, Size: info.Size(), MTimeNS: info.ModTime().UnixNano()}},
		Excluded: []snapshot.Exclusion{{Path: "sub", Reason: snapshot.ReasonNestedRepository}},
	}
	got, err := TakeInventory(root, m, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Captured != 1 || strings.Join(got.Uncaptured, ",") != "sub" {
		t.Fatalf("%+v", got)
	}
	if got, err = TakeInventory(root, m, []string{"sub"}, nil); err != nil || strings.Join(got.Named, ",") != "sub" || len(got.Uncaptured) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestDrainDeletesOnlyInsideTheTrash(t *testing.T) {
	common := t.TempDir()
	if err := StartDrain(common, filepath.Join(common, "elsewhere")); err == nil {
		t.Fatal("drained outside the trash")
	}
	source := filepath.Join(common, "tree")
	write(t, filepath.Join(source, "f"), "f")
	trashed, err := MoveToTrash(common, source, "a-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MoveToTrash(common, filepath.Join(common, "missing"), "a-2"); err == nil {
		t.Fatal("moved a missing tree")
	}
	if err := StartDrain(common, trashed); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Lstat(trashed); errors.Is(err, os.ErrNotExist) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the drain did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
