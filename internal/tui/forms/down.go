package forms

import "github.com/arafathusayn/herdsman/internal/tui/components"

func downArgs(v []any) []string {
	args := appendGrace([]string{"down", text(v[0])}, v[1])
	if on(v[2]) {
		args = append(args, "--keep-branch")
	}
	if on(v[3]) {
		args = append(args, "--include", "associated")
	}
	if on(v[4]) {
		args = append(args, "--force")
	}
	return args
}

// Down is the form that stops a task and then frees what it holds.
func Down(d TargetDefaults) Spec {
	form := components.NewForm("Stop and free a task", d.Style,
		targetField("Task", d.Targets, "--merged", "--all"),
		components.Entry{Field: components.NewTextInput("Seconds before a forced stop", ""), Validate: seconds},
		components.Entry{Field: components.NewConfirm("Keep the branch", false)},
		components.Entry{Field: components.NewConfirm("Also stop what is only associated", false)},
		components.Entry{Field: components.NewConfirm("Go on with unsaved work (asks once more)", false)},
	).WithPlan(func(v []any) string { return d.plan(downArgs(v)) })
	return Spec{Form: form, Args: func(v []any) []string { return append(downArgs(v), d.consent()...) }}
}
