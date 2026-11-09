package wtree_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon"
	"github.com/arafathusayn/herdsman/internal/landing/ghtest"
)

// restrict puts the repository under the restricted profile.
func (w *world) restrict(t *testing.T, roots ...string) {
	t.Helper()
	body := "[schema]\nversion = \"1.0.0\"\n[policy]\nprofile = \"restricted\"\n"
	if len(roots) > 0 {
		body += "roots = [\"" + strings.Join(roots, `", "`) + "\"]\n"
	}
	if err := os.WriteFile(filepath.Join(w.repo, "herdsman.config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRestrictedNewMakesAnIndependentClone(t *testing.T) {
	w := newWorld(t)
	w.restrict(t)
	resp := w.create(t, "own-copy")
	info, err := os.Stat(filepath.Join(resp.Path, ".git"))
	if err != nil || !info.IsDir() {
		t.Fatalf("%s is not a repository of its own: %v", resp.Path, err)
	}
	if _, err := os.Stat(filepath.Join(resp.Path, ".git", "objects", "info", "alternates")); !os.IsNotExist(err) {
		t.Fatalf("the copy borrows objects: %v", err)
	}
	if got := git(t, resp.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat/own-copy" {
		t.Fatalf("branch = %q", got)
	}
	if listed := git(t, w.repo, "worktree", "list"); strings.Contains(listed, resp.Path) {
		t.Fatalf("a linked worktree was made:\n%s", listed)
	}
}

func TestRestrictedCloneShowsInListAndGo(t *testing.T) {
	w := newWorld(t)
	w.restrict(t)
	resp := w.create(t, "seen")
	list, err := w.service.List(context.Background(), daemon.WorktreeListRequest{Dir: w.repo})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range list.Rows {
		found = found || (row.Task == "seen" && row.Branch == "feat/seen" && row.Path == resp.Path)
	}
	if !found {
		t.Fatalf("%+v", list.Rows)
	}
	target, err := w.service.Go(context.Background(), daemon.WorktreeGoRequest{Dir: w.repo, Selector: "seen"})
	if err != nil || target.Path != resp.Path {
		t.Fatalf("%+v %v", target, err)
	}
}

func TestRemovedCloneLeavesTheList(t *testing.T) {
	w := newWorld(t)
	w.restrict(t)
	resp := w.create(t, "gone")
	if err := os.RemoveAll(resp.Path); err != nil {
		t.Fatal(err)
	}
	list, err := w.service.List(context.Background(), daemon.WorktreeListRequest{Dir: w.repo})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range list.Rows {
		if row.Task == "gone" {
			t.Fatalf("%+v", row)
		}
	}
}

func TestRestrictedLaunchLineIsConfined(t *testing.T) {
	w := newWorld(t)
	w.restrict(t)
	resp, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "held", Agent: "codex --model fast"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"'--cd' '" + resp.Path + "'", `sandbox_mode="workspace-write"`, filepath.Join(resp.Path, ".git"), "'--model' 'fast'"} {
		if !strings.Contains(resp.Launch, want) {
			t.Errorf("%q not in %s", want, resp.Launch)
		}
	}
}

func TestRestrictedNewRefusesAnAgentItCannotConfine(t *testing.T) {
	for _, agent := range []string{"claude", "codex --yolo", "codex --sandbox danger-full-access", "CODEX_HOME=/tmp/x codex", "make setup && codex"} {
		w := newWorld(t)
		w.restrict(t)
		_, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "refused", Agent: agent})
		if err == nil {
			t.Errorf("%q was admitted", agent)
		}
		if _, statErr := os.Stat(filepath.Join(filepath.Dir(w.repo), "repo.worktrees", "refused")); !os.IsNotExist(statErr) {
			t.Errorf("%q: a clone was made before the refusal", agent)
		}
	}
}

func TestOnlyAnIntegratorGetsThePolicyRoots(t *testing.T) {
	w := newWorld(t)
	shared := filepath.Join(w.root, "shared")
	if err := os.Mkdir(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	w.restrict(t, shared)
	implementer, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "impl", Agent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(implementer.Launch, shared) {
		t.Fatalf("implementer was granted the root: %s", implementer.Launch)
	}
	integrator, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "join", Agent: "codex", Integrator: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(integrator.Launch, shared) {
		t.Fatalf("integrator lacks the root: %s", integrator.Launch)
	}
}

func TestPolicyRootOverTheSourceRepositoryIsRefused(t *testing.T) {
	w := newWorld(t)
	w.restrict(t, w.repo)
	_, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "wide", Agent: "codex", Integrator: true})
	if err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("err = %v", err)
	}
}

func TestIntegratorOutsideRestrictedWarns(t *testing.T) {
	w := newWorld(t)
	resp, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "plain", Agent: "claude", Integrator: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(resp.Warnings, "\n"), "integrator") || resp.Launch != "cd "+resp.Path+" && claude" {
		t.Fatalf("%+v", resp)
	}
}

func TestBrokenPolicyStopsTheTask(t *testing.T) {
	w := newWorld(t)
	if err := os.WriteFile(filepath.Join(w.repo, "herdsman.config.toml"), []byte("[broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "stopped"}); err == nil {
		t.Fatal("an unreadable configuration let a task through")
	}
}

func TestRestrictedGoPullRequestMakesAClone(t *testing.T) {
	w := newWorld(t)
	w.restrict(t)
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
	if info, err := os.Stat(filepath.Join(resp.Path, ".git")); err != nil || !info.IsDir() {
		t.Fatalf("linked worktree at %s: %v", resp.Path, err)
	}
	if got := git(t, resp.Path, "rev-parse", "HEAD"); got != sha {
		t.Fatalf("%s, want %s", got, sha)
	}
}

// TestLaunchLineStartsTheAgentConfined runs the printed line through a shell,
// as a person would, with a stand-in that records what the agent received.
func TestLaunchLineStartsTheAgentConfined(t *testing.T) {
	w := newWorld(t)
	w.restrict(t)
	bin := filepath.Join(w.root, "bin")
	record := filepath.Join(w.root, "record")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := "#!/bin/sh\n{ pwd; for a in \"$@\"; do printf '%s\\n' \"$a\"; done; } > " + record + "\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	resp, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "ran", Agent: "codex --model fast"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", resp.Launch)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(data)), "\n")
	want := []string{resp.Path, "--cd", resp.Path, "-c", `sandbox_mode="workspace-write"`, "-c",
		`sandbox_workspace_write.writable_roots=["` + filepath.Join(resp.Path, ".git") + `"]`, "--model", "fast"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}
