package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arafathusayn/herdsman/internal/gitrun"
	"github.com/arafathusayn/herdsman/internal/project"
)

// linkedTree is a worktree other than the repository's own.
type linkedTree struct {
	path   string
	branch string
}

// linkedTrees reads git's list and nothing else: unlike the worktree service
// it opens no state, so it is safe on a daemon that may not write.
func linkedTrees(dir string) ([]linkedTree, error) {
	result, err := (&gitrun.Runner{}).Run(context.Background(), dir, "worktree", "list", "--porcelain")
	if err != nil {
		var exit *gitrun.ExitError
		if errors.As(err, &exit) && strings.Contains(exit.Stderr, "not a git repository") {
			return nil, nil
		}
		return nil, err
	}
	var out []linkedTree
	blocks := strings.Split(strings.TrimSpace(string(result.Stdout)), "\n\n")
	for _, block := range blocks[1:] { // The first is the repository itself.
		var tree linkedTree
		for line := range strings.SplitSeq(block, "\n") {
			if path, ok := strings.CutPrefix(line, "worktree "); ok {
				tree.path = path
			}
			if branch, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
				tree.branch = branch
			}
		}
		out = append(out, tree)
	}
	return out, nil
}

// taskAt names the task of the linked worktree holding folder. A task's
// worktree folder is named after it.
func taskAt(folder string) (string, bool, error) {
	if folder == "" {
		return "", false, nil
	}
	canon, err := filepath.EvalSymlinks(folder)
	if err != nil {
		return "", false, err
	}
	trees, err := linkedTrees(canon)
	if err != nil {
		return "", false, err
	}
	for _, tree := range trees {
		if canon == tree.path || strings.HasPrefix(canon, tree.path+string(filepath.Separator)) {
			return filepath.Base(tree.path), true, nil
		}
	}
	return "", false, nil
}

// refuseLiveWorktrees stops a delete that would orphan a task's worktree. A
// repository that is gone holds none; any other failure to look refuses, since
// the record is what ties the worktree to its task.
func refuseLiveWorktrees(store *project.Store, t *project.Task) error {
	p, err := store.GetProject(t.ProjectID)
	if err != nil {
		return err
	}
	for _, r := range p.Repos {
		if !isTaskRepo(t, r.Name) {
			continue
		}
		if _, err := os.Stat(r.Path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		trees, err := linkedTrees(r.Path)
		if err != nil {
			return fmt.Errorf("%w: cannot look for worktrees of task %s in %s: %v", project.ErrRefused, t.Slug, r.Name, err)
		}
		for _, tree := range trees {
			if filepath.Base(tree.path) == t.Slug || filepath.Base(tree.branch) == t.Slug {
				return fmt.Errorf("%w: task %s still has a worktree at %s; take it down first with `herdsman down %s`, or delete with --force", project.ErrRefused, t.Slug, tree.path, t.Slug)
			}
		}
	}
	return nil
}

func isTaskRepo(t *project.Task, name string) bool {
	for _, r := range t.Repos {
		if r == name {
			return true
		}
	}
	return false
}
