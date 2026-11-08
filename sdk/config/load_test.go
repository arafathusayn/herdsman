package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
	"google.golang.org/protobuf/proto"
)

// fixtureFormats are the files of testdata/fixtures, one encoding each of the
// same settings.
var fixtureFormats = []string{"toml", "yaml", "json", "bin"}

func fixtureMessage() *configv1.Config {
	return &configv1.Config{
		Schema:   &configv1.Schema{Version: proto.String("1.2.0")},
		Worktree: &configv1.Worktree{Path: proto.String("/srv/trees/{{ task }}")},
		Branch:   &configv1.Branch{Name: proto.String("work/{{ task }}"), Types: []string{"wip", "spike"}},
		Merge: &configv1.Merge{
			Strategy: configv1.MergeStrategy_MERGE_STRATEGY_REBASE,
			Land:     configv1.LandMode_LAND_MODE_PUSH,
			Verify:   proto.Bool(false),
		},
		Teardown: &configv1.Teardown{Grace: proto.Int32(3), Volumes: configv1.VolumePolicy_VOLUME_POLICY_ON_TEARDOWN},
		Commit: &configv1.Commit{Generation: &configv1.CommitGeneration{
			TemplateAppend: proto.String("Name the ticket."),
		}},
		Hooks: &configv1.Hooks{
			PreMerge: &configv1.HookSpec{Form: &configv1.HookSpec_Command{Command: "make lint"}},
			PostStart: &configv1.HookSpec{Form: &configv1.HookSpec_Named{Named: &configv1.NamedCommands{
				Commands: map[string]string{"cache-warm": "make warm"},
			}}},
			PreCommit: &configv1.HookSpec{Form: &configv1.HookSpec_Pipeline{Pipeline: &configv1.Pipeline{Stages: []*configv1.PipelineStage{
				{Form: &configv1.PipelineStage_Command{Command: "make fmt"}},
				{Form: &configv1.PipelineStage_Named{Named: &configv1.NamedCommands{Commands: map[string]string{"unit": "make unit"}}}},
			}}}},
		},
		Launch: &configv1.Launch{
			Command: []string{"agent", "--fast"},
			Prelude: []*configv1.LaunchStep{{Argv: []string{"agent", "update"}, Retry: configv1.RetryPolicy_RETRY_POLICY_NEVER}},
		},
		Policy:  &configv1.Policy{MaxAgents: proto.Int32(4), Protected: []string{"trunk"}},
		Aliases: map[string]string{"ship-it": "merge --land"},
	}
}

func fixturePath(format string) string {
	return filepath.Join("testdata", "fixtures", "all."+format)
}

// isolated returns options that read nothing from the real machine.
func isolated(t *testing.T) Options {
	t.Helper()
	return Options{UserDir: t.TempDir(), Environ: []string{}}
}

func writeFile(t *testing.T, path, text string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func configFile(t *testing.T, text string) string {
	t.Helper()
	return writeFile(t, filepath.Join(t.TempDir(), "herdsman.config.toml"), text)
}

func TestEveryDecoderBindsTheSameMessage(t *testing.T) {
	want := fixtureMessage()
	for _, format := range fixtureFormats {
		t.Run(format, func(t *testing.T) {
			got, err := ReadFile(fixturePath(format))
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Warnings) != 0 {
				t.Fatalf("warnings: %v", got.Warnings)
			}
			if !proto.Equal(got.Config, want) {
				t.Fatalf("got %v\nwant %v", got.Config, want)
			}
		})
	}
}

func TestExportRoundTripsEveryPairWithoutLoss(t *testing.T) {
	want := fixtureMessage()
	for _, from := range fixtureFormats {
		for _, to := range fixtureFormats {
			t.Run(from+"-to-"+to, func(t *testing.T) {
				data, err := ExportFile(fixturePath(from), to)
				if err != nil {
					t.Fatal(err)
				}
				out := writeFile(t, filepath.Join(t.TempDir(), "out."+to), string(data))
				got, err := ReadFile(out)
				if err != nil {
					t.Fatalf("%v\n%s", err, data)
				}
				if !proto.Equal(got.Config, want) {
					t.Fatalf("got %v\nwant %v\n%s", got.Config, want, data)
				}
			})
		}
	}
}

func TestKebabSpellingsAreNormalized(t *testing.T) {
	path := configFile(t, "[commit.generation]\ntemplate-append = \"x\"\n[hooks]\npre-merge = \"y\"\n[hooks.post-start]\nkeep-this-name = \"z\"\n")
	got, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if v := got.Config.GetCommit().GetGeneration().GetTemplateAppend(); v != "x" {
		t.Fatalf("template_append = %q", v)
	}
	if v := got.Config.GetHooks().GetPreMerge().GetCommand(); v != "y" {
		t.Fatalf("pre_merge = %q", v)
	}
	if _, ok := got.Config.GetHooks().GetPostStart().GetNamed().GetCommands()["keep-this-name"]; !ok {
		t.Fatalf("a hook's own name was rewritten: %v", got.Config.GetHooks().GetPostStart())
	}
}

func TestTwoSpellingsOfOneKeyAreRefused(t *testing.T) {
	path := configFile(t, "[hooks]\npre-merge = \"a\"\npre_merge = \"b\"\n")
	if _, err := ReadFile(path); err == nil || !strings.Contains(err.Error(), "pre_merge") {
		t.Fatalf("err = %v", err)
	}
}

func TestUnknownKeyWarnsWithSuggestionOutsideStrictSections(t *testing.T) {
	path := configFile(t, "[branch]\nnmae = \"x\"\n[mystery]\na = 1\n")
	got, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Warnings) != 2 {
		t.Fatalf("warnings: %v", got.Warnings)
	}
	if got.Warnings[0].Key != "branch.nmae" || got.Warnings[0].Suggestion != "name" {
		t.Fatalf("warning: %+v", got.Warnings[0])
	}
}

func TestMisspelledSafetyKeyStops(t *testing.T) {
	for name, text := range map[string]string{
		"policy":        "[policy]\nmax_agnets = 2\n",
		"policy nested": "[policy.budget]\nwal = \"1h\"\n",
		"launch":        "[launch]\npreludes = []\n",
		"launch step":   "[[launch.prelude]]\nargvs = [\"a\"]\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ReadFile(configFile(t, text))
			var strict *StrictError
			if !errors.As(err, &strict) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestStrictModeTurnsWarningsIntoErrors(t *testing.T) {
	opts := isolated(t)
	opts.ConfigPath = configFile(t, "[branch]\nnmae = \"x\"\n")
	opts.Strict = true
	if _, err := Load(opts); err == nil {
		t.Fatal("no error")
	}
}

func TestPrecedenceAndSources(t *testing.T) {
	opts := isolated(t)
	writeFile(t, filepath.Join(opts.UserDir, "config.toml"), `
[branch]
name = "user/{{ task }}"
types = ["one"]
[worktree]
path = "/user"
[teardown]
grace = 7
[hooks.post-start]
from-user = "u"
`)
	opts.ConfigPath = configFile(t, `
[branch]
types = ["two"]
[worktree]
path = "/project"
[merge]
land = "local"
[hooks.post-start]
from-project = "p"
`)
	opts.Environ = []string{"HERDSMAN_MERGE__LAND=push", "HERDSMAN_WORKTREE__PATH=/env", "HERDSMAN_STATE_DIR=/ignored"}
	opts.Flags = map[string]string{"worktree.path": "/flag"}
	got, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	if v := got.Config.GetWorktree().GetPath(); v != "/flag" {
		t.Fatalf("path = %q", v)
	}
	if v := got.Config.GetMerge().GetLand(); v != configv1.LandMode_LAND_MODE_PUSH {
		t.Fatalf("land = %v", v)
	}
	if v := got.Config.GetBranch().GetTypes(); len(v) != 2 || v[0] != "one" || v[1] != "two" {
		t.Fatalf("lists must append, got %v", v)
	}
	if v := got.Config.GetHooks().GetPostStart().GetNamed().GetCommands(); len(v) != 2 {
		t.Fatalf("hook maps must append, got %v", v)
	}
	for key, want := range map[string]string{
		"worktree.path":              SourceFlag,
		"merge.land":                 SourceEnv,
		"branch.name":                SourceUser,
		"teardown.grace":             SourceUser,
		"branch.types":               SourceProject,
		"merge.strategy":             SourceDefault,
		"hooks.post_start.from-user": SourceUser,
	} {
		if s := got.Sources[key]; s != want {
			t.Errorf("source of %s = %q, want %q", key, s, want)
		}
	}
}

func TestDefaultListIsReplacedNotAppended(t *testing.T) {
	opts := isolated(t)
	opts.ConfigPath = configFile(t, "[branch]\ntypes = [\"only\"]\n")
	got, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	if v := got.Config.GetBranch().GetTypes(); len(v) != 1 || v[0] != "only" {
		t.Fatalf("types = %v", v)
	}
}

func TestLaunchListsReplaceAcrossFiles(t *testing.T) {
	opts := isolated(t)
	writeFile(t, filepath.Join(opts.UserDir, "config.toml"), "[launch]\ncommand = [\"a\", \"b\"]\n")
	opts.ConfigPath = configFile(t, "[launch]\ncommand = [\"c\"]\n")
	got, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	if v := got.Config.GetLaunch().GetCommand(); len(v) != 1 || v[0] != "c" {
		t.Fatalf("an argv must not be spliced: %v", v)
	}
}

func TestProjectOverrideFromUserFile(t *testing.T) {
	opts := isolated(t)
	writeFile(t, filepath.Join(opts.UserDir, "config.toml"), `
[worktree]
path = "/general"
[projects."host.example/team/app".worktree]
path = "/special"
`)
	opts.ProjectKey = "host.example/team/app"
	got, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	if v := got.Config.GetWorktree().GetPath(); v != "/special" {
		t.Fatalf("path = %q", v)
	}
}

func TestNestedProjectsRefused(t *testing.T) {
	path := configFile(t, "[projects.a.projects.b.worktree]\npath = \"x\"\n")
	if _, err := ReadFile(path); err == nil {
		t.Fatal("no error")
	}
}

func TestShowListsEveryValueWithItsSource(t *testing.T) {
	opts := isolated(t)
	opts.ConfigPath = configFile(t, "[teardown]\ngrace = 2\n")
	got, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]Shown{}
	for _, v := range got.Show() {
		found[v.Key] = v
	}
	if v := found["teardown.grace"]; v.Value != "2" || v.Source != SourceProject {
		t.Fatalf("teardown.grace: %+v", v)
	}
	if v := found["merge.strategy"]; v.Value != "squash" || v.Source != SourceDefault {
		t.Fatalf("merge.strategy: %+v", v)
	}
}

func TestDiscoveryOrder(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	parent := writeFile(t, filepath.Join(base, "herdsman.config.toml"), "[teardown]\ngrace = 1\n")
	inside := writeFile(t, filepath.Join(repo, "herdsman.config.toml"), "[teardown]\ngrace = 2\n")
	named := writeFile(t, filepath.Join(base, "other", "any-name.toml"), "[teardown]\ngrace = 3\n")
	viaEnv := writeFile(t, filepath.Join(base, "env", "e.toml"), "[teardown]\ngrace = 4\n")

	opts := isolated(t)
	opts.RepoRoot = repo
	check := func(wantPath string, wantGrace int32) {
		t.Helper()
		got, err := Load(opts)
		if err != nil {
			t.Fatal(err)
		}
		if got.ProjectPath != wantPath || got.Config.GetTeardown().GetGrace() != wantGrace {
			t.Fatalf("path %q grace %d, want %q %d", got.ProjectPath, got.Config.GetTeardown().GetGrace(), wantPath, wantGrace)
		}
	}
	check(parent, 1)
	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	check(inside, 2)
	opts.Environ = []string{"HERDSMAN_CONFIG=" + viaEnv}
	check(viaEnv, 4)
	opts.ConfigPath = named
	check(named, 3)
}

func TestTwoFormatsSideBySideNameBoth(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(base, "herdsman.config.toml"), "")
	writeFile(t, filepath.Join(base, "herdsman.config.yaml"), "")
	opts := isolated(t)
	opts.RepoRoot = repo
	_, err := Load(opts)
	if err == nil || !strings.Contains(err.Error(), "herdsman.config.toml") || !strings.Contains(err.Error(), "herdsman.config.yaml") {
		t.Fatalf("err = %v", err)
	}
}

func TestSchemaMajorGate(t *testing.T) {
	if _, err := ReadFile(configFile(t, "[schema]\nversion = \"99.0.0\"\n")); err == nil || !strings.Contains(err.Error(), "99.0.0") {
		t.Fatalf("higher major: %v", err)
	}
	if _, err := ReadFile(configFile(t, "[schema]\nversion = \"one\"\n")); err == nil {
		t.Fatal("a version that is not SemVer was accepted")
	}
}

func TestWrongTypesAreErrors(t *testing.T) {
	for name, text := range map[string]string{
		"string for int":  "[teardown]\ngrace = \"soon\"\n",
		"int range":       "[teardown]\ngrace = 3000000000\n",
		"bad enum":        "[merge]\nstrategy = \"octopus\"\n",
		"unspecified":     "[merge]\nstrategy = \"unspecified\"\n",
		"scalar section":  "[merge]\n[teardown]\ngrace = [1]\n",
		"list for string": "[worktree]\npath = [\"a\"]\n",
		"bad hook":        "[hooks]\npre_merge = 3\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadFile(configFile(t, text)); err == nil {
				t.Fatal("no error")
			}
		})
	}
}

func TestSetGetUnsetKeepTomlComments(t *testing.T) {
	path := configFile(t, "# top note\n[hooks]\npre-merge = \"old\" # why\n")
	if err := SetFile(path, "hooks.pre_merge", "new"); err != nil {
		t.Fatal(err)
	}
	if err := SetFile(path, "teardown.grace", "9"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "# top note") || strings.Contains(text, "pre_merge") {
		t.Fatalf("file:\n%s", text)
	}
	got, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := got.Get("hooks.pre_merge"); v != "new" {
		t.Fatalf("get = %q", v)
	}
	if v, _ := got.Get("teardown.grace"); v != "9" {
		t.Fatalf("get = %q", v)
	}
	removed, err := UnsetFile(path, "teardown.grace")
	if err != nil || !removed {
		t.Fatalf("unset: %v %v", removed, err)
	}
	if removed, err = UnsetFile(path, "teardown.grace"); err != nil || removed {
		t.Fatalf("second unset: %v %v", removed, err)
	}
}

func TestSetWorksInEveryFormat(t *testing.T) {
	for _, format := range fixtureFormats {
		t.Run(format, func(t *testing.T) {
			data, err := os.ReadFile(fixturePath(format))
			if err != nil {
				t.Fatal(err)
			}
			path := writeFile(t, filepath.Join(t.TempDir(), "c."+format), string(data))
			if err := SetFile(path, "merge.strategy", "merge"); err != nil {
				t.Fatal(err)
			}
			got, err := ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got.Config.GetMerge().GetStrategy() != configv1.MergeStrategy_MERGE_STRATEGY_MERGE {
				t.Fatalf("strategy = %v", got.Config.GetMerge().GetStrategy())
			}
			if got.Config.GetBranch().GetName() != "work/{{ task }}" {
				t.Fatalf("lost a value: %v", got.Config)
			}
		})
	}
}

func TestSetRefusesBadValueAndLeavesFile(t *testing.T) {
	const text = "[merge]\nstrategy = \"squash\"\n"
	path := configFile(t, text)
	if err := SetFile(path, "merge.strategy", "octopus"); err == nil {
		t.Fatal("no error")
	}
	if err := SetFile(path, "merge.nothing", "x"); err == nil {
		t.Fatal("no error for an unknown key")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != text {
		t.Fatalf("file changed:\n%s", data)
	}
}

func TestUpdateRewritesCanonicalNamesKeepingComments(t *testing.T) {
	path := configFile(t, "# keep me\n[hooks]\npre-merge = \"a\"\n[commit.generation]\ntemplate-append = \"b\"\n")
	if err := UpdateFile(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "pre-merge") || strings.Contains(text, "template-append") || !strings.Contains(text, "# keep me") {
		t.Fatalf("file:\n%s", text)
	}
	got, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.GetHooks().GetPreMerge().GetCommand() != "a" || got.Config.GetCommit().GetGeneration().GetTemplateAppend() != "b" {
		t.Fatalf("config: %v", got.Config)
	}
}

func TestInitWritesLoadableDefaultsAndRefusesOverwrite(t *testing.T) {
	for _, format := range fixtureFormats {
		t.Run(format, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "herdsman.config."+format)
			if err := InitFile(path, format); err != nil {
				t.Fatal(err)
			}
			got, err := ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(got.Config, Defaults()) {
				t.Fatalf("got %v", got.Config)
			}
			if err := InitFile(path, format); err == nil {
				t.Fatal("an existing file was overwritten")
			}
		})
	}
}

func TestJSONSchemaIsGeneratedFromTheDescriptor(t *testing.T) {
	data, err := JSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	props := doc["properties"].(map[string]any)
	merge := props["merge"].(map[string]any)["properties"].(map[string]any)
	enum := merge["strategy"].(map[string]any)["enum"].([]any)
	if len(enum) != 3 || enum[2] != "squash" {
		t.Fatalf("enum = %v", enum)
	}
	if props["policy"].(map[string]any)["additionalProperties"] != false {
		t.Fatal("a strict section must close its properties")
	}
	if props["branch"].(map[string]any)["additionalProperties"] == false {
		t.Fatal("an ordinary section must stay open")
	}
	hooks := props["hooks"].(map[string]any)["properties"].(map[string]any)
	if _, ok := hooks["pre-merge"]; !ok {
		t.Fatal("the kebab spelling is missing")
	}
	if _, ok := hooks["pre_merge"].(map[string]any)["oneOf"]; !ok {
		t.Fatalf("hook form: %v", hooks["pre_merge"])
	}
}
