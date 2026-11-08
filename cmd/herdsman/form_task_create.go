package main

import (
	"io"

	"github.com/arafathusayn/herdsman/internal/tui/forms"
)

// openTaskCreateForm leaves the project blank, which the verb reads as the
// project bound to this folder, and shows the command line as the last screen,
// since recording a task destroys nothing and has no plan mode to ask.
func openTaskCreateForm(args *[]string, stdin io.Reader, stdout, stderr io.Writer) (int, bool) {
	return presentForm(args, stdin, stdout, stderr, func() (forms.Spec, bool) {
		common := formCommon()
		common.Plan = nil
		return forms.TaskCreate(forms.TaskCreateDefaults{Common: common}), true
	})
}
