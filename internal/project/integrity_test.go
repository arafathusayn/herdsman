package project_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/project"
)

// failingCommit lets the work of a transaction run, then fails it the way a
// failed COMMIT does.
type failingCommit struct {
	store.Conn
}

var errCommit = errors.New("commit failed")

func (f failingCommit) Tx(fn func(store.Conn) error) error {
	return f.Conn.Tx(func(c store.Conn) error {
		if err := fn(c); err != nil {
			return err
		}
		return errCommit
	})
}

// twoBound binds a project to two folders, returned in the store's path order.
func twoBound(t *testing.T, s *project.Store, p *project.Project) (string, string) {
	t.Helper()
	root := folder(t)
	first, second := filepath.Join(root, "a"), filepath.Join(root, "b")
	for _, d := range []string{first, second} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Bind(p.ID, d); err != nil {
			t.Fatal(err)
		}
	}
	return first, second
}

func addRoute(t *testing.T, db store.Conn, name, state string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO routes (route, inbox, repo, herdr_socket, db_path, created, state, open_gen, reserved_message_id, reserved_seq)
		VALUES (?, ?, 'repo', 'socket', ?, 1, ?, 'gen', 0, 0)`, name, "inbox-"+name, "db-"+name, state); err != nil {
		t.Fatal(err)
	}
}

func TestProjectDeleteRefusesAnActiveRoute(t *testing.T) {
	s, db := openStore(t)
	p := mustProject(t, s, "orchard", "api")
	task := mustTask(t, s, p, "Finished")
	route, status := "live-route", project.Done
	if _, err := s.UpdateTask(task.ID, project.TaskPatch{Route: &route, Status: &status}); err != nil {
		t.Fatal(err)
	}
	addRoute(t, db, route, "active")

	wantErr(t, s.DeleteProject(p.ID, false), project.ErrRefused, route, task.Slug)
	if _, err := s.GetTask("", task.ID); err != nil {
		t.Fatalf("the refused delete lost the task: %v", err)
	}

	if _, err := db.Exec("UPDATE routes SET state = 'removed' WHERE route = ?", route); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProject(p.ID, false); err != nil {
		t.Fatalf("a removed route still blocked: %v", err)
	}
}

func TestProjectForceDeleteKeepsATaskOnAnActiveRoute(t *testing.T) {
	s, db := openStore(t)
	p := mustProject(t, s, "orchard", "api")
	task := mustTask(t, s, p, "Finished")
	route, status := "live-route", project.Done
	if _, err := s.UpdateTask(task.ID, project.TaskPatch{Route: &route, Status: &status}); err != nil {
		t.Fatal(err)
	}
	addRoute(t, db, route, "active")
	if err := s.DeleteProject(p.ID, true); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetTask("", task.ID); err != nil || got.Route != route {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestCreateTaskNeedsARepository(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "bare")
	_, err := s.CreateTask(project.TaskInput{ProjectID: p.ID, Title: "Nowhere"})
	wantErr(t, err, project.ErrInvalid, "bare", "repositor")
	list, err := s.ListTasks(project.TaskFilter{ProjectID: p.ID})
	if err != nil || len(list) != 0 {
		t.Fatalf("%+v %v", list, err)
	}
}

func TestRemovingARepositoryDropsItsLinks(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard", "api", "web")
	task := mustTask(t, s, p, "Both")
	for _, l := range [][2]string{{"web", "pr:1"}, {"web", "issue:2"}, {"api", "pr:3"}} {
		if _, err := s.LinkTask(task.ID, l[0], l[1]); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.UpdateTask(task.ID, project.TaskPatch{RemoveRepos: []string{"web"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Links) != 1 || got.Links[0].Repo != "api" {
		t.Fatalf("links %+v", got.Links)
	}
	if _, err := s.UpdateProject(p.ID, project.ProjectPatch{RemoveRepos: []string{"web"}}); err != nil {
		t.Fatalf("the repository stayed in use: %v", err)
	}
}

func TestALinkNeedsARepositoryOfTheTask(t *testing.T) {
	s, db := openStore(t)
	p := mustProject(t, s, "orchard", "api", "web")
	task := mustTask(t, s, p, "One", "api")
	if _, err := db.Exec("INSERT INTO task_links (task_id, repo, kind, target) VALUES (?, 'web', 'pr', '1')", task.ID); err == nil {
		t.Fatal("a link to a repository the task does not name was stored")
	}
}

func TestQuotedIdentityKeys(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard")
	dir := folder(t)
	writeConfig(t, dir, "[project]\n\"id\" = \""+p.ID+"\"\n'name' = 'old'\ncontext = true\n")
	if _, err := s.Bind(p.ID, dir); err != nil {
		t.Fatal(err)
	}
	want := "[project]\nid = \"" + p.ID + "\"\nname = \"orchard\"\ncontext = true\n"
	if got := readConfig(t, dir); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestQuotedForeignIdentityIsAConflict(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard")
	dir := folder(t)
	text := "[project]\n\"id\" = \"01HZY7K3M9P2Q4R6S8T0VWXYZA\"\n"
	writeConfig(t, dir, text)
	_, err := s.Bind(p.ID, dir)
	wantErr(t, err, project.ErrConflict, "01HZY7K3M9P2Q4R6S8T0VWXYZA")
	if got := readConfig(t, dir); got != text {
		t.Errorf("the refused bind changed the file:\n%s", got)
	}
	if b, ok, err := project.FindBinding(dir); err != nil || !ok || b.ID != "01HZY7K3M9P2Q4R6S8T0VWXYZA" {
		t.Errorf("%+v %v %v", b, ok, err)
	}
}

func TestIdentityFormsThatCannotBeEditedAreRefused(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard")
	for _, bad := range []string{
		"[project]\nid = \"a\"\n\"id\" = \"b\"\n",
		"[project]\nname = \"a\"\n'name' = \"b\"\n",
		"[project]\n\"\\u0069d\" = \"a\"\n",
	} {
		dir := folder(t)
		writeConfig(t, dir, bad)
		_, err := s.Bind(p.ID, dir)
		wantErr(t, err, project.ErrInvalid, configName)
		if got := readConfig(t, dir); got != bad {
			t.Errorf("%q was rewritten to %q", bad, got)
		}
		got, err := s.GetProject(p.ID)
		if err != nil || len(got.Folders) != 0 {
			t.Errorf("%q left a record: %+v %v", bad, got, err)
		}
	}
}

func TestMalformedFolderLeavesEveryFileAlone(t *testing.T) {
	for _, force := range []bool{false, true} {
		s, _ := openStore(t)
		p := mustProject(t, s, "orchard")
		first, second := twoBound(t, s, p)
		firstText := readConfig(t, first)
		writeConfig(t, second, "[project]\nid = 7\n")

		wantErr(t, s.DeleteProject(p.ID, force), project.ErrInvalid, configName)
		if got := readConfig(t, first); got != firstText {
			t.Errorf("force %v: delete changed the first folder:\n%s", force, got)
		}
		_, err := s.UpdateProject(p.ID, project.ProjectPatch{Name: ptr("grove")})
		wantErr(t, err, project.ErrInvalid, configName)
		if got := readConfig(t, first); got != firstText {
			t.Errorf("force %v: rename changed the first folder:\n%s", force, got)
		}
		got, err := s.GetProject(p.ID)
		if err != nil || got.Name != "orchard" || got.State != project.Active || len(got.Folders) != 2 {
			t.Errorf("force %v: %+v %v", force, got, err)
		}
	}
}

// lockFolder makes a folder refuse new files for the rest of the test.
func lockFolder(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores the folder mode")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
}

func TestFailedWriteRestoresEarlierFiles(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard")
	first, second := twoBound(t, s, p)
	if err := os.Chmod(filepath.Join(first, configName), 0o640); err != nil {
		t.Fatal(err)
	}
	firstText := readConfig(t, first)
	writeConfig(t, second, "[keep]\nx = 1\n\n"+readConfig(t, second))
	lockFolder(t, second)

	if err := s.DeleteProject(p.ID, false); err == nil {
		t.Fatal("delete succeeded with a read-only folder")
	}
	if got := readConfig(t, first); got != firstText {
		t.Errorf("after delete:\n%q\nwant\n%q", got, firstText)
	}
	info, err := os.Stat(filepath.Join(first, configName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("restored mode %v", info.Mode().Perm())
	}

	if _, err := s.UpdateProject(p.ID, project.ProjectPatch{Name: ptr("grove")}); err == nil {
		t.Fatal("rename succeeded with a read-only folder")
	}
	if got := readConfig(t, first); got != firstText {
		t.Errorf("after rename:\n%q\nwant\n%q", got, firstText)
	}
	got, err := s.GetProject(p.ID)
	if err != nil || got.Name != "orchard" || len(got.Folders) != 2 {
		t.Errorf("%+v %v", got, err)
	}
}

func TestFailedCommitUndoesTheFiles(t *testing.T) {
	s, db := openStore(t)
	p := mustProject(t, s, "orchard")
	broken := project.New(db, ops.NewEngine(db, ops.Options{Effects: project.Effects(failingCommit{db})}))

	created := folder(t)
	_, err := broken.Bind(p.ID, created)
	wantCommitError(t, err)
	if _, err := os.Stat(filepath.Join(created, configName)); !os.IsNotExist(err) {
		t.Errorf("a file created for the failed bind stayed: %v", err)
	}

	bound := folder(t)
	if _, err := s.Bind(p.ID, bound); err != nil {
		t.Fatal(err)
	}
	text := readConfig(t, bound)
	_, err = broken.Unbind(bound)
	wantCommitError(t, err)
	if got := readConfig(t, bound); got != text {
		t.Errorf("after unbind:\n%s", got)
	}
	_, err = broken.UpdateProject(p.ID, project.ProjectPatch{Name: ptr("grove")})
	wantCommitError(t, err)
	if got := readConfig(t, bound); got != text {
		t.Errorf("after rename:\n%s", got)
	}
	wantCommitError(t, broken.DeleteProject(p.ID, false))
	if got := readConfig(t, bound); got != text {
		t.Errorf("after delete:\n%s", got)
	}
}

func wantCommitError(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), errCommit.Error()) {
		t.Errorf("error %v, want the failed commit", err)
	}
}

func TestUnbindKeepsALinkedConfig(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard")
	target := filepath.Join(folder(t), "shared.toml")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dir := folder(t)
	link := filepath.Join(dir, configName)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Bind(p.ID, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Unbind(dir); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link did not survive: %v %v", info, err)
	}
	b, err := os.ReadFile(target)
	if err != nil || strings.Contains(string(b), p.ID) {
		t.Errorf("target %q %v", b, err)
	}
}
