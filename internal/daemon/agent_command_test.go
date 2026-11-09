package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestAgentCommandKeepsQuotedWords(t *testing.T) {
	for input, want := range map[string][]string{
		`claude`: {"claude"},
		`claude --append-system-prompt "follow two rules"`: {"claude", "--append-system-prompt", "follow two rules"},
		`codex -c 'a b' x\ y`: {"codex", "-c", "a b", "x y"},
		`pi "say \"hi\""`:     {"pi", `say "hi"`},
		`pi ""`:               {"pi", ""},
		"  ":                  nil,
	} {
		got, err := splitAgentCommand(input)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%q: %q %v", input, got, err)
		}
	}
}

func TestAgentCommandRefusesWhatItCannotRun(t *testing.T) {
	for _, input := range []string{`claude "open`, `claude 'open`, `claude \`, `FOO=bar claude`} {
		if got, err := splitAgentCommand(input); err == nil {
			t.Errorf("%q: %q", input, got)
		}
	}
}

func TestNewRefusesABadAgentBeforeMakingAnything(t *testing.T) {
	repo, environ := taskRepo(t)
	service := (&WorktreeService{}).forCaller(environ)
	if _, err := service.New(context.Background(), WorktreeNewRequest{Dir: repo, Task: "one", Agent: `claude "open`}); err == nil {
		t.Fatal("accepted")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(repo), "repo.worktrees", "one")); err == nil {
		t.Fatal("a worktree was made")
	}
}
