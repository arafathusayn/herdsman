package config

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func TestUnknownKeyInARepositorysSafetySectionStops(t *testing.T) {
	for name, text := range map[string]string{
		"policy": "[projects.repo.policy]\nmax_agnets = 1\n",
		"launch": "[projects.repo.launch]\ncomand = [\"x\"]\n",
		"nested": "[[projects.repo.launch.prelude]]\nargz = [\"x\"]\n",
	} {
		t.Run(name, func(t *testing.T) {
			opts := isolated(t)
			writeFile(t, filepath.Join(opts.UserDir, "config.toml"), text)
			opts.ProjectKey = "repo"
			_, err := Load(opts)
			var strict *StrictError
			if !errors.As(err, &strict) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestUnknownKeyElsewhereUnderARepositoryOnlyWarns(t *testing.T) {
	opts := isolated(t)
	writeFile(t, filepath.Join(opts.UserDir, "config.toml"), "[projects.repo.branch]\nnmae = \"x\"\n")
	got, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Warnings) != 1 || got.Warnings[0].Key != "projects.repo.branch.nmae" {
		t.Fatalf("warnings: %v", got.Warnings)
	}
}

func TestUnknownWireFieldInARepositorysSafetySectionStops(t *testing.T) {
	policy := protowire.AppendTag(nil, 100, protowire.VarintType)
	policy = protowire.AppendVarint(policy, 1)
	override, err := proto.Marshal(&configv1.Config{})
	if err != nil {
		t.Fatal(err)
	}
	override = protowire.AppendTag(override, 8, protowire.BytesType)
	override = protowire.AppendBytes(override, policy)
	entry := protowire.AppendTag(nil, 1, protowire.BytesType)
	entry = protowire.AppendString(entry, "repo")
	entry = protowire.AppendTag(entry, 2, protowire.BytesType)
	entry = protowire.AppendBytes(entry, override)
	data := protowire.AppendTag(nil, 10, protowire.BytesType)
	data = protowire.AppendBytes(data, entry)

	_, err = ReadFile(writeFile(t, filepath.Join(t.TempDir(), "c.bin"), string(data)))
	var strict *StrictError
	if !errors.As(err, &strict) {
		t.Fatalf("err = %v", err)
	}
}

func TestEnvironmentTypoInASafetySectionStops(t *testing.T) {
	for _, name := range []string{"HERDSMAN_POLICY__MAX_AGNETS", "HERDSMAN_LAUNCH__COMAND", "HERDSMAN_PROJECTS__REPO__POLICY__MAX_AGNETS"} {
		t.Run(name, func(t *testing.T) {
			opts := isolated(t)
			opts.Environ = []string{name + "=2"}
			if _, err := Load(opts); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestEnvironmentTypoElsewhereOnlyWarns(t *testing.T) {
	opts := isolated(t)
	opts.Environ = []string{"HERDSMAN_BRANCH__NMAE=x"}
	got, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Warnings) != 1 || got.Warnings[0].Key != "HERDSMAN_BRANCH__NMAE" {
		t.Fatalf("warnings: %v", got.Warnings)
	}
}

func TestEnvironmentKeepsTheSpellingOfAMapKey(t *testing.T) {
	opts := isolated(t)
	opts.ConfigPath = configFile(t, "[launch.env]\nPATH = \"/old\"\n")
	opts.Environ = []string{"HERDSMAN_LAUNCH__ENV__PATH=/custom/bin"}
	got, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	env := got.Config.GetLaunch().GetEnv()
	if len(env) != 1 || env["PATH"] != "/custom/bin" {
		t.Fatalf("env: %v", env)
	}
}

func TestUnsupportedVersionFromAnyLayerIsRefused(t *testing.T) {
	t.Run("environment", func(t *testing.T) {
		opts := isolated(t)
		opts.Environ = []string{"HERDSMAN_SCHEMA__VERSION=99.0.0"}
		if _, err := Load(opts); err == nil || !strings.Contains(err.Error(), "upgrade herdsman") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("flag", func(t *testing.T) {
		opts := isolated(t)
		opts.Flags = map[string]string{"schema.version": "99.0.0"}
		if _, err := Load(opts); err == nil || !strings.Contains(err.Error(), "upgrade herdsman") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("repository override", func(t *testing.T) {
		opts := isolated(t)
		writeFile(t, filepath.Join(opts.UserDir, "config.toml"), "[projects.repo.schema]\nversion = \"99.0.0\"\n")
		opts.ProjectKey = "repo"
		if _, err := Load(opts); err == nil || !strings.Contains(err.Error(), "upgrade herdsman") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestSchemaClosesSafetySectionsInsideRepositories(t *testing.T) {
	data, err := JSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	step := func(node map[string]any, key string) map[string]any {
		next, ok := node[key].(map[string]any)
		if !ok {
			t.Fatalf("no %q in %v", key, node)
		}
		return next
	}
	override := step(step(step(doc, "properties"), "projects"), "additionalProperties")
	for _, section := range []string{"policy", "launch"} {
		if closed := step(step(override, "properties"), section)["additionalProperties"]; closed != false {
			t.Errorf("%s: additionalProperties = %v", section, closed)
		}
	}
	if closed, present := step(step(override, "properties"), "branch")["additionalProperties"]; present {
		t.Errorf("branch: additionalProperties = %v", closed)
	}
}

func TestEnvironmentWithAnEmptySegmentIsNotASetting(t *testing.T) {
	opts := isolated(t)
	opts.Environ = []string{"HERDSMAN_ALIASES__=x"}
	got, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Config.GetAliases()) != 0 || len(got.Warnings) != 1 {
		t.Fatalf("aliases %v, warnings %v", got.Config.GetAliases(), got.Warnings)
	}
}
