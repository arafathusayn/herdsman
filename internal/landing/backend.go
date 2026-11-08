// Package landing holds the contract between the merge pipeline and the service
// that lands a change. A backend reports what happened in these terms; which
// contract a landing may accept is the pipeline's decision.
package landing

import "context"

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
	// request is still open at the pinned head. Never send the merge again; call
	// Reconcile until another state appears.
	MergeUnknown MergeState = "unknown"
	// MergeHeadMoved means the pull request's head is not the pinned commit, and it did not merge.
	MergeHeadMoved MergeState = "head_moved"
	// MergeFailed means the service refused the merge, or the pull request closed unmerged.
	MergeFailed MergeState = "failed"
	// MergeConflict means something this request did not make owns the outcome:
	// another pending request, a queue entry, or a merge at a different head. A
	// person decides.
	MergeConflict MergeState = "conflict"
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

// MergeRequest asks for one merge, pinned to the full commit id in Head.
type MergeRequest struct {
	Number  int
	Head    string
	Method  string // squash, merge or rebase
	Title   string
	Message string
}

// MergeRef names a merge to reconcile. UUID is empty when the request's answer was lost.
type MergeRef struct {
	Number int
	Head   string
	UUID   string
}

// MergeResult is one reading of a merge. Commit is set only for MergeMerged.
type MergeResult struct {
	State  MergeState
	Commit string
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
	// Merge requests one merge. An error means no request was sent; every outcome
	// after sending is a MergeResult.
	Merge(ctx context.Context, req MergeRequest) (MergeResult, error)
	// Reconcile reads where a merge ended, and never sends one. An error means ref is invalid.
	Reconcile(ctx context.Context, ref MergeRef) (MergeResult, error)
	// Status reads a pull request and every check on its head; a partial read is an error.
	Status(ctx context.Context, number int) (Status, error)
	// Rules reads the base branch's rules; a partial read is an error.
	Rules(ctx context.Context, base string) (Rules, error)
}
