package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/sdk/internal/encoding/toml"
	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
)

func repoWith(t *testing.T, names ...string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		writeFile(t, filepath.Join(root, name), "")
	}
	return root
}

func TestLoadUsesDetectedDefaultsWithoutAFile(t *testing.T) {
	opts := isolated(t)
	opts.RepoRoot = repoWith(t, "bun.lock", "uv.lock")
	got, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	setup := got.Config.GetSetup()
	if !slices.Equal(setup.GetShare(), []string{"node_modules"}) || setup.GetInstall() != "bun install --frozen-lockfile && uv sync" {
		t.Fatalf("setup: %v", setup)
	}
	if got.Sources["setup.install"] != SourceDefault {
		t.Fatalf("source: %q", got.Sources["setup.install"])
	}
}

func TestAProjectFileTurnsDetectionOff(t *testing.T) {
	opts := isolated(t)
	opts.RepoRoot = repoWith(t, "bun.lock", "herdsman.config.toml")
	got, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.GetSetup().GetInstall() != "" || len(got.Config.GetSetup().GetShare()) != 0 {
		t.Fatalf("setup: %v", got.Config.GetSetup())
	}
}

func TestLoadToleratesARepositoryThatIsGone(t *testing.T) {
	opts := isolated(t)
	opts.RepoRoot = filepath.Join(t.TempDir(), "missing")
	if _, err := Load(opts); err != nil {
		t.Fatal(err)
	}
}

func TestDetectEcosystem(t *testing.T) {
	for name, tc := range map[string]struct {
		files []string
		check func(Ecosystem) bool
	}{
		"pnpm":    {[]string{"pnpm-lock.yaml"}, func(e Ecosystem) bool { return e.Install[0] == "pnpm install --frozen-lockfile" }},
		"npm":     {[]string{"package-lock.json"}, func(e Ecosystem) bool { return e.Install[0] == "npm ci" }},
		"yarn":    {[]string{"yarn.lock"}, func(e Ecosystem) bool { return e.Install[0] == "yarn install --frozen-lockfile" }},
		"berry":   {[]string{"yarn.lock", ".yarnrc.yml"}, func(e Ecosystem) bool { return e.Install[0] == "yarn install --immutable" }},
		"cargo":   {[]string{"Cargo.lock"}, func(e Ecosystem) bool { return e.Share[0] == "target" }},
		"poetry":  {[]string{"poetry.lock"}, func(e Ecosystem) bool { return e.Install[0] == "poetry install" }},
		"go":      {[]string{"go.sum"}, func(e Ecosystem) bool { return e.GoCachePerTask }},
		"compose": {[]string{"compose.yaml"}, func(e Ecosystem) bool { return e.Compose }},
		"env": {[]string{".env.example", ".env", "other"}, func(e Ecosystem) bool {
			return slices.Equal(e.EnvFiles, []string{".env", ".env.example"})
		}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := DetectEcosystem(repoWith(t, tc.files...))
			if err != nil {
				t.Fatal(err)
			}
			if !tc.check(got) {
				t.Fatalf("%+v", got)
			}
		})
	}
	if _, err := DetectEcosystem(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a missing folder was accepted")
	}
}

func TestInitWritesDetectedDefaultsWithACommentOnEveryKey(t *testing.T) {
	root := repoWith(t, "bun.lock")
	path := filepath.Join(t.TempDir(), "herdsman.config.toml")
	if _, err := InitDetected(path, FormatTOML, root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := toml.Decode(data)
	if err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	if install, ok := decoded.Lookup("setup", "install"); !ok || install.Str != "bun install --frozen-lockfile" {
		t.Fatalf("no detected install:\n%s", data)
	}
	lines := strings.Split(string(data), "\n")
	section := ""
	for i, line := range lines {
		if strings.HasPrefix(line, "[") {
			section = strings.Trim(line, "[]")
			continue
		}
		if key, _, ok := strings.Cut(line, " = "); ok && !strings.HasPrefix(line, "#") {
			if i == 0 || !strings.HasPrefix(lines[i-1], "# ") {
				t.Errorf("%s.%s has no comment", section, key)
			}
		}
	}
}

func TestEveryDefaultKeyHasAnExplanation(t *testing.T) {
	var walk func(node *tree.Node, prefix string)
	walk = func(node *tree.Node, prefix string) {
		if node.Kind != tree.Table {
			if keyComments[prefix] == "" {
				t.Errorf("%s has no explanation", prefix)
			}
			return
		}
		for _, key := range node.Keys() {
			item, _ := node.Get(key)
			walk(item, joinKey(prefix, key))
		}
	}
	walk(messageTree(Defaults().ProtoReflect()), "")
	if text := strings.Join(InitExplanations(), "\n"); !strings.Contains(text, "merge.strategy: ") || !strings.Contains(text, "setup.env: ") {
		t.Fatalf("explanations:\n%s", text)
	}
}

func TestInitDetectedInJSONAndWithoutARepository(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	if _, err := InitDetected(path, FormatJSON, ""); err != nil {
		t.Fatal(err)
	}
	if err := func() error { _, err := InitDetected(path, FormatJSON, ""); return err }(); err == nil {
		t.Fatal("replaced an existing file")
	}
	if _, err := InitDetected(filepath.Join(t.TempDir(), "c.x"), "nope", ""); err == nil {
		t.Fatal("an unknown format was accepted")
	}
	if _, err := InitDetected(filepath.Join(t.TempDir(), "c.toml"), FormatTOML, filepath.Join(t.TempDir(), "gone")); err != nil {
		t.Fatal(err)
	}
}
