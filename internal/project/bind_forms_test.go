package project_test

import (
	"testing"

	"github.com/arafathusayn/herdsman/internal/project"
)

// A section this parser cannot edit must stop a bind: appending [project] after
// any of these would write a file TOML rejects or that names two identities.
func TestProjectSectionFormsThatCannotBeEditedAreRefused(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard")
	for _, bad := range []string{
		"[\"\\u0070roject\"]\nid = \"a\"\n",
		"[[project]]\nid = \"a\"\n",
		"[[ 'project' ]]\nid = \"a\"\n",
		"\"\\u0070roject\".id = \"a\"\n",
		"[project.id]\nx = 1\n",
		"[project.name]\nx = 1\n",
		"[[project.id]]\nx = 1\n",
		"['project'.\"id\"]\nx = 1\n",
		"[project]\n\"id\".x = 1\n",
		"[project]\nid.x = 1\n",
		"[project]\n'name' . x = 1\n",
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

func TestKeysThatOnlyStartLikeAnIdentityAreLeftAlone(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard")
	for _, ok := range []string{"[project.other]\nx = 1\n", "[project]\nidentity.x = 1\n", "[project]\nnames.x = 1\n"} {
		dir := folder(t)
		writeConfig(t, dir, ok)
		if _, err := s.Bind(p.ID, dir); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
		if _, err := s.Unbind(dir); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}

func TestOtherSectionsNamedLikeProjectAreLeftAlone(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard")
	dir := folder(t)
	writeConfig(t, dir, "[projects]\nid = \"a\"\n\n[\"\\u0071\"]\nx = 1\n")
	if _, err := s.Bind(p.ID, dir); err != nil {
		t.Fatal(err)
	}
}
