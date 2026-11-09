// Package profile turns a policy's profile into the shape of a task: what
// folder it lives in, what its harness may write and which launches are
// refused. A restriction exists here only when something enforces it, so a
// launch that nothing could confine is an error rather than a weaker launch.
package profile

import (
	"fmt"

	"github.com/arafathusayn/herdsman/internal/policy"
)

// Shape is how a task's files relate to the main repository.
type Shape string

// The shapes.
const (
	// ShapeLinkedWorktree shares the main repository's git folder.
	ShapeLinkedWorktree Shape = "linked-worktree"
	// ShapeOwnClone is a complete repository that shares nothing.
	ShapeOwnClone Shape = "own-clone"
)

// PairManual is the pair mode that asks a person on the terminal.
const PairManual = "manual"

// ShapeFor is the task shape a policy calls for.
func ShapeFor(p policy.Policy) Shape {
	if p.EffectiveProfile() == policy.ProfileRestricted {
		return ShapeOwnClone
	}
	return ShapeLinkedWorktree
}

// CheckPair refuses a pair mode the profile does not allow. An automatic
// approval rests on assuming that an attested pane is a person's shell, which
// the restricted profile does not assume.
func CheckPair(p policy.Policy, mode string) error {
	if p.EffectiveProfile() == policy.ProfileRestricted && mode != PairManual {
		return fmt.Errorf("profile restricted requires --pair %s, got %q", PairManual, mode)
	}
	return nil
}
