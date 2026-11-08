package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/ledger"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/snapshot"
	"github.com/arafathusayn/herdsman/internal/statedir"
	"github.com/arafathusayn/herdsman/internal/wtree"
)

// WorktreeCleanPlanRequest selects the trees a clean would free.
type WorktreeCleanPlanRequest struct {
	Dir           string `json:"dir"`
	Selector      string `json:"selector,omitempty"`
	Route         string `json:"route,omitempty"`
	All           bool   `json:"all,omitempty"`
	Merged        bool   `json:"merged,omitempty"`
	MinAgeSeconds int64  `json:"min_age_seconds,omitempty"`
	KeepBranch    bool   `json:"keep_branch,omitempty"`
}

// WorktreeCleanPlan says what cleaning one tree would do and what stands in
// its way.
type WorktreeCleanPlan struct {
	Task       string `json:"task"`
	Branch     string `json:"branch"`
	Path       string `json:"path"`
	Head       string `json:"head"`
	Proof      string `json:"proof,omitempty"`
	NeedsForce bool   `json:"needs_force,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Blocked    string `json:"blocked,omitempty"`
	// PullRequestHead is the merged pull request's head, passed back to Clean
	// so that a whole pass reads GitHub once. A stale value proves nothing,
	// because it must equal the branch's head when it is used.
	PullRequestHead string   `json:"pull_request_head,omitempty"`
	Running         []string `json:"running,omitempty"`
	BranchAction    string   `json:"branch_action,omitempty"`
}

// WorktreeCleanPlanResponse is one plan per tree.
type WorktreeCleanPlanResponse struct {
	Plans    []WorktreeCleanPlan `json:"plans"`
	Warnings []string            `json:"warnings,omitempty"`
}

// WorktreeCleanRequest frees the named tasks. Each is checked again here,
// because the machine may have changed since its plan was printed.
type WorktreeCleanRequest struct {
	Dir        string   `json:"dir"`
	Tasks      []string `json:"tasks"`
	Force      bool     `json:"force,omitempty"`
	KeepBranch bool     `json:"keep_branch,omitempty"`
	Named      []string `json:"named,omitempty"`
	// PullRequestHeads maps branch to head from the plan. Nil reads GitHub.
	PullRequestHeads map[string]string `json:"pull_request_heads,omitempty"`
}

// WorktreeCleanReport is one task's outcome.
type WorktreeCleanReport struct {
	wtree.CleanReport
	Error string `json:"error,omitempty"`
}

// WorktreeCleanResponse is one report per task.
type WorktreeCleanResponse struct {
	Reports  []WorktreeCleanReport `json:"reports"`
	Warnings []string              `json:"warnings,omitempty"`
}

// mergedHeads reads every merged pull request in one call, so that a pass over
// many trees costs the same as a pass over one. It maps branch to head.
func (s *WorktreeService) mergedHeads(ctx context.Context, r *repository) (map[string]string, error) {
	repo, err := s.repoName(ctx, r)
	if err != nil {
		return nil, err
	}
	program := s.GH
	if program == "" {
		program = "gh"
	}
	out, err := exec.CommandContext(ctx, program, "pr", "list", "--repo", repo, "--state", "merged", "--limit", "1000", "--json", "headRefName,headRefOid").Output()
	if err != nil {
		return nil, fmt.Errorf("read merged pull requests: %w", err)
	}
	var list []struct {
		HeadRefName string `json:"headRefName"`
		HeadRefOid  string `json:"headRefOid"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("read merged pull requests: %w", err)
	}
	heads := make(map[string]string, len(list))
	for _, pr := range list {
		heads[pr.HeadRefName] = pr.HeadRefOid
	}
	return heads, nil
}

// blockedReason names why a tree is never cleaned, or "" when it may be.
func (r *repository) blockedReason(row wtree.Row, bulk bool) string {
	switch {
	case row.Main || row.Bare || row.IsDefault:
		return "it is the repository's main tree"
	case row.Locked:
		return "the worktree is locked"
	case bulk && (r.dir == row.Path || strings.HasPrefix(r.dir, row.Path+string(filepath.Separator))):
		return "it is the current tree"
	}
	return ""
}

func (r *repository) cleanTargets(ctx context.Context, req WorktreeCleanPlanRequest) ([]wtree.Row, error) {
	return r.targets(ctx, WorktreeTeardownPlanRequest{Dir: req.Dir, Selector: req.Selector, Route: req.Route, All: req.All || req.Merged})
}

// ages maps each recorded task to when its worktree was made.
func (r *repository) ages(ctx context.Context) (map[string]time.Time, error) {
	worktrees, err := r.state.Worktrees(ctx)
	if err != nil {
		return nil, err
	}
	created := make(map[string]time.Time, len(worktrees))
	for _, worktree := range worktrees {
		created[worktree.Task] = worktree.CreatedAt
	}
	return created, nil
}

// runningLines lists what still runs in or for a task. Beyond what teardown may
// stop, it counts any process working inside the tree whatever its evidence:
// such a process may not be this task's to stop, but its folder is about to go.
func runningLines(ctx context.Context, common, task string) ([]string, error) {
	plan, err := ledgerService.Plan(ctx, LedgerRequest{CommonDir: common, Task: task})
	if err != nil {
		return nil, err
	}
	var running []string
	for _, line := range plan.Lines {
		inside := line.Action != ledger.ActionExclude && slices.Contains(line.Signals, ledger.SignalCwd)
		if inside || line.Action == ledger.ActionStop || line.Action == ledger.ActionAsk {
			running = append(running, line.Kind+" "+line.ID)
		}
	}
	return running, nil
}

func (r *repository) planOne(ctx context.Context, row wtree.Row, bulk, keepBranch bool, heads map[string]string) (WorktreeCleanPlan, wtree.Safety, error) {
	plan := WorktreeCleanPlan{Task: row.Task, Branch: row.Branch, Path: row.Path, Head: row.Head, BranchAction: "keep", PullRequestHead: heads[row.Branch]}
	if plan.Blocked = r.blockedReason(row, bulk); plan.Blocked != "" {
		return plan, wtree.Safety{}, nil
	}
	safety, err := wtree.CheckSafety(ctx, r.git, row.Path, row.Head, wtree.BranchProof{Target: r.dflt, PullRequestHead: heads[row.Branch]})
	if err != nil {
		return plan, safety, err
	}
	plan.Proof, plan.NeedsForce, plan.Reason = safety.Proof, safety.NeedsForce(), safety.Reason()
	if safety.Proof != "" && !keepBranch && row.Branch != "" {
		plan.BranchAction = "delete"
	}
	plan.Running, err = runningLines(ctx, r.common, row.Task)
	return plan, safety, err
}

// CleanPlan builds one plan per selected tree and changes nothing.
func (s *WorktreeService) CleanPlan(ctx context.Context, req WorktreeCleanPlanRequest) (WorktreeCleanPlanResponse, error) {
	resp := WorktreeCleanPlanResponse{Plans: []WorktreeCleanPlan{}}
	r, err := s.open(ctx, req.Dir)
	if err != nil {
		return resp, err
	}
	defer r.close()
	rows, err := r.cleanTargets(ctx, req)
	if err != nil {
		return resp, err
	}
	created, err := r.ages(ctx)
	if err != nil {
		return resp, err
	}
	heads, err := s.mergedHeads(ctx, r)
	if err != nil {
		resp.Warnings = append(resp.Warnings, "merged pull requests are not considered: "+err.Error())
	}
	bulk := req.All || req.Merged || req.Route != ""
	for _, row := range rows {
		if req.MinAgeSeconds > 0 && time.Since(created[row.Task]) < time.Duration(req.MinAgeSeconds)*time.Second {
			continue
		}
		plan, _, err := r.planOne(ctx, row, bulk, req.KeepBranch, heads)
		if err != nil {
			return resp, err
		}
		if req.Merged && plan.Blocked == "" && plan.Proof == "" {
			continue
		}
		resp.Plans = append(resp.Plans, plan)
	}
	return resp, nil
}

// recovery opens the account's snapshot store.
func (s *WorktreeService) recovery() (*ops.Recovery, func(), error) {
	root := s.StateDir
	if root == "" {
		dir, err := statedir.Resolve("", os.Getenv)
		if err != nil {
			return nil, nil, err
		}
		root = dir.Root
	}
	folder := filepath.Join(root, "daemon")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return nil, nil, err
	}
	db, err := store.Open(filepath.Join(folder, "account.db"), store.Writer, store.KindAccount)
	if err != nil {
		return nil, nil, fmt.Errorf("account database: %w", err)
	}
	rec := ops.NewRecovery(ops.RecoveryOptions{StateDir: root})
	rec.Attach(ops.NewEngine(db, ops.Options{Effects: rec.Effects()}))
	return rec, func() { db.Close() }, nil
}

func takeSnapshot(rec *ops.Recovery) func(context.Context, wtree.SnapshotQuery) (string, *snapshot.Manifest, error) {
	return func(ctx context.Context, q wtree.SnapshotQuery) (string, *snapshot.Manifest, error) {
		op, err := rec.Snapshot(ctx, ops.SnapshotRequest{Task: q.Task, Worktree: q.Path, Rebuildable: q.Rebuildable, FileCap: q.FileCap})
		if err != nil {
			return "", nil, err
		}
		records, err := rec.Snapshots()
		if err != nil {
			return "", nil, err
		}
		at := slices.IndexFunc(records, func(record ops.SnapshotRecord) bool { return record.ID == op.ID })
		if at < 0 {
			return "", nil, fmt.Errorf("snapshot %s has no record", op.ID)
		}
		manifest, err := snapshot.ReadManifest(records[at].Dir)
		return op.ID, manifest, err
	}
}

func (s *WorktreeService) cleanOne(ctx context.Context, r *repository, rec *ops.Recovery, req WorktreeCleanRequest, task string, heads map[string]string) (*wtree.CleanReport, error) {
	row, err := wtree.ResolveSelector(wtree.Selector{Kind: wtree.SelectName, Value: task}, r.selectContext(""))
	if err != nil {
		return nil, err
	}
	if reason := r.blockedReason(row, false); reason != "" {
		return nil, errors.New(reason)
	}
	vars := r.vars(row.Task, row.Branch, row.Path, "feat", "", row.Head)
	before, err := s.plan(r, "pre-clean", vars)
	if err != nil {
		return nil, err
	}
	after, err := s.plan(r, "post-clean", vars)
	if err != nil {
		return nil, err
	}
	deps := wtree.CleanDeps{
		Git: r.git, State: r.state, Snapshot: takeSnapshot(rec),
		Running: func(ctx context.Context) ([]string, error) { return runningLines(ctx, r.common, row.Task) },
		PreClean: func(ctx context.Context) error {
			_, err := r.runner(vars, row.Path).Run(ctx, "pre-clean", before)
			return err
		},
		// The tree is gone by now, so these hooks run from the main one.
		PostClean: func(context.Context) error {
			_, err := r.runner(vars, r.main).Start("post-clean", after)
			return err
		},
		Drain: func(trashed string) error { return wtree.StartDrain(r.common, trashed) },
	}
	return wtree.Clean(ctx, deps, wtree.CleanRequest{
		Common: r.common, Main: r.main, Task: row.Task, Branch: row.Branch, Path: row.Path, Head: row.Head, Default: r.dflt,
		PullRequestHead: heads[row.Branch], Force: req.Force, KeepBranch: req.KeepBranch, Named: req.Named,
		Rebuildable: snapshot.DefaultRebuildable(), FileCap: s.FileCap,
	})
}

// Clean frees each task in turn. One refusal does not stop the next task,
// because each tree's safety stands alone.
func (s *WorktreeService) Clean(ctx context.Context, req WorktreeCleanRequest) (WorktreeCleanResponse, error) {
	var resp WorktreeCleanResponse
	r, err := s.open(ctx, req.Dir)
	if err != nil {
		return resp, err
	}
	defer r.close()
	rec, closeStore, err := s.recovery()
	if err != nil {
		return resp, err
	}
	defer closeStore()
	heads := req.PullRequestHeads
	if heads == nil {
		if heads, err = s.mergedHeads(ctx, r); err != nil {
			resp.Warnings = append(resp.Warnings, "merged pull requests are not considered: "+err.Error())
		}
	}
	for _, task := range req.Tasks {
		report := WorktreeCleanReport{CleanReport: wtree.CleanReport{Task: task}}
		done, err := s.cleanOne(ctx, r, rec, req, task, heads)
		if done != nil {
			report.CleanReport = *done
		}
		if err != nil {
			report.Error = err.Error()
		}
		resp.Reports = append(resp.Reports, report)
	}
	return resp, nil
}
