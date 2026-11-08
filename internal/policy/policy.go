// Package policy decides what herdsman may do on a task or route: how much it
// may spend, which branches and commands are off limits, and what a landing
// must show. It holds the decisions only. The daemon gives it usage and the
// ports that act, so every rule is testable without a process or a clock.
//
// A guarantee here covers operations that pass through herdsman. An agent that
// calls git or docker directly is outside it.
package policy

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Contract is what a landing promises about the tree that lands.
type Contract string

// The landing contracts.
const (
	LandingExact     Contract = "exact"
	LandingDelegated Contract = "delegated"
)

// Review says whether a landing can go without an authoritative review.
type Review string

// The review settings.
const (
	ReviewOptional Review = "optional"
	ReviewRequired Review = "required"
)

// Profile names how much of a task's isolation a sandbox enforces.
type Profile string

// The profiles.
const (
	ProfileTrustedLocal Profile = "trusted-local"
	ProfileRestricted   Profile = "restricted"
)

// DefaultGrace is the wait between asking a process to stop and forcing it.
const DefaultGrace = 10 * time.Second

// BudgetSection is a budget as a configuration file spells it.
type BudgetSection struct {
	Wall    string
	Machine string
	Tokens  int64
	Cost    string
}

// Section is the policy section as a configuration file spells it.
type Section struct {
	Budget         BudgetSection
	Protected      []string
	RequiredChecks []string
	Deny           []string
	Roots          []string
	Landing        string
	Review         string
	MaxAgents      int
	Profile        string
}

// Policy is one validated section, or the composition of several. A zero limit
// means the file said nothing about it.
type Policy struct {
	// Budget applies separately to each task and to each route.
	Budget         Budget
	Protected      []string
	RequiredChecks []string
	Deny           []string
	Roots          []string
	// NoRoots records that files granted roots and none survived composition,
	// which differs from no file having granted any.
	NoRoots   bool
	Landing   Contract
	Review    Review
	MaxAgents int
	Profile   Profile
}

// EffectiveLanding is the contract in force, exact unless a file relaxed it.
func (p Policy) EffectiveLanding() Contract {
	if p.Landing == "" {
		return LandingExact
	}
	return p.Landing
}

// EffectiveProfile is the profile in force.
func (p Policy) EffectiveProfile() Profile {
	if p.Profile == "" {
		return ProfileTrustedLocal
	}
	return p.Profile
}

// Parse validates a section. A value it cannot enforce is an error rather than
// a setting dropped, because a safety setting must never silently do nothing.
func Parse(s Section) (Policy, error) {
	var err error
	p := Policy{
		Protected:      s.Protected,
		RequiredChecks: s.RequiredChecks,
		Deny:           s.Deny,
		Roots:          s.Roots,
		MaxAgents:      s.MaxAgents,
	}
	if p.Budget.Wall, err = parseLimit("wall", s.Budget.Wall); err != nil {
		return Policy{}, err
	}
	if p.Budget.Machine, err = parseLimit("machine", s.Budget.Machine); err != nil {
		return Policy{}, err
	}
	if p.Budget.CostMicros, err = parseCost(s.Budget.Cost); err != nil {
		return Policy{}, err
	}
	if s.Budget.Tokens < 0 {
		return Policy{}, fmt.Errorf("policy: tokens budget %d is negative", s.Budget.Tokens)
	}
	p.Budget.Tokens = s.Budget.Tokens
	if s.MaxAgents < 0 {
		return Policy{}, fmt.Errorf("policy: max_agents %d is negative", s.MaxAgents)
	}
	if p.Landing, err = oneOf("landing", s.Landing, LandingExact, LandingDelegated); err != nil {
		return Policy{}, err
	}
	if p.Review, err = oneOf("review", s.Review, ReviewOptional, ReviewRequired); err != nil {
		return Policy{}, err
	}
	if p.Profile, err = oneOf("profile", s.Profile, ProfileTrustedLocal, ProfileRestricted); err != nil {
		return Policy{}, err
	}
	for _, pattern := range s.Protected {
		if pattern == "" {
			return Policy{}, fmt.Errorf("policy: empty protected pattern")
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return Policy{}, fmt.Errorf("policy: protected pattern %q: %w", pattern, err)
		}
	}
	if err := noBlank("deny", s.Deny); err != nil {
		return Policy{}, err
	}
	if err := noBlank("required_checks", s.RequiredChecks); err != nil {
		return Policy{}, err
	}
	return p, nil
}

func noBlank(key string, values []string) error {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("policy: empty entry in %s", key)
		}
	}
	return nil
}

func oneOf[T ~string](key, value string, allowed ...T) (T, error) {
	if value == "" {
		return "", nil
	}
	for _, candidate := range allowed {
		if value == string(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("policy: %s %q is not one of %v", key, value, allowed)
}

func parseLimit(key, value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	limit, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("policy: %s budget: %w", key, err)
	}
	if limit <= 0 {
		return 0, fmt.Errorf("policy: %s budget %q must be positive", key, value)
	}
	return limit, nil
}

var costPattern = regexp.MustCompile(`^([0-9]+)(?:\.([0-9]{1,6}))?$`)

const microsPerUnit = 1_000_000

// parseCost reads an amount into millionths, so sums never collect the
// rounding errors of binary fractions.
func parseCost(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	match := costPattern.FindStringSubmatch(strings.TrimPrefix(value, "$"))
	if match == nil {
		return 0, fmt.Errorf("policy: cost budget %q is not an amount with at most six decimals", value)
	}
	units, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil || units > (1<<63-1)/microsPerUnit-1 {
		return 0, fmt.Errorf("policy: cost budget %q is too large", value)
	}
	// The pattern bounds the fraction to six digits, so this cannot fail.
	fraction, _ := strconv.ParseInt((match[2] + "000000")[:6], 10, 64)
	micros := units*microsPerUnit + fraction
	if micros == 0 {
		return 0, fmt.Errorf("policy: cost budget %q must be positive", value)
	}
	return micros, nil
}
