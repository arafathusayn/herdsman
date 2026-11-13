package wtree

import (
	"fmt"

	"github.com/arafathusayn/herdsman/sdk/config"
)

// Plan is what a repository's root files imply for setting up a worktree.
type Plan = config.Ecosystem

// Detect reads the root of a repository once and returns its setup plan.
func Detect(root string) (Plan, error) {
	plan, err := config.DetectEcosystem(root)
	if err != nil {
		return Plan{}, fmt.Errorf("wtree: detect ecosystem: %w", err)
	}
	return plan, nil
}
