package wtree

import (
	"context"
	"errors"
	"strings"

	"github.com/arafathusayn/herdsman/internal/gitrun"
)

// What may prove a branch is integrated.
const (
	ProofAncestry    = "ancestry"
	ProofLanding     = "landing"
	ProofPullRequest = "pull_request"
)

// BranchProof is the evidence available for one branch.
type BranchProof struct {
	Target          string // The branch it would have reached.
	Landed          bool   // A landing record names this head.
	PullRequestHead string // The head of its merged pull request.
}

func isAncestor(ctx context.Context, git *gitrun.Runner, dir, commit, target string) (bool, error) {
	_, err := git.Run(ctx, dir, "merge-base", "--is-ancestor", commit, "refs/heads/"+target)
	var exit *gitrun.ExitError
	if errors.As(err, &exit) {
		return false, nil
	}
	return err == nil, err
}

// IntegrationProof names what proves head integrated, or "" for nothing.
func IntegrationProof(ctx context.Context, git *gitrun.Runner, dir, head string, proof BranchProof) (string, error) {
	if proof.Landed {
		return ProofLanding, nil
	}
	if head != "" && proof.PullRequestHead == head {
		return ProofPullRequest, nil
	}
	if head == "" {
		return "", nil
	}
	ancestor, err := isAncestor(ctx, git, dir, head, proof.Target)
	if ancestor {
		return ProofAncestry, err
	}
	return "", err
}

func checkedOut(ctx context.Context, git *gitrun.Runner, dir, branch string) (bool, error) {
	out, err := git.Run(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.Contains(string(out.Stdout)+"\n", "branch refs/heads/"+branch+"\n"), nil
}

// DeleteBranch removes a branch only with proof, only while no worktree holds
// it, and only at head: the delete is conditional on the ref's value, so a
// branch that moved since it was read stays. It returns the proof it used.
func DeleteBranch(ctx context.Context, git *gitrun.Runner, dir, branch, head string, proof BranchProof) (bool, string, error) {
	if branch == "" || branch == proof.Target {
		return false, "", nil
	}
	reason, err := IntegrationProof(ctx, git, dir, head, proof)
	if err != nil || reason == "" {
		return false, "", err
	}
	held, err := checkedOut(ctx, git, dir, branch)
	if err != nil || held {
		return false, "", err
	}
	_, err = git.Run(ctx, dir, "update-ref", "-d", "refs/heads/"+branch, head)
	var exit *gitrun.ExitError
	if errors.As(err, &exit) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	return true, reason, nil
}
