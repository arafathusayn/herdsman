package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/docgen"
)

const docsDir = "../../docs"

func specOutput(t *testing.T, write func(*bytes.Buffer) error) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := write(&out); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestEveryVerbHasACurrentPage(t *testing.T) {
	spec := specOutput(t, func(b *bytes.Buffer) error { return writeSpecCLI(b) })
	var document struct {
		Commands []struct {
			Name string `json:"name"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(spec, &document); err != nil {
		t.Fatal(err)
	}
	pages, err := docgen.CommandPages(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range document.Commands {
		if _, err := os.Stat(filepath.Join(docsDir, "commands", command.Name+".md")); err != nil {
			t.Errorf("verb %s has no page: %v", command.Name, err)
		}
	}
	for name, want := range pages {
		got, err := os.ReadFile(filepath.Join(docsDir, "commands", name))
		if err != nil || string(got) != want {
			t.Errorf("docs/commands/%s is out of date (run make docs): %v", name, err)
		}
	}
}

func TestConfigPageIsCurrent(t *testing.T) {
	want, err := docgen.ConfigPage(
		specOutput(t, func(b *bytes.Buffer) error { return writeSpecDescriptor(b) }),
		string(specOutput(t, func(b *bytes.Buffer) error { return writeSpecProto(b) })),
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(docsDir, "config.md"))
	if err != nil || string(got) != want {
		t.Errorf("docs/config.md is out of date (run make docs): %v", err)
	}
}

func specVerbs(t *testing.T) map[string]bool {
	t.Helper()
	var document struct {
		Commands []struct {
			Name string `json:"name"`
		} `json:"commands"`
	}
	spec := specOutput(t, func(b *bytes.Buffer) error { return writeSpecCLI(b) })
	if err := json.Unmarshal(spec, &document); err != nil {
		t.Fatal(err)
	}
	verbs := make(map[string]bool, len(document.Commands))
	for _, command := range document.Commands {
		verbs[command.Name] = true
	}
	return verbs
}

var readmeVerbRow = regexp.MustCompile("(?m)^\\| `([a-z][a-z-]*)")

// The README's table is the one list of commands a new reader sees.
func TestReadmeListsEveryVerb(t *testing.T) {
	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, found := strings.Cut(string(data), "## The herdsman CLI\n")
	if !found {
		t.Fatal("the README has no command section")
	}
	section, _, found = strings.Cut(section, "### The daemon\n")
	if !found {
		t.Fatal("the command section has no end")
	}
	verbs := specVerbs(t)
	listed := map[string]bool{}
	for _, match := range readmeVerbRow.FindAllStringSubmatch(section, -1) {
		listed[match[1]] = true
		if !verbs[match[1]] {
			t.Errorf("the README lists %s, which is no command", match[1])
		}
	}
	for verb := range verbs {
		if !listed[verb] {
			t.Errorf("the README's command table lacks %s", verb)
		}
	}
}

var skillCommand = regexp.MustCompile("`herdsman ([a-z][a-z-]*)")

// skillCommands counts the commands a skill text names and returns the ones
// that are not verbs.
func skillCommands(text string, verbs map[string]bool) (int, []string) {
	matches := skillCommand.FindAllStringSubmatch(text, -1)
	var unknown []string
	for _, match := range matches {
		if !verbs[match[1]] {
			unknown = append(unknown, match[1])
		}
	}
	return len(matches), unknown
}

func TestSkillCommandsCatchesAnUnknownVerb(t *testing.T) {
	text := "Run `herdsman wait`, then `herdsman nosuchverb --flag`. Not `/herdsman run`."
	found, unknown := skillCommands(text, map[string]bool{"wait": true})
	if found != 2 || len(unknown) != 1 || unknown[0] != "nosuchverb" {
		t.Errorf("found %d, unknown %v", found, unknown)
	}
}

// A skill that names a command the binary lacks sends an agent to a usage error
// in the middle of a route.
func TestSkillNamesOnlyRealCommands(t *testing.T) {
	files, err := filepath.Glob("../../skills/herdsman/references/*.md")
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, "../../skills/herdsman/SKILL.md")
	verbs := specVerbs(t)
	seen := 0
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		found, unknown := skillCommands(string(data), verbs)
		seen += found
		for _, name := range unknown {
			t.Errorf("%s names herdsman %s, which is no command", filepath.Base(file), name)
		}
	}
	if seen == 0 {
		t.Fatal("found no commands to check")
	}
}
