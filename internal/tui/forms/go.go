package forms

import "github.com/arafathusayn/herdsman/internal/tui/components"

// GoDefaults lists the worktrees that can be entered.
type GoDefaults struct {
	Common
	Targets []string
}

// Go is the picker for entering a worktree. It changes nothing, so it has no
// plan screen.
func Go(d GoDefaults) Spec {
	form := components.NewForm("Enter a worktree", d.Style, targetField("Worktree", d.Targets))
	return Spec{Form: form, Args: func(v []any) []string { return []string{"go", text(v[0])} }}
}
