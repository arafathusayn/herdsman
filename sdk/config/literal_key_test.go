package config

import (
	"os"
	"strings"
	"testing"
)

func TestSetTreatsMapKeysAsLiteralText(t *testing.T) {
	path := configFile(t, "[aliases]\nship-it = \"old\"\n")
	if err := SetFile(path, "aliases.ship_it", "new"); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	aliases := got.Config.GetAliases()
	if aliases["ship-it"] != "old" || aliases["ship_it"] != "new" {
		t.Fatalf("aliases: %v", aliases)
	}
}

func TestUnsetLeavesADifferentlySpelledMapKey(t *testing.T) {
	const text = "[aliases]\nship-it = \"old\"\n"
	path := configFile(t, text)
	removed, err := UnsetFile(path, "aliases.ship_it")
	if err != nil || removed {
		t.Fatalf("removed %v, err %v", removed, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != text {
		t.Fatalf("file:\n%s", data)
	}
}

func TestHookEntryNamesAreLiteralButTheHookFieldIsNot(t *testing.T) {
	path := configFile(t, "[hooks.post-start]\nmy-name = \"a\"\n")
	if err := SetFile(path, "hooks.post_start.my_name", "b"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "post_start") {
		t.Fatalf("a second spelling of the field was added:\n%s", data)
	}
	got, err := ReadFile(path)
	if err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	commands := got.Config.GetHooks().GetPostStart().GetNamed().GetCommands()
	if commands["my-name"] != "a" || commands["my_name"] != "b" {
		t.Fatalf("commands: %v", commands)
	}
}

func TestProjectKeysAreLiteralAndInnerFieldsAreNot(t *testing.T) {
	path := configFile(t, "[projects.a-b.commit.generation]\ntemplate-append = \"x\"\n")
	if err := SetFile(path, "projects.a-b.commit.generation.template_append", "y"); err != nil {
		t.Fatal(err)
	}
	if err := SetFile(path, "projects.a_b.worktree.path", "/other"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(path)
	if err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	projects := got.Config.GetProjects()
	if projects["a-b"].GetCommit().GetGeneration().GetTemplateAppend() != "y" {
		t.Fatalf("a-b: %v", projects["a-b"])
	}
	if projects["a_b"].GetWorktree().GetPath() != "/other" || projects["a-b"].GetWorktree() != nil {
		t.Fatalf("projects: %v", projects)
	}
}
