package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/arafathusayn/herdsman/internal/landing"
)

// A pin must be a full commit id: GitHub compares it with the head literally.
var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

const directMerge = "direct_merge"

// asyncReply is an answer of the merge-async API. GitHub puts the request's
// uuid under details and leaves the top-level field null.
type asyncReply struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	UUID    string `json:"uuid"`
	Details struct {
		UUID            string `json:"uuid"`
		SHA             string `json:"sha"`
		Message         string `json:"message"`
		ExpectedHeadSHA string `json:"expected_head_sha"`
		MergeMethod     string `json:"merge_method"`
		MergeAction     string `json:"merge_action"`
		BypassRules     bool   `json:"bypass_rules"`
	} `json:"details"`
}

func (a asyncReply) uuid() string {
	if a.Details.UUID != "" {
		return a.Details.UUID
	}
	return a.UUID
}

// Merge requests one direct merge pinned to req.Head and waits for its end, up
// to PollTimeout. The request is sent at most once: after it, the only calls
// are reads, whatever came back.
func (b *Backend) Merge(ctx context.Context, req landing.MergeRequest) (landing.MergeResult, error) {
	if err := validateRef(req.Number, req.Head); err != nil {
		return landing.MergeResult{}, err
	}
	if req.Base == "" {
		return landing.MergeResult{}, errors.New("github: merge needs a base branch")
	}
	if req.Method != "squash" && req.Method != "merge" && req.Method != "rebase" {
		return landing.MergeResult{}, fmt.Errorf("github: invalid merge method %q", req.Method)
	}
	pr, err := b.readPR(ctx, req.Number)
	if err != nil {
		return landing.MergeResult{}, err
	}
	if pr.Base.Ref != req.Base {
		return landing.MergeResult{State: landing.MergeConflict, Reason: fmt.Sprintf("the pull request targets %s, not %s", pr.Base.Ref, req.Base)}, nil
	}
	if res, final := classify(pr, req.Head); final {
		return res, nil
	}
	if err := ctx.Err(); err != nil {
		return landing.MergeResult{}, err
	}

	payload := map[string]any{"merge_method": req.Method, "merge_action": directMerge, "sha": req.Head}
	if req.Title != "" {
		payload["commit_title"] = req.Title
	}
	if req.Message != "" {
		payload["commit_message"] = req.Message
	}
	r := b.call(ctx, "PUT", b.api("pulls/"+strconv.Itoa(req.Number)+"/merge-async"), payload)

	var a asyncReply
	parsed := json.Unmarshal(r.body, &a) == nil
	ref := landing.MergeRef{Number: req.Number, Head: req.Head}
	switch {
	case r.status == 409:
		if !parsed || !sameRequest(a, req) {
			return landing.MergeResult{State: landing.MergeConflict, UUID: a.uuid(), Reason: "another merge request is pending with different options"}, nil
		}
		ref.UUID = a.uuid()
	case refused(r.status):
		return landing.MergeResult{State: landing.MergeFailed, Reason: r.reason}, nil
	case parsed && a.Status == "enqueued":
		return landing.MergeResult{State: landing.MergeConflict, Reason: "the pull request is already in a merge queue"}, nil
	case parsed && a.Status == "pending":
		ref.UUID = a.uuid()
	}
	return b.settle(ctx, ref), nil
}

func validateRef(number int, head string) error {
	if number <= 0 {
		return fmt.Errorf("github: invalid pull request number %d", number)
	}
	if !fullSHA.MatchString(head) {
		return errors.New("github: the head must be a full commit id")
	}
	return nil
}

// refused means GitHub answered and did not take the request. A timeout or a
// rate limit is not a refusal of this request's effect, so those are read back.
func refused(status int) bool {
	return status >= 400 && status < 500 && status != 408 && status != 429
}

// sameRequest says whether a pending request is the one this merge would have
// made, so it may be followed as this merge.
func sameRequest(a asyncReply, req landing.MergeRequest) bool {
	return a.uuid() != "" && a.Details.ExpectedHeadSHA == req.Head && a.Details.MergeMethod == req.Method &&
		a.Details.MergeAction == directMerge && !a.Details.BypassRules
}

// settle reads the merge until it has a final state or PollTimeout passes.
func (b *Backend) settle(ctx context.Context, ref landing.MergeRef) landing.MergeResult {
	timeout := time.NewTimer(b.opt.PollTimeout)
	defer timeout.Stop()
	for {
		res := b.reconcile(ctx, ref)
		if res.State != landing.MergePending && res.State != landing.MergeUnknown {
			return res
		}
		tick := time.NewTimer(b.opt.PollInterval)
		select {
		case <-tick.C:
		case <-timeout.C:
			tick.Stop()
			return res
		case <-ctx.Done():
			tick.Stop()
			return res
		}
	}
}
