package main

import (
	"io"

	"github.com/arafathusayn/herdsman/internal/tui/forms"
)

func openDownForm(args *[]string, stdin io.Reader, stdout, stderr io.Writer) (int, bool) {
	return presentTargetForm(args, stdin, stdout, stderr, func(targets []string) forms.Spec {
		return forms.Down(forms.TargetDefaults{Common: consentCommon(), Targets: targets})
	})
}
