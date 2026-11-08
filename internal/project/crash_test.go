package project_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/project"
)

type crash struct{}

// crashAt runs fn on a Store whose engine dies at the named journal point.
func crashAt(t *testing.T, db store.Conn, point string, fn func(*project.Store)) {
	t.Helper()
	s := project.New(db, engine(db, func(p string) {
		if p == point {
			panic(crash{})
		}
	}))
	defer func() {
		if r := recover(); r != (crash{}) {
			t.Fatalf("point %s: recovered %v", point, r)
		}
	}()
	fn(s)
}

func restart(t *testing.T, db store.Conn) {
	t.Helper()
	if err := engine(db, nil).Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func journalPoints(steps int) []string {
	points := []string{"submit"}
	for i := range steps {
		for _, at := range []string{"intended", "applied", "done"} {
			points = append(points, fmt.Sprintf("step:%d:%s", i, at))
		}
	}
	return append(points, "finish")
}

func TestBindSurvivesACrashAtEveryPoint(t *testing.T) {
	for _, point := range journalPoints(2) {
		t.Run(point, func(t *testing.T) {
			s, db := openStore(t)
			p := mustProject(t, s, "orchard")
			dir := folder(t)
			crashAt(t, db, point, func(s *project.Store) { s.Bind(p.ID, dir) })
			restart(t, db)
			got, err := s.GetProject(p.ID)
			if err != nil || len(got.Folders) != 1 || got.Folders[0] != dir {
				t.Fatalf("%+v %v", got, err)
			}
			if !strings.Contains(readConfig(t, dir), p.ID) {
				t.Errorf("config:\n%s", readConfig(t, dir))
			}
		})
	}
}

func TestRenameSurvivesACrashAtEveryPoint(t *testing.T) {
	for _, point := range journalPoints(3) {
		t.Run(point, func(t *testing.T) {
			s, db := openStore(t)
			p := mustProject(t, s, "orchard", "web")
			first, second := twoBound(t, s, p)
			patch := project.ProjectPatch{Name: ptr("grove"), AddRepos: []project.Repo{repo("api")}}
			crashAt(t, db, point, func(s *project.Store) { s.UpdateProject(p.ID, patch) })
			restart(t, db)
			got, err := s.GetProject(p.ID)
			if err != nil || got.Name != "grove" || len(got.Repos) != 2 {
				t.Fatalf("%+v %v", got, err)
			}
			for _, d := range []string{first, second} {
				if !strings.Contains(readConfig(t, d), `name = "grove"`) {
					t.Errorf("%s:\n%s", d, readConfig(t, d))
				}
			}
		})
	}
}

func TestDeleteSurvivesACrashAtEveryPoint(t *testing.T) {
	for _, point := range journalPoints(3) {
		t.Run(point, func(t *testing.T) {
			s, db := openStore(t)
			p := mustProject(t, s, "orchard", "web")
			first, second := twoBound(t, s, p)
			crashAt(t, db, point, func(s *project.Store) { s.DeleteProject(p.ID, false) })
			restart(t, db)
			_, err := s.GetProject(p.ID)
			wantErr(t, err, project.ErrNotFound)
			for _, d := range []string{first, second} {
				if _, err := os.Stat(filepath.Join(d, configName)); !os.IsNotExist(err) {
					t.Errorf("%s still has a config: %v", d, err)
				}
			}
		})
	}
}

func TestRestartLeavesAnEditedConfigToAPerson(t *testing.T) {
	s, db := openStore(t)
	p := mustProject(t, s, "orchard")
	dir := folder(t)
	if _, err := s.Bind(p.ID, dir); err != nil {
		t.Fatal(err)
	}
	crashAt(t, db, "step:0:intended", func(s *project.Store) {
		s.UpdateProject(p.ID, project.ProjectPatch{Name: ptr("grove")})
	})
	edited := readConfig(t, dir) + "\n[mine]\nx = 1\n"
	writeConfig(t, dir, edited)
	restart(t, db)
	if got := readConfig(t, dir); got != edited {
		t.Errorf("the edit was overwritten:\n%s", got)
	}
	got, err := s.GetProject(p.ID)
	if err != nil || got.Name != "orchard" {
		t.Errorf("%+v %v", got, err)
	}
	var state string
	if err := db.Query("SELECT state FROM operations", nil, func(r store.Row) error { state = r.Text(0); return nil }); err != nil {
		t.Fatal(err)
	}
	if state != string(ops.OpConflict) {
		t.Errorf("operation is %s", state)
	}
	if _, err := s.UpdateProject(p.ID, project.ProjectPatch{Name: ptr("meadow")}); err == nil {
		t.Error("a second rename ran past the undecided operation")
	}
}

func TestAccountMigrationHoldsTheRecords(t *testing.T) {
	_, db := openStore(t)
	for _, table := range []string{"projects", "project_folders", "project_repos", "tasks", "task_labels", "task_repos", "task_links", "project_commits"} {
		found := false
		if err := db.Query("SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = ?", []any{table}, func(store.Row) error { found = true; return nil }); err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Errorf("no table %s", table)
		}
	}
	if got := store.Newest(store.KindAccount); got < 3 {
		t.Errorf("newest account schema %d", got)
	}
}
