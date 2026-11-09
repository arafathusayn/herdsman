package project_test

import (
	"testing"

	"github.com/arafathusayn/herdsman/internal/project"
)

func TestProjectOfRoute(t *testing.T) {
	s, _ := openStore(t)
	if got, err := s.ProjectOfRoute("live"); err != nil || got != "" {
		t.Fatalf("%q %v", got, err)
	}
	first := mustProject(t, s, "orchard", "api")
	task := mustTask(t, s, first, "One")
	route := "live"
	if _, err := s.UpdateTask(task.ID, project.TaskPatch{Route: &route}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ProjectOfRoute(route); err != nil || got != first.ID {
		t.Fatalf("%q %v", got, err)
	}
	second := mustProject(t, s, "grove", "api")
	other := mustTask(t, s, second, "Two")
	if _, err := s.UpdateTask(other.ID, project.TaskPatch{Route: &route}); err != nil {
		t.Fatal(err)
	}
	_, err := s.ProjectOfRoute(route)
	wantErr(t, err, project.ErrConflict, route)
}
