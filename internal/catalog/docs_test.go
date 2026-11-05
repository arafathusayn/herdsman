package catalog

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// A29 step 2: a harness listed through its own command (Muse's
// model-profile, another kind's models) carries that command as its model
// docs, so the form can point the user at it.
func TestListedKindsCarryTheirModelDocs(t *testing.T) {
	bin := t.TempDir()
	fakeBin(t, bin, "muse", `case "$1" in --help) cat '`+fixturePath(t, "catalogdata/muse-help.txt")+`';; model-profile) printf 'm-one\n';; esac`)
	fakeBin(t, bin, "opencode", `case "$1" in --help) printf 'Commands:\n  opencode models   list models\n';; models) printf 'p/m-one\n';; esac`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/bin:/usr/bin")
	for kind, want := range map[string]string{"muse": "muse model-profile show <model>", "opencode": "opencode models"} {
		k := Pipeline(context.Background(), ExecRunner, kind)[0]
		var m map[string]any
		b, _ := json.Marshal(k)
		_ = json.Unmarshal(b, &m)
		if k.Unavailable != "" || m["docs_command"] != want {
			t.Errorf("%s: docs %v, kind %+v", kind, m["docs_command"], k)
		}
	}
}

// A saved effort keeps the option the harness's --help names for it.
func TestEffortFlagOf(t *testing.T) {
	bin := t.TempDir()
	fakeBin(t, bin, "cursor-agent", `printf 'Options:\n  --reasoning-effort <E>  effort\n'`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/bin:/usr/bin")
	if f, err := EffortFlagOf(context.Background(), ExecRunner, "cursor"); err != nil || f != "--reasoning-effort" {
		t.Fatalf("%q %v", f, err)
	}
	if _, err := EffortFlagOf(context.Background(), ExecRunner, "not-installed"); err == nil {
		t.Fatal("a missing harness gave an option")
	}
}
