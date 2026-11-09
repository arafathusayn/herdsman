package forms

import "github.com/arafathusayn/herdsman/internal/tui/components"

// TaskCreateDefaults prefills the form that records a task.
type TaskCreateDefaults struct {
	Common
	// Project is the project to file it under; blank means the bound one.
	Project string
}

func taskCreateArgs(v []any) []string {
	args := []string{"task", "create", text(v[0])}
	if project := text(v[1]); project != "" {
		args = append(args, "--project", project)
	}
	for _, repo := range commaList(v[2]) {
		args = append(args, "--repo", repo)
	}
	if body := text(v[3]); body != "" {
		args = append(args, "--body", body)
	}
	for _, label := range commaList(v[4]) {
		args = append(args, "--label", label)
	}
	return args
}

// TaskCreate is the form that records a new task.
func TaskCreate(d TaskCreateDefaults) Spec {
	form := components.NewForm("New task", d.Style,
		components.Entry{Field: components.NewTextInput("Title", ""), Validate: required("a title is required")},
		components.Entry{Field: components.NewTextInput("Project (blank: the bound one)", d.Project)},
		components.Entry{Field: components.NewTextInput("Repositories, comma separated (blank: all)", "")},
		components.Entry{Field: components.NewTextArea("Description", "")},
		components.Entry{Field: components.NewTextInput("Labels, comma separated", "")},
	).WithPlan(func(v []any) string { return d.plan(taskCreateArgs(v)) })
	return Spec{Form: form, Args: taskCreateArgs}
}
