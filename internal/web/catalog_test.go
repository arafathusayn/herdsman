package web

import (
	"context"
	"embed"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// The fixtures under catalogdata/ are real output of this machine's
// harnesses: `claude --help` and `herdr agent start --help` as printed, and
// `codex debug models` cut down to the fields the parser reads (the real
// output is about 559 KB, almost all of it each model's base_instructions;
// the models, their order and their efforts are unchanged).

// catalogFiles are the captured outputs; only tests read them (A28/A29).
//
//go:embed catalogdata
var catalogFiles embed.FS

func catalogFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := catalogFiles.ReadFile("catalogdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeRunner answers each command from a table and counts the calls.
func fakeRunner(out map[string][]byte, fail map[string]error, calls *atomic.Int32) runner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		if calls != nil {
			calls.Add(1)
		}
		key := strings.Join(append([]string{name}, args...), " ")
		if err := fail[key]; err != nil {
			return nil, err
		}
		return out[key], nil
	}
}

func realOutputs(t *testing.T) map[string][]byte {
	return map[string][]byte{
		"codex debug models":       catalogFixture(t, "codex-debug-models.json"),
		"claude --help":            catalogFixture(t, "claude-help.txt"),
		"herdr agent start --help": catalogFixture(t, "herdr-agent-start-help.txt"),
	}
}

func ids(ms []CatalogModel) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func TestCodexCatalogParse(t *testing.T) {
	k, err := parseCodexModels(catalogFixture(t, "codex-debug-models.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gpt-6.1-sol", "gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}
	if !slices.Equal(ids(k.Models), want) {
		t.Fatalf("models = %v", ids(k.Models))
	}
	if k.DefaultModel != "gpt-6.1-sol" || k.OtherModel || k.Offline || k.SkillPrefix != "$" || !k.Checkpointer {
		t.Fatalf("kind = %+v", k)
	}
	if m := k.Models[0]; !slices.Equal(m.Efforts, []string{"low", "medium", "high", "xhigh", "max", "ultra"}) || m.DefaultEffort != "medium" || m.Label != "GPT-6.1-Sol" {
		t.Fatalf("first model = %+v", m)
	}
	if m := k.Models[3]; !slices.Equal(m.Efforts, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("gpt-6-luna efforts = %v", m.Efforts)
	}
}

func TestCodexCatalogRefusesBadOutput(t *testing.T) {
	for _, in := range []string{"", "not json", `{"models":[]}`, `{"models":[{"slug":"x","visibility":"hide","supported_reasoning_levels":[{"effort":"low"}]}]}`, `{"models":[{"slug":"bad slug","visibility":"list"}]}`} {
		if _, err := parseCodexModels([]byte(in)); err == nil {
			t.Errorf("%q: want an error", in)
		}
	}
}

func TestClaudeHelpParse(t *testing.T) {
	k, err := parseClaudeHelp(catalogFixture(t, "claude-help.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids(k.Models), []string{"fable", "opus", "sonnet"}) || k.DefaultModel != "fable" || !k.OtherModel || k.SkillPrefix != "/" || !k.Checkpointer {
		t.Fatalf("kind = %+v", k)
	}
	for _, m := range k.Models {
		// Claude's help names no default effort, so there is none (A28).
		if !slices.Equal(m.Efforts, []string{"low", "medium", "high", "xhigh", "max"}) || m.DefaultEffort != "" {
			t.Fatalf("model = %+v", m)
		}
	}
}

func TestClaudeHelpRefusesBadOutput(t *testing.T) {
	for _, in := range []string{"", "Usage: claude\n  --model <model>  Model for the session\n", "  --model <model>  an alias ('fable')\n"} {
		if _, err := parseClaudeHelp([]byte(in)); err == nil {
			t.Errorf("%q: want an error", in)
		}
	}
}

func TestKindsParse(t *testing.T) {
	kinds, err := parseKinds(catalogFixture(t, "herdr-agent-start-help.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(kinds) < 10 || kinds[0] != "pi" || !slices.Contains(kinds, "claude") || !slices.Contains(kinds, "muse") {
		t.Fatalf("kinds = %v", kinds)
	}
	if _, err := parseKinds([]byte("no list here")); err == nil {
		t.Fatal("want an error")
	}
}

func TestCatalogFromCommands(t *testing.T) {
	var calls atomic.Int32
	src := newCatalogSource(fakeRunner(realOutputs(t), nil, &calls))
	c := src.get(context.Background())
	if c.Kinds[0].Kind != "claude" || c.Kinds[1].Kind != "codex" || c.Kinds[0].Offline || c.Kinds[1].Offline {
		t.Fatalf("kinds = %+v", c.Kinds[:2])
	}
	pi := c.kind("pi")
	if pi == nil || len(pi.Models) != 0 || !pi.FreeModel || pi.Checkpointer || pi.SkillPrefix != "/" {
		t.Fatalf("pi = %+v", pi)
	}
	if c.kind("claude") == nil || c.kind("nope") != nil {
		t.Fatal("kind lookup")
	}
	// Cached for the server's life: a second read runs nothing.
	n := calls.Load()
	src.get(context.Background())
	if calls.Load() != n || n != 3 {
		t.Fatalf("calls = %d then %d", n, calls.Load())
	}
}

func TestCatalogOfflineOnError(t *testing.T) {
	fail := map[string]error{"codex debug models": errors.New("exit 1"), "claude --help": errors.New("not found"), "herdr agent start --help": errors.New("no herdr")}
	c := newCatalogSource(fakeRunner(nil, fail, nil)).get(context.Background())
	checkOffline(t, c)
}

func TestCatalogOfflineOnBadOutput(t *testing.T) {
	out := map[string][]byte{"codex debug models": []byte("{"), "claude --help": []byte("Usage: claude"), "herdr agent start --help": []byte("?")}
	c := newCatalogSource(fakeRunner(out, nil, nil)).get(context.Background())
	checkOffline(t, c)
}

// A harness that could not be asked offers no models: no captured list
// stands in (A28/A29), and nothing it might name is accepted.
func checkOffline(t *testing.T, c Catalog) {
	t.Helper()
	cl, cx := c.kind("claude"), c.kind("codex")
	if cl == nil || cx == nil || !cl.Offline || !cx.Offline || len(cl.Models) != 0 || len(cx.Models) != 0 || cl.FreeModel || cx.FreeModel {
		t.Fatalf("claude=%+v codex=%+v", cl, cx)
	}
	if cx.SkillPrefix != "$" || cl.SkillPrefix != "/" {
		t.Fatalf("prefixes %q %q", cl.SkillPrefix, cx.SkillPrefix)
	}
	if c.kind("gemini") != nil {
		t.Fatal("a kind list without herdr's answer")
	}
}

func TestOfflineHarnessLaunchesNothing(t *testing.T) {
	c := newCatalogSource(fakeRunner(nil, map[string]error{"codex debug models": errors.New("x"), "claude --help": errors.New("x"), "herdr agent start --help": errors.New("x")}, nil)).get(context.Background())
	for _, m := range []string{"model-a", "gpt-6.1-sol"} {
		if _, err := launchArgs(c.kind("codex"), m, "low"); err == nil {
			t.Fatalf("%s launched from an offline harness", m)
		}
	}
}

func TestLaunchArgs(t *testing.T) {
	c := newCatalogSource(fakeRunner(realOutputs(t), nil, nil)).get(context.Background())
	cases := []struct {
		kind, model, effort string
		want                []string
		bad                 bool
	}{
		{"claude", "fable", "medium", []string{"--model", "fable", "--effort", "medium"}, false},
		{"claude", "claude-opus-5-5[1m]", "high", []string{"--model", "claude-opus-5-5[1m]", "--effort", "high"}, false},
		{"claude", "bad model", "high", nil, true},
		{"claude", "fable", "ultra", nil, true},
		{"claude", "", "low", nil, true},
		{"codex", "gpt-5.6-luna", "low", []string{"--model", "gpt-5.6-luna", "-c", "model_reasoning_effort=low"}, false},
		{"codex", "gpt-5.6-luna", "ultra", nil, true},
		{"codex", "gpt-reserve", "low", nil, true},
		{"pi", "some-model", "", []string{"--model", "some-model"}, false},
		{"pi", "", "", []string{}, false},
		{"pi", "x;rm", "", nil, true},
		{"pi", "m", "high", nil, true},
	}
	for _, tc := range cases {
		got, err := launchArgs(c.kind(tc.kind), tc.model, tc.effort)
		if tc.bad != (err != nil) || (!tc.bad && !slices.Equal(got, tc.want)) {
			t.Errorf("%s %q %q: got %v, %v", tc.kind, tc.model, tc.effort, got, err)
		}
	}
	if _, err := launchArgs(nil, "m", ""); err == nil {
		t.Error("an unknown kind was accepted")
	}
}

func TestSkillInstalled(t *testing.T) {
	home := t.TempDir()
	if skillInstalled(home, "claude") || skillInstalled(home, "codex") || skillInstalled(home, "pi") {
		t.Fatal("found a skill in an empty home")
	}
	put := func(dir string) {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, dir, "SKILL.md"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put(".claude/skills/herdsman")
	if !skillInstalled(home, "claude") || skillInstalled(home, "codex") {
		t.Fatal("claude skill lookup")
	}
	put(".agents/skills/herdsman")
	if !skillInstalled(home, "codex") || !skillInstalled(home, "pi") {
		t.Fatal("agents skill lookup")
	}
}

func TestCheckpointerScript(t *testing.T) {
	home := t.TempDir()
	if p, _ := checkpointerScript(home, ""); p != "" {
		t.Fatalf("found %q in an empty home", p)
	}
	skill := filepath.Join(t.TempDir(), "herdsman")
	if err := os.MkdirAll(filepath.Join(skill, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "scripts", "checkpointer.sh"), []byte("#!/bin/bash\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if p, _ := checkpointerScript(home, skill); p != realPath(filepath.Join(skill, "scripts", "checkpointer.sh")) {
		t.Fatalf("--skill-dir: %q", p)
	}
	if p, why := checkpointerScript(home, filepath.Join(home, "missing")); p != "" || !strings.Contains(why, "--skill-dir") {
		t.Fatalf("missing --skill-dir: %q %q", p, why)
	}
	// The installed skill is a link, as the setup makes it.
	if err := os.MkdirAll(filepath.Join(home, ".claude", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(skill, filepath.Join(home, ".claude", "skills", "herdsman")); err != nil {
		t.Fatal(err)
	}
	if p, _ := checkpointerScript(home, ""); p != realPath(filepath.Join(skill, "scripts", "checkpointer.sh")) {
		t.Fatalf("linked skill: %q", p)
	}
	quoted := filepath.Join(t.TempDir(), "it's")
	if err := os.MkdirAll(filepath.Join(quoted, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(quoted, "scripts", "checkpointer.sh"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if p, _ := checkpointerScript(home, quoted); p != "" {
		t.Fatalf("a path with a quote was accepted: %q", p)
	}
}

// A28: the default effort is the harness's own reported default, never a
// level preferred by name, and none when the harness reports none.
func TestDefaultEffortIsTheHarnessDefault(t *testing.T) {
	list := []string{"effort-a", "medium", "effort-b"}
	for _, tc := range []struct{ def, want string }{
		{"effort-b", "effort-b"},
		{"", ""},
		{"effort-z", ""},
	} {
		if got := defaultEffort(list, tc.def); got != tc.want {
			t.Errorf("defaultEffort(%q) = %q, want %q", tc.def, got, tc.want)
		}
	}
}

// A provider/model id reaches the agent as one argv element.
func TestLaunchArgsProviderModel(t *testing.T) {
	k := &CatalogKind{Kind: "opencode", Label: "OpenCode", FreeModel: true, Models: []CatalogModel{}}
	args, err := launchArgs(k, "provider/model-a", "")
	if err != nil || len(args) != 2 || args[1] != "provider/model-a" {
		t.Fatalf("%v %v", args, err)
	}
}
