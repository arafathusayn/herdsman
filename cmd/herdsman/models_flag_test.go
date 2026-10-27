package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/choices"
)

// A30: a saved effort of a harness other than Claude and Codex keeps the
// option that passes it, read from the harness's --help or given with
// --effort-flag; with neither, the pick is refused instead of saved
// unusable.
func TestModelsSaveKeepsTheEffortOption(t *testing.T) {
	tempChoices(t)
	bin := t.TempDir()
	for name, help := range map[string]string{
		"withopt": "Options:\n  --reasoning-effort <E>  effort\n",
		"noopt":   "Options:\n  --model <M>  model\n",
	} {
		script := "#!/bin/sh\nprintf '" + help + "'\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/bin:/usr/bin")
	save := func(args ...string) (choices.Pick, int) {
		out, _, code := call(t, append([]string{"models", "save", "--role", "reviewer", "--model", "m1", "--effort", "high", "--json"}, args...)...)
		var p choices.Pick
		_ = json.Unmarshal([]byte(out), &p)
		return p, code
	}
	if p, code := save("--harness", "withopt"); code != 0 || p.EffortFlag != "--reasoning-effort" {
		t.Fatalf("from --help: %d %+v", code, p)
	}
	if p, code := save("--harness", "noopt"); code != 1 {
		t.Fatalf("no option known: %d %+v", code, p)
	}
	if p, code := save("--harness", "not-installed"); code != 1 {
		t.Fatalf("a harness that cannot be asked: %d %+v", code, p)
	}
	if p, code := save("--harness", "noopt", "--effort-flag", "--variant"); code != 0 || p.EffortFlag != "--variant" {
		t.Fatalf("given: %d %+v", code, p)
	}
}
