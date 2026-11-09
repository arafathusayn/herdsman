package wtree_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/landing/ghtest"
)

func writeProjectFile(t *testing.T, w *world, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(filepath.Dir(w.repo), name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func approveAll(t *testing.T, w *world) {
	t.Helper()
	if _, err := w.service.Hook(context.Background(), daemon.WorktreeHookRequest{Dir: w.repo, Approve: true}); err != nil {
		t.Fatal(err)
	}
}

func TestNewUsesTheLoadedConfiguration(t *testing.T) {
	w := newWorld(t)
	writeProjectFile(t, w, "herdsman.config.toml", `
[hooks]
pre_start = "touch from-toml"
[worktree]
path = "{{ repo_path }}/../elsewhere/{{ task }}"
[branch]
name = "work/{{ task }}"
[setup]
install = "touch installed"
`)
	if _, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "early"}); err == nil {
		t.Fatal("unapproved commands ran")
	}
	approveAll(t, w)
	resp := w.create(t, "one")
	if resp.Branch != "work/one" || filepath.Base(filepath.Dir(resp.Path)) != "elsewhere" {
		t.Fatalf("%+v", resp)
	}
	for _, file := range []string{"from-toml", "installed"} {
		if _, err := os.Stat(filepath.Join(resp.Path, file)); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(resp.Setup, []string{"touch installed"}) {
		t.Fatalf("%+v", resp)
	}
}

func TestUserFileHooksApplyAndTheProjectFileWins(t *testing.T) {
	w := newWorld(t)
	w.service.ConfigDir = filepath.Join(w.root, "user")
	os.MkdirAll(w.service.ConfigDir, 0o755)
	os.WriteFile(filepath.Join(w.service.ConfigDir, "config.toml"), []byte("[hooks]\npre_start = \"touch from-user\"\npost_commit = \"true\"\n"), 0o644)
	approveAll(t, w)
	resp := w.create(t, "one")
	if _, err := os.Stat(filepath.Join(resp.Path, "from-user")); err != nil {
		t.Fatal(err)
	}
	writeProjectFile(t, w, "herdsman.config.toml", "[hooks]\npre_start = \"touch from-project\"\n")
	approveAll(t, w)
	second := w.create(t, "two")
	if _, err := os.Stat(filepath.Join(second.Path, "from-project")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(second.Path, "from-user")); err == nil {
		t.Fatal("the user file beat the project file")
	}
}

func TestHookFilesWithTimeoutsKeepWorking(t *testing.T) {
	w := newWorld(t)
	w.config(t, map[string]any{"pre-start": map[string]any{"slow": map[string]any{"command": "touch kept", "timeout": "30s"}}})
	approveAll(t, w)
	if _, err := os.Stat(filepath.Join(w.create(t, "one").Path, "kept")); err != nil {
		t.Fatal(err)
	}
}

func TestABrokenConfigurationStopsNew(t *testing.T) {
	w := newWorld(t)
	writeProjectFile(t, w, "herdsman.config.toml", "[hooks\n")
	if _, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "one"}); err == nil {
		t.Fatal("accepted")
	}
}

// herdrWorld answers worktree creation with real git and lists one pane there.
func herdrWorld(t *testing.T, w *world) (*herdrtest.Server, *[]string) {
	t.Helper()
	server := herdrtest.Start(t)
	w.service.HerdrSocket = server.Socket
	var typed []string
	var path string
	add := func(args ...string) string {
		return git(t, w.repo, append([]string{"worktree", "add", "-q"}, args...)...)
	}
	server.Handle("worktree.create", func(params map[string]any) (any, string) {
		path = params["path"].(string)
		add("-b", params["branch"].(string), path, params["base"].(string))
		return map[string]any{}, ""
	})
	server.Handle("worktree.open", func(params map[string]any) (any, string) {
		path = params["path"].(string)
		return map[string]any{}, ""
	})
	server.Handle("pane.list", func(map[string]any) (any, string) {
		return map[string]any{"panes": []map[string]any{{"pane_id": "p1", "tab_id": "t1", "workspace_id": "w1", "cwd": path}}}, ""
	})
	server.Handle("pane.send_input", func(params map[string]any) (any, string) {
		typed = append(typed, params["text"].(string))
		return map[string]any{}, ""
	})
	return server, &typed
}

func TestNewFromAPullRequestGoesThroughHerdr(t *testing.T) {
	w := newWorld(t)
	sha := pullRequest(t, w, 9, "topic")
	fake := ghtest.New(t, "acme/repo")
	fake.AddPR(ghtest.PR{Number: 9, State: "open", HeadRef: "topic", HeadSHA: sha, BaseRef: "main"})
	w.service.GH, w.service.Repo = fake.Bin(), "acme/repo"
	server, _ := herdrWorld(t, w)
	resp, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "review-it", From: "pr:9"})
	if err != nil || !resp.Herdr || git(t, resp.Path, "rev-parse", "HEAD") != sha {
		t.Fatalf("%+v %v", resp, err)
	}
	if !slices.Contains(server.Methods(), "worktree.create") {
		t.Fatal(server.Methods())
	}
}

func TestNewOnABranchThatExistsOpensItInHerdr(t *testing.T) {
	w := newWorld(t)
	sha := pullRequest(t, w, 9, "topic")
	git(t, w.repo, "fetch", "-q", "origin", "refs/pull/9/head:refs/heads/topic")
	w.service.Repo = "acme/repo"
	w.service.GH = fakeGH(t, w, `{"base":{"ref":"main"},"head":{"ref":"topic","sha":"`+sha+`","repo":{"full_name":"acme/repo"}}}`)
	server, _ := herdrWorld(t, w)
	resp, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "again", From: "pr:9"})
	if err != nil || !resp.Herdr || !slices.Contains(server.Methods(), "worktree.open") {
		t.Fatalf("%+v %v %v", resp, err, server.Methods())
	}
}

func TestNewSetsTheTaskEnvironmentInTheWorkspace(t *testing.T) {
	w := newWorld(t)
	_, typed := herdrWorld(t, w)
	w.create(t, "one")
	if len(*typed) != 1 || !strings.Contains((*typed)[0], "HERDSMAN_TASK='one'") {
		t.Fatalf("%q", *typed)
	}
}

func TestNewStartsTheAgentAndSendsThePrompt(t *testing.T) {
	w := newWorld(t)
	server, _ := herdrWorld(t, w)
	var started, prompted map[string]any
	server.Handle("agent.start", func(params map[string]any) (any, string) {
		started = params
		return map[string]any{"agent": map[string]any{"name": params["name"]}}, ""
	})
	server.Handle("agent.prompt", func(params map[string]any) (any, string) {
		prompted = params
		return map[string]any{}, ""
	})
	resp, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "one", Agent: "claude --model x", Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.AgentName != "one" || started["kind"] != "claude" || started["pane_id"] != "p1" {
		t.Fatalf("%+v %v", resp, started)
	}
	if args, _ := json.Marshal(started["args"]); string(args) != `["--model","x"]` {
		t.Fatal(string(args))
	}
	if prompted["target"] != "one" || prompted["text"] != "do it" {
		t.Fatal(prompted)
	}
}

func TestNewReportsAnAgentThatCouldNotStart(t *testing.T) {
	w := newWorld(t)
	server, _ := herdrWorld(t, w)
	server.Handle("agent.start", func(map[string]any) (any, string) { return nil, "agent_start_failed" })
	resp, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "one", Agent: "claude"})
	if err == nil || !strings.Contains(err.Error(), "agent") || resp.Path == "" {
		t.Fatalf("%+v %v", resp, err)
	}
}

func TestNewFailsWhenTheWorkspaceCannotGetTheEnvironment(t *testing.T) {
	w := newWorld(t)
	server, _ := herdrWorld(t, w)
	server.Handle("pane.send_input", func(map[string]any) (any, string) { return nil, "write_failed" })
	resp, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "one"})
	if err == nil || resp.Path == "" {
		t.Fatalf("%+v %v", resp, err)
	}
}

func TestNewFailsWhenTheWorkspaceHasNoPane(t *testing.T) {
	w := newWorld(t)
	server, _ := herdrWorld(t, w)
	server.Handle("pane.list", func(map[string]any) (any, string) { return map[string]any{"panes": []map[string]any{}}, "" })
	if resp, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "one"}); err == nil {
		t.Fatalf("%+v", resp)
	}
}
