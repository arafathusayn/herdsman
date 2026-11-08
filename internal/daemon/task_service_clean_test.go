package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/ledger"
	"github.com/arafathusayn/herdsman/internal/wtree"
)

type cleanFixture struct {
	t    *testing.T
	main string
	root string
	svc  *WorktreeService
	log  string
}

func fixtureGit(t *testing.T, dir string, args ...string) string {
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

// stubLedger reports only what the records say, without looking at the machine.
func stubLedger(t *testing.T, read func(context.Context, ledger.Snapshot) (ledger.Snapshot, error)) {
	t.Helper()
	old := ledgerService
	t.Cleanup(func() { ledgerService = old })
	ledgerService = &LedgerService{Read: read}
}

func newCleanFixture(t *testing.T, pullRequests string) *cleanFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stubLedger(t, func(_ context.Context, s ledger.Snapshot) (ledger.Snapshot, error) { return s, nil })
	main := filepath.Join(root, "repo")
	if err := os.Mkdir(main, 0o755); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, main, "init", "-q", "-b", "main")
	fixtureGit(t, main, "remote", "add", "origin", "https://github.com/acme/repo.git")
	if err := os.WriteFile(filepath.Join(main, ".gitignore"), []byte(".env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, main, "add", ".")
	fixtureGit(t, main, "commit", "-qm", "base")
	log := filepath.Join(root, "gh.log")
	gh := filepath.Join(root, "gh")
	body := "#!/bin/sh\necho call >> " + log + "\ncat <<'JSON'\n" + pullRequests + "\nJSON\n"
	if err := os.WriteFile(gh, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	return &cleanFixture{t: t, main: main, root: root, log: log, svc: &WorktreeService{GH: gh, Repo: "acme/repo", StateDir: state, Approvals: filepath.Join(root, "approvals")}}
}

// addTask makes a recorded worktree. Landed trees are merged into main.
func (f *cleanFixture) addTask(task string, landed bool, created time.Time) (string, string) {
	f.t.Helper()
	tree := filepath.Join(f.root, "trees", task)
	branch := "feat/" + task
	fixtureGit(f.t, f.main, "worktree", "add", "-q", "-b", branch, tree)
	if err := os.WriteFile(filepath.Join(tree, task+".txt"), []byte(task), 0o644); err != nil {
		f.t.Fatal(err)
	}
	fixtureGit(f.t, tree, "add", ".")
	fixtureGit(f.t, tree, "commit", "-qm", task)
	if landed {
		fixtureGit(f.t, f.main, "merge", "-q", "--no-edit", branch)
	}
	state, err := wtree.OpenState(filepath.Join(f.main, ".git"))
	if err != nil {
		f.t.Fatal(err)
	}
	defer state.Close()
	if err := state.PutWorktree(context.Background(), wtree.Worktree{Task: task, Branch: branch, Path: tree, CreatedAt: created}); err != nil {
		f.t.Fatal(err)
	}
	// PutWorktree stamps the current time; age tests need another one.
	if _, err := state.DB().Exec(`UPDATE worktrees SET created_at = ? WHERE task = ?`, created.Unix(), task); err != nil {
		f.t.Fatal(err)
	}
	return tree, fixtureGit(f.t, tree, "rev-parse", "HEAD")
}

func (f *cleanFixture) plan(req WorktreeCleanPlanRequest) WorktreeCleanPlanResponse {
	f.t.Helper()
	req.Dir = f.main
	resp, err := f.svc.CleanPlan(context.Background(), req)
	if err != nil {
		f.t.Fatal(err)
	}
	return resp
}

func (f *cleanFixture) ghCalls() int {
	raw, err := os.ReadFile(f.log)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.Count(string(raw), "call")
}

func TestCleanMergedIsOnePassWithOneGitHubCall(t *testing.T) {
	f := newCleanFixture(t, "@PR@")
	old := time.Unix(1000, 0)
	treeA, _ := f.addTask("a", true, old)
	treeB, headB := f.addTask("b", false, old)
	f.addTask("c", false, old)
	raw, err := json.Marshal([]map[string]string{{"headRefName": "feat/b", "headRefOid": headB}})
	if err != nil {
		t.Fatal(err)
	}
	gh := strings.ReplaceAll(mustRead(t, f.svc.GH), "@PR@", string(raw))
	if err := os.WriteFile(f.svc.GH, []byte(gh), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := f.plan(WorktreeCleanPlanRequest{Merged: true})
	if f.ghCalls() != 1 {
		t.Fatalf("gh calls %d", f.ghCalls())
	}
	var names []string
	for _, p := range plan.Plans {
		names = append(names, p.Task)
		if p.BranchAction != "delete" || p.NeedsForce {
			t.Fatalf("%+v", p)
		}
	}
	if strings.Join(names, ",") != "a,b" {
		t.Fatalf("%v", names)
	}
	resp, err := f.svc.Clean(context.Background(), WorktreeCleanRequest{Dir: f.main, Tasks: names})
	if err != nil {
		t.Fatal(err)
	}
	if f.ghCalls() != 2 {
		t.Fatalf("clean made %d gh calls in all", f.ghCalls())
	}
	for _, report := range resp.Reports {
		if report.Error != "" || report.Snapshot == "" || !report.BranchDeleted {
			t.Fatalf("%+v", report)
		}
	}
	for _, tree := range []string{treeA, treeB} {
		if _, err := os.Lstat(tree); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s left: %v", tree, err)
		}
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestCleanPlanNamesBlockersAndChangesNothing(t *testing.T) {
	f := newCleanFixture(t, "[]")
	tree, _ := f.addTask("a", false, time.Now())
	if err := os.WriteFile(filepath.Join(tree, "dirt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := f.plan(WorktreeCleanPlanRequest{Selector: "a"})
	if len(plan.Plans) != 1 || !plan.Plans[0].NeedsForce || !strings.Contains(plan.Plans[0].Reason, "uncommitted") || plan.Plans[0].BranchAction != "keep" {
		t.Fatalf("%+v", plan)
	}
	if _, err := os.Lstat(filepath.Join(tree, "dirt")); err != nil {
		t.Fatal(err)
	}
	resp, err := f.svc.Clean(context.Background(), WorktreeCleanRequest{Dir: f.main, Tasks: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Reports[0].Error, "uncommitted") {
		t.Fatalf("%+v", resp.Reports[0])
	}
}

func TestCleanPlanBlocksTheMainTreeAndALockedOne(t *testing.T) {
	f := newCleanFixture(t, "[]")
	tree, _ := f.addTask("a", true, time.Now())
	if plan := f.plan(WorktreeCleanPlanRequest{Selector: "main"}); plan.Plans[0].Blocked == "" {
		t.Fatalf("%+v", plan)
	}
	fixtureGit(t, f.main, "worktree", "lock", tree)
	if plan := f.plan(WorktreeCleanPlanRequest{Selector: "a"}); !strings.Contains(plan.Plans[0].Blocked, "locked") {
		t.Fatalf("%+v", plan)
	}
	resp, err := f.svc.Clean(context.Background(), WorktreeCleanRequest{Dir: f.main, Tasks: []string{"a"}})
	if err != nil || !strings.Contains(resp.Reports[0].Error, "locked") {
		t.Fatalf("%+v %v", resp, err)
	}
	if plan := f.plan(WorktreeCleanPlanRequest{All: true}); len(plan.Plans) != 1 || plan.Plans[0].Blocked == "" {
		t.Fatalf("%+v", plan)
	}
}

func TestCleanPlanReportsALedgerFailure(t *testing.T) {
	f := newCleanFixture(t, "[]")
	f.addTask("a", true, time.Now())
	boom := errors.New("ps failed")
	stubLedger(t, func(context.Context, ledger.Snapshot) (ledger.Snapshot, error) { return ledger.Snapshot{}, boom })
	if _, err := f.svc.CleanPlan(context.Background(), WorktreeCleanPlanRequest{Dir: f.main, Selector: "a"}); !errors.Is(err, boom) {
		t.Fatalf("%v", err)
	}
}

func TestCleanMinAgeLeavesYoungTrees(t *testing.T) {
	f := newCleanFixture(t, "[]")
	f.addTask("young", true, time.Now())
	f.addTask("old", true, time.Now().Add(-48*time.Hour))
	plan := f.plan(WorktreeCleanPlanRequest{Merged: true, MinAgeSeconds: 86400})
	if len(plan.Plans) != 1 || plan.Plans[0].Task != "old" {
		t.Fatalf("%+v", plan)
	}
}

func TestCleanBulkSkipsTheCurrentTree(t *testing.T) {
	f := newCleanFixture(t, "[]")
	tree, _ := f.addTask("a", true, time.Now())
	resp, err := f.svc.CleanPlan(context.Background(), WorktreeCleanPlanRequest{Dir: tree, Merged: true})
	if err != nil || len(resp.Plans) != 1 || !strings.Contains(resp.Plans[0].Blocked, "current") {
		t.Fatalf("%+v %v", resp, err)
	}
}

func TestCleanWarnsWhenGitHubCannotBeRead(t *testing.T) {
	f := newCleanFixture(t, "[]")
	f.addTask("a", false, time.Now())
	if err := os.WriteFile(f.svc.GH, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := f.plan(WorktreeCleanPlanRequest{Merged: true})
	if len(plan.Warnings) != 1 || len(plan.Plans) != 0 {
		t.Fatalf("%+v", plan)
	}
	if err := os.WriteFile(f.svc.GH, []byte("#!/bin/sh\necho nope\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if plan := f.plan(WorktreeCleanPlanRequest{Merged: true}); len(plan.Warnings) != 1 {
		t.Fatalf("%+v", plan)
	}
}

func TestCleanCallDispatchesAndReportsBadInput(t *testing.T) {
	f := newCleanFixture(t, "[]")
	f.addTask("a", true, time.Now())
	data, _ := json.Marshal(WorktreeCleanPlanRequest{Dir: f.main, Selector: "a"})
	if _, err := f.svc.Call(context.Background(), "CleanPlan", data); err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(WorktreeCleanRequest{Dir: f.main, Tasks: []string{"missing"}})
	raw, err := f.svc.Call(context.Background(), "Clean", data)
	if err != nil || !strings.Contains(string(raw), "error") {
		t.Fatalf("%s %v", raw, err)
	}
	if _, err := f.svc.CleanPlan(context.Background(), WorktreeCleanPlanRequest{Dir: "relative"}); err == nil {
		t.Fatal("relative folder accepted")
	}
	if _, err := f.svc.Clean(context.Background(), WorktreeCleanRequest{Dir: "relative"}); err == nil {
		t.Fatal("relative folder accepted")
	}
	if _, err := f.svc.CleanPlan(context.Background(), WorktreeCleanPlanRequest{Dir: f.main, Selector: "nope"}); err == nil {
		t.Fatal("unknown task accepted")
	}
	f.svc.StateDir = filepath.Join(f.root, ".gitignore-not-a-dir", "x")
	if err := os.WriteFile(filepath.Join(f.root, ".gitignore-not-a-dir"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Clean(context.Background(), WorktreeCleanRequest{Dir: f.main, Tasks: []string{"a"}}); err == nil {
		t.Fatal("unusable state folder accepted")
	}
}
