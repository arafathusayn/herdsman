package wtree

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/arafathusayn/herdsman/internal/gitrun"
	"github.com/arafathusayn/herdsman/internal/snapshot"
)

// ErrStillRunning and ErrUnsafe are the refusals before anything is removed.
var (
	ErrStillRunning = errors.New("the task still runs")
	ErrUnsafe       = errors.New("the tree holds work that is not saved elsewhere")
)

// UncapturedError lists the paths no snapshot, pattern or name accounts for.
type UncapturedError struct {
	Paths   []string
	Reasons map[string]string
}

func (e *UncapturedError) Error() string {
	shown := make([]string, 0, len(e.Paths))
	for _, p := range e.Paths {
		if reason := e.Reasons[p]; reason != "" {
			p += " (" + reason + ")"
		}
		shown = append(shown, p)
	}
	return fmt.Sprintf("not captured by the snapshot: %s; add a rebuildable pattern or name them with --force-irreversible", strings.Join(shown, ", "))
}

// SnapshotQuery asks for one verified snapshot of a tree.
type SnapshotQuery struct {
	Task, Path  string
	Rebuildable []string
	FileCap     int64
}

// CleanDeps are the effects Clean does not perform itself. A nil hook, Running
// or Drain does nothing; Snapshot is required.
type CleanDeps struct {
	Git       *gitrun.Runner
	State     *State
	Snapshot  func(context.Context, SnapshotQuery) (string, *snapshot.Manifest, error)
	Running   func(context.Context) ([]string, error)
	PreClean  func(context.Context) error
	PostClean func(context.Context) error
	Drain     func(trashed string) error
}

// CleanRequest describes one tree to free.
type CleanRequest struct {
	Common, Main, Task, Branch, Path, Head, Default string
	PullRequestHead                                 string
	Landed, Force, KeepBranch                       bool
	Named, Rebuildable                              []string
	FileCap                                         int64
}

// CleanReport is what happened to one tree.
type CleanReport struct {
	Task           string   `json:"task"`
	Snapshot       string   `json:"snapshot,omitempty"`
	Forced         bool     `json:"forced,omitempty"`
	Rebuildable    []string `json:"rebuildable,omitempty"`
	Submodules     []string `json:"submodules,omitempty"`
	Lost           []string `json:"lost,omitempty"`
	UnmatchedNames []string `json:"unmatched_names,omitempty"`
	BranchDeleted  bool     `json:"branch_deleted,omitempty"`
	BranchReason   string   `json:"branch_reason,omitempty"`
	Kept           []string `json:"kept,omitempty"`
	FreeBefore     uint64   `json:"free_before,omitempty"`
	FreeAfter      uint64   `json:"free_after,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
}

// Safety says what removing a tree would put at risk.
type Safety struct {
	Dirty, Unpushed bool
	Proof           string
}

// NeedsForce is true when work exists only in this tree.
func (s Safety) NeedsForce() bool { return s.Dirty || s.Unpushed && s.Proof == "" }

// Reason describes why force is needed.
func (s Safety) Reason() string {
	if s.Dirty {
		return "the tree has uncommitted changes"
	}
	if s.NeedsForce() {
		return "the branch has commits on no remote and is not integrated"
	}
	return ""
}

// CheckSafety reads a tree's state; it changes nothing.
func CheckSafety(ctx context.Context, git *gitrun.Runner, path, head string, proof BranchProof) (Safety, error) {
	var s Safety
	status, err := git.Run(ctx, path, "status", "--porcelain")
	if err != nil {
		return s, err
	}
	s.Dirty = len(strings.TrimSpace(string(status.Stdout))) > 0
	if head != "" {
		out, err := git.Run(ctx, path, "rev-list", "-n", "1", head, "--not", "--remotes")
		if err != nil {
			return s, err
		}
		s.Unpushed = len(strings.TrimSpace(string(out.Stdout))) > 0
	}
	s.Proof, err = IntegrationProof(ctx, git, path, head, proof)
	return s, err
}

func freeBytes(path string) uint64 {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return 0
	}
	return uint64(st.Bavail) * uint64(st.Bsize)
}

func callHook(ctx context.Context, hook func(context.Context) error) error {
	if hook == nil {
		return nil
	}
	return hook(ctx)
}

// keptKinds are resources Clean leaves in place: no snapshot holds them yet,
// and something no snapshot holds is not deleted.
var keptKinds = []string{"volume", "network", "scratch", "database"}

func keptResources(ctx context.Context, state *State, task string) ([]string, error) {
	resources, err := state.Resources(ctx, task)
	if err != nil {
		return nil, err
	}
	var kept []string
	for _, r := range resources {
		for _, kind := range keptKinds {
			if r.Kind == kind {
				kept = append(kept, r.Kind+" "+r.ID)
			}
		}
	}
	return kept, nil
}

func forgetTask(ctx context.Context, state *State, task string) error {
	if _, err := state.DB().ExecContext(ctx, `DELETE FROM resources WHERE task = ? AND kind NOT IN ('volume','network','scratch','database')`, task); err != nil {
		return err
	}
	return state.DeleteWorktree(ctx, task)
}

func refuseWhileRunning(ctx context.Context, deps CleanDeps) error {
	if deps.Running == nil {
		return nil
	}
	running, err := deps.Running(ctx)
	if err != nil {
		return err
	}
	if len(running) > 0 {
		return fmt.Errorf("%w: %s; run teardown or down first", ErrStillRunning, strings.Join(running, ", "))
	}
	return nil
}

// Clean frees one tree. Each refusal comes before the rename, so a tree either
// stays whole or was captured and accounted for path by path.
func Clean(ctx context.Context, deps CleanDeps, req CleanRequest) (*CleanReport, error) {
	if err := ValidateNamed(req.Named); err != nil {
		return nil, err
	}
	if err := refuseWhileRunning(ctx, deps); err != nil {
		return nil, err
	}
	report := &CleanReport{Task: req.Task, Forced: req.Force, FreeBefore: freeBytes(req.Common)}

	id, manifest, err := deps.Snapshot(ctx, SnapshotQuery{Task: req.Task, Path: req.Path, Rebuildable: req.Rebuildable, FileCap: req.FileCap})
	if err != nil {
		return nil, fmt.Errorf("snapshot: %w; nothing was removed", err)
	}
	report.Snapshot = id

	problems, err := CheckSubmodules(ctx, deps.Git, req.Path, manifest)
	if err != nil {
		return nil, err
	}
	inventory, err := TakeInventory(req.Path, manifest, req.Named, problems)
	if err != nil {
		return nil, err
	}
	if len(inventory.Uncaptured) > 0 {
		return nil, &UncapturedError{Paths: inventory.Uncaptured, Reasons: inventory.Reasons}
	}
	report.Rebuildable, report.Lost, report.UnmatchedNames = inventory.Rebuildable, inventory.Named, inventory.UnmatchedNames
	report.Submodules = inventory.Submodules

	proof := BranchProof{Target: req.Default, Landed: req.Landed, PullRequestHead: req.PullRequestHead}
	safety, err := CheckSafety(ctx, deps.Git, req.Path, req.Head, proof)
	if err != nil {
		return nil, err
	}
	if safety.NeedsForce() && !req.Force {
		return nil, fmt.Errorf("%w: %s; --force goes on, the snapshot holds it", ErrUnsafe, safety.Reason())
	}

	if err := callHook(ctx, deps.PreClean); err != nil {
		return nil, fmt.Errorf("pre-clean: %w; nothing was removed", err)
	}
	if report.Kept, err = keptResources(ctx, deps.State, req.Task); err != nil {
		return nil, err
	}

	trashed, err := MoveToTrash(req.Common, req.Path, req.Task+"-"+filepath.Base(id))
	if err != nil {
		return nil, err
	}
	if deps.Drain != nil {
		if err := deps.Drain(trashed); err != nil {
			report.Warnings = append(report.Warnings, "the trash was not drained: "+err.Error())
		}
	}
	// The worktree record must go first: git counts a pruned tree's branch as free.
	if _, err := deps.Git.Run(ctx, req.Main, "worktree", "prune"); err != nil {
		return report, err
	}
	if !req.KeepBranch {
		if report.BranchDeleted, report.BranchReason, err = DeleteBranch(ctx, deps.Git, req.Main, req.Branch, req.Head, proof); err != nil {
			return report, err
		}
	}
	if err := forgetTask(ctx, deps.State, req.Task); err != nil {
		return report, err
	}
	if err := callHook(ctx, deps.PostClean); err != nil {
		report.Warnings = append(report.Warnings, "post-clean: "+err.Error())
	}
	report.FreeAfter = freeBytes(req.Common)
	return report, nil
}
