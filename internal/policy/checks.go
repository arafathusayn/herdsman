package policy

import (
	"fmt"
	"slices"
	"strings"
)

// Landing describes what a merge holds at the moment it would land.
type Landing struct {
	Contract Contract
	// Checks maps each check that ran to whether it passed.
	Checks map[string]bool
	// Reviewed says an authoritative review covers exactly what lands.
	Reviewed bool
	// Waiver is the reason given for landing without such a review.
	Waiver string
}

// CheckLanding applies the landing rules: the contract, the review, and every
// required check. A check that never ran counts the same as one that failed.
func (p Policy) CheckLanding(l Landing) error {
	if l.Contract == LandingDelegated && p.EffectiveLanding() != LandingDelegated {
		return &Refusal{
			Reason:  ReasonLanding,
			Message: `delegated landing refused: set landing = "delegated" in [policy] to accept it`,
		}
	}
	if p.Review == ReviewRequired {
		if l.Waiver != "" {
			return &Refusal{Reason: ReasonReview, Message: `review waiver refused: [policy] review = "required" forbids waivers`}
		}
		if !l.Reviewed {
			return &Refusal{Reason: ReasonReview, Message: `landing refused: [policy] review = "required" and no review covers this candidate`}
		}
	}
	var missing []string
	for _, check := range p.RequiredChecks {
		if !l.Checks[check] {
			missing = append(missing, check)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return &Refusal{
			Reason:  ReasonChecks,
			Missing: missing,
			Message: "landing refused: required checks have not passed: " + strings.Join(missing, ", "),
		}
	}
	return nil
}

// describe is the event text for one limit.
func describe(scope Scope, over Exceeded) string {
	return fmt.Sprintf("%s budget of %s spent: limit %s, used %s, over by %s", over.Dimension, scope, over.Limit, over.Used, over.Overshoot)
}
