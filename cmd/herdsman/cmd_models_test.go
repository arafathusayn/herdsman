package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/catalog"
	"github.com/arafathusayn/herdsman/internal/choices"
)

// fakeHarnesses puts fake claude and codex programs first on PATH: each
// prints the real output recorded under internal/web/catalogdata.
func fakeHarnesses(t *testing.T, kinds ...string) {
	t.Helper()
	bin := t.TempDir()
	data, err := filepath.Abs(filepath.Join("..", "..", "internal", "web", "catalogdata"))
	if err != nil {
		t.Fatal(err)
	}
	scripts := map[string]string{
		"claude": "#!/bin/sh\ncase \"$1\" in --version) echo '9.9.9 (fake)';; --help) cat '" + data + "/claude-help.txt';; *) exit 3;; esac\n",
		"codex":  "#!/bin/sh\ncase \"$1 $2\" in '--version ') echo 'codex-cli 9.9.9';; 'debug models') cat '" + data + "/codex-debug-models.json';; *) exit 3;; esac\n",
	}
	for _, k := range kinds {
		if err := os.WriteFile(filepath.Join(bin, k), []byte(scripts[k]), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/bin:/usr/bin")
}

func TestModelsCommand(t *testing.T) {
	fakeHarnesses(t, "claude", "codex")
	out, errOut, code := call(t, "models")
	// No herdr on PATH: it says so, and claude and codex are still asked.
	if code != 0 || errOut != "" || !strings.HasPrefix(out, "herdr: herdr agent start --help: ") || !strings.Contains(out, "\nclaude 9.9.9 (fake) (models: https://") || !strings.Contains(out, "\ncodex codex-cli 9.9.9 (models: https://") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	if !strings.Contains(out, "; default: none\n") {
		t.Error("claude's models show no default as none")
	}
	out, _, code = call(t, "models", "--harness", "codex", "--json")
	var rep struct{ Harnesses []catalog.Kind }
	if code != 0 || json.Unmarshal([]byte(out), &rep) != nil || len(rep.Harnesses) != 1 || rep.Harnesses[0].Kind != "codex" || len(rep.Harnesses[0].Models) == 0 || strings.Count(out, "\n") != 1 {
		t.Fatalf("%d %q", code, out)
	}
	if m := rep.Harnesses[0].Models[0]; m.DefaultEffort == "" || m.Label == "" {
		t.Errorf("%+v", m)
	}
}

// A missing harness gets one line; the others still print.
func TestModelsMissingHarness(t *testing.T) {
	fakeHarnesses(t, "codex")
	out, _, code := call(t, "models")
	if code != 0 || !strings.Contains(out, "\nclaude unknown: unavailable: not found on PATH\ncodex codex-cli 9.9.9") {
		t.Fatalf("%d %q", code, out)
	}
	out, _, code = call(t, "models", "--harness", "claude")
	if code != 1 || out != "claude unknown: unavailable: not found on PATH\n" {
		t.Fatalf("%d %q", code, out)
	}
}

func TestModelsUsage(t *testing.T) {
	out, _, code := call(t, "models", "--help")
	if code != 0 || !strings.Contains(out, "Exit codes:") {
		t.Errorf("%d %q", code, out)
	}
	for _, args := range [][]string{{"models", "x"}, {"models", "--harness", "No such!"}, {"models", "--bad"}} {
		_, errOut, code := call(t, args...)
		if code != 2 || !strings.HasPrefix(errOut, "usage: herdsman models") {
			t.Errorf("%v: %d %q", args, code, errOut)
		}
	}
}

// tempChoices points the choices store at a temp config folder.
func tempChoices(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "herdsman", "choices.json")
	choicesStore = func() (*choices.Store, error) { return &choices.Store{Path: p}, nil }
	t.Cleanup(func() { choicesStore = choices.Default })
	return p
}

func TestModelsLastAndSave(t *testing.T) {
	p := tempChoices(t)
	out, _, code := call(t, "models", "last")
	if code != 1 || out != "no previous choice\n" {
		t.Fatalf("%d %q", code, out)
	}
	out, _, code = call(t, "models", "last", "--json")
	if code != 1 || out != `{"picks":[]}`+"\n" {
		t.Fatalf("%d %q", code, out)
	}
	out, errOut, code := call(t, "models", "save", "--role", "reviewer", "--harness", "codex", "--model", "model-a", "--effort", "effortx", "--route", "r-1")
	if code != 0 || out != "saved reviewer runner=herdr harness=codex model=model-a effort=effortx\n" {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	if _, _, code = call(t, "models", "save", "--role", "implementer", "--runner", "subagent", "--harness", "claude", "--model", "model-b"); code != 0 {
		t.Fatal(code)
	}
	out, _, code = call(t, "models", "last")
	if code != 0 || !regexp.MustCompile(`^implementer runner=subagent harness=claude model=model-b effort=none route=none at=\S+Z\nreviewer runner=herdr harness=codex model=model-a effort=effortx route=r-1 at=\S+Z\n$`).MatchString(out) {
		t.Fatalf("%d %q", code, out)
	}
	out, _, code = call(t, "models", "last", "--json")
	var f choices.File
	if code != 0 || json.Unmarshal([]byte(out), &f) != nil || len(f.Picks) != 2 {
		t.Fatalf("%d %q", code, out)
	}
	_, errOut, code = call(t, "models", "save", "--role", "reviewer", "--runner", "cloud", "--harness", "codex", "--model", "m")
	if code != 1 || !strings.Contains(errOut, `runner "cloud"`) {
		t.Errorf("%d %q", code, errOut)
	}
	_, errOut, code = call(t, "models", "save", "--role", "boss", "--harness", "codex", "--model", "m")
	if code != 1 || !strings.HasPrefix(errOut, "herdsman models save: role \"boss\"") {
		t.Errorf("%d %q", code, errOut)
	}
	if err := os.WriteFile(p, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errOut, code = call(t, "models", "last")
	if code != 1 || !strings.HasPrefix(errOut, "herdsman models last: ") {
		t.Errorf("%d %q", code, errOut)
	}
	for _, args := range [][]string{{"models", "last", "x"}, {"models", "save", "--role", "reviewer"}, {"models", "save", "--bad"}} {
		_, errOut, code := call(t, args...)
		if code != 2 || !strings.HasPrefix(errOut, "usage: herdsman models") {
			t.Errorf("%v: %d %q", args, code, errOut)
		}
	}
}

func TestWebChoices(t *testing.T) {
	p := tempChoices(t)
	if st := webChoices(); st == nil || st.Path != p {
		t.Errorf("%+v", st)
	}
	choicesStore = func() (*choices.Store, error) { return nil, os.ErrNotExist }
	if webChoices() != nil {
		t.Error("a store without a config folder")
	}
}

// A29: herdr's other kinds, when installed, list the models their own
// `models` subcommand prints, with the efforts their --help lists; a named
// missing one is unavailable.
func TestModelsOtherKinds(t *testing.T) {
	fakeHarnesses(t, "codex")
	bin := filepath.SplitList(os.Getenv("PATH"))[0]
	help, err := os.ReadFile(filepath.Join("..", "..", "internal", "catalog", "catalogdata", "agy-help.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "agy-help.txt"), help, 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$1\" in --version) echo 'agy 1.0';; models) echo 'prov/m1';; --help) while IFS= read -r l; do printf '%s\\n' \"$l\"; done < '" + filepath.Join(bin, "agy-help.txt") + "';; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "agy"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	herdr := "#!/bin/sh\necho '  [possible values: claude, codex, agy, gemini]'\n"
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(herdr), 0o755); err != nil {
		t.Fatal(err)
	}
	out, _, code := call(t, "models")
	if code != 0 || !strings.Contains(out, "\nagy agy 1.0 (models: its own list)\n  prov/m1  efforts: low, medium, high, xhigh, max; default: none\n") || strings.Contains(out, "gemini") || strings.Contains(out, "herdr:") {
		t.Fatalf("%d %q", code, out)
	}
	out, _, code = call(t, "models", "--harness", "gemini")
	if code != 1 || out != "gemini unknown: unavailable: not found on PATH\n" {
		t.Errorf("%d %q", code, out)
	}
}

// models save --json prints the saved pick; last and save show their help.
func TestModelsSaveJSONAndHelp(t *testing.T) {
	tempChoices(t)
	out, _, code := call(t, "models", "save", "--role", "reviewer", "--harness", "codex", "--model", "model-a", "--json")
	var p choices.Pick
	if code != 0 || json.Unmarshal([]byte(out), &p) != nil || p.Role != "reviewer" || p.Runner != "herdr" || p.Model != "model-a" {
		t.Fatalf("%d %q", code, out)
	}
	for _, sub := range []string{"last", "save"} {
		out, _, code := call(t, "models", sub, "--help")
		if code != 0 || !strings.Contains(out, "Exit codes:") {
			t.Errorf("%s --help: %d %q", sub, code, out)
		}
	}
}
