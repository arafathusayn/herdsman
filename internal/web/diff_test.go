package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/route"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// Each task with a worktree or a branch gets its own diff, once; the project
// checkout comes last.
func TestRouteDiffs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	proj := t.TempDir()
	gitIn(t, proj, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(proj, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, proj, "add", ".")
	gitIn(t, proj, "commit", "-q", "-m", "base")
	gitIn(t, proj, "worktree", "add", "-q", "-b", "feat/auth", ".herdsman/worktrees/auth")
	gitIn(t, proj, "branch", "feat/docs")
	if err := os.WriteFile(filepath.Join(proj, ".herdsman/worktrees/auth/a.txt"), []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &route.Route{Dir: filepath.Join(proj, ".herdsman", "r-2026-10-24"), Tasks: []route.Task{
		{Task: "auth", Agent: "im-auth", Branch: "feat/auth", Worktree: "<repo>/.herdsman/worktrees/auth"},
		{Task: "auth again", Worktree: ".herdsman/worktrees/auth"},
		{Task: "docs", Branch: "feat/docs"},
		{Task: "review", Agent: "rv-codex"},
		{Task: "lost", Worktree: "../nowhere"},
	}}
	got := routeDiffs(context.Background(), r, nil)
	var ids []string
	for _, s := range got {
		ids = append(ids, s.ID)
	}
	if len(got) != 4 || ids[0] != "task:auth" || ids[1] != "task:docs" || ids[2] != "task:lost" || ids[3] != "project" {
		t.Fatalf("sources = %v", ids)
	}
	auth := got[0]
	if auth.Error != "" || auth.Diff == nil || auth.Diff.Head != "feat/auth" || len(auth.Diff.Files) != 1 || auth.Diff.Files[0].Add != 1 {
		t.Fatalf("auth = %+v %+v", auth, auth.Diff)
	}
	if got[1].Error != "" || got[1].Diff == nil || len(got[1].Diff.Files) != 0 {
		t.Fatalf("docs = %+v", got[1])
	}
	if got[2].Error == "" {
		t.Fatal("a missing worktree reported no error")
	}
}

// A worktree the task table does not name shows when an agent works in it;
// each agent counts for the deepest work tree holding its folder.
func TestRouteDiffsFindActiveWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	proj := t.TempDir()
	gitIn(t, proj, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(proj, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, proj, "add", ".")
	gitIn(t, proj, "commit", "-q", "-m", "base")
	nested := filepath.Join(proj, ".claude", "worktrees", "fix")
	gitIn(t, proj, "worktree", "add", "-q", "-b", "fix/bug", nested)
	idle := filepath.Join(t.TempDir(), "idle")
	gitIn(t, proj, "worktree", "add", "-q", "-b", "idle", idle)
	if err := os.WriteFile(filepath.Join(nested, "a.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &route.Route{Dir: filepath.Join(proj, ".herdsman", "r-2026-10-24")}
	got := routeDiffs(context.Background(), r, []worker{
		{name: "im-fix", dir: filepath.Join(nested, "sub")},
		{name: "orchestrator", dir: proj},
		{name: "elsewhere", dir: t.TempDir()},
	})
	if len(got) != 2 {
		t.Fatalf("sources = %+v", got)
	}
	wt, co := got[0], got[1]
	if wt.Kind != "worktree" || wt.Label != "fix/bug" || !slices.Equal(wt.Agents, []string{"im-fix"}) || wt.Diff == nil || len(wt.Diff.Files) != 1 {
		t.Fatalf("worktree = %+v %+v", wt, wt.Diff)
	}
	if co.Kind != "checkout" || !slices.Equal(co.Agents, []string{"orchestrator"}) {
		t.Fatalf("checkout = %+v", co)
	}
}

func TestWorkersPreferTheRouteName(t *testing.T) {
	str := func(s string) *string { return &s }
	snap := herdr.Snapshot{
		Panes: []herdr.Pane{{ID: "w1:p2", Cwd: str("/a"), ForegroundCwd: str("/a/sub")}, {ID: "w1:p3", Cwd: str("/b")}},
		Agents: []herdr.Agent{
			{PaneID: "w1:p2", Name: str("herdr-name")},
			{PaneID: "w1:p3", Agent: str("codex")},
			{PaneID: "w1:p4"}, // no folder known: left out
		},
	}
	r := &route.Route{Tasks: []route.Task{{Task: "auth", Agent: "im-auth", Pane: "w1:p2"}}}
	got := workers(snap, r)
	if !slices.Equal(got, []worker{{"im-auth", "/a/sub"}, {"codex", "/b"}}) {
		t.Fatalf("workers = %+v", got)
	}
}

func TestWorktreeDir(t *testing.T) {
	home, _ := os.UserHomeDir()
	for cell, want := range map[string]string{
		"../wt-auth":               "/p/wt-auth",
		"/abs/wt":                  "/abs/wt",
		"<repo>/.herdsman/wt/x":    "/p/proj/.herdsman/wt/x",
		"~/wt":                     filepath.Join(home, "wt"),
		".claude/worktrees/x/../y": "/p/proj/.claude/worktrees/y",
	} {
		if got := worktreeDir("/p/proj", cell); got != want {
			t.Errorf("worktreeDir(%q) = %q, want %q", cell, got, want)
		}
	}
}

func TestRouteDiffEndpoint(t *testing.T) {
	f := newFixture(t)
	if res := f.request(t, http.MethodGet, "/api/route/diff?route=t-2026-10-24", "", nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unpaired: %d", res.StatusCode)
	}
	if res := f.request(t, http.MethodGet, "/api/route/diff?route=nope", "", f.authed); res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown route: %d", res.StatusCode)
	}
	res := f.request(t, http.MethodGet, "/api/route/diff?route=t-2026-10-24", "", f.authed)
	var body struct{ Sources []DiffSource }
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil || res.StatusCode != 200 {
		t.Fatalf("status %d: %v", res.StatusCode, err)
	}
	// The fixture's project is not a git repository: one source, with the error.
	if len(body.Sources) != 1 || body.Sources[0].ID != "project" || body.Sources[0].Error == "" {
		t.Fatalf("sources = %+v", body.Sources)
	}
	hdr := map[string]string{"Authorization": f.authed["Authorization"], "If-None-Match": res.Header.Get("ETag")}
	if res := f.request(t, http.MethodGet, "/api/route/diff?route=t-2026-10-24", "", hdr); res.StatusCode != http.StatusNotModified {
		t.Fatalf("repeat: %d, want 304", res.StatusCode)
	}
}
