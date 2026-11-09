package main

import (
	"io"

	"github.com/arafathusayn/herdsman/internal/tui/forms"
)

// formTargets lists what a worktree form can act on; nil when there is nothing
// to choose or the list cannot be read, which the verb then reports itself.
var formTargets = func() []string { return listedTargets() }

// formList is replaced by tests.
var formList = readList

func listedTargets() []string {
	dir, err := workingFolder()
	if err != nil {
		return nil
	}
	result, _, err := formList(listRequest{Dir: dir})
	if err != nil {
		return nil
	}
	var targets []string
	for _, row := range result.Rows {
		target := row.Task
		if target == "" {
			target = row.Branch
		}
		if target != "" {
			targets = append(targets, target)
		}
	}
	return targets
}

// presentTargetForm opens a form that needs at least one worktree to act on.
func presentTargetForm(args *[]string, stdin io.Reader, stdout, stderr io.Writer, build func([]string) forms.Spec) (int, bool) {
	return presentForm(args, stdin, stdout, stderr, func() (forms.Spec, bool) {
		targets := formTargets()
		if len(targets) == 0 {
			return forms.Spec{}, false
		}
		return build(targets), true
	})
}

func openGoForm(args *[]string, stdin io.Reader, stdout, stderr io.Writer) (int, bool) {
	return presentTargetForm(args, stdin, stdout, stderr, func(targets []string) forms.Spec {
		return forms.Go(forms.GoDefaults{Common: formCommon(), Targets: targets})
	})
}
