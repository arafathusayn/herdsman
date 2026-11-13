package project_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/project"
)

var errFault = errors.New("injected fault")

// flaky fails the at-th statement run through it, inside a transaction or not.
type flaky struct {
	store.Conn
	at    int
	count *int
}

func (f flaky) hit() bool {
	*f.count++
	return *f.count == f.at
}

func (f flaky) Exec(q string, args ...any) (int64, error) {
	if f.hit() {
		return 0, errFault
	}
	return f.Conn.Exec(q, args...)
}

func (f flaky) Query(q string, args []any, fn func(store.Row) error) error {
	if f.hit() {
		return errFault
	}
	return f.Conn.Query(q, args, fn)
}

func (f flaky) Tx(fn func(store.Conn) error) error {
	return f.Conn.Tx(func(c store.Conn) error {
		return fn(flaky{Conn: c, at: f.at, count: f.count})
	})
}

// sweep runs op with a fault at each statement in turn, until a run reaches its
// end without meeting one. Every faulted run must report the fault.
func sweep(t *testing.T, setup func(*testing.T, *project.Store, store.Conn) func(*project.Store) error) {
	t.Helper()
	for at := 1; ; at++ {
		if at > 400 {
			t.Fatal("the operation never ran out of statements")
		}
		s, db := openStore(t)
		op := setup(t, s, db)
		count := 0
		conn := flaky{Conn: db, at: at, count: &count}
		broken := project.New(conn, ops.NewEngine(db, ops.Options{Effects: project.Effects(conn)}))
		err := op(broken)
		if count < at {
			if err != nil {
				t.Fatalf("no fault reached at %d, yet: %v", at, err)
			}
			return
		}
		if err == nil || !strings.Contains(err.Error(), errFault.Error()) {
			t.Fatalf("statement %d: error %v", at, err)
		}
	}
}

// rich is a project with a bound folder, two repositories and a linked task.
func rich(t *testing.T, s *project.Store) (*project.Project, *project.Task, string) {
	t.Helper()
	p := mustProject(t, s, "orchard", "api", "web")
	dir := folder(t)
	if _, err := s.Bind(p.ID, dir); err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask(project.TaskInput{ProjectID: p.ID, Title: "One", Labels: []string{"a"}, Repos: []string{"api", "web"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LinkTask(task.ID, "web", "pr:1"); err != nil {
		t.Fatal(err)
	}
	return p, task, dir
}

func finish(t *testing.T, s *project.Store, id string) {
	t.Helper()
	status := project.Done
	if _, err := s.UpdateTask(id, project.TaskPatch{Status: &status}); err != nil {
		t.Fatal(err)
	}
}

func discard[T any](_ T, err error) error { return err }

func TestEveryStatementFailureIsReported(t *testing.T) {
	cases := map[string]func(*testing.T, *project.Store, store.Conn) func(*project.Store) error{
		"create project": func(*testing.T, *project.Store, store.Conn) func(*project.Store) error {
			return func(b *project.Store) error {
				return discard(b.CreateProject(project.ProjectInput{Name: "x", Repos: []project.Repo{repo("api")}}))
			}
		},
		"list projects": func(t *testing.T, s *project.Store, _ store.Conn) func(*project.Store) error {
			rich(t, s)
			return func(b *project.Store) error { return discard(b.ListProjects(true)) }
		},
		"list active projects": func(t *testing.T, s *project.Store, _ store.Conn) func(*project.Store) error {
			rich(t, s)
			return func(b *project.Store) error { return discard(b.ListProjects(false)) }
		},
		"update project": func(t *testing.T, s *project.Store, _ store.Conn) func(*project.Store) error {
			p, task, _ := rich(t, s)
			if _, err := s.UpdateTask(task.ID, project.TaskPatch{RemoveRepos: []string{"web"}}); err != nil {
				t.Fatal(err)
			}
			return func(b *project.Store) error {
				return discard(b.UpdateProject(p.ID, project.ProjectPatch{Name: ptr("grove"), Description: ptr("d"), AddRepos: []project.Repo{repo("docs")}, RemoveRepos: []string{"web"}}))
			}
		},
		"delete project": func(t *testing.T, s *project.Store, _ store.Conn) func(*project.Store) error {
			p, task, _ := rich(t, s)
			finish(t, s, task.ID)
			return func(b *project.Store) error { return b.DeleteProject(p.ID, false) }
		},
		"archive project": func(t *testing.T, s *project.Store, _ store.Conn) func(*project.Store) error {
			p, _, _ := rich(t, s)
			return func(b *project.Store) error { return b.DeleteProject(p.ID, true) }
		},
		"bind": func(t *testing.T, s *project.Store, _ store.Conn) func(*project.Store) error {
			p := mustProject(t, s, "orchard")
			dir := folder(t)
			return func(b *project.Store) error { return discard(b.Bind(p.ID, dir)) }
		},
		"unbind": func(t *testing.T, s *project.Store, _ store.Conn) func(*project.Store) error {
			_, _, dir := rich(t, s)
			return func(b *project.Store) error { return discard(b.Unbind(dir)) }
		},
		"create task": func(t *testing.T, s *project.Store, _ store.Conn) func(*project.Store) error {
			p := mustProject(t, s, "orchard", "api")
			return func(b *project.Store) error {
				return discard(b.CreateTask(project.TaskInput{ProjectID: p.ID, Title: "T", Labels: []string{"a"}}))
			}
		},
		"create task with a slug": func(t *testing.T, s *project.Store, _ store.Conn) func(*project.Store) error {
			p := mustProject(t, s, "orchard", "api")
			return func(b *project.Store) error {
				return discard(b.CreateTask(project.TaskInput{ProjectID: p.ID, Title: "T", Slug: "free"}))
			}
		},
		"list tasks": func(t *testing.T, s *project.Store, _ store.Conn) func(*project.Store) error {
			p, _, _ := rich(t, s)
			return func(b *project.Store) error {
				return discard(b.ListTasks(project.TaskFilter{ProjectID: p.ID, Status: project.Todo, Repo: "web", Label: "a"}))
			}
		},
		"get task by slug": func(t *testing.T, s *project.Store, _ store.Conn) func(*project.Store) error {
			p, _, _ := rich(t, s)
			return func(b *project.Store) error { return discard(b.GetTask(p.ID, "one")) }
		},
		"ambiguous slug": func(t *testing.T, s *project.Store, _ store.Conn) func(*project.Store) error {
			rich(t, s)
			other := mustProject(t, s, "grove", "api")
			mustTask(t, s, other, "One")
			return func(b *project.Store) error {
				err := discard(b.GetTask("", "one"))
				if errors.Is(err, project.ErrConflict) {
					return nil
				}
				return err
			}
		},
		"update task": func(t *testing.T, s *project.Store, _ store.Conn) func(*project.Store) error {
			_, task, _ := rich(t, s)
			status := project.Doing
			return func(b *project.Store) error {
				return discard(b.UpdateTask(task.ID, project.TaskPatch{Title: ptr("New"), Body: ptr("b"), Status: &status, Route: ptr("r"),
					RemoveRepos: []string{"web"}, AddLabels: []string{"b"}, RemoveLabels: []string{"a"}}))
			}
		},
		"add a task repository": func(t *testing.T, s *project.Store, _ store.Conn) func(*project.Store) error {
			p := mustProject(t, s, "orchard", "api", "web")
			task := mustTask(t, s, p, "One", "api")
			return func(b *project.Store) error {
				return discard(b.UpdateTask(task.ID, project.TaskPatch{AddRepos: []string{"web"}}))
			}
		},
		"delete task": func(t *testing.T, s *project.Store, _ store.Conn) func(*project.Store) error {
			_, task, _ := rich(t, s)
			return func(b *project.Store) error { return b.DeleteTask(task.ID, false) }
		},
		"link task": func(t *testing.T, s *project.Store, _ store.Conn) func(*project.Store) error {
			_, task, _ := rich(t, s)
			return func(b *project.Store) error { return discard(b.LinkTask(task.ID, "web", "pr:2")) }
		},
		"route project": func(t *testing.T, s *project.Store, _ store.Conn) func(*project.Store) error {
			return func(b *project.Store) error { return discard(b.ProjectOfRoute("live")) }
		},
		"delete a project with tasks on a route": func(t *testing.T, s *project.Store, db store.Conn) func(*project.Store) error {
			p, task, _ := rich(t, s)
			finish(t, s, task.ID)
			addRoute(t, db, "live", "active")
			if _, err := s.UpdateTask(task.ID, project.TaskPatch{Route: ptr("live")}); err != nil {
				t.Fatal(err)
			}
			return func(b *project.Store) error {
				err := b.DeleteProject(p.ID, false)
				if errors.Is(err, project.ErrRefused) {
					return nil
				}
				return err
			}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) { sweep(t, setup) })
	}
}

// A config that cannot be read must stop the change before anything moves.
func TestAnUnreadableConfigStopsEveryChange(t *testing.T) {
	s, db := openStore(t)
	p := mustProject(t, s, "orchard")
	dir := folder(t)
	if _, err := s.Bind(p.ID, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO project_folders (path, project_id) VALUES (?, ?)", filepath.Join(dir, "sub"), p.ID); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub", configName)
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateProject(p.ID, project.ProjectPatch{Name: ptr("grove")}); err == nil {
		t.Error("rename read a folder as a config")
	}
	if err := s.DeleteProject(p.ID, true); err == nil {
		t.Error("delete read a folder as a config")
	}
	if _, err := s.Unbind(filepath.Join(dir, "sub")); err == nil {
		t.Error("unbind read a folder as a config")
	}
	other := filepath.Join(folder(t), "elsewhere")
	if err := os.MkdirAll(filepath.Join(other, configName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Bind(p.ID, other); err == nil {
		t.Error("bind read a folder as a config")
	}
	if got, err := s.GetProject(p.ID); err != nil || got.Name != "orchard" {
		t.Errorf("%+v %v", got, err)
	}
}

func TestMalformedConfigsStopRenameAndBind(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard")
	dir := folder(t)
	if _, err := s.Bind(p.ID, dir); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, dir, "[project]\nid = 7\n")
	_, err := s.Bind(p.ID, dir)
	wantErr(t, err, project.ErrInvalid, configName)
	_, err = s.UpdateProject(p.ID, project.ProjectPatch{Name: ptr("grove")})
	wantErr(t, err, project.ErrInvalid, configName)
	_, err = s.Unbind(dir)
	wantErr(t, err, project.ErrInvalid, configName)
}

func TestARemovedConfigIsNothingToRename(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard")
	dir := folder(t)
	if _, err := s.Bind(p.ID, dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, configName)); err != nil {
		t.Fatal(err)
	}
	got, err := s.UpdateProject(p.ID, project.ProjectPatch{Name: ptr("grove")})
	if err != nil || got.Name != "grove" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestAddingAMissingSectionKeepsTheFile(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard")
	dir := folder(t)
	writeConfig(t, dir, "[project]\nname = \"named\"\n")
	if _, err := s.Bind(p.ID, dir); err != nil {
		t.Fatal(err)
	}
	want := "[project]\nid = \"" + p.ID + "\"\nname = \"orchard\"\n"
	if got := readConfig(t, dir); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRefusalsInsideTransactions(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard", "api", "web")
	task := mustTask(t, s, p, "One", "api")

	_, err := s.UpdateProject(p.ID, project.ProjectPatch{AddRepos: []project.Repo{repo("api")}})
	wantErr(t, err, project.ErrConflict, "api")
	_, err = s.UpdateProject(p.ID, project.ProjectPatch{RemoveRepos: []string{"api"}})
	wantErr(t, err, project.ErrRefused, task.Slug)
	_, err = s.UpdateProject(p.ID, project.ProjectPatch{RemoveRepos: []string{"nope"}})
	wantErr(t, err, project.ErrNotFound, "nope")
	_, err = s.UpdateProject(p.ID, project.ProjectPatch{Name: ptr(" ")})
	wantErr(t, err, project.ErrInvalid)
	_, err = s.UpdateProject(p.ID, project.ProjectPatch{AddRepos: []project.Repo{{Path: "relative"}}})
	wantErr(t, err, project.ErrInvalid)
	mustProject(t, s, "grove")
	_, err = s.UpdateProject(p.ID, project.ProjectPatch{Name: ptr("grove")})
	wantErr(t, err, project.ErrConflict, "grove")

	_, err = s.UpdateTask(task.ID, project.TaskPatch{AddRepos: []string{"api"}})
	wantErr(t, err, project.ErrConflict, "api")
	_, err = s.UpdateTask(task.ID, project.TaskPatch{AddRepos: []string{"nope"}})
	wantErr(t, err, project.ErrInvalid, "nope")
	_, err = s.UpdateTask(task.ID, project.TaskPatch{AddLabels: []string{" "}})
	wantErr(t, err, project.ErrInvalid)
	_, err = s.UpdateTask("nope", project.TaskPatch{})
	wantErr(t, err, project.ErrNotFound)
	_, err = s.CreateTask(project.TaskInput{ProjectID: p.ID, Title: "T", Labels: []string{" "}})
	wantErr(t, err, project.ErrInvalid)
	_, err = s.CreateTask(project.TaskInput{ProjectID: "nope", Title: "T"})
	wantErr(t, err, project.ErrNotFound)
	_, err = s.CreateTask(project.TaskInput{ProjectID: p.ID, Title: "T", Repos: []string{"nope"}})
	wantErr(t, err, project.ErrInvalid, "nope")
	_, err = s.LinkTask("nope", "api", "pr:1")
	wantErr(t, err, project.ErrNotFound)
	_, err = s.LinkTask(task.ID, "web", "pr:1")
	wantErr(t, err, project.ErrInvalid, "web")
	wantErr(t, s.DeleteTask("nope", false), project.ErrNotFound)
	wantErr(t, s.DeleteProject("nope", false), project.ErrNotFound)
	_, err = s.Bind("nope", folder(t))
	wantErr(t, err, project.ErrNotFound)
}

func TestFoldersThatCannotBeBound(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard")
	_, err := s.Bind(p.ID, "relative")
	wantErr(t, err, project.ErrInvalid, "absolute")
	_, err = s.Bind(p.ID, filepath.Join(folder(t), "gone"))
	wantErr(t, err, project.ErrInvalid, "folder")
	file := filepath.Join(folder(t), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = s.Bind(p.ID, file)
	wantErr(t, err, project.ErrInvalid, "not a folder")
	_, err = s.Unbind("relative")
	wantErr(t, err, project.ErrInvalid, "absolute")
	_, err = s.Unbind(filepath.Join(folder(t), "gone"))
	wantErr(t, err, project.ErrNotFound)
}

func TestReadsReportStatementFailures(t *testing.T) {
	for at := 1; at <= 2; at++ {
		s, db := openStore(t)
		p := mustProject(t, s, "orchard", "api")
		count := 0
		b := project.New(flaky{Conn: db, at: at, count: &count}, engine(db, nil))
		if _, err := b.GetProject("orchard"); err == nil || !strings.Contains(err.Error(), errFault.Error()) {
			t.Errorf("%d: %v", at, err)
		}
		if _, err := s.GetProject(p.ID); err != nil {
			t.Fatal(err)
		}
	}
}

// step gives an effect the fields it reads.
func step(spec string) ops.Step { return ops.Step{StepSpec: ops.StepSpec{Spec: spec}, OpID: "op"} }

func TestJournalEffectsRefuseBadSpecs(t *testing.T) {
	_, db := openStore(t)
	effects := project.Effects(db)
	ctx := context.Background()
	config, records := effects["project.config"], effects["project.records"]

	if err := config.Apply(ctx, step("{")); err == nil {
		t.Error("config applied a broken spec")
	}
	if v, _, err := config.Probe(ctx, step("{")); err == nil || v != ops.Unknown {
		t.Errorf("%v %v", v, err)
	}
	if err := records.Apply(ctx, step("{")); err == nil {
		t.Error("records applied a broken spec")
	}
	if err := records.Apply(ctx, step(`[{"sql":"SELECT 1","args":[1.5]}]`)); err == nil {
		t.Error("records applied a number that is not an integer")
	}
	if err := records.Apply(ctx, step(`[{"sql":"NOT SQL","args":[]}]`)); err == nil {
		t.Error("records applied broken SQL")
	}
}

func TestConfigEffectReadFailures(t *testing.T) {
	_, db := openStore(t)
	effect := project.Effects(db)["project.config"]
	ctx := context.Background()
	dir := folder(t) // a folder where the file should be: reading it fails
	spec := `{"path":"` + dir + `","after":"x"}`
	if err := effect.Apply(ctx, step(spec)); err == nil {
		t.Error("applied over a folder")
	}
	if v, _, err := effect.Probe(ctx, step(spec)); err == nil || v != ops.Unknown {
		t.Errorf("%v %v", v, err)
	}
}

func TestConfigEffectRemovesAndSeesRemoved(t *testing.T) {
	_, db := openStore(t)
	effect := project.Effects(db)["project.config"]
	ctx := context.Background()
	dir := folder(t)
	writeConfig(t, dir, "old\n")
	path := filepath.Join(dir, configName)
	spec := `{"path":"` + path + `","before":"old\n","existed":true,"remove":true}`
	if err := effect.Apply(ctx, step(spec)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("still there: %v", err)
	}
	if err := effect.Apply(ctx, step(spec)); err != nil {
		t.Fatalf("a repeat failed: %v", err)
	}
	writeConfig(t, dir, "changed\n")
	if err := effect.Apply(ctx, step(spec)); err == nil {
		t.Error("a changed file was removed")
	}
}

func TestRecordsProbeReportsAFailedRead(t *testing.T) {
	_, db := openStore(t)
	count := 0
	effect := project.Effects(flaky{Conn: db, at: 1, count: &count})["project.records"]
	if v, _, err := effect.Probe(context.Background(), step("[]")); err == nil || v != ops.Unknown {
		t.Errorf("%v %v", v, err)
	}
	second := 0
	if err := project.Effects(flaky{Conn: db, at: 1, count: &second})["project.records"].Apply(context.Background(), step(`[]`)); err == nil {
		t.Error("the mark was lost silently")
	}
}

// runPlan's two outcomes that are neither done nor failed.
func TestAnEngineWithoutTheEffectsStopsTheChange(t *testing.T) {
	s, db := openStore(t)
	p := mustProject(t, s, "orchard")
	bare := project.New(db, ops.NewEngine(db, ops.Options{}))
	dir := folder(t)
	if _, err := bare.Bind(p.ID, dir); err == nil {
		t.Fatal("the bind ran with no effects to run it")
	}
	if got, err := s.GetProject(p.ID); err != nil || len(got.Folders) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestSharedConfigOfTwoFolders(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard")
	first, second := folder(t), folder(t)
	if _, err := s.Bind(p.ID, first); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(first, configName), filepath.Join(second, configName)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Bind(p.ID, second); err != nil {
		t.Fatal(err)
	}
	got, err := s.UpdateProject(p.ID, project.ProjectPatch{Name: ptr("grove")})
	if err != nil || got.Name != "grove" {
		t.Fatalf("%+v %v", got, err)
	}
	if !strings.Contains(readConfig(t, first), `name = "grove"`) {
		t.Errorf("%s", readConfig(t, first))
	}
}

func TestRenameLeavesAFolderBoundElsewhere(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard")
	dir := folder(t)
	if _, err := s.Bind(p.ID, dir); err != nil {
		t.Fatal(err)
	}
	text := "[project]\nid = \"01HZY7K3M9P2Q4R6S8T0VWXYZA\"\nname = \"theirs\"\n"
	writeConfig(t, dir, text)
	if _, err := s.UpdateProject(p.ID, project.ProjectPatch{Name: ptr("grove")}); err != nil {
		t.Fatal(err)
	}
	if got := readConfig(t, dir); got != text {
		t.Errorf("%s", got)
	}
}

func TestBindAddsANameBesideAnExistingID(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard")
	dir := folder(t)
	writeConfig(t, dir, "[project]\nid = \""+p.ID+"\"\n")
	if _, err := s.Bind(p.ID, dir); err != nil {
		t.Fatal(err)
	}
	want := "[project]\nid = \"" + p.ID + "\"\nname = \"orchard\"\n"
	if got := readConfig(t, dir); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}
