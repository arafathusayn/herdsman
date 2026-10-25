package catalog

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The fixtures are the real harness output that internal/web embeds.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "web", "catalogdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParsers(t *testing.T) {
	cx, err := ParseCodexModels(fixture(t, "codex-debug-models.json"))
	if err != nil || len(cx.Models) == 0 || cx.DefaultModel != cx.Models[0].ID || cx.DocsURL == "" {
		t.Fatalf("codex: %+v %v", cx, err)
	}
	// Codex reports a default for each model; it is kept as reported.
	if m := cx.Models[0]; m.DefaultEffort == "" || !slices.Contains(m.Efforts, m.DefaultEffort) {
		t.Errorf("codex default %+v", m)
	}
	cl, err := ParseClaudeHelp(fixture(t, "claude-help.txt"))
	if err != nil || len(cl.Models) == 0 || cl.DocsURL == "" {
		t.Fatalf("claude: %+v %v", cl, err)
	}
	// Claude's help reports no default effort: there is none.
	for _, m := range cl.Models {
		if m.DefaultEffort != "" {
			t.Errorf("claude default %+v", m)
		}
	}
	kinds, err := ParseKinds(fixture(t, "herdr-agent-start-help.txt"))
	if err != nil || !slices.Contains(kinds, "claude") {
		t.Errorf("kinds %v %v", kinds, err)
	}
	for _, in := range []string{"", "not json", `{"models":[]}`, `{"models":[{"slug":"x","visibility":"hide"}]}`,
		`{"models":[{"slug":"bad slug","visibility":"list"}]}`, `{"models":[{"slug":"m","visibility":"list","supported_reasoning_levels":[{"effort":"BAD"}]}]}`,
		`{"models":[{"slug":"m","visibility":"list"}]}`} {
		if _, err := ParseCodexModels([]byte(in)); err == nil {
			t.Errorf("codex %q: no error", in)
		}
	}
	if _, err := ParseClaudeHelp([]byte("--model <m>")); err == nil {
		t.Error("claude: no error")
	}
	if _, err := ParseKinds([]byte("none")); err == nil {
		t.Error("kinds: no error")
	}
	if k, err := ParseCodexModels([]byte(`{"models":[{"slug":"model-a","visibility":"list","default_reasoning_level":"effortx","supported_reasoning_levels":[{"effort":"effortx"}]}]}`)); err != nil || k.Models[0].Label != "model-a" || k.Models[0].DefaultEffort != "effortx" {
		t.Errorf("%+v %v", k, err)
	}
}

func TestDefaultEffort(t *testing.T) {
	list := []string{"effort-a", "medium", "effort-b"}
	for def, want := range map[string]string{"effort-b": "effort-b", "": "", "effort-z": ""} {
		if got := DefaultEffort(list, def); got != want {
			t.Errorf("%q: %q", def, got)
		}
	}
}

// fake answers commands from a table; a missing program is exec.ErrNotFound.
func fake(out map[string]string, fail map[string]error) Runner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		if err, ok := fail[key]; ok {
			return nil, err
		}
		if err, ok := fail[name]; ok {
			return nil, err
		}
		return []byte(out[key]), nil
	}
}

func TestProbe(t *testing.T) {
	out := map[string]string{
		"codex debug models": string(fixture(t, "codex-debug-models.json")),
		"codex --version":    "codex-cli 1.2.3\nmore\n",
		"claude --help":      string(fixture(t, "claude-help.txt")),
		"claude --version":   "4.5.6 (Claude Code)\n",
	}
	cx := Probe(context.Background(), fake(out, nil), "codex")
	if cx.Error != "" || cx.Version != "codex-cli 1.2.3" || len(cx.Models) == 0 || cx.DocsURL == "" {
		t.Errorf("%+v", cx)
	}
	cl := Probe(context.Background(), fake(out, nil), "claude")
	if cl.Error != "" || cl.Version != "4.5.6 (Claude Code)" || len(cl.Models) == 0 {
		t.Errorf("%+v", cl)
	}
	// No version: the models still print.
	h := Probe(context.Background(), fake(out, map[string]error{"codex --version": errors.New("exit status 2")}), "codex")
	if h.Error != "" || h.Version != "unknown" {
		t.Errorf("%+v", h)
	}
	h = Probe(context.Background(), fake(out, map[string]error{"codex": exec.ErrNotFound}), "codex")
	if h.Error != "not found on PATH" || h.Models != nil {
		t.Errorf("%+v", h)
	}
	h = Probe(context.Background(), fake(out, map[string]error{"codex debug models": errors.New("exit status 1")}), "codex")
	if h.Error != "codex debug models: exit status 1" {
		t.Errorf("%+v", h)
	}
	bad := map[string]string{"claude --help": "no models here"}
	h = Probe(context.Background(), fake(bad, nil), "claude")
	if !strings.HasPrefix(h.Error, "claude --help: ") {
		t.Errorf("%+v", h)
	}
	h = Probe(context.Background(), fake(out, nil), "other")
	if h.Error != "no model list to probe" {
		t.Errorf("%+v", h)
	}
}

func TestExecRunner(t *testing.T) {
	if _, err := ExecRunner(context.Background(), filepath.Join(t.TempDir(), "none")); err == nil {
		t.Error("no error for a missing program")
	}
	out, err := ExecRunner(context.Background(), "/bin/echo", "hi")
	if err != nil || string(out) != "hi\n" {
		t.Errorf("%q %v", out, err)
	}
}
