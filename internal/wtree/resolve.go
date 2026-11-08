package wtree

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Naming holds the two templates that decide where a task lives and what its
// branch is called.
type Naming struct {
	WorktreePath string
	BranchName   string
}

// DefaultNaming is used when the configuration sets neither template: a
// folder beside the repository, and a branch of the form type/task.
func DefaultNaming() Naming {
	return Naming{
		WorktreePath: "{{ repo_path }}/../{{ repo }}.worktrees/{{ task }}",
		BranchName:   "{{ type }}/{{ task }}",
	}
}

// ResolvePath renders the worktree path. The result is absolute and clean, and
// is never the repository's own folder.
func (n Naming) ResolvePath(v Vars) (string, error) {
	rendered, err := Render("worktree path", n.WorktreePath, v)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(rendered) {
		return "", fmt.Errorf("worktree path %q is not absolute", rendered)
	}
	path := filepath.Clean(rendered)
	if repo := v.Values["repo_path"]; repo != "" && path == filepath.Clean(repo) {
		return "", fmt.Errorf("worktree path %q is the repository itself", path)
	}
	return path, nil
}

// ResolveBranch renders the branch name and checks that git would accept it.
func (n Naming) ResolveBranch(v Vars) (string, error) {
	branch, err := Render("branch name", n.BranchName, v)
	if err != nil {
		return "", err
	}
	if err := ValidateBranch(branch); err != nil {
		return "", err
	}
	return branch, nil
}

// ValidateBranch applies git's rules for a branch name without starting git.
func ValidateBranch(name string) error {
	if reason := branchProblem(name); reason != "" {
		return fmt.Errorf("invalid branch name %q: %s", name, reason)
	}
	return nil
}

func branchProblem(name string) string {
	switch {
	case name == "":
		return "empty"
	case name == "@" || name == "HEAD":
		return "reserved"
	case name[0] == '-':
		return "starts with a dash"
	case name[len(name)-1] == '.':
		return "ends with a dot"
	case strings.Contains(name, ".."):
		return `contains ".."`
	case strings.Contains(name, "@{"):
		return `contains "@{"`
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return fmt.Sprintf("contains %q", r)
		}
	}
	for component := range strings.SplitSeq(name, "/") {
		switch {
		case component == "":
			return "has an empty path component"
		case component[0] == '.':
			return "has a component starting with a dot"
		case strings.HasSuffix(component, ".lock"):
			return `has a component ending in ".lock"`
		}
	}
	return ""
}

const taskNameLimit = 60

var taskName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidateTask requires a kebab-case name, since a task name becomes a folder,
// part of a branch and part of a database name.
func ValidateTask(name string) error {
	if len(name) > taskNameLimit {
		return fmt.Errorf("task name is longer than %d characters", taskNameLimit)
	}
	if !taskName.MatchString(name) {
		return errors.New("task name must be kebab-case: lowercase letters and digits joined by single dashes")
	}
	return nil
}
