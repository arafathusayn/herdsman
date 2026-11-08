package doctor

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/secrets"
)

func level(t *testing.T, cs []Check, part string) string {
	t.Helper()
	for _, c := range find(cs, "enforcement") {
		if strings.Contains(c.Detail, part) {
			return c.Level
		}
	}
	t.Fatalf("no enforcement line with %q in %+v", part, find(cs, "enforcement"))
	return ""
}

func withPolicy(p Probes, profile string) Probes {
	p.Profile = func() (string, error) { return profile, nil }
	return p
}

func TestEnforcementUnknownWithoutAPolicySource(t *testing.T) {
	cs := run(t, fakes(t, nil))
	if got := level(t, cs, "profile unknown"); got != Warn {
		t.Errorf("level = %s", got)
	}
	if Failed(cs) {
		t.Error("an unknown profile failed the report")
	}
}

func TestEnforcementPolicyUnreadableFails(t *testing.T) {
	p := fakes(t, nil)
	p.Profile = func() (string, error) { return "", errors.New("broken file") }
	cs := run(t, p)
	if got := level(t, cs, "broken file"); got != Fail || !Failed(cs) {
		t.Errorf("level = %s", got)
	}
}

func TestEnforcementTrustedLocalPromisesNoSandbox(t *testing.T) {
	cs := run(t, withPolicy(fakes(t, nil), ""))
	if got := level(t, cs, "trusted-local"); got != OK {
		t.Errorf("level = %s", got)
	}
	if got := level(t, cs, "no sandbox"); got != OK {
		t.Errorf("level = %s", got)
	}
	if len(find(cs, "enforcement")) != 2 {
		t.Errorf("lines = %+v", find(cs, "enforcement"))
	}
}

func TestEnforcementRestrictedListsEachGuarantee(t *testing.T) {
	p := withPolicy(fakes(t, nil), profileRestricted)
	p.GOOS = "linux"
	cs := run(t, p)
	for part, want := range map[string]string{"folder sandbox": OK, "own clone": OK, "--pair manual": OK, "Landlock": Warn} {
		if got := level(t, cs, part); got != want {
			t.Errorf("%s = %s, want %s", part, got, want)
		}
	}
	if Failed(cs) {
		t.Error("failed")
	}
}

func TestEnforcementRestrictedSaysSoOnMacOS(t *testing.T) {
	p := withPolicy(fakes(t, nil), profileRestricted)
	p.GOOS = "darwin"
	if got := level(t, run(t, p), "macOS"); got != Warn {
		t.Errorf("level = %s", got)
	}
}

func TestEnforcementRestrictedFailsWithoutItsTools(t *testing.T) {
	for _, tool := range []string{"codex", "git"} {
		p := withPolicy(fakes(t, nil), profileRestricted)
		found := p.LookPath
		p.LookPath = func(name string) (string, error) {
			if name == tool {
				return "", exec.ErrNotFound
			}
			return found(name)
		}
		cs := run(t, p)
		if got := level(t, cs, tool); got != Fail || !Failed(cs) {
			t.Errorf("%s: level = %s", tool, got)
		}
	}
}

func TestEnforcementSecretReferences(t *testing.T) {
	p := fakes(t, map[string]string{secrets.RefsVariable: "A=s/a,B=s/b"})
	cs := run(t, p)
	if got := level(t, cs, "2 secrets"); got != OK {
		t.Errorf("level = %s", got)
	}

	cs = run(t, fakes(t, map[string]string{secrets.RefsVariable: "broken"}))
	if got := level(t, cs, "not NAME"); got != Fail {
		t.Errorf("level = %s", got)
	}

	p = fakes(t, map[string]string{secrets.RefsVariable: "A=s/a"})
	found := p.LookPath
	p.LookPath = func(name string) (string, error) {
		if name == secrets.ToolName {
			return "", exec.ErrNotFound
		}
		return found(name)
	}
	if got := level(t, run(t, p), secrets.ToolName); got != Fail {
		t.Errorf("level = %s", got)
	}
}
