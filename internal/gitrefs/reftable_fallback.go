package gitrefs

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/arafathusayn/herdsman/internal/gitrun"
)

// SetRunner makes the reader's git calls go through runner, so that they share
// its trace. Set it before the first read.
func (r *Reader) SetRunner(runner *gitrun.Runner) { r.git = runner }

func (r *Reader) runGit(ctx context.Context, args ...string) (string, error) {
	// --git-dir, not -C: discovery could answer from a repository around gitDir.
	// An explicit git directory makes git assume a working tree, though, so a
	// bare layout whose config leaves core.bare to discovery has to say so. That
	// holds only for a git directory opened as itself: from a checkout, git
	// answers correctly with no flag.
	global := []string{"--git-dir=" + r.gitDir}
	if r.openedOnGitDir && r.currentState().bareUnset && filepath.Base(r.commonDir) != ".git" {
		global = append(global, "-c", "core.bare=true")
	}
	runner := r.git
	if runner == nil {
		runner = &gitrun.Runner{}
	}
	result, err := runner.Run(ctx, r.gitDir, append(global, args...)...)
	if err != nil {
		return "", fmt.Errorf("gitrefs: git %s: %w", args[0], err)
	}
	return string(result.Stdout), nil
}

func (r *Reader) forkRefs(ctx context.Context) ([]Ref, error) {
	out, err := r.runGit(ctx, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(symref)")
	if err != nil {
		return nil, err
	}
	return parseForEachRef(out)
}

func (r *Reader) forkHeads(ctx context.Context) ([]Head, error) {
	out, err := r.runGit(ctx, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	return parseWorktreeList(out)
}

func parseForEachRef(out string) ([]Ref, error) {
	var refs []Ref
	for line := range strings.Lines(out) {
		fields := strings.Split(strings.TrimSuffix(line, "\n"), "\x00")
		if len(fields) != 3 {
			return nil, fmt.Errorf("gitrefs: unexpected for-each-ref line: %q", line)
		}
		refs = append(refs, Ref{Name: fields[0], Object: fields[1], Symref: fields[2]})
	}
	return refs, nil
}

// parseWorktreeList reads git worktree list --porcelain -z: NUL-terminated
// attributes, with an empty attribute after each worktree.
func parseWorktreeList(out string) ([]Head, error) {
	var heads []Head
	var current Head
	for attribute := range strings.SplitSeq(out, "\x00") {
		if attribute != "" {
			key, value, _ := strings.Cut(attribute, " ")
			switch key {
			case "worktree":
				current.Path = value
			case "HEAD":
				current.Object = value
			case "branch":
				current.Branch = value
			case "bare":
				current.State = HeadBare
			}
			continue
		}
		if current == (Head{}) {
			continue
		}
		if current.Path == "" {
			return nil, errors.New("gitrefs: worktree list record has no path")
		}
		heads = append(heads, classifyHead(current))
		current = Head{}
	}
	return heads, nil
}

// classifyHead sets the state. A broken HEAD arrives as a null object with no
// branch and is reported as detached at nothing.
func classifyHead(head Head) Head {
	null := strings.Trim(head.Object, "0") == ""
	if null {
		head.Object = ""
	}
	switch {
	case head.State == HeadBare:
	case head.Branch == "":
		head.State = HeadDetached
	case null:
		head.State = HeadUnborn
	default:
		head.State = HeadAttached
	}
	return head
}
