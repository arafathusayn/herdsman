package hooks

import (
	"fmt"
	"slices"

	"github.com/arafathusayn/herdsman/internal/approvals"
)

// Approvals records, per project, the exact hook templates a person accepted,
// in the store that config approvals lists and clears. Approving the template
// rather than its expansion keeps an approval valid for every task.
type Approvals struct {
	Path string
}

func (a *Approvals) store() *approvals.Store { return approvals.Open(a.Path) }

// Approved says whether the exact template was accepted for the project.
func (a *Approvals) Approved(project, template string) (bool, error) {
	ok, err := a.store().Approved(project, template)
	if err != nil {
		return false, fmt.Errorf("hooks: approvals: %w", err)
	}
	return ok, nil
}

// Unapproved lists the plan's templates that were not accepted, without repeats.
func (a *Approvals) Unapproved(project string, plan Plan) ([]string, error) {
	held, err := a.store().List()
	if err != nil {
		return nil, fmt.Errorf("hooks: approvals: %w", err)
	}
	var missing []string
	for _, step := range plan {
		for _, command := range step {
			if !slices.Contains(held[project], command.Template) && !slices.Contains(missing, command.Template) {
				missing = append(missing, command.Template)
			}
		}
	}
	return missing, nil
}

// Approve accepts the templates for the project.
func (a *Approvals) Approve(project string, templates []string) error {
	store := a.store()
	for _, template := range templates {
		if err := store.Approve(project, template); err != nil {
			return fmt.Errorf("hooks: approvals: %w", err)
		}
	}
	return nil
}
