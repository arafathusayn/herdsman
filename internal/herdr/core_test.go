package herdr_test

import (
	"testing"

	"github.com/arafathusayn/herdsman/internal/herdr"
)

func TestIsCoreProcess(t *testing.T) {
	cases := []struct {
		command string
		want    bool
	}{
		// A holder and a server, under each name the program is installed as.
		{"/opt/bin/hc hold --socket /run/herdr-core/default.holder.3cd8168b.sock", true},
		{"/opt/bin/herdr-core hold --socket /state/herdr-core/default/default.holder.8f6529c6.sock", true},
		{"/opt/bin/hc --hc-launchd /tmp/hc-launchd-1/ready", true},
		{"/opt/bin/herdr-core serve", true},
		// The arguments are not read: a renamed subcommand is still herdr-core.
		{"hc keep-terminals", true},
		{"hc", true},
		// Another program, or a script that only carries the name.
		{"/usr/bin/hcl fmt", false},
		{"/bin/bash /opt/bin/hc", false},
		{"herdsman hold", false},
		{"/bin/zsh -l", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := herdr.IsCoreProcess(tc.command); got != tc.want {
			t.Errorf("IsCoreProcess(%q) = %v, want %v", tc.command, got, tc.want)
		}
	}
}
