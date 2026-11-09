package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestManPagesComeFromTheSpecNotTheCommandTable(t *testing.T) {
	doc := cliSpecDoc{Commands: []specEntry{
		{Name: "only-in-spec", Summary: "a command the table has never heard of", Help: "only-in-spec help\n",
			Subcommands: []specEntry{{Name: "child", Help: "child help\n"}}},
		{Name: "no-help", Summary: "summary as the body"},
	}}
	pages := manPagesFrom(doc)
	page := pages["herdsman-only-in-spec.1"]
	for _, want := range []string{"a command the table has never heard of", "only-in-spec help", "only-in-spec child:", "child help"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q:\n%s", want, page)
		}
	}
	if !strings.Contains(pages["herdsman-no-help.1"], "summary as the body") {
		t.Errorf("no fallback body:\n%s", pages["herdsman-no-help.1"])
	}
	if !strings.Contains(pages["herdsman.1"], ".BR herdsman-only-in-spec (1)") {
		t.Errorf("the main page does not point at it:\n%s", pages["herdsman.1"])
	}
	if len(pages) != 3 {
		t.Errorf("%d pages", len(pages))
	}
}

// The published spec is stored apart from the generator, so a command dropped
// from what generates the pages still fails here.
func TestManPagesMatchThePublishedSpec(t *testing.T) {
	data, err := os.ReadFile("../../sdk/conformance/cli/testdata/spec.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var published struct {
		Commands []struct {
			Name string `json:"name"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(data, &published); err != nil {
		t.Fatal(err)
	}
	if len(published.Commands) == 0 {
		t.Fatal("the published spec lists no commands")
	}
	pages := manPages()
	for _, c := range published.Commands {
		if _, ok := pages["herdsman-"+c.Name+".1"]; !ok {
			t.Errorf("%s is in the published spec and has no page", c.Name)
		}
	}
	if len(pages) != len(published.Commands)+1 {
		t.Errorf("%d pages for %d published commands and the program", len(pages), len(published.Commands))
	}
}
