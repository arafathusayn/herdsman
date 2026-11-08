package forms

import "github.com/arafathusayn/herdsman/internal/tui/components"

func cleanArgs(v []any) []string {
	args := []string{"clean", text(v[0])}
	if on(v[1]) {
		args = append(args, "--keep-branch")
	}
	if on(v[2]) {
		args = append(args, "--force")
	}
	return args
}

// Clean is the form that frees what a task holds. A forced run still asks once
// more on the terminal, because the command always does.
func Clean(d TargetDefaults) Spec {
	form := components.NewForm("Free a task", d.Style,
		targetField("Task", d.Targets, "--merged", "--all"),
		components.Entry{Field: components.NewConfirm("Keep the branch", false)},
		components.Entry{Field: components.NewConfirm("Go on with unsaved work (asks once more)", false)},
	).WithPlan(func(v []any) string { return d.plan(cleanArgs(v)) })
	return Spec{Form: form, Args: func(v []any) []string { return append(cleanArgs(v), d.consent()...) }}
}
