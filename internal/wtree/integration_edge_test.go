package wtree_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon"
	"github.com/arafathusayn/herdsman/internal/landing/ghtest"
)

func TestNewWarnsAboutAPromptWithNoAgent(t *testing.T) {
	w := newWorld(t)
	resp, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "t", Prompt: "do it"})
	if err != nil || len(resp.Warnings) != 1 || !strings.Contains(resp.Warnings[0], "prompt") {
		t.Fatalf("%+v %v", resp, err)
	}
}

func TestGoReadsTheRepositoryFromOrigin(t *testing.T) {
	w := newWorld(t)
	sha := pullRequest(t, w, 5, "from-origin")
	fake := ghtest.New(t, "acme/repo")
	fake.AddPR(ghtest.PR{Number: 5, State: "open", HeadRef: "from-origin", HeadSHA: sha, BaseRef: "main"})
	w.service.GH = fake.Bin()
	// The rewrite lets git keep reading the local bare origin.
	git(t, w.repo, "config", "url."+w.origin+".insteadOf", "https://github.com/acme/repo.git")
	git(t, w.repo, "remote", "set-url", "origin", "https://github.com/acme/repo.git")
	resp, err := w.service.Go(context.Background(), daemon.WorktreeGoRequest{Dir: w.repo, Selector: "https://github.com/acme/repo/pull/5"})
	if err != nil || resp.Task != "pr-5" {
		t.Fatalf("%+v %v", resp, err)
	}
}

func TestStatuslineShowsDivergence(t *testing.T) {
	w := newWorld(t)
	resp := w.create(t, "t")
	git(t, resp.Path, "config", "user.name", "t")
	git(t, resp.Path, "config", "user.email", "t@t")
	git(t, resp.Path, "commit", "-q", "--allow-empty", "-m", "ahead")
	git(t, w.repo, "commit", "-q", "--allow-empty", "-m", "behind")
	list, err := w.service.List(context.Background(), daemon.WorktreeListRequest{Dir: resp.Path, Statusline: true})
	if err != nil || list.Statusline != "feat/t ↑1 ↓1" {
		t.Fatalf("%q %v", list.Statusline, err)
	}
}

func TestPackageEntryUsesTheCallersEnvironment(t *testing.T) {
	w := newWorld(t)
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(w.root, "none.sock"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(w.root, "xdg"))
	w.config(t, map[string]any{"pre-commit": "true"})
	request, _ := json.Marshal(daemon.WorktreeHookRequest{Dir: w.repo, Type: "pre-commit", Approve: true})
	if _, err := daemon.WorktreeCall(context.Background(), "Hook", request); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.root, "xdg", "herdsman", "approvals.json")); err != nil {
		t.Fatal(err)
	}
}

func TestHookNeedsANameToRun(t *testing.T) {
	w := newWorld(t)
	if _, err := w.service.Hook(context.Background(), daemon.WorktreeHookRequest{Dir: w.repo}); err == nil {
		t.Fatal("ran no hook")
	}
}
