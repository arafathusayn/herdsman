package main

import (
	"context"
	"testing"

	pluginv1 "github.com/arafathusayn/herdsman/sdk/plugin/v1"
)

func TestGenerateByKind(t *testing.T) {
	for kind, want := range map[string]string{"commit": "chore: Add a File", "branch": "add-a-file", "summary": "Add a File"} {
		got, err := generator{}.Generate(context.Background(), &pluginv1.GenerateRequest{Kind: kind, Prompt: "Add a File\nmore"})
		if err != nil || got.GetText() != want {
			t.Errorf("%s: %q %v", kind, got.GetText(), err)
		}
	}
	if _, err := (generator{}).Generate(context.Background(), &pluginv1.GenerateRequest{Kind: "x"}); err == nil {
		t.Fatal("no error")
	}
}
