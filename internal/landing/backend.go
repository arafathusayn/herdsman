// Package landing holds the contract between the merge pipeline and the service
// that lands a change. A backend reports what happened in these terms; which
// contract a landing may accept is the pipeline's decision.
package landing

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Contract says which commit herdsman's evidence covers once a change lands.
type Contract string

const (
	// ContractExact means the landed tree equals the candidate tree, or nothing lands.
	ContractExact Contract = "exact"
	// ContractDelegated means the service builds the landed commit.
	ContractDelegated Contract = "delegated"
)

// MergeState is where a merge request ended, as far as the service shows.
type MergeState string

const (
	// MergeMerged means the pull request merged at the pinned head.
	MergeMerged MergeState = "merged"
	// MergePending means an accepted request has not finished; poll with Reconcile.
	MergePending MergeState = "pending"
	// MergeUnknown means the request's outcome could not be read and the pull
	// request is still open at the pinned head. The request may never have reached
	// the service, so this state can last forever. Never send the merge again: use
	// Settle, which ends in MergeNeedsPerson.
	MergeUnknown MergeState = "unknown"
	// MergeHeadMoved means the pull request's head is not the pinned commit, and it did not merge.
	MergeHeadMoved MergeState = "head_moved"
	// MergeFailed means the service refused the merge, or the pull request closed unmerged.
	MergeFailed MergeState = "failed"
	// MergeConflict means something this request did not make owns the outcome:
	// another pending request, a queue entry, or a merge at a different head. A
	// person decides.
	MergeConflict MergeState = "conflict"
	// MergeNeedsPerson means a bounded Settle found no final state, or the pull
	// request is not on the expected base, merged or not. The merge must not be
	// sent again; a person looks at the pull request, the head, the base and the uuid.
	MergeNeedsPerson MergeState = "needs_person"
)

// OpenRequest asks for a pull request from Head into Base.
type OpenRequest struct {
	Head  string
	Base  string
	Title string
	Body  string
	Draft bool
}

// PullRequest identifies an open pull request.
type PullRequest struct {
	Number  int
	URL     string
	HeadRef string
	HeadSHA string
	BaseRef string
	Draft   bool
}

// MergeRequest asks for one merge, pinned to the full commit id in Head, into
// Base, the branch whose rules were read. A pull request already on another base
// is not sent. GitHub has no pin for the base, so a retarget after the backend's
// last read before the request, or while the request is pending, can still merge
// into the other base. That window cannot be closed here: such a merge is never
// reported as MergeMerged, and it ends in MergeNeedsPerson.
type MergeRequest struct {
	Number  int
	Head    string
	Base    string
	Method  string // squash, merge or rebase
	Title   string
	Message string
}

// MergeRef names a merge to reconcile, with the base it was requested for. UUID
// is empty when the request's answer was lost.
type MergeRef struct {
	Number int
	Head   string
	Base   string
	UUID   string
}

// MergeResult is one reading of a merge. Commit is set only for MergeMerged, and
// Head and Base, the expected base, only for MergeNeedsPerson.
type MergeResult struct {
	State  MergeState
	Commit string
	Head   string
	Base   string
	UUID   string
	Reason string
}

// CheckState is a check's result reduced to what a gate needs.
type CheckState string

const (
	// CheckPassed covers success, and results that do not block: neutral and skipped.
	CheckPassed CheckState = "passed"
	// CheckPending means the check has not finished.
	CheckPending CheckState = "pending"
	// CheckFailed covers every other result.
	CheckFailed CheckState = "failed"
)

// Check is one check on the pull request's head.
type Check struct {
	Name  string
	State CheckState
}

// Status is a pull request as the service shows it now. State is open, closed
// or merged. MergeCommit is set only when State is merged.
type Status struct {
	Number         int
	State          string
	Draft          bool
	HeadRef        string
	HeadSHA        string
	BaseRef        string
	MergeCommit    string
	MergeableState string
	Checks         []Check
}

// Rules is what the base branch's rules allow. Contract is exact only when the
// up-to-date rule is in force and no merge queue applies.
type Rules struct {
	Contract   Contract
	UpToDate   bool
	MergeQueue bool
	Reason     string
}

// Backend lands changes on one hosting service. Rules is separate from Merge on
// purpose: the pipeline reads the contract and decides, before it asks to merge.
type Backend interface {
	// Open returns the open pull request for the head and base, creating it if there is none.
	Open(ctx context.Context, req OpenRequest) (PullRequest, error)
	// Merge requests one merge. An error, including a context done before sending,
	// means no request was sent; every outcome after sending is a MergeResult.
	Merge(ctx context.Context, req MergeRequest) (MergeResult, error)
	// Reconcile takes one reading of where a merge ended, and never sends one. An
	// error means ref is invalid. Use Settle to repeat it within a bound.
	Reconcile(ctx context.Context, ref MergeRef) (MergeResult, error)
	// Status reads a pull request and every check on its head; a partial read is an error.
	Status(ctx context.Context, number int) (Status, error)
	// Rules reads the base branch's rules; a partial read is an error.
	Rules(ctx context.Context, base string) (Rules, error)
}

// SettleLimit bounds Settle: at most Attempts readings, Interval apart.
type SettleLimit struct {
	Attempts int
	Interval time.Duration
}

// Settle is the exit from MergePending and MergeUnknown. It reads the merge
// until a final state appears, and after limit.Attempts readings it stops with
// MergeNeedsPerson, naming the head and the uuid. It never sends a merge. An
// error means the limit or ref is invalid, or ctx ended; then the result is the
// last reading.
func Settle(ctx context.Context, b Backend, ref MergeRef, limit SettleLimit) (MergeResult, error) {
	if limit.Attempts < 1 {
		return MergeResult{}, errors.New("landing: settle needs at least one attempt")
	}
	var last MergeResult
	for attempt := range limit.Attempts {
		if attempt > 0 {
			select {
			case <-time.After(limit.Interval):
			case <-ctx.Done():
				return last, ctx.Err()
			}
		}
		var err error
		if last, err = b.Reconcile(ctx, ref); err != nil {
			return MergeResult{}, err
		}
		if last.State != MergePending && last.State != MergeUnknown {
			return last, nil
		}
	}
	return MergeResult{
		State: MergeNeedsPerson, Head: ref.Head, Base: ref.Base, UUID: ref.UUID,
		Reason: fmt.Sprintf("pull request %d at head %s into %s (request uuid %q) has no final state after %d readings, last %s: %s; do not send the merge again",
			ref.Number, ref.Head, ref.Base, ref.UUID, limit.Attempts, last.State, last.Reason),
	}, nil
}
