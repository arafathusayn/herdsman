package forms

import "github.com/arafathusayn/herdsman/internal/tui/components"

func teardownArgs(v []any) []string {
	args := appendGrace([]string{"teardown", text(v[0])}, v[1])
	if on(v[2]) {
		args = append(args, "--include", "associated")
	}
	return args
}

// Teardown is the form that stops what a task runs. Its last screen is the
// command's own plan, and confirming it approves that plan and no other.
func Teardown(d TargetDefaults) Spec {
	form := components.NewForm("Stop a task", d.Style,
		targetField("Task", d.Targets, "--all"),
		components.Entry{Field: components.NewTextInput("Seconds before a forced stop", ""), Validate: seconds},
		components.Entry{Field: components.NewConfirm("Also stop what is only associated", false)},
	).WithPlan(func(v []any) string { return d.plan(teardownArgs(v)) })
	return Spec{Form: form, Args: func(v []any) []string { return append(teardownArgs(v), d.consent()...) }}
}
