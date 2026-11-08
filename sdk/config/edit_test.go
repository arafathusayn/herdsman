package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
	"google.golang.org/protobuf/proto"
)

func TestUpdateRenamesTablesAndNestedKeys(t *testing.T) {
	path := configFile(t, "# note\n[hooks.post-start]\nmy-name = \"a\"\n[projects.\"x.y\".hooks]\npre-merge = \"b\"\n[mystery-section]\nleft-alone = 1\n")
	if err := UpdateFile(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"my-name", "mystery-section", "left-alone", "# note"} {
		if !strings.Contains(text, want) {
			t.Fatalf("%q is gone:\n%s", want, text)
		}
	}
	got, err := ReadFile(path)
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	if got.Config.GetHooks().GetPostStart().GetNamed().GetCommands()["my-name"] != "a" {
		t.Fatalf("config: %v", got.Config)
	}
	if got.Config.GetProjects()["x.y"].GetHooks().GetPreMerge().GetCommand() != "b" {
		t.Fatalf("config: %v", got.Config)
	}
	if strings.Contains(text, "post-start") || strings.Contains(text, "pre-merge") {
		t.Fatalf("not canonical:\n%s", text)
	}
}

func TestUpdateOtherFormatsAndNoWork(t *testing.T) {
	path := writeFile(t, filepath.Join(t.TempDir(), "c.json"), `{"hooks":{"pre-merge":"a"},"commit":{"generation":{"template-append":"t"}}}`)
	if err := UpdateFile(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "pre-merge") || strings.Contains(string(data), "template-append") {
		t.Fatalf("file:\n%s", data)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateFile(path); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("a file with nothing to rewrite was rewritten")
	}
}

func TestSetReachesMapAndHookEntries(t *testing.T) {
	path := configFile(t, "[hooks.post-start]\nold-one = \"a\"\n")
	for key, value := range map[string]string{
		"hooks.post_start.second":         "b",
		"aliases.go-now":                  "up",
		`projects."h.io/a".worktree.path`: "/p",
		"branch.types":                    "one, two",
		"merge.verify":                    "false",
		"teardown.grace":                  "4",
		"hooks.pre_commit":                "make it",
	} {
		if err := SetFile(path, key, value); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}
	got, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c := got.Config
	if len(c.GetHooks().GetPostStart().GetNamed().GetCommands()) != 2 || c.GetAliases()["go-now"] != "up" {
		t.Fatalf("config: %v", c)
	}
	if c.GetProjects()["h.io/a"].GetWorktree().GetPath() != "/p" || len(c.GetBranch().GetTypes()) != 2 {
		t.Fatalf("config: %v", c)
	}
	if c.GetMerge().GetVerify() || c.GetTeardown().GetGrace() != 4 || c.GetHooks().GetPreCommit().GetCommand() != "make it" {
		t.Fatalf("config: %v", c)
	}
	if v, err := got.Get("hooks.post_start.second"); err != nil || v != "b" {
		t.Fatalf("get: %q %v", v, err)
	}
	if v, err := got.Get("branch.types"); err != nil || v != `["one","two"]` {
		t.Fatalf("get list: %q %v", v, err)
	}
	if _, err := got.Get("worktree.path"); err == nil {
		t.Fatal("a key the file lacks was found")
	}
	if _, err := got.Get("no.such"); err == nil {
		t.Fatal("an unknown key was found")
	}
}

func TestBadKeysAndTextValues(t *testing.T) {
	path := configFile(t, "")
	for key, value := range map[string]string{
		"":                     "x",
		"a..b":                 "x",
		`"open`:                "x",
		"merge":                "x",
		"merge.verify":         "maybe",
		"teardown.grace":       "soon",
		"worktree.path.deeper": "x",
		"aliases":              "x",
		"aliases.a.b":          "x",
		"projects.only":        "x",
		"hooks.pre_merge.a.b":  "x",
		"launch.prelude":       "x",
		"branch.nmae":          "x",
		"zzzzzzzzzzzz":         "x",
	} {
		if err := SetFile(path, key, value); err == nil {
			t.Errorf("%q accepted", key)
		}
	}
}

func TestEditsOnMissingOrBrokenFiles(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "none.toml")
	if err := SetFile(missing, "merge.verify", "true"); err == nil {
		t.Fatal("set on a missing file")
	}
	if _, err := UnsetFile(missing, "merge.verify"); err == nil {
		t.Fatal("unset on a missing file")
	}
	if err := UpdateFile(missing); err == nil {
		t.Fatal("update on a missing file")
	}
	if _, err := ExportFile(missing, "json"); err == nil {
		t.Fatal("export of a missing file")
	}
	broken := configFile(t, "[[[")
	if err := SetFile(broken, "merge.verify", "true"); err == nil {
		t.Fatal("set on a broken file")
	}
	if _, err := ReadFile(writeFile(t, filepath.Join(t.TempDir(), "c.ini"), "")); err == nil {
		t.Fatal("unknown extension accepted")
	}
	if _, err := ExportFile(configFile(t, ""), "ini"); err == nil {
		t.Fatal("unknown export format accepted")
	}
	if err := InitFile(filepath.Join(t.TempDir(), "c.ini"), "ini"); err == nil {
		t.Fatal("unknown init format accepted")
	}
}

func TestUnsetInEveryFormat(t *testing.T) {
	for _, format := range fixtureFormats {
		t.Run(format, func(t *testing.T) {
			data, err := os.ReadFile(fixturePath(format))
			if err != nil {
				t.Fatal(err)
			}
			path := writeFile(t, filepath.Join(t.TempDir(), "c."+format), string(data))
			removed, err := UnsetFile(path, "hooks.pre_merge")
			if err != nil || !removed {
				t.Fatalf("unset: %v %v", removed, err)
			}
			got, err := ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got.Config.GetHooks().GetPreMerge() != nil {
				t.Fatalf("still set: %v", got.Config.GetHooks())
			}
		})
	}
}

func TestDecoderEdges(t *testing.T) {
	for name, text := range map[string]string{
		"duplicate": `{"a":1,"a":2}`, "trailing": `{} {}`, "cut": `{"a":`, "cut array": `[1,`,
		"cut key": `{`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeJSON([]byte(text)); err == nil {
				t.Fatal("no error")
			}
		})
	}
	root, err := decodeJSON([]byte(`{"f":1.5,"n":null,"big":1e400}`))
	if err == nil {
		t.Fatalf("an unrepresentable number was accepted: %v", root)
	}
	root, err = decodeJSON([]byte(`{"f":1.5,"n":null,"e":[],"t":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := root.Get("f"); v.Flt != 1.5 {
		t.Fatalf("float: %v", v)
	}
	out, err := encodeText(FormatJSON, root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"e": []`) || !strings.Contains(string(out), `"t": {}`) {
		t.Fatalf("json:\n%s", out)
	}
	yamlOut, err := encodeText(FormatYAML, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"e: []", "t: {}", `"n": null`} {
		if !strings.Contains(string(yamlOut), want) {
			t.Fatalf("yaml lacks %q:\n%s", want, yamlOut)
		}
	}
	if _, err := decodeText(FormatBin, nil); err == nil {
		t.Fatal("bin is not text")
	}
	if _, err := encodeText(FormatBin, root); err == nil {
		t.Fatal("bin is not text")
	}
	if got, err := decodeText(FormatYAML, []byte("  \n")); err != nil || got.Len() != 0 {
		t.Fatalf("blank yaml: %v %v", got, err)
	}
	if got, err := decodeText(FormatYAML, []byte("~\n")); err != nil || got.Len() != 0 {
		t.Fatalf("null yaml: %v %v", got, err)
	}
	if _, err := decodeText(FormatYAML, []byte("a: [\n")); err == nil {
		t.Fatal("broken yaml accepted")
	}
	if _, err := decodeBin([]byte{0xff}, &report{}); err == nil {
		t.Fatal("broken binary accepted")
	}
}

func TestYAMLQuotesKeysThatWouldMisread(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	msg := &configv1.Config{Aliases: map[string]string{"on": "true", "a b": "0x10", "y": "- item"}}
	data, err := encodeMessage(msg, FormatYAML)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data))
	got, err := ReadFile(path)
	if err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	if !proto.Equal(got.Config, msg) {
		t.Fatalf("got %v\n%s", got.Config, data)
	}
}

func TestEmptyPipelineAndListRoundTrip(t *testing.T) {
	msg := &configv1.Config{
		Hooks:  &configv1.Hooks{PreStart: &configv1.HookSpec{Form: &configv1.HookSpec_Pipeline{Pipeline: &configv1.Pipeline{}}}},
		Policy: &configv1.Policy{Budget: &configv1.PolicyBudget{Tokens: proto.Int64(9000000000)}},
	}
	for _, format := range fixtureFormats {
		t.Run(format, func(t *testing.T) {
			data, err := encodeMessage(msg, format)
			if err != nil {
				t.Fatal(err)
			}
			got, err := ReadFile(writeFile(t, filepath.Join(t.TempDir(), "c."+format), string(data)))
			if err != nil {
				t.Fatalf("%v\n%s", err, data)
			}
			if got.Config.GetPolicy().GetBudget().GetTokens() != 9000000000 {
				t.Fatalf("got %v", got.Config)
			}
			if _, ok := got.Config.GetHooks().GetPreStart().GetForm().(*configv1.HookSpec_Pipeline); !ok {
				t.Fatalf("hook form lost: %v\n%s", got.Config.GetHooks(), data)
			}
		})
	}
}

func TestMoreWrongShapes(t *testing.T) {
	for name, text := range map[string]string{
		"section as scalar":   "[hooks]\npre_merge = true\n",
		"map as scalar":       "aliases = 1\n",
		"section scalar":      "merge = 1\n",
		"message map scalar":  "projects = 1\n",
		"map entry scalar":    "[projects]\na = 1\n",
		"prelude scalar":      "[launch]\nprelude = 1\n",
		"prelude item":        "[launch]\nprelude = [1]\n",
		"list item type":      "[branch]\ntypes = [1]\n",
		"map value type":      "[aliases]\na = 1\n",
		"bool type":           "[merge]\nverify = \"yes\"\n",
		"int64 type":          "[policy.budget]\ntokens = \"many\"\n",
		"enum type":           "[merge]\nland = 1\n",
		"hook named value":    "[hooks.pre_merge]\na = 1\n",
		"hook stage type":     "[hooks]\npre_merge = [1]\n",
		"hook stage named":    "[hooks]\npre_merge = [{ a = 1 }]\n",
		"aliases per project": "[projects.a.aliases]\nx = \"y\"\n",
		"version type":        "[schema]\nversion = 1\n",
		"version letters":     "[schema]\nversion = \"1.x.0\"\n",
		"inner section":       "[projects.a]\nmerge = 1\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadFile(configFile(t, text)); err == nil {
				t.Fatal("no error")
			}
		})
	}
}

func TestEnvironmentAndFlagEdges(t *testing.T) {
	opts := isolated(t)
	opts.Environ = []string{"HERDSMAN_NOPE__AT_ALL=1", "NOT_OURS__X=1", "HERDSMAN_BRANCH__TYPES=a,b", "broken"}
	got, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Warnings) != 1 || got.Warnings[0].Key != "HERDSMAN_NOPE__AT_ALL" {
		t.Fatalf("warnings: %v", got.Warnings)
	}
	if len(got.Config.GetBranch().GetTypes()) != 2 || got.Sources["branch.types"] != SourceEnv {
		t.Fatalf("types: %v", got.Config.GetBranch())
	}
	if got.Warnings[0].String() != "unknown key HERDSMAN_NOPE__AT_ALL" {
		t.Fatalf("line: %s", got.Warnings[0])
	}

	opts = isolated(t)
	opts.Strict = true
	opts.Environ = []string{"HERDSMAN_NOPE__AT_ALL=1"}
	if _, err := Load(opts); err == nil {
		t.Fatal("strict let an unknown variable through")
	}
	opts = isolated(t)
	opts.Environ = []string{"HERDSMAN_TEARDOWN__GRACE=soon"}
	if _, err := Load(opts); err == nil {
		t.Fatal("bad variable accepted")
	}
	opts = isolated(t)
	opts.Flags = map[string]string{"no.such": "1"}
	if _, err := Load(opts); err == nil {
		t.Fatal("unknown flag accepted")
	}
	opts = isolated(t)
	opts.Flags = map[string]string{"teardown.grace": "soon"}
	if _, err := Load(opts); err == nil {
		t.Fatal("bad flag accepted")
	}
}

func TestUserDirLookup(t *testing.T) {
	base := t.TempDir()
	environ := []string{"HOME=" + base, "XDG_CONFIG_HOME=" + filepath.Join(base, "xdg")}
	writeFile(t, filepath.Join(UserDir(environ), "config.toml"), "[teardown]\ngrace = 5\n")
	got, err := Load(Options{Environ: environ})
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.GetTeardown().GetGrace() != 5 || got.UserPath == "" {
		t.Fatalf("grace %d path %q", got.Config.GetTeardown().GetGrace(), got.UserPath)
	}
	if got, err = Load(Options{Environ: []string{}}); err != nil || got.UserPath != "" {
		t.Fatalf("nothing: %v %v", got, err)
	}
	if UserDir([]string{}) != "" {
		t.Fatalf("no home: %q", UserDir([]string{}))
	}
}

func TestLoadReportsBrokenLayers(t *testing.T) {
	opts := isolated(t)
	writeFile(t, filepath.Join(opts.UserDir, "config.toml"), "[[[")
	if _, err := Load(opts); err == nil {
		t.Fatal("broken user file accepted")
	}
	opts = isolated(t)
	opts.ConfigPath = configFile(t, "[[[")
	if _, err := Load(opts); err == nil {
		t.Fatal("broken project file accepted")
	}
	opts = isolated(t)
	opts.ConfigPath = filepath.Join(t.TempDir(), "gone.toml")
	if _, err := Load(opts); err == nil {
		t.Fatal("a missing named file was ignored")
	}
	opts = isolated(t)
	writeFile(t, filepath.Join(opts.UserDir, "config.toml"), "")
	writeFile(t, filepath.Join(opts.UserDir, "config.json"), "{}")
	if _, err := Load(opts); err == nil {
		t.Fatal("two user files accepted")
	}
	opts = isolated(t)
	opts.ConfigPath = configFile(t, "[teardown]\ngrace = 3000000000\n")
	if _, err := Load(opts); err == nil {
		t.Fatal("an out-of-range value bound")
	}
}

func TestProjectsLayerWithoutMatchingKey(t *testing.T) {
	opts := isolated(t)
	writeFile(t, filepath.Join(opts.UserDir, "config.toml"), "[projects.other.worktree]\npath = \"/o\"\n")
	opts.ProjectKey = "mine"
	got, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(got.Config.GetWorktree().GetPath(), "/o") {
		t.Fatalf("path %q", got.Config.GetWorktree().GetPath())
	}
}

func TestReplacingATableForgetsItsSources(t *testing.T) {
	opts := isolated(t)
	writeFile(t, filepath.Join(opts.UserDir, "config.toml"), "[hooks.pre-merge]\na = \"x\"\n")
	opts.ConfigPath = configFile(t, "[hooks]\npre-merge = \"y\"\n")
	got, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, stale := got.Sources["hooks.pre_merge.a"]; stale {
		t.Fatalf("sources: %v", got.Sources)
	}
	if got.Sources["hooks.pre_merge"] != SourceProject {
		t.Fatalf("sources: %v", got.Sources)
	}
}

func TestSuggestionsAndWarningText(t *testing.T) {
	w := Warning{Key: "a.b", Suggestion: "c"}
	if w.String() != "unknown key a.b (did you mean c?)" {
		t.Fatal(w.String())
	}
	if (&StrictError{Warning: w}).Error() != w.String() {
		t.Fatal("strict text")
	}
	if d := editDistance("nmae", "name"); d != 1 {
		t.Fatalf("distance %d", d)
	}
	if got := enumText((&configv1.Merge{}).ProtoReflect().Descriptor().Fields().ByName("land").Enum(), 99); got != "99" {
		t.Fatalf("unknown number: %s", got)
	}
}

func TestShowRendersEveryKind(t *testing.T) {
	got, err := ReadFile(fixturePath("toml"))
	if err != nil {
		t.Fatal(err)
	}
	shown := map[string]string{}
	for _, v := range got.Show() {
		shown[v.Key] = v.Value
	}
	if shown["merge.verify"] != "false" || shown["branch.types"] != `["wip","spike"]` {
		t.Fatalf("shown: %v", shown)
	}
	if !strings.Contains(shown["hooks.pre_commit"], `{"unit":"make unit"}`) {
		t.Fatalf("shown: %v", shown["hooks.pre_commit"])
	}
}

func TestSetKeepsFileMode(t *testing.T) {
	path := configFile(t, "")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetFile(path, "merge.verify", "true"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
}
