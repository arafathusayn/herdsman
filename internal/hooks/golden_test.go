package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/wtree"
)

var goldenVariables = []string{
	"base", "branch", "commit", "default_branch", "hook_name", "hook_type", "owner", "pr_number", "pr_url",
	"remote", "repo", "repo_path", "route", "short_commit", "target", "task", "type", "worktree_name", "worktree_path",
}

// Rendering is a contract with every configuration already written, so each
// hook point and variable is pinned.
func TestEveryHookRendersEveryVariable(t *testing.T) {
	values := map[string]string{}
	var template strings.Builder
	for _, name := range goldenVariables {
		if name != "hook_name" && name != "hook_type" {
			values[name] = "<" + name + ">"
		}
		fmt.Fprintf(&template, "%s={{ %s }}\n", name, name)
	}
	var got strings.Builder
	for _, hook := range Types {
		config, err := Parse(map[string]any{hook: map[string]any{"all": template.String()}})
		if err != nil {
			t.Fatal(err)
		}
		plan, err := config.Plan(hook, wtree.Vars{Values: values})
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&got, "# %s\n%s", hook, plan[0][0].Rendered)
	}
	path := filepath.Join("testdata", "render.golden")
	if os.Getenv("HERDSMAN_WRITE_GOLDEN") != "" {
		if err := os.WriteFile(path, []byte(got.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != string(want) {
		t.Fatalf("rendering changed:\n%s", got.String())
	}
}
