package main

import (
	"errors"
	"strings"
	"testing"
)

const sampleTable = `# Heading

Prose that is not a table.

| Capability | darwin | linux | How it is checked |
| --- | --- | --- | --- |
| ` + "`lsof`" + ` | provided | provided | runs it |
| ` + "`docker`" + ` | absent | provided | asks the engine |
`

func TestParseReadsEachPlatformsDeclaration(t *testing.T) {
	r, err := ParseRequirements(sampleTable)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		capability, goos string
		want             Declaration
	}{
		{"lsof", "darwin", Provided},
		{"docker", "darwin", Absent},
		{"docker", "linux", Provided},
		{"keychain", "linux", Undeclared},
		{"lsof", "plan9", Undeclared},
	} {
		if got := r.Declared(c.goos, c.capability); got != c.want {
			t.Errorf("%s on %s = %q, want %q", c.capability, c.goos, got, c.want)
		}
	}
}

func TestParseRefusesAnUnclearTable(t *testing.T) {
	for name, table := range map[string]string{
		"no table":       "just prose\n",
		"unknown word":   "| Capability | darwin | linux | How |\n| --- | --- | --- | --- |\n| `a` | maybe | provided | x |\n",
		"duplicate":      "| Capability | darwin | linux | How |\n| --- | --- | --- | --- |\n| `a` | absent | absent | x |\n| `a` | absent | absent | x |\n",
		"short row":      "| Capability | darwin | linux | How |\n| --- | --- | --- | --- |\n| `a` | absent |\n",
		"missing column": "| Capability | darwin | How |\n| --- | --- | --- |\n| `a` | absent | x |\n",
	} {
		if _, err := ParseRequirements(table); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDecide(t *testing.T) {
	reqs, err := ParseRequirements(sampleTable)
	if err != nil {
		t.Fatal(err)
	}
	broken := errors.New("not on this machine")
	ok := func() error { return nil }
	bad := func() error { return broken }
	for _, c := range []struct {
		name   string
		goos   string
		needs  []string
		probes map[string]Probe
		status Status
		reason []string
	}{
		{name: "provided and working runs", goos: "linux", needs: []string{"lsof", "docker"},
			probes: map[string]Probe{"lsof": ok, "docker": ok}, status: StatusRun},
		{name: "declared absent skips by name", goos: "darwin", needs: []string{"docker"},
			probes: map[string]Probe{"docker": bad}, status: StatusSkip, reason: []string{"docker", "darwin"}},
		{name: "declared but missing fails", goos: "linux", needs: []string{"docker"},
			probes: map[string]Probe{"docker": bad}, status: StatusFail, reason: []string{"docker", "not on this machine"}},
		{name: "undeclared capability fails", goos: "linux", needs: []string{"keychain"},
			probes: map[string]Probe{"keychain": ok}, status: StatusFail, reason: []string{"keychain"}},
		{name: "undeclared platform fails", goos: "plan9", needs: []string{"lsof"},
			probes: map[string]Probe{"lsof": ok}, status: StatusFail, reason: []string{"plan9"}},
		{name: "no probe fails", goos: "linux", needs: []string{"lsof"},
			probes: map[string]Probe{}, status: StatusFail, reason: []string{"lsof", "probe"}},
		{name: "a broken need outranks an absent one", goos: "darwin", needs: []string{"docker", "lsof"},
			probes: map[string]Probe{"docker": ok, "lsof": bad}, status: StatusFail, reason: []string{"lsof"}},
		{name: "no needs runs", goos: "linux", status: StatusRun},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := Decide(reqs, c.goos, c.probes, Gate{Name: "g", Needs: c.needs})
			if got.Status != c.status {
				t.Fatalf("status %q (%s), want %q", got.Status, got.Reason, c.status)
			}
			for _, want := range c.reason {
				if !strings.Contains(got.Reason, want) {
					t.Errorf("reason %q lacks %q", got.Reason, want)
				}
			}
		})
	}
}

func TestEveryProbeFailsWithoutItsTool(t *testing.T) {
	// A planted missing tool: nothing resolves on an empty PATH.
	t.Setenv("PATH", t.TempDir())
	for _, name := range []string{"ps", "lsof", "docker", "btrfs-loopback", "keychain"} {
		if err := probes()[name](); err == nil {
			t.Errorf("%s probe passed with no tools on PATH", name)
		}
	}
	// These two need no program.
	for _, name := range []string{"signals", "pty"} {
		if err := probes()[name](); err != nil {
			t.Errorf("%s probe: %v", name, err)
		}
	}
}

func TestTheRepositoryDeclarationCoversEveryGateOnBothPlatforms(t *testing.T) {
	reqs, err := ParseRequirements(requirementsFile)
	if err != nil {
		t.Fatal(err)
	}
	known := probes()
	for _, g := range Gates {
		for _, need := range g.Needs {
			if _, ok := known[need]; !ok {
				t.Errorf("gate %s needs %q, which has no probe", g.Name, need)
			}
			for _, goos := range []string{"darwin", "linux"} {
				if reqs.Declared(goos, need) == Undeclared {
					t.Errorf("gate %s needs %q, undeclared on %s", g.Name, need, goos)
				}
			}
		}
	}
	for _, capability := range []string{"docker", "lsof", "btrfs-loopback", "keychain", "pty"} {
		if _, ok := known[capability]; !ok {
			t.Errorf("no probe for %s", capability)
		}
		for _, goos := range []string{"darwin", "linux"} {
			if reqs.Declared(goos, capability) == Undeclared {
				t.Errorf("%s undeclared on %s", capability, goos)
			}
		}
	}
}
