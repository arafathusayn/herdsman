package wtree_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/landing/ghtest"
)

func TestMain(m *testing.M) {
	ghtest.Main()
	os.Exit(m.Run())
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

type world struct {
	root, origin, repo string
	service            *daemon.WorktreeService
}

// newWorld builds a repository cloned from a bare origin, in a folder of its
// own so that the default worktree path stays inside the test's directory.
func newWorld(t *testing.T) *world {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	origin := filepath.Join(root, "origin.git")
	repo := filepath.Join(root, "work", "repo")
	os.MkdirAll(filepath.Dir(repo), 0o755)
	git(t, root, "init", "-q", "--bare", "-b", "main", origin)
	seed := filepath.Join(root, "seed")
	git(t, root, "init", "-q", "-b", "main", seed)
	os.WriteFile(filepath.Join(seed, "a.txt"), []byte("a\n"), 0o644)
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-q", "-m", "first")
	git(t, seed, "remote", "add", "origin", origin)
	git(t, seed, "push", "-q", "origin", "main")
	git(t, root, "clone", "-q", origin, repo)
	git(t, repo, "config", "user.name", "t")
	git(t, repo, "config", "user.email", "t@t")
	return &world{root: root, origin: origin, repo: repo, service: &daemon.WorktreeService{
		HerdrSocket: filepath.Join(root, "no-herdr.sock"),
		Approvals:   filepath.Join(root, "config", "approvals.json"),
		GH:          filepath.Join(root, "no-gh"),
	}}
}

func (w *world) config(t *testing.T, hooks map[string]any) {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"hooks": hooks})
	if err := os.WriteFile(filepath.Join(filepath.Dir(w.repo), "herdsman.config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (w *world) create(t *testing.T, task string) daemon.WorktreeNewResponse {
	t.Helper()
	resp, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: task})
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestNewBranchesFromTheFetchedDefaultBranch(t *testing.T) {
	w := newWorld(t)
	seed := filepath.Join(w.root, "seed")
	os.WriteFile(filepath.Join(seed, "b.txt"), []byte("b\n"), 0o644)
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-q", "-m", "second")
	git(t, seed, "push", "-q", "origin", "main")
	want := git(t, seed, "rev-parse", "HEAD")

	resp := w.create(t, "first-task")
	if resp.Branch != "feat/first-task" || resp.Base != "main" {
		t.Fatalf("%+v", resp)
	}
	if want2 := filepath.Join(w.root, "work", "repo.worktrees", "first-task"); resp.Path != want2 {
		t.Fatalf("%s, want %s", resp.Path, want2)
	}
	if got := git(t, resp.Path, "rev-parse", "HEAD"); got != want {
		t.Fatalf("started at %s, want the fetched %s", got, want)
	}
	if resp.Launch == "" || resp.Port == 0 {
		t.Fatalf("no launch line or port: %+v", resp)
	}
}

func TestNewFromAnExplicitBase(t *testing.T) {
	w := newWorld(t)
	git(t, w.repo, "branch", "side")
	git(t, w.repo, "commit", "-q", "--allow-empty", "-m", "on main only")
	resp, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "t", Base: "side"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := git(t, resp.Path, "rev-parse", "HEAD"), git(t, w.repo, "rev-parse", "side"); got != want {
		t.Fatalf("%s %s", got, want)
	}
}

func TestNewRefuses(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if _, err := w.service.New(ctx, daemon.WorktreeNewRequest{Dir: w.repo, Task: "Bad Name"}); err == nil {
		t.Error("bad task accepted")
	}
	if _, err := w.service.New(ctx, daemon.WorktreeNewRequest{Dir: w.repo, Task: "t", Base: "nope"}); err == nil {
		t.Error("missing base accepted")
	}
	if _, err := w.service.New(ctx, daemon.WorktreeNewRequest{Dir: "relative", Task: "t"}); err == nil {
		t.Error("relative folder accepted")
	}
}

func TestNewClobberRenamesAStaleFolder(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	stale := filepath.Join(w.root, "work", "repo.worktrees", "t")
	os.MkdirAll(stale, 0o755)
	os.WriteFile(filepath.Join(stale, "keep"), []byte("x"), 0o644)
	if _, err := w.service.New(ctx, daemon.WorktreeNewRequest{Dir: w.repo, Task: "t"}); err == nil || !strings.Contains(err.Error(), "--clobber") {
		t.Fatalf("%v", err)
	}
	if _, err := w.service.New(ctx, daemon.WorktreeNewRequest{Dir: w.repo, Task: "t", Clobber: true}); err != nil {
		t.Fatal(err)
	}
	backups, _ := filepath.Glob(stale + ".bak.*")
	if len(backups) != 1 {
		t.Fatalf("%v", backups)
	}
	if _, err := os.Stat(filepath.Join(backups[0], "keep")); err != nil {
		t.Fatal(err)
	}
}

func TestNewClobberNeverMovesALiveWorktree(t *testing.T) {
	w := newWorld(t)
	resp := w.create(t, "t")
	if _, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "t", Clobber: true}); err == nil {
		t.Fatal("clobbered a worktree")
	}
	if _, err := os.Stat(filepath.Join(resp.Path, ".git")); err != nil {
		t.Fatal(err)
	}
}

func TestNewRefusesUnapprovedHooksBeforeCreatingAnything(t *testing.T) {
	w := newWorld(t)
	w.config(t, map[string]any{"pre-start": "echo hi"})
	_, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "t"})
	if err == nil || !strings.Contains(err.Error(), "echo hi") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(w.root, "work", "repo.worktrees", "t")); err == nil {
		t.Fatal("created the tree")
	}
}

func approve(t *testing.T, w *world, hook string) {
	t.Helper()
	if _, err := w.service.Hook(context.Background(), daemon.WorktreeHookRequest{Dir: w.repo, Type: hook, Approve: true}); err != nil {
		t.Fatal(err)
	}
}

func TestNewRunsTheHooksInOrder(t *testing.T) {
	w := newWorld(t)
	w.config(t, map[string]any{"pre-start": "echo pre-{{ task }} > pre.txt", "post-start": "echo post > post.txt"})
	approve(t, w, "pre-start")
	approve(t, w, "post-start")
	resp := w.create(t, "t")
	data, _ := os.ReadFile(filepath.Join(resp.Path, "pre.txt"))
	if strings.TrimSpace(string(data)) != "pre-t" {
		t.Fatalf("%q", data)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(resp.Path, "post.txt")); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("post-start never ran")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestNewReportsAFailedHookAndKeepsTheTree(t *testing.T) {
	w := newWorld(t)
	w.config(t, map[string]any{"pre-start": "exit 4"})
	approve(t, w, "pre-start")
	_, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "t"})
	if err == nil || !strings.Contains(err.Error(), "repo.worktrees/t") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(w.root, "work", "repo.worktrees", "t", ".git")); err != nil {
		t.Fatal(err)
	}
}

func TestNewRunsDetectedSetupUnlessToldNotTo(t *testing.T) {
	w := newWorld(t)
	seed := filepath.Join(w.root, "seed")
	os.WriteFile(filepath.Join(seed, "uv.lock"), nil, 0o644)
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-q", "-m", "lock")
	git(t, seed, "push", "-q", "origin", "main")
	resp, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "off", NoSetup: true})
	if err != nil || len(resp.Setup) != 0 {
		t.Fatalf("%+v %v", resp, err)
	}
	bin := filepath.Join(w.root, "bin")
	os.MkdirAll(bin, 0o755)
	os.WriteFile(filepath.Join(bin, "uv"), []byte("#!/bin/sh\ntouch ran-setup\n"), 0o755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	resp = w.create(t, "on")
	if !slices.Equal(resp.Setup, []string{"uv sync"}) {
		t.Fatalf("%+v", resp)
	}
	if _, err := os.Stat(filepath.Join(resp.Path, "ran-setup")); err != nil {
		t.Fatal(err)
	}
}

func TestNewGoesThroughHerdrWhenReachable(t *testing.T) {
	w := newWorld(t)
	server := herdrtest.Start(t)
	w.service.HerdrSocket = server.Socket
	repo := w.repo
	var path string
	server.Handle("worktree.create", func(params map[string]any) (any, string) {
		path = params["path"].(string)
		cmd := exec.Command("git", "-C", repo, "worktree", "add", "-q", "-b", params["branch"].(string), path, params["base"].(string))
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, string(out)
		}
		return map[string]any{}, ""
	})
	server.Handle("pane.list", func(map[string]any) (any, string) {
		return map[string]any{"panes": []map[string]any{{"pane_id": "p1", "cwd": path}}}, ""
	})
	server.Handle("pane.send_input", func(map[string]any) (any, string) { return map[string]any{}, "" })
	resp := w.create(t, "t")
	if !resp.Herdr {
		t.Fatalf("%+v", resp)
	}
	if !slices.Contains(server.Methods(), "worktree.create") {
		t.Fatalf("%v", server.Methods())
	}
}

func TestNewFallsBackToGitWhenHerdrRefuses(t *testing.T) {
	w := newWorld(t)
	server := herdrtest.Start(t)
	w.service.HerdrSocket = server.Socket
	server.Handle("worktree.create", func(map[string]any) (any, string) { return nil, "unavailable" })
	resp := w.create(t, "t")
	if resp.Herdr || len(resp.Warnings) == 0 {
		t.Fatalf("%+v", resp)
	}
	if _, err := os.Stat(filepath.Join(resp.Path, ".git")); err != nil {
		t.Fatal(err)
	}
}

func TestNewReportsAnAgentItDidNotStart(t *testing.T) {
	w := newWorld(t)
	resp, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "t", Agent: "claude"})
	if err != nil || len(resp.Warnings) == 0 || !strings.Contains(resp.Launch, "claude") {
		t.Fatalf("%+v %v", resp, err)
	}
}

func row(t *testing.T, rows []daemon.WorktreeRow, task string) daemon.WorktreeRow {
	t.Helper()
	for _, r := range rows {
		if r.Task == task {
			return r
		}
	}
	t.Fatalf("no row %q in %+v", task, rows)
	return daemon.WorktreeRow{}
}

func TestListColumns(t *testing.T) {
	w := newWorld(t)
	resp := w.create(t, "t")
	os.WriteFile(filepath.Join(resp.Path, "new.txt"), nil, 0o644)
	git(t, resp.Path, "config", "user.name", "t")
	git(t, resp.Path, "config", "user.email", "t@t")
	git(t, resp.Path, "commit", "-q", "--allow-empty", "-m", "work on it")
	list, err := w.service.List(context.Background(), daemon.WorktreeListRequest{Dir: resp.Path})
	if err != nil {
		t.Fatal(err)
	}
	got := row(t, list.Rows, "t")
	if got.Branch != "feat/t" || got.Status != "?" || got.Ahead != 1 || got.Behind != 0 || got.Subject != "work on it" || got.Path != resp.Path {
		t.Fatalf("%+v", got)
	}
	if main := row(t, list.Rows, "repo"); !main.Main || !main.Default {
		t.Fatalf("%+v", main)
	}
}

func TestListAllAddsBranchesWithoutATree(t *testing.T) {
	w := newWorld(t)
	git(t, w.repo, "branch", "loose")
	ctx := context.Background()
	list, err := w.service.List(ctx, daemon.WorktreeListRequest{Dir: w.repo})
	if err != nil || len(list.Rows) != 1 {
		t.Fatalf("%+v %v", list, err)
	}
	list, err = w.service.List(ctx, daemon.WorktreeListRequest{Dir: w.repo, All: true})
	if err != nil || len(list.Rows) != 2 {
		t.Fatalf("%+v %v", list, err)
	}
	if got := list.Rows[1]; got.Branch != "loose" || got.Path != "" {
		t.Fatalf("%+v", got)
	}
}

func TestListStatusline(t *testing.T) {
	w := newWorld(t)
	resp := w.create(t, "t")
	os.WriteFile(filepath.Join(resp.Path, "new.txt"), nil, 0o644)
	list, err := w.service.List(context.Background(), daemon.WorktreeListRequest{Dir: filepath.Join(resp.Path), Statusline: true})
	if err != nil {
		t.Fatal(err)
	}
	if list.Statusline != "feat/t ?" {
		t.Fatalf("%q", list.Statusline)
	}
	if _, err := w.service.List(context.Background(), daemon.WorktreeListRequest{Dir: w.root, Statusline: true}); err == nil {
		t.Fatal("no repository, no error")
	}
}

func TestGoEverySelector(t *testing.T) {
	w := newWorld(t)
	first := w.create(t, "one")
	second := w.create(t, "two")
	ctx := context.Background()
	cases := []struct{ selector, dir, want string }{
		{"one", w.repo, first.Path},
		{"feat/two", w.repo, second.Path},
		{second.Path, w.repo, second.Path},
		{"^", first.Path, w.repo},
		{"@", first.Path, first.Path},
	}
	for _, c := range cases {
		resp, err := w.service.Go(ctx, daemon.WorktreeGoRequest{Dir: c.dir, Selector: c.selector})
		if err != nil || resp.Path != c.want {
			t.Errorf("%s: %+v %v", c.selector, resp, err)
		}
	}
	// The last jump left from one, so - returns there.
	if _, err := w.service.Go(ctx, daemon.WorktreeGoRequest{Dir: first.Path, Selector: "two"}); err != nil {
		t.Fatal(err)
	}
	resp, err := w.service.Go(ctx, daemon.WorktreeGoRequest{Dir: second.Path, Selector: "-"})
	if err != nil || resp.Path != first.Path {
		t.Fatalf("%+v %v", resp, err)
	}
	if _, err := w.service.Go(ctx, daemon.WorktreeGoRequest{Dir: w.repo, Selector: "nothing"}); err == nil {
		t.Fatal("found nothing")
	}
}

func TestGoRunsSwitchHooks(t *testing.T) {
	w := newWorld(t)
	first := w.create(t, "one")
	w.config(t, map[string]any{"pre-switch": "echo {{ task }} > switched.txt"})
	approve(t, w, "pre-switch")
	if _, err := w.service.Go(context.Background(), daemon.WorktreeGoRequest{Dir: w.repo, Selector: "one"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(first.Path, "switched.txt"))
	if strings.TrimSpace(string(data)) != "one" {
		t.Fatalf("%q", data)
	}
}

func TestGoFocusesThePaneInHerdr(t *testing.T) {
	w := newWorld(t)
	first := w.create(t, "one")
	server := herdrtest.Start(t)
	w.service.HerdrSocket = server.Socket
	server.Handle("pane.list", func(map[string]any) (any, string) {
		return map[string]any{"panes": []map[string]any{{"pane_id": "p", "tab_id": "tab-9", "cwd": first.Path}}}, ""
	})
	server.Handle("tab.focus", func(map[string]any) (any, string) { return map[string]any{}, "" })
	resp, err := w.service.Go(context.Background(), daemon.WorktreeGoRequest{Dir: w.repo, Selector: "one", Focus: true})
	if err != nil || !resp.Focused {
		t.Fatalf("%+v %v", resp, err)
	}
}

// pullRequest publishes a branch as refs/pull/<n>/head on the origin, then
// removes the branch, as for a request whose head lives only there.
func pullRequest(t *testing.T, w *world, number int, branch string) string {
	t.Helper()
	seed := filepath.Join(w.root, "seed")
	git(t, seed, "checkout", "-q", "-b", branch)
	git(t, seed, "commit", "-q", "--allow-empty", "-m", "pull request work")
	sha := git(t, seed, "rev-parse", "HEAD")
	git(t, seed, "push", "-q", "origin", fmt.Sprintf("HEAD:refs/pull/%d/head", number))
	git(t, seed, "checkout", "-q", "main")
	return sha
}

func TestGoPullRequestAgainstTheFakeGH(t *testing.T) {
	w := newWorld(t)
	sha := pullRequest(t, w, 7, "fix/thing")
	git(t, w.repo, "remote", "set-url", "--push", "origin", "https://github.com/acme/repo.git")
	fake := ghtest.New(t, "acme/repo")
	fake.AddPR(ghtest.PR{Number: 7, State: "open", HeadRef: "fix/thing", HeadSHA: sha, BaseRef: "main"})
	w.service.GH = fake.Bin()
	w.service.Repo = "acme/repo"

	resp, err := w.service.Go(context.Background(), daemon.WorktreeGoRequest{Dir: w.repo, Selector: "pr:7"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Task != "pr-7" || resp.Branch != "fix/thing" || !resp.Created {
		t.Fatalf("%+v", resp)
	}
	if got := git(t, resp.Path, "rev-parse", "HEAD"); got != sha {
		t.Fatalf("%s, want %s", got, sha)
	}
	again, err := w.service.Go(context.Background(), daemon.WorktreeGoRequest{Dir: w.repo, Selector: "pr:7"})
	if err != nil || again.Created || again.Path != resp.Path {
		t.Fatalf("%+v %v", again, err)
	}
}

func fakeGH(t *testing.T, w *world, body string) string {
	t.Helper()
	path := filepath.Join(w.root, "fake-gh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\ncat <<'JSON'\n"+body+"\nJSON\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGoForkPullRequestSetsThePushRemote(t *testing.T) {
	w := newWorld(t)
	sha := pullRequest(t, w, 8, "patch")
	w.service.Repo = "acme/repo"
	w.service.GH = fakeGH(t, w, fmt.Sprintf(`{"number":8,"state":"open","base":{"ref":"main"},"head":{"ref":"patch","sha":%q,"repo":{"full_name":"someone/repo","clone_url":"https://example.test/someone/repo.git","owner":{"login":"someone"}}}}`, sha))
	resp, err := w.service.Go(context.Background(), daemon.WorktreeGoRequest{Dir: w.repo, Selector: "pr:8"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.PushRemote != "someone" {
		t.Fatalf("%+v", resp)
	}
	if got := git(t, w.repo, "config", "branch.patch.pushRemote"); got != "someone" {
		t.Fatal(got)
	}
	if got := git(t, w.repo, "remote", "get-url", "someone"); !strings.Contains(got, "someone/repo") {
		t.Fatal(got)
	}
}

func TestGoRefusesRequestsItCannotServe(t *testing.T) {
	w := newWorld(t)
	w.service.Repo = "acme/repo"
	ctx := context.Background()
	if _, err := w.service.Go(ctx, daemon.WorktreeGoRequest{Dir: w.repo, Selector: "mr:3"}); err == nil {
		t.Error("merge request accepted")
	}
	if _, err := w.service.Go(ctx, daemon.WorktreeGoRequest{Dir: w.repo, Selector: "https://github.com/other/repo/pull/3"}); err == nil {
		t.Error("another repository accepted")
	}
	if _, err := w.service.Go(ctx, daemon.WorktreeGoRequest{Dir: w.repo, Selector: "pr:3"}); err == nil {
		t.Error("missing gh accepted")
	}
	w.service.GH = fakeGH(t, w, "not json")
	if _, err := w.service.Go(ctx, daemon.WorktreeGoRequest{Dir: w.repo, Selector: "pr:3"}); err == nil {
		t.Error("garbage accepted")
	}
}

func TestHookDryRunApproveAndRun(t *testing.T) {
	w := newWorld(t)
	first := w.create(t, "one")
	w.config(t, map[string]any{"pre-commit": "echo {{ task }} > hook.txt"})
	ctx := context.Background()
	request := daemon.WorktreeHookRequest{Dir: first.Path, Type: "pre-commit"}

	if _, err := w.service.Hook(ctx, request); err == nil {
		t.Fatal("ran unapproved")
	}
	dry := request
	dry.DryRun = true
	plan, err := w.service.Hook(ctx, dry)
	if err != nil || len(plan.Commands) != 1 || plan.Commands[0].Rendered != "echo one > hook.txt" || plan.Commands[0].Approved {
		t.Fatalf("%+v %v", plan, err)
	}
	if _, err := os.Stat(filepath.Join(first.Path, "hook.txt")); err == nil {
		t.Fatal("dry run ran it")
	}
	yes := request
	yes.Approve = true
	if approved, err := w.service.Hook(ctx, yes); err != nil || !slices.Equal(approved.Approved, []string{"echo {{ task }} > hook.txt"}) {
		t.Fatalf("%+v %v", approved, err)
	}
	result, err := w.service.Hook(ctx, request)
	if err != nil || !result.OK {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(first.Path, "hook.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestHookFromAnotherTask(t *testing.T) {
	w := newWorld(t)
	w.create(t, "one")
	w.config(t, map[string]any{"pre-commit": "echo {{ task }}"})
	plan, err := w.service.Hook(context.Background(), daemon.WorktreeHookRequest{Dir: w.repo, Type: "pre-commit", From: "one", DryRun: true})
	if err != nil || plan.Commands[0].Rendered != "echo one" {
		t.Fatalf("%+v %v", plan, err)
	}
}

func TestHookShowListsEveryConfiguredHook(t *testing.T) {
	w := newWorld(t)
	w.config(t, map[string]any{"pre-commit": "a", "post-merge": "b"})
	resp, err := w.service.Hook(context.Background(), daemon.WorktreeHookRequest{Dir: w.repo, DryRun: true})
	if err != nil || len(resp.Commands) != 2 || resp.Commands[0].Hook != "pre-commit" {
		t.Fatalf("%+v %v", resp, err)
	}
}

func TestHookRefusesWhatItCannotRead(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if _, err := w.service.Hook(ctx, daemon.WorktreeHookRequest{Dir: w.repo, Type: "pre-launch"}); err == nil {
		t.Error("unknown hook accepted")
	}
	parent := filepath.Dir(w.repo)
	os.WriteFile(filepath.Join(parent, "herdsman.config.json"), []byte("{"), 0o644)
	if _, err := w.service.Hook(ctx, daemon.WorktreeHookRequest{Dir: w.repo, Type: "pre-commit"}); err == nil {
		t.Error("corrupt config accepted")
	}
	os.Remove(filepath.Join(parent, "herdsman.config.json"))
	os.WriteFile(filepath.Join(parent, "herdsman.config.toml"), []byte("[hooks\n"), 0o644)
	if _, err := w.service.Hook(ctx, daemon.WorktreeHookRequest{Dir: w.repo, Type: "pre-commit"}); err == nil {
		t.Error("corrupt toml accepted")
	}
}

func TestCallDispatchesByMethodName(t *testing.T) {
	w := newWorld(t)
	req, _ := json.Marshal(daemon.WorktreeListRequest{Dir: w.repo})
	out, err := w.service.Call(context.Background(), "List", req)
	if err != nil {
		t.Fatal(err)
	}
	var resp daemon.WorktreeListResponse
	if err := json.Unmarshal(out, &resp); err != nil || len(resp.Rows) != 1 {
		t.Fatalf("%s %v", out, err)
	}
	for method, body := range map[string]string{"Nope": "{}", "List": "{", "New": "{", "Go": "{", "Hook": "{"} {
		if _, err := w.service.Call(context.Background(), method, []byte(body)); err == nil {
			t.Errorf("%s %s accepted", method, body)
		}
	}
}

func TestNewFromAPullRequest(t *testing.T) {
	w := newWorld(t)
	sha := pullRequest(t, w, 9, "topic")
	fake := ghtest.New(t, "acme/repo")
	fake.AddPR(ghtest.PR{Number: 9, State: "open", HeadRef: "topic", HeadSHA: sha, BaseRef: "main"})
	w.service.GH, w.service.Repo = fake.Bin(), "acme/repo"
	resp, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "review-it", From: "pr:9"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Branch != "topic" || resp.Base != "main" || git(t, resp.Path, "rev-parse", "HEAD") != sha {
		t.Fatalf("%+v", resp)
	}
	if _, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "bad", From: "main"}); err == nil {
		t.Fatal("a non-request source accepted")
	}
}
