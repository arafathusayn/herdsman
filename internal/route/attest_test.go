package route_test

import (
	"testing"

	"github.com/arafathusayn/herdsman/internal/route"
)

func TestCoreTerminalCommand(t *testing.T) {
	cases := []struct {
		program, want string
	}{
		// A shell waiting for a person to type.
		{"/bin/zsh -l", ""},
		{"-zsh", ""},
		{"zsh", ""},
		{"bash -il", ""},
		{"/opt/homebrew/bin/fish --login", ""},
		// A program run directly.
		{"herdsman up r -p x", "herdsman"},
		{"/usr/bin/env herdsman up r", "env"},
		// A shell that runs a command string or a script.
		{"/bin/sh -c herdsman up r", "sh"},
		{"/bin/bash -lc herdsman up r", "bash"},
		{"/bin/bash /tmp/start-route.sh", "bash"},
		{"fish -cherdsman", "fish"},
		{"fish --command=herdsman", "fish"},
		{"fish --init-command=herdsman", "fish"},
		{"fish -Cherdsman", "fish"},
		{"", "unknown"},
	}
	for _, tc := range cases {
		if got := route.CoreTerminalCommand(tc.program); got != tc.want {
			t.Errorf("CoreTerminalCommand(%q) = %q, want %q", tc.program, got, tc.want)
		}
	}
}
