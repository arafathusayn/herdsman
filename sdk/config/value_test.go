package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTypedValueAndSource(t *testing.T) {
	opts := isolated(t)
	opts.ConfigPath = configFile(t, "[teardown]\ngrace = 2\n[hooks.post-start]\na = \"x\"\n")
	opts.Environ = []string{"HERDSMAN_HOOKS__POST_START__b=y"}
	got, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	value, source, err := got.Typed("teardown.grace")
	if err != nil || value != int64(2) || source != SourceProject {
		t.Fatalf("%v %q %v", value, source, err)
	}
	if _, source, err := got.Typed("hooks.post_start"); err != nil || source != "mixed" {
		t.Fatalf("%q %v", source, err)
	}
	if _, source, err := got.Typed("merge.land"); err != nil || source != SourceDefault {
		t.Fatalf("%q %v", source, err)
	}
	if _, _, err := got.Typed("worktree.nope"); err == nil {
		t.Fatal("an unknown key was accepted")
	}
	if _, _, err := got.Typed("hooks.pre_merge"); err == nil {
		t.Fatal("an unset key was accepted")
	}
	data, err := got.Encode(FormatJSON)
	if err != nil || !strings.Contains(string(data), `"grace": 2`) {
		t.Fatalf("%s %v", data, err)
	}
}

func TestEditFileReportsOldAndNew(t *testing.T) {
	path := configFile(t, "[branch]\nname = \"a\"\n")
	edit, err := EditFile(path, "branch.name", "b", false)
	if err != nil {
		t.Fatal(err)
	}
	if edit.Path != path || edit.Key != "branch.name" || !edit.HadOld || edit.Old != "a" || edit.New != "b" {
		t.Fatalf("%+v", edit)
	}
	edit, err = EditFile(path, "merge.land", "push", false)
	if err != nil || edit.HadOld || edit.New != "push" {
		t.Fatalf("%+v %v", edit, err)
	}
}

func TestEditFileAppendsToAList(t *testing.T) {
	path := configFile(t, "# c\n[branch]\ntypes = [\"a\"] # why\n")
	edit, err := EditFile(path, "branch.types", "b,c", true)
	if err != nil {
		t.Fatal(err)
	}
	if edit.Old != `["a"]` || edit.New != `["a","b,c"]` {
		t.Fatalf("%+v", edit)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "# c") || !strings.Contains(string(data), "# why") {
		t.Fatalf("lost a comment:\n%s", data)
	}
	if edit, err = EditFile(path, "setup.share", "x", true); err != nil || edit.New != `["x"]` {
		t.Fatalf("%+v %v", edit, err)
	}
	if _, err := EditFile(path, "branch.name", "x", true); err == nil || !strings.Contains(err.Error(), "not a list") {
		t.Fatalf("err = %v", err)
	}
	if _, err := EditFile(path, "aliases.a", "x", true); err == nil {
		t.Fatal("append reached a map entry")
	}
}

func TestUserFilePath(t *testing.T) {
	opts := isolated(t)
	path, exists, err := UserFile(opts)
	if err != nil || exists || path != filepath.Join(opts.UserDir, "config.toml") {
		t.Fatalf("%q %v %v", path, exists, err)
	}
	written := writeFile(t, filepath.Join(opts.UserDir, "config.yaml"), "")
	path, exists, err = UserFile(opts)
	if err != nil || !exists || path != written {
		t.Fatalf("%q %v %v", path, exists, err)
	}
	if _, _, err := UserFile(Options{Environ: []string{"HOME="}, UserDir: ""}); err == nil {
		t.Fatal("no folder was accepted")
	}
}
