package main

import (
	"io"

	"github.com/arafathusayn/herdsman/internal/tui/forms"
)

func openCleanForm(args *[]string, stdin io.Reader, stdout, stderr io.Writer) (int, bool) {
	return presentTargetForm(args, stdin, stdout, stderr, func(targets []string) forms.Spec {
		return forms.Clean(forms.TargetDefaults{Common: consentCommon(), Targets: targets})
	})
}
