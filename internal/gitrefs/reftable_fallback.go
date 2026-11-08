package gitrefs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// repositoryEnv lists the variables that point git at another repository
// (git rev-parse --local-env-vars, plus GIT_NAMESPACE). The reader was opened
// on an explicit path, so the caller's values must not redirect it.
var repositoryEnv = []string{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_CONFIG", "GIT_CONFIG_COUNT",
	"GIT_CONFIG_PARAMETERS", "GIT_DIR", "GIT_GRAFT_FILE", "GIT_IMPLICIT_WORK_TREE", "GIT_INDEX_FILE",
	"GIT_NAMESPACE", "GIT_NO_REPLACE_OBJECTS", "GIT_OBJECT_DIRECTORY", "GIT_PREFIX",
	"GIT_REPLACE_REF_BASE", "GIT_SHALLOW_FILE", "GIT_WORK_TREE",
}

func (r *Reader) runGit(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", r.gitDir}, args...)...)
	cmd.Env = slices.DeleteFunc(os.Environ(), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		return slices.Contains(repositoryEnv, name)
	})
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gitrefs: git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
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
