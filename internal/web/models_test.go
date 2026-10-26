package web

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestHarnessModels(t *testing.T) {
	failing := func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("not installed") }
	offline := harnessModels(context.Background(), failing)
	if len(offline) == 0 {
		t.Fatal("the last-known catalogs list no model")
	}
	for _, k := range []CatalogKind{
		fromOutput(parseClaudeHelp, "claude-help.txt", func() ([]byte, error) { return nil, errors.New("x") }),
		fromOutput(parseCodexModels, "codex-debug-models.json", func() ([]byte, error) { return nil, errors.New("x") }),
	} {
		for _, m := range k.Models {
			if !slices.Contains(offline, m.ID) {
				t.Errorf("%s missing from %v", m.ID, offline)
			}
		}
	}
	live := func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "codex" {
			return []byte(`{"models":[{"slug":"model-a","display_name":"Model A","default_reasoning_level":"effortx","supported_reasoning_levels":[{"effort":"effortx"}],"visibility":"list"}]}`), nil
		}
		return nil, errors.New("not installed")
	}
	if got := harnessModels(context.Background(), live); !slices.Contains(got, "model-a") {
		t.Errorf("a live catalog's model is missing: %v", got)
	}
}
