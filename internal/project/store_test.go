package project_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/project"
)

// openStore gives a Store on a fresh account database, whose tables come from
// the numbered migrations alone.
func openStore(t *testing.T) (*project.Store, store.Conn) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "s")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "a.db"), store.Writer, store.KindAccount)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return project.New(db, engine(db, nil)), db
}

// engine is an operation engine that knows the records' effects.
func engine(db store.Conn, fault func(string)) *ops.Engine {
	return ops.NewEngine(db, ops.Options{Effects: project.Effects(db), Fault: fault})
}

func repo(name string) project.Repo {
	return project.Repo{Name: name, Path: "/src/" + name, DefaultBranch: "main", Remote: "origin"}
}

func mustProject(t *testing.T, s *project.Store, name string, repos ...string) *project.Project {
	t.Helper()
	in := project.ProjectInput{Name: name}
	for _, r := range repos {
		in.Repos = append(in.Repos, repo(r))
	}
	p, err := s.CreateProject(in)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mustTask(t *testing.T, s *project.Store, p *project.Project, title string, repos ...string) *project.Task {
	t.Helper()
	got, err := s.CreateTask(project.TaskInput{ProjectID: p.ID, Title: title, Repos: repos})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func wantErr(t *testing.T, err, target error, fragments ...string) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("err = %v, want %v", err, target)
	}
	for _, f := range fragments {
		if !strings.Contains(err.Error(), f) {
			t.Errorf("err %q does not name %q", err, f)
		}
	}
}

func ptr(s string) *string { return &s }

func TestProjectCreateGetList(t *testing.T) {
	s, _ := openStore(t)
	p, err := s.CreateProject(project.ProjectInput{Name: " orchard ", Description: "fruit", Repos: []project.Repo{repo("api"), {Path: "/src/web/"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !project.IsID(p.ID) || p.Name != "orchard" || p.State != project.Active || p.Created == 0 {
		t.Fatalf("created %+v", p)
	}
	if len(p.Repos) != 2 || p.Repos[1].Name != "web" || p.Repos[1].Path != "/src/web" {
		t.Fatalf("repos %+v", p.Repos)
	}
	for _, ref := range []string{p.ID, "orchard"} {
		got, err := s.GetProject(ref)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, p) {
			t.Errorf("get %s:\n%+v\n%+v", ref, got, p)
		}
	}
	mustProject(t, s, "beta")
	list, err := s.ListProjects(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "beta" || list[1].Name != "orchard" {
		t.Fatalf("list %+v", list)
	}
}

func TestProjectCreateRefusals(t *testing.T) {
	s, _ := openStore(t)
	mustProject(t, s, "taken")
	cases := []struct {
		name string
		in   project.ProjectInput
		want error
	}{
		{"empty", project.ProjectInput{Name: "  "}, project.ErrInvalid},
		{"at", project.ProjectInput{Name: "@"}, project.ErrInvalid},
		{"control", project.ProjectInput{Name: "a\nb"}, project.ErrInvalid},
		{"id shaped", project.ProjectInput{Name: "01HZY7K3M9P2Q4R6S8T0VWXYZA"}, project.ErrInvalid},
		{"taken", project.ProjectInput{Name: "taken"}, project.ErrConflict},
		{"relative repo", project.ProjectInput{Name: "x", Repos: []project.Repo{{Path: "src/a"}}}, project.ErrInvalid},
		{"duplicate repo", project.ProjectInput{Name: "x", Repos: []project.Repo{repo("a"), repo("a")}}, project.ErrInvalid},
		{"unnamed root repo", project.ProjectInput{Name: "x", Repos: []project.Repo{{Path: "/"}}}, project.ErrInvalid},
	}
	for _, c := range cases {
		if _, err := s.CreateProject(c.in); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if _, err := s.GetProject("nothing"); !errors.Is(err, project.ErrNotFound) {
		t.Errorf("get: %v", err)
	}
	if _, err := s.GetProject("01HZY7K3M9P2Q4R6S8T0VWXYZA"); !errors.Is(err, project.ErrNotFound) {
		t.Errorf("get by id: %v", err)
	}
}

func TestProjectUpdate(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard", "api", "web")
	mustProject(t, s, "other")
	task := mustTask(t, s, p, "Fix", "api")

	got, err := s.UpdateProject(p.ID, project.ProjectPatch{Name: ptr("grove"), Description: ptr("trees"), AddRepos: []project.Repo{repo("docs")}, RemoveRepos: []string{"web"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "grove" || got.Description != "trees" || len(got.Repos) != 2 || got.Repos[0].Name != "api" || got.Repos[1].Name != "docs" {
		t.Fatalf("updated %+v", got)
	}
	if _, err := s.UpdateProject(p.ID, project.ProjectPatch{Name: ptr("grove")}); err != nil {
		t.Errorf("renaming to its own name: %v", err)
	}

	_, err = s.UpdateProject(p.ID, project.ProjectPatch{RemoveRepos: []string{"api"}})
	wantErr(t, err, project.ErrRefused, task.Slug)
	_, err = s.UpdateProject(p.ID, project.ProjectPatch{RemoveRepos: []string{"ghost"}})
	wantErr(t, err, project.ErrNotFound, "ghost")
	_, err = s.UpdateProject(p.ID, project.ProjectPatch{AddRepos: []project.Repo{repo("api")}})
	wantErr(t, err, project.ErrConflict, "api")
	_, err = s.UpdateProject(p.ID, project.ProjectPatch{AddRepos: []project.Repo{{Path: "rel"}}})
	wantErr(t, err, project.ErrInvalid)
	_, err = s.UpdateProject(p.ID, project.ProjectPatch{Name: ptr("other")})
	wantErr(t, err, project.ErrConflict, "other")
	_, err = s.UpdateProject(p.ID, project.ProjectPatch{Name: ptr("@")})
	wantErr(t, err, project.ErrInvalid)
	_, err = s.UpdateProject("ghost", project.ProjectPatch{})
	wantErr(t, err, project.ErrNotFound)
}

func TestProjectDelete(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard", "api")
	task := mustTask(t, s, p, "Pick apples")
	err := s.DeleteProject("orchard", false)
	wantErr(t, err, project.ErrRefused, task.Slug)

	st := project.Done
	if _, err := s.UpdateTask(task.ID, project.TaskPatch{Status: &st}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProject("orchard", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetProject(p.ID); !errors.Is(err, project.ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
	if _, err := s.GetTask("", task.ID); !errors.Is(err, project.ErrNotFound) {
		t.Errorf("task after delete: %v", err)
	}
	if err := s.DeleteProject("orchard", false); !errors.Is(err, project.ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestProjectForceDeleteArchives(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard", "api")
	task := mustTask(t, s, p, "Pick apples")
	if err := s.DeleteProject("orchard", true); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProject(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != project.Archived {
		t.Errorf("state %s", got.State)
	}
	archived, err := s.GetTask("", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if archived.Status != project.StatusArchived {
		t.Errorf("task status %s", archived.Status)
	}
	if _, err := s.GetProject("orchard"); !errors.Is(err, project.ErrNotFound) {
		t.Errorf("archived name still resolves: %v", err)
	}
	active, _ := s.ListProjects(false)
	all, _ := s.ListProjects(true)
	if len(active) != 0 || len(all) != 1 {
		t.Errorf("active %d all %d", len(active), len(all))
	}
	reborn := mustProject(t, s, "orchard")
	if reborn.ID == p.ID {
		t.Error("the name was not reusable")
	}
	_, err = s.UpdateProject(p.ID, project.ProjectPatch{Description: ptr("x")})
	wantErr(t, err, project.ErrRefused, "archived")
	_, err = s.CreateTask(project.TaskInput{ProjectID: p.ID, Title: "t"})
	wantErr(t, err, project.ErrRefused, "archived")
	wantErr(t, s.DeleteProject(p.ID, true), project.ErrRefused, "archived")
}

func TestOpenTasksCounted(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard", "api")
	first := mustTask(t, s, p, "One")
	mustTask(t, s, p, "Two")
	st := project.Done
	if _, err := s.UpdateTask(first.ID, project.TaskPatch{Status: &st}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProject(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.OpenTasks != 1 {
		t.Errorf("open tasks %d", got.OpenTasks)
	}
}

func TestTaskCreateDefaults(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard", "web", "api")
	task, err := s.CreateTask(project.TaskInput{ProjectID: p.ID, Title: "Fix the Login Page!", Body: "# why", Labels: []string{"b", "a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if task.Slug != "fix-the-login-page" || task.Status != project.Todo || task.ProjectID != p.ID || task.Body != "# why" {
		t.Fatalf("task %+v", task)
	}
	if !reflect.DeepEqual(task.Repos, []string{"api", "web"}) || !reflect.DeepEqual(task.Labels, []string{"a", "b"}) {
		t.Fatalf("repos %v labels %v", task.Repos, task.Labels)
	}
	again, err := s.CreateTask(project.TaskInput{ProjectID: p.ID, Title: "Fix the login page"})
	if err != nil {
		t.Fatal(err)
	}
	if again.Slug != "fix-the-login-page-2" {
		t.Errorf("second slug %s", again.Slug)
	}
	third, err := s.CreateTask(project.TaskInput{ProjectID: p.ID, Title: "Fix the login page"})
	if err != nil {
		t.Fatal(err)
	}
	if third.Slug != "fix-the-login-page-3" {
		t.Errorf("third slug %s", third.Slug)
	}
	chosen, err := s.CreateTask(project.TaskInput{ProjectID: p.ID, Title: "x", Slug: "my-slug", Repos: []string{"web"}})
	if err != nil {
		t.Fatal(err)
	}
	if chosen.Slug != "my-slug" || !reflect.DeepEqual(chosen.Repos, []string{"web"}) {
		t.Errorf("chosen %+v", chosen)
	}
}

func TestTaskCreateRefusals(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard", "api")
	mustProject(t, s, "other", "elsewhere")
	if _, err := s.CreateTask(project.TaskInput{ProjectID: p.ID, Title: "x", Slug: "dup"}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		in   project.TaskInput
		want error
	}{
		{"no title", project.TaskInput{ProjectID: p.ID, Title: " "}, project.ErrInvalid},
		{"bad slug", project.TaskInput{ProjectID: p.ID, Title: "x", Slug: "Not Valid"}, project.ErrInvalid},
		{"taken slug", project.TaskInput{ProjectID: p.ID, Title: "x", Slug: "dup"}, project.ErrConflict},
		{"repo of another project", project.TaskInput{ProjectID: p.ID, Title: "x", Repos: []string{"elsewhere"}}, project.ErrInvalid},
		{"empty label", project.TaskInput{ProjectID: p.ID, Title: "x", Labels: []string{" "}}, project.ErrInvalid},
		{"unknown project", project.TaskInput{ProjectID: "ghost", Title: "x"}, project.ErrNotFound},
	}
	for _, c := range cases {
		if _, err := s.CreateTask(c.in); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
}

func TestSameSlugOnTwoProjects(t *testing.T) {
	s, _ := openStore(t)
	a := mustProject(t, s, "a", "r")
	b := mustProject(t, s, "b", "r")
	ta := mustTask(t, s, a, "Same")
	tb := mustTask(t, s, b, "Same")
	if ta.Slug != tb.Slug {
		t.Fatalf("%s %s", ta.Slug, tb.Slug)
	}
	got, err := s.GetTask(b.ID, "same")
	if err != nil || got.ID != tb.ID {
		t.Fatalf("%+v %v", got, err)
	}
	_, err = s.GetTask("", "same")
	wantErr(t, err, project.ErrConflict, "a", "b")
	if _, err := s.GetTask(a.ID, "ghost"); !errors.Is(err, project.ErrNotFound) {
		t.Errorf("%v", err)
	}
	if _, err := s.GetTask("", "ghost"); !errors.Is(err, project.ErrNotFound) {
		t.Errorf("%v", err)
	}
	only := mustTask(t, s, a, "Only here")
	got, err = s.GetTask("", only.Slug)
	if err != nil || got.ID != only.ID {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestTaskNeverChangesProject(t *testing.T) {
	s, db := openStore(t)
	a := mustProject(t, s, "a", "r")
	b := mustProject(t, s, "b", "r")
	task := mustTask(t, s, a, "Stay")

	if f, ok := reflect.TypeOf(project.TaskPatch{}).FieldByName("ProjectID"); ok {
		t.Errorf("TaskPatch has an update path for the project: %v", f)
	}
	_, err := db.Exec("UPDATE tasks SET project_id = ? WHERE id = ?", b.ID, task.ID)
	if err == nil || !strings.Contains(err.Error(), "project of a task is fixed") {
		t.Fatalf("raw update: %v", err)
	}
	got, err := s.GetTask("", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectID != a.ID {
		t.Errorf("project %s", got.ProjectID)
	}
	if _, err := db.Exec("INSERT INTO task_repos (task_id, project_id, repo) VALUES (?, ?, 'r')", task.ID, b.ID); err == nil {
		t.Error("a task was attached to a repository of a second project")
	}
}

func TestTaskUpdate(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard", "api", "web", "docs")
	task := mustTask(t, s, p, "Old", "api", "web")
	st := project.Review
	route := "route-1"
	got, err := s.UpdateTask(task.ID, project.TaskPatch{
		Title: ptr("New"), Body: ptr("text"), Status: &st, Route: &route,
		AddRepos: []string{"docs"}, RemoveRepos: []string{"web"},
		AddLabels: []string{"x", "y"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "New" || got.Body != "text" || got.Status != project.Review || got.Route != "route-1" {
		t.Fatalf("%+v", got)
	}
	if !reflect.DeepEqual(got.Repos, []string{"api", "docs"}) || !reflect.DeepEqual(got.Labels, []string{"x", "y"}) {
		t.Fatalf("repos %v labels %v", got.Repos, got.Labels)
	}
	got, err = s.UpdateTask(task.Slug, project.TaskPatch{RemoveLabels: []string{"x", "absent"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Labels, []string{"y"}) || got.Slug != task.Slug {
		t.Fatalf("labels %v", got.Labels)
	}

	bad := project.Status("later")
	_, err = s.UpdateTask(task.ID, project.TaskPatch{Status: &bad})
	wantErr(t, err, project.ErrInvalid, "later")
	_, err = s.UpdateTask(task.ID, project.TaskPatch{Title: ptr(" ")})
	wantErr(t, err, project.ErrInvalid)
	_, err = s.UpdateTask(task.ID, project.TaskPatch{AddRepos: []string{"ghost"}})
	wantErr(t, err, project.ErrInvalid, "ghost")
	_, err = s.UpdateTask(task.ID, project.TaskPatch{AddRepos: []string{"api"}})
	wantErr(t, err, project.ErrConflict, "api")
	_, err = s.UpdateTask(task.ID, project.TaskPatch{RemoveRepos: []string{"web"}})
	wantErr(t, err, project.ErrNotFound, "web")
	_, err = s.UpdateTask(task.ID, project.TaskPatch{AddLabels: []string{""}})
	wantErr(t, err, project.ErrInvalid)
	_, err = s.UpdateTask(task.ID, project.TaskPatch{RemoveRepos: []string{"api", "docs"}})
	wantErr(t, err, project.ErrRefused, "last")
	_, err = s.UpdateTask("ghost", project.TaskPatch{})
	wantErr(t, err, project.ErrNotFound)

	after, err := s.GetTask("", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after.Repos, []string{"api", "docs"}) {
		t.Errorf("a refused update changed the task: %v", after.Repos)
	}
}

func TestTaskDelete(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard", "api")
	task := mustTask(t, s, p, "Busy")
	route := "route-1"
	if _, err := s.UpdateTask(task.ID, project.TaskPatch{Route: &route, AddLabels: []string{"l"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LinkTask(task.ID, "", "pr:7"); err != nil {
		t.Fatal(err)
	}
	wantErr(t, s.DeleteTask(task.ID, false), project.ErrRefused, "route-1", "down")
	if err := s.DeleteTask(task.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTask("", task.ID); !errors.Is(err, project.ErrNotFound) {
		t.Errorf("%v", err)
	}
	plain := mustTask(t, s, p, "Plain")
	if err := s.DeleteTask(plain.Slug, false); err != nil {
		t.Fatal(err)
	}
	wantErr(t, s.DeleteTask("ghost", false), project.ErrNotFound)
}

func TestTaskList(t *testing.T) {
	s, _ := openStore(t)
	a := mustProject(t, s, "a", "api", "web")
	b := mustProject(t, s, "b", "api")
	one := mustTask(t, s, a, "One", "api")
	two := mustTask(t, s, a, "Two", "web")
	three := mustTask(t, s, b, "Three")
	st := project.Doing
	if _, err := s.UpdateTask(two.ID, project.TaskPatch{Status: &st, AddLabels: []string{"hot"}}); err != nil {
		t.Fatal(err)
	}
	slugs := func(f project.TaskFilter) string {
		t.Helper()
		list, err := s.ListTasks(f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, task := range list {
			out = append(out, task.Slug)
		}
		return strings.Join(out, " ")
	}
	for _, c := range []struct {
		name string
		f    project.TaskFilter
		want string
	}{
		{"all", project.TaskFilter{}, one.Slug + " " + three.Slug + " " + two.Slug},
		{"project", project.TaskFilter{ProjectID: a.ID}, one.Slug + " " + two.Slug},
		{"status", project.TaskFilter{Status: project.Doing}, two.Slug},
		{"repo", project.TaskFilter{ProjectID: a.ID, Repo: "api"}, one.Slug},
		{"label", project.TaskFilter{Label: "hot"}, two.Slug},
		{"nothing", project.TaskFilter{Label: "cold"}, ""},
	} {
		if got := slugs(c.f); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	_, err := s.ListTasks(project.TaskFilter{Status: "later"})
	wantErr(t, err, project.ErrInvalid, "later")
}

func TestTaskLink(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard", "api", "web")
	single := mustTask(t, s, p, "One", "api")
	got, err := s.LinkTask(single.ID, "", "pr:12")
	if err != nil {
		t.Fatal(err)
	}
	want := []project.Link{{Repo: "api", Kind: project.LinkPR, Target: "12"}}
	if !reflect.DeepEqual(got.Links, want) {
		t.Fatalf("links %+v", got.Links)
	}
	got, err = s.LinkTask(single.ID, "", "pr:12")
	if err != nil || len(got.Links) != 1 {
		t.Fatalf("repeat link: %+v %v", got, err)
	}
	both := mustTask(t, s, p, "Both")
	_, err = s.LinkTask(both.ID, "", "issue:3")
	wantErr(t, err, project.ErrInvalid, "repository")
	_, err = s.LinkTask(both.ID, "ghost", "issue:3")
	wantErr(t, err, project.ErrInvalid, "ghost")
	got, err = s.LinkTask(both.ID, "web", "issue:3")
	if err != nil || got.Links[0].Kind != project.LinkIssue || got.Links[0].Target != "3" {
		t.Fatalf("%+v %v", got, err)
	}
	got, err = s.LinkTask(both.ID, "web", "https://example.test/x")
	if err != nil || got.Links[1].Kind != project.LinkURL {
		t.Fatalf("%+v %v", got, err)
	}
	for _, bad := range []string{"", "pr:", "pr:x", "issue:-1", "nonsense", "ftp://a"} {
		if _, err := s.LinkTask(both.ID, "web", bad); !errors.Is(err, project.ErrInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	_, err = s.LinkTask("ghost", "", "pr:1")
	wantErr(t, err, project.ErrNotFound)
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Fix the Login Page!":            "fix-the-login-page",
		"  --a__b--  ":                   "a-b",
		"Überraschung":                   "berraschung",
		"日本語":                            "task",
		"":                               "task",
		strings.Repeat("ab ", 40):        strings.TrimRight(strings.Repeat("ab-", 16), "-"),
		strings.Repeat("a", 47) + " b c": strings.Repeat("a", 47),
	} {
		if got := project.Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
		if !project.ValidSlug(project.Slugify(in)) {
			t.Errorf("Slugify(%q) is not a valid slug", in)
		}
	}
	for _, bad := range []string{"", "-a", "a-", "a--b", "A", "a b", "a.b", strings.Repeat("a", 65)} {
		if project.ValidSlug(bad) {
			t.Errorf("ValidSlug(%q)", bad)
		}
	}
}

func TestIDs(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := project.NewID()
		if !project.IsID(id) || seen[id] {
			t.Fatalf("id %q", id)
		}
		seen[id] = true
	}
	for _, bad := range []string{"", "short", strings.Repeat("8", 26), strings.Repeat("U", 26), strings.Repeat("a", 26)} {
		if project.IsID(bad) {
			t.Errorf("IsID(%q)", bad)
		}
	}
}
