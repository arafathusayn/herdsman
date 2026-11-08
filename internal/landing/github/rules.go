package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/arafathusayn/herdsman/internal/landing"
)

type requiredCheck struct {
	Context string `json:"context"`
}

// anyCheck says whether a list names a check. GitHub ignores the up-to-date
// setting when no check is required, so strict alone proves nothing.
func anyCheck(checks []requiredCheck) bool {
	for _, c := range checks {
		if c.Context != "" {
			return true
		}
	}
	return false
}

type rule struct {
	Type       string `json:"type"`
	Parameters struct {
		Strict bool            `json:"strict_required_status_checks_policy"`
		Checks []requiredCheck `json:"required_status_checks"`
	} `json:"parameters"`
}

// Rules reads the base branch's rulesets and branch protection. Only the
// up-to-date rule, with no merge queue, gives the exact contract: the merge
// endpoints pin the head and never the base, so that rule is what makes GitHub
// refuse a merge whose base moved. Its absence is a reading, not an error, and
// is reported as delegated; a read that fails is an error, never a contract.
func (b *Backend) Rules(ctx context.Context, base string) (landing.Rules, error) {
	if base == "" {
		return landing.Rules{}, errors.New("github: rules need a base branch")
	}
	branch := url.PathEscape(base)
	rules, err := pages(ctx, b, b.api("rules/branches/"+branch), func(body []byte) ([]rule, error) {
		var page []rule
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, err
		}
		if page == nil {
			return nil, errNoList
		}
		return page, nil
	})
	if err != nil {
		return landing.Rules{}, err
	}
	var res landing.Rules
	for _, r := range rules {
		res.MergeQueue = res.MergeQueue || r.Type == "merge_queue"
		res.UpToDate = res.UpToDate || (r.Type == "required_status_checks" && r.Parameters.Strict && anyCheck(r.Parameters.Checks))
	}
	if res.MergeQueue {
		res.Contract, res.Reason = landing.ContractDelegated, "the base branch uses a merge queue"
		return res, nil
	}
	if res.UpToDate {
		res.Contract = landing.ContractExact
		return res, nil
	}

	r := b.call(ctx, "GET", b.api("branches/"+branch+"/protection/required_status_checks"), nil)
	switch r.status {
	case 200:
		var protection struct {
			Strict   *bool           `json:"strict"`
			Contexts []string        `json:"contexts"`
			Checks   []requiredCheck `json:"checks"`
		}
		if err := json.Unmarshal(r.body, &protection); err != nil || protection.Strict == nil {
			return landing.Rules{}, fmt.Errorf("github: branch protection of %s is unreadable", base)
		}
		for _, name := range protection.Contexts {
			protection.Checks = append(protection.Checks, requiredCheck{Context: name})
		}
		if *protection.Strict && anyCheck(protection.Checks) {
			res.Contract, res.UpToDate = landing.ContractExact, true
			return res, nil
		}
	case 403, 404:
		// GitHub answers these both for no protection and for a token without
		// admin rights. Neither proves the rule, so exact cannot be claimed.
		res.Contract, res.Reason = landing.ContractDelegated, "no up-to-date rule could be read: "+r.reason
		return res, nil
	default:
		return landing.Rules{}, fmt.Errorf("github: branch protection of %s: %s", base, r.reason)
	}
	res.Contract, res.Reason = landing.ContractDelegated, "the base branch does not require being up to date on a required check"
	return res, nil
}
