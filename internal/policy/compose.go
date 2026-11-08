package policy

import (
	"cmp"
	"slices"
)

// Compose folds configuration files, broadest first, into the policy in force.
// Every rule only ever tightens: limits take the minimum, deny-style lists take
// the union, and a later file can narrow an earlier file's grant but not extend
// it. A file that says nothing about a setting leaves it as it was.
func Compose(files ...Policy) Policy {
	var out Policy
	for _, file := range files {
		out = compose(out, file)
	}
	return out
}

func compose(a, b Policy) Policy {
	out := Policy{
		Budget: Budget{
			Wall:       minSet(a.Budget.Wall, b.Budget.Wall),
			Machine:    minSet(a.Budget.Machine, b.Budget.Machine),
			Tokens:     minSet(a.Budget.Tokens, b.Budget.Tokens),
			CostMicros: minSet(a.Budget.CostMicros, b.Budget.CostMicros),
		},
		Protected:      union(a.Protected, b.Protected),
		RequiredChecks: union(a.RequiredChecks, b.RequiredChecks),
		Deny:           union(a.Deny, b.Deny),
		MaxAgents:      minSet(a.MaxAgents, b.MaxAgents),
		Landing:        stricter(a.Landing, b.Landing, LandingExact),
		Review:         stricter(a.Review, b.Review, ReviewRequired),
		Profile:        stricter(a.Profile, b.Profile, ProfileRestricted),
	}
	out.Roots, out.NoRoots = composeRoots(a, b)
	return out
}

// minSet is the smaller of two limits, where zero means no limit was stated.
func minSet[T cmp.Ordered](a, b T) T {
	var unset T
	switch {
	case a == unset:
		return b
	case b == unset:
		return a
	default:
		return min(a, b)
	}
}

func stricter[T comparable](a, b, strict T) T {
	var unset T
	switch {
	case a == strict || b == strict:
		return strict
	case a == unset:
		return b
	default:
		return a
	}
}

func union(a, b []string) []string {
	if len(a)+len(b) == 0 {
		return nil
	}
	out := slices.Concat(a, b)
	slices.Sort(out)
	return slices.Compact(out)
}

// composeRoots narrows a grant. Once files have granted nothing in common, the
// result stays empty, so a further file cannot bring a removed root back.
func composeRoots(a, b Policy) ([]string, bool) {
	switch {
	case a.NoRoots || b.NoRoots:
		return nil, true
	case len(a.Roots) == 0:
		return b.Roots, false
	case len(b.Roots) == 0:
		return a.Roots, false
	}
	var common []string
	for _, root := range a.Roots {
		if slices.Contains(b.Roots, root) {
			common = append(common, root)
		}
	}
	return common, len(common) == 0
}
