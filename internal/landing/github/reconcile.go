package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/arafathusayn/herdsman/internal/landing"
)

// Reconcile reads where a merge ended. It only reads: a lost response is
// settled by fetching, never by asking for the merge again.
func (b *Backend) Reconcile(ctx context.Context, ref landing.MergeRef) (landing.MergeResult, error) {
	if err := validateRef(ref.Number, ref.Head); err != nil {
		return landing.MergeResult{}, err
	}
	return b.reconcile(ctx, ref), nil
}

// reconcile reads the request first and the pull request second, so the newest
// fact decides: a request that failed because the head moved reads as a moved head.
func (b *Backend) reconcile(ctx context.Context, ref landing.MergeRef) landing.MergeResult {
	var request landing.MergeResult
	if ref.UUID != "" {
		request = b.readRequest(ctx, ref)
	}
	pr, err := b.readPR(ctx, ref.Number)
	if err != nil {
		return landing.MergeResult{State: landing.MergeUnknown, UUID: ref.UUID, Reason: err.Error()}
	}
	if res, final := classify(pr, ref.Head); final {
		res.UUID = ref.UUID
		return res
	}
	if ref.UUID == "" {
		return landing.MergeResult{State: landing.MergeUnknown, Reason: "the pull request is open at the pinned head and no request is known"}
	}
	return request
}

// classify decides what the pull request alone proves. The pull request wins
// over the request's status, because it says what actually landed.
func classify(pr prJSON, head string) (landing.MergeResult, bool) {
	switch {
	case pr.Merged && pr.Head.SHA == head:
		return landing.MergeResult{State: landing.MergeMerged, Commit: pr.MergeCommitSHA}, true
	case pr.Merged:
		return landing.MergeResult{State: landing.MergeConflict, Reason: fmt.Sprintf("merged at %s, not at the pinned head", pr.Head.SHA)}, true
	case pr.State != "open":
		return landing.MergeResult{State: landing.MergeFailed, Reason: "the pull request closed without merging"}, true
	case pr.Head.SHA != head:
		return landing.MergeResult{State: landing.MergeHeadMoved, Reason: fmt.Sprintf("the head is now %s", pr.Head.SHA)}, true
	}
	return landing.MergeResult{}, false
}

// readRequest reads a known request for a pull request still open at the pin.
func (b *Backend) readRequest(ctx context.Context, ref landing.MergeRef) landing.MergeResult {
	res := landing.MergeResult{State: landing.MergePending, UUID: ref.UUID}
	r := b.call(ctx, "GET", b.api("pulls/"+strconv.Itoa(ref.Number)+"/merge-async/"+ref.UUID), nil)
	if r.status == 404 {
		res.State, res.Reason = landing.MergeUnknown, "the merge request is gone"
		return res
	}
	var a asyncReply
	if r.status != 200 || json.Unmarshal(r.body, &a) != nil {
		res.Reason = "the merge request could not be read"
		return res
	}
	switch a.Status {
	case "merged":
		if a.Details.SHA != "" {
			res.State, res.Commit = landing.MergeMerged, a.Details.SHA
		}
	case "enqueued":
		res.State, res.Reason = landing.MergeConflict, "the pull request is in a merge queue"
	case "failed":
		res.State, res.Reason = landing.MergeFailed, a.Details.Message
	}
	return res
}
