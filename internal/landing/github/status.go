package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/arafathusayn/herdsman/internal/landing"
)

const perPage = 100

// maxPages bounds a paged read; a variable so a test can reach it.
var maxPages = 50

// pages reads a list to its end. Any page that fails fails the whole read: a
// rule or a check on a page not read must never look like one that is absent.
func pages[T any](ctx context.Context, b *Backend, path string, decode func([]byte) ([]T, error)) ([]T, error) {
	var all []T
	for page := 1; page <= maxPages; page++ {
		body, err := b.get(ctx, fmt.Sprintf("%s?per_page=%d&page=%d", path, perPage, page))
		if err != nil {
			return nil, err
		}
		items, err := decode(body)
		if err != nil {
			return nil, fmt.Errorf("github: %s page %d: %w", path, page, err)
		}
		all = append(all, items...)
		if len(items) < perPage {
			return all, nil
		}
	}
	return nil, fmt.Errorf("github: %s has more than %d pages", path, maxPages)
}

var errNoList = errors.New("the answer has no list")

type checkRun struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

type commitStatus struct {
	Context string `json:"context"`
	State   string `json:"state"`
}

// Status reads the pull request and every check on its head.
func (b *Backend) Status(ctx context.Context, number int) (landing.Status, error) {
	if number <= 0 {
		return landing.Status{}, fmt.Errorf("github: invalid pull request number %d", number)
	}
	pr, err := b.readPR(ctx, number)
	if err != nil {
		return landing.Status{}, err
	}
	runs, err := pages(ctx, b, b.api("commits/"+pr.Head.SHA+"/check-runs"), func(body []byte) ([]checkRun, error) {
		var page struct {
			CheckRuns []checkRun `json:"check_runs"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, err
		}
		if page.CheckRuns == nil {
			return nil, errNoList
		}
		return page.CheckRuns, nil
	})
	if err != nil {
		return landing.Status{}, err
	}
	statuses, err := pages(ctx, b, b.api("commits/"+pr.Head.SHA+"/status"), func(body []byte) ([]commitStatus, error) {
		var page struct {
			Statuses []commitStatus `json:"statuses"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, err
		}
		if page.Statuses == nil {
			return nil, errNoList
		}
		return page.Statuses, nil
	})
	if err != nil {
		return landing.Status{}, err
	}

	st := landing.Status{
		Number: pr.Number, State: pr.State, Draft: pr.Draft, HeadRef: pr.Head.Ref, HeadSHA: pr.Head.SHA,
		BaseRef: pr.Base.Ref, MergeableState: pr.MergeableState,
	}
	if pr.Merged {
		st.State, st.MergeCommit = "merged", pr.MergeCommitSHA
	}
	for _, run := range runs {
		st.Checks = append(st.Checks, landing.Check{Name: run.Name, State: runState(run)})
	}
	for _, s := range statuses {
		st.Checks = append(st.Checks, landing.Check{Name: s.Context, State: statusState(s.State)})
	}
	return st, nil
}

func runState(run checkRun) landing.CheckState {
	if !strings.EqualFold(run.Status, "completed") {
		return landing.CheckPending
	}
	switch run.Conclusion {
	case "success", "neutral", "skipped":
		return landing.CheckPassed
	}
	return landing.CheckFailed
}

func statusState(state string) landing.CheckState {
	switch state {
	case "success":
		return landing.CheckPassed
	case "pending":
		return landing.CheckPending
	}
	return landing.CheckFailed
}
