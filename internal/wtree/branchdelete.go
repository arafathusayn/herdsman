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
	// ProofPatchEquivalent is no proof: it is what a person's --delete-branch
	// accepted, for a branch whose every change is already on the target.
	ProofPatchEquivalent = "patch_equivalent"
)

// BranchProof is the evidence available for one branch.
type BranchProof struct {
	Target          string // The branch it would have reached.
	Landed          bool   // A landing record names this head.
	PullRequestHead string // The head of its merged pull request.
	// AcceptEquivalent is a person's authority to delete a branch whose every
	// commit has a patch-identical one on the target. It proves nothing alone,
	// so it decides only the branch and never whether a tree is safe to free.
	AcceptEquivalent bool
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

// PatchEquivalent reports a head with commits ahead of target, every one of
// which has a patch-identical commit on target.
func PatchEquivalent(ctx context.Context, git *gitrun.Runner, dir, head, target string) (bool, error) {
	out, err := git.Run(ctx, dir, "cherry", "refs/heads/"+target, head)
	if err != nil {
		return false, err
	}
	lines := strings.Fields(string(out.Stdout))
	if len(lines) == 0 {
		return false, nil
	}
	for i := 0; i < len(lines); i += 2 {
		if lines[i] != "-" {
			return false, nil
		}
	}
	return true, nil
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
	if err != nil {
		return false, "", err
	}
	if reason == "" && proof.AcceptEquivalent {
		equivalent, err := PatchEquivalent(ctx, git, dir, head, proof.Target)
		if err != nil {
			return false, "", err
		}
		if equivalent {
			reason = ProofPatchEquivalent
		}
	}
	if reason == "" {
		return false, "", nil
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
