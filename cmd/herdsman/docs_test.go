package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
