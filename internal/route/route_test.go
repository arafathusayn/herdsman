package route_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/route"
)

const current = "# Route cur (2026-10-24)\n\n" +
	"Adjutant: `front`, pane w1:p6 (tab w1:t3), relays to the orchestrator.\n" +
	"Orchestrator: this session, pane w1:p1 (tab w1:t1 `proj-orchestrator`), some harness.\n\n" +
	"## Decisions\n\n- Go: \"Go\"\n\n" +
	"## Task table\n\n" +
	"| Task | Branch | Worktree | Agent | Pane | Contract |\n" +
	"| --- | --- | --- | --- | --- | --- |\n" +
	"| auth | feat/auth | ../wt-auth | im-auth | w1:p2 | 11-task-auth.md |\n" +
	"| review (Codex) | - | - | rv-codex (Codex high) | `w1:p3` (tab w1:t2 reviewers) | - |\n" +
	"| checkpointer | - | - | - | w1:p4 | - |\n\n" +
	"## Checklist\n\n" +
	"- [x] C0.1 all: Read first.\n" +
	"- [ ] C2.11 pr: The base's gate results known.\n" +
	"- [-] C3.2 nopane: Panes listed. -- the user picked no panes\n" +
	"- [X] done without an id\n\n" +
	"## Run state\n\n" +
	"- 14:47 route folder created\n" +
	"- 14:53 im-auth started\n"

// project writes a project with two route folders. The fixtures live in the
// test, because .herdsman folders are ignored by git.
func project(t *testing.T) (proj, cur, old string) {
	t.Helper()
	proj = t.TempDir()
	cur = filepath.Join(proj, ".herdsman", "cur-2026-10-24")
	old = filepath.Join(proj, ".herdsman", "old-2026-10-01")
	write(t, filepath.Join(cur, route.RouteFile), current)
	write(t, filepath.Join(cur, "11-task-auth.md"), "# Task auth\n")
	write(t, filepath.Join(cur, "reports", "review-auth-1.md"), "VERDICT: pass\n")
	write(t, filepath.Join(cur, "run-check.sh"), "#!/bin/bash\n")
	write(t, filepath.Join(old, route.RouteFile), "# Route old\n")
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(old, route.RouteFile), past, past); err != nil {
		t.Fatal(err)
	}
	return proj, cur, old
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindWalksUpAndSortsNewestFirst(t *testing.T) {
	proj, cur, old := project(t)
	deep := filepath.Join(proj, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	got := route.Find([]string{deep, proj, t.TempDir()})
	if !slices.Equal(got, []string{cur, old}) {
		t.Fatalf("Find = %v, want [%s %s]", got, cur, old)
	}
}

func TestLoad(t *testing.T) {
	_, cur, _ := project(t)
	r, err := route.Load(cur)
	if err != nil {
		t.Fatal(err)
	}
	if r.Title != "Route cur (2026-10-24)" || r.Name != "cur-2026-10-24" || r.OrchestratorPane != "w1:p1" || r.AdjutantPane != "w1:p6" {
		t.Fatalf("route = %q %q %q %q", r.Title, r.Name, r.OrchestratorPane, r.AdjutantPane)
	}
	want := []route.Task{
		{Task: "auth", Branch: "feat/auth", Worktree: "../wt-auth", Agent: "im-auth", Pane: "w1:p2", Contract: "11-task-auth.md"},
		{Task: "review (Codex)", Agent: "rv-codex", Pane: "w1:p3"},
		{Task: "checkpointer", Pane: "w1:p4"},
	}
	if !slices.Equal(r.Tasks, want) {
		t.Fatalf("tasks = %+v\nwant  %+v", r.Tasks, want)
	}
	wantCheck := []route.CheckItem{
		{Done: true, ID: "C0.1", Text: "all: Read first."},
		{Done: false, ID: "C2.11", Text: "pr: The base's gate results known."},
		{Skipped: true, ID: "C3.2", Text: "nopane: Panes listed. -- the user picked no panes"},
		{Done: true, Text: "done without an id"},
	}
	if !slices.Equal(r.Checklist, wantCheck) {
		t.Fatalf("checklist = %+v", r.Checklist)
	}
	if len(r.RunState) != 2 || r.RunState[1] != (route.LogLine{Time: "14:53", Text: "im-auth started"}) {
		t.Fatalf("run state = %+v", r.RunState)
	}
	var paths []string
	for _, f := range r.Files {
		paths = append(paths, f.Path)
	}
	slices.Sort(paths)
	if !slices.Equal(paths, []string{"00-route.md", "11-task-auth.md", "reports/review-auth-1.md"}) {
		t.Fatalf("files = %v (only Markdown files are listed)", paths)
	}
}

// Hundreds of probe files must not push the route's own files out of the list.
func TestFilesKeepRouteFilesPastTheCap(t *testing.T) {
	_, cur, _ := project(t)
	for i := range 250 {
		write(t, filepath.Join(cur, "probes", "case", "out", strings.Repeat("x", 1)+string(rune('a'+i%26))+time.Duration(i).String()+".md"), "x")
	}
	old := time.Now().Add(-72 * time.Hour)
	for _, f := range []string{route.RouteFile, "11-task-auth.md", "reports/review-auth-1.md"} {
		if err := os.Chtimes(filepath.Join(cur, f), old, old); err != nil {
			t.Fatal(err)
		}
	}
	r, err := route.Load(cur)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range r.Files {
		paths = append(paths, f.Path)
	}
	for _, want := range []string{route.RouteFile, "11-task-auth.md", "reports/review-auth-1.md"} {
		if !slices.Contains(paths, want) {
			t.Errorf("%s missing from %d listed files", want, len(paths))
		}
	}
	if len(paths) > 203 {
		t.Errorf("listed %d files, want the cap to hold", len(paths))
	}
}

func TestReadFileStaysInsideTheRoute(t *testing.T) {
	proj, cur, _ := project(t)
	write(t, filepath.Join(proj, "secret.md"), "secret")
	if err := os.Symlink(filepath.Join(proj, "secret.md"), filepath.Join(cur, "link.md")); err != nil {
		t.Fatal(err)
	}
	if b, err := route.ReadFile(cur, "reports/review-auth-1.md"); err != nil || string(b) != "VERDICT: pass\n" {
		t.Fatalf("inside read = %q, %v", b, err)
	}
	for _, p := range []string{"../../secret.md", "/etc/passwd", "..", "link.md", "reports/../../../secret.md"} {
		if _, err := route.ReadFile(cur, p); err == nil {
			t.Errorf("ReadFile(%q) succeeded; want refusal", p)
		}
	}
}

func TestAppendLog(t *testing.T) {
	_, cur, _ := project(t)
	at := time.Date(2026, 10, 24, 9, 5, 0, 0, time.Local)
	if err := route.AppendLog(cur, "web: prompted im-auth:\n\"go\"", at); err != nil {
		t.Fatal(err)
	}
	if err := route.AppendLog(cur, "  \n ", at); err == nil {
		t.Fatal("empty text was logged")
	}
	b, err := os.ReadFile(filepath.Join(cur, route.RouteFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(b), "- 14:53 im-auth started\n- 09:05 web: prompted im-auth: \"go\"\n") {
		t.Fatalf("tail = %q", string(b)[len(b)-80:])
	}
	r, err := route.Load(cur)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.RunState[len(r.RunState)-1]; got.Text != `web: prompted im-auth: "go"` {
		t.Fatalf("last run-state line = %+v", got)
	}
}
