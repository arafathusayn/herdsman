package project_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/project"
)

const configName = "herdsman.config.toml"

func folder(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func readConfig(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, configName))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeConfig(t *testing.T, dir, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, configName), []byte(text), 0o640); err != nil {
		t.Fatal(err)
	}
}

func TestBindCreatesConfigAndRecord(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, `or"chard`)
	dir := folder(t)
	got, err := s.Bind(p.ID, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Folders) != 1 || got.Folders[0] != dir {
		t.Fatalf("folders %v", got.Folders)
	}
	want := "[project]\nid = \"" + p.ID + "\"\nname = \"or\\\"chard\"\n"
	if text := readConfig(t, dir); text != want {
		t.Fatalf("config:\n%s\nwant:\n%s", text, want)
	}
	if _, err := s.Bind(p.ID, dir); err != nil {
		t.Errorf("binding twice: %v", err)
	}
	if text := readConfig(t, dir); text != want {
		t.Errorf("second bind changed the file:\n%s", text)
	}
}

func TestBindKeepsTheRestOfTheFile(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard")
	dir := folder(t)
	before := "# top comment\n[commit]\ntypes = [\n  \"feat\",\n]\n\n[suggest] # tips\nnote = \"\"\"\n[project]\nid = \"inside a string\"\n\"\"\"\n"
	writeConfig(t, dir, before)
	if _, err := s.Bind(p.ID, dir); err != nil {
		t.Fatal(err)
	}
	after := readConfig(t, dir)
	if !strings.HasPrefix(after, before) {
		t.Fatalf("existing text changed:\n%s", after)
	}
	if !strings.HasSuffix(after, "\n\n[project]\nid = \""+p.ID+"\"\nname = \"orchard\"\n") {
		t.Fatalf("section:\n%s", after)
	}
	info, err := os.Stat(filepath.Join(dir, configName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	if _, err := s.Unbind(dir); err != nil {
		t.Fatal(err)
	}
	if text := readConfig(t, dir); text != before {
		t.Fatalf("unbind did not reverse the bind:\n%q\nwant\n%q", text, before)
	}
}

func TestBindFillsAnExistingSection(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard")
	dir := folder(t)
	writeConfig(t, dir, "[ \"project\" ] # mine\ncontext = true\nname = 'old' # was\n\n# about suggest\n[suggest]\nx = 1\n")
	if _, err := s.Bind(p.ID, dir); err != nil {
		t.Fatal(err)
	}
	want := "[ \"project\" ] # mine\nid = \"" + p.ID + "\"\ncontext = true\nname = \"orchard\"\n\n# about suggest\n[suggest]\nx = 1\n"
	if text := readConfig(t, dir); text != want {
		t.Fatalf("got:\n%s\nwant:\n%s", text, want)
	}
	if _, err := s.Unbind(dir); err != nil {
		t.Fatal(err)
	}
	if text := readConfig(t, dir); text != "# about suggest\n[suggest]\nx = 1\n" {
		t.Fatalf("after unbind:\n%q", text)
	}
}

func TestUnbindRemovesAFileItCreated(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard")
	dir := folder(t)
	if _, err := s.Bind(p.ID, dir); err != nil {
		t.Fatal(err)
	}
	got, err := s.Unbind(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != p.ID || len(got.Folders) != 0 {
		t.Fatalf("%+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, configName)); !os.IsNotExist(err) {
		t.Errorf("config left behind: %v", err)
	}
	if _, err := s.Unbind(dir); !errors.Is(err, project.ErrNotFound) {
		t.Errorf("second unbind: %v", err)
	}
}

func TestUnbindLeavesAnotherProjectsSection(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard")
	dir := folder(t)
	if _, err := s.Bind(p.ID, dir); err != nil {
		t.Fatal(err)
	}
	text := "[project]\nid = \"01HZY7K3M9P2Q4R6S8T0VWXYZA\"\n"
	writeConfig(t, dir, text)
	if _, err := s.Unbind(dir); err != nil {
		t.Fatal(err)
	}
	if got := readConfig(t, dir); got != text {
		t.Errorf("foreign section changed:\n%s", got)
	}
}

func TestBindRefusals(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard")
	other := mustProject(t, s, "other")
	dir := folder(t)
	if _, err := s.Bind(p.ID, dir); err != nil {
		t.Fatal(err)
	}
	_, err := s.Bind(other.ID, dir)
	wantErr(t, err, project.ErrConflict, "orchard")
	if !strings.Contains(readConfig(t, dir), p.ID) {
		t.Error("the refused bind changed the file")
	}

	claimed := folder(t)
	writeConfig(t, claimed, "[project]\nid = \"01HZY7K3M9P2Q4R6S8T0VWXYZA\"\n")
	_, err = s.Bind(p.ID, claimed)
	wantErr(t, err, project.ErrConflict, "01HZY7K3M9P2Q4R6S8T0VWXYZA")

	_, err = s.Bind("ghost", folder(t))
	wantErr(t, err, project.ErrNotFound)
	_, err = s.Bind(p.ID, filepath.Join(folder(t), "missing"))
	wantErr(t, err, project.ErrInvalid)
	_, err = s.Bind(p.ID, "relative/path")
	wantErr(t, err, project.ErrInvalid)
	file := filepath.Join(folder(t), "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = s.Bind(p.ID, file)
	wantErr(t, err, project.ErrInvalid, "folder")
	_, err = s.Unbind("relative/path")
	wantErr(t, err, project.ErrInvalid)

	for _, bad := range []string{
		"project.id = \"x\"\n",
		"project = { id = \"x\" }\n",
		"\"project\".id = \"x\"\n",
		"[project]\nid = 7\n",
		"[project]\nname = 7\n",
		"[project]\nid = \"bad \\q escape\"\n",
		"[project]\nid = \"unterminated\\\n",
	} {
		d := folder(t)
		writeConfig(t, d, bad)
		_, err := s.Bind(p.ID, d)
		wantErr(t, err, project.ErrInvalid, configName)
		if got := readConfig(t, d); got != bad {
			t.Errorf("%q was rewritten to %q", bad, got)
		}
	}

	archivedDir := folder(t)
	if err := s.DeleteProject(other.ID, true); err != nil {
		t.Fatal(err)
	}
	_, err = s.Bind(other.ID, archivedDir)
	wantErr(t, err, project.ErrRefused, "archived")
}

func TestBindRollsBackWhenTheFileCannotBeWritten(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the folder mode")
	}
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard")
	dir := folder(t)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if _, err := s.Bind(p.ID, dir); err == nil {
		t.Fatal("bind in a read-only folder succeeded")
	}
	got, err := s.GetProject(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Folders) != 0 {
		t.Errorf("the record stayed without the file: %v", got.Folders)
	}
}

func TestBindFollowsSymlinks(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard")
	real := folder(t)
	link := filepath.Join(folder(t), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	got, err := s.Bind(p.ID, link)
	if err != nil {
		t.Fatal(err)
	}
	if got.Folders[0] != real {
		t.Errorf("folder %v, want %s", got.Folders, real)
	}
	shared := folder(t)
	target := filepath.Join(shared, "shared.toml")
	if err := os.WriteFile(target, []byte("[a]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := folder(t)
	if err := os.Symlink(target, filepath.Join(second, configName)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Bind(p.ID, second); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(second, configName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("the bind replaced the link")
	}
	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), p.ID) {
		t.Errorf("target:\n%s", b)
	}
}

func TestRenameUpdatesBoundFolders(t *testing.T) {
	s, _ := openStore(t)
	p := mustProject(t, s, "orchard")
	dir, gone := folder(t), folder(t)
	if _, err := s.Bind(p.ID, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Bind(p.ID, gone); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateProject(p.ID, project.ProjectPatch{Name: ptr("grove")}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readConfig(t, dir), `name = "grove"`) {
		t.Errorf("config:\n%s", readConfig(t, dir))
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("a missing folder was recreated: %v", err)
	}
}

func TestDeleteClearsBoundFolders(t *testing.T) {
	for _, force := range []bool{false, true} {
		s, _ := openStore(t)
		p := mustProject(t, s, "orchard")
		dir, gone := folder(t), folder(t)
		if _, err := s.Bind(p.ID, dir); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Bind(p.ID, gone); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(gone); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteProject(p.ID, force); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, configName)); !os.IsNotExist(err) {
			t.Errorf("force %v: config left: %v", force, err)
		}
		other := mustProject(t, s, "next")
		if _, err := s.Bind(other.ID, dir); err != nil {
			t.Errorf("force %v: the folder stayed taken: %v", force, err)
		}
	}
}

func TestFindBinding(t *testing.T) {
	root := folder(t)
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, root, "[project]\nid = 'ID1'\nname = \"top\"\n")
	writeConfig(t, filepath.Join(root, "a"), "[commit]\nx = 1\n")
	got, ok, err := project.FindBinding(deep)
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if got.Folder != root || got.ID != "ID1" || got.Name != "top" {
		t.Fatalf("%+v", got)
	}
	writeConfig(t, filepath.Join(root, "a", "b"), "[project]\nid = \"NEAR\"\n")
	got, _, err = project.FindBinding(deep)
	if err != nil || got.ID != "NEAR" {
		t.Fatalf("nearest: %+v %v", got, err)
	}
	writeConfig(t, filepath.Join(root, "a", "b"), "[project]\nname = \"no id\"\n")
	if _, _, err := project.FindBinding(deep); !errors.Is(err, project.ErrInvalid) {
		t.Errorf("no id: %v", err)
	}
	writeConfig(t, filepath.Join(root, "a", "b"), "[project]\nid = 7\n")
	if _, _, err := project.FindBinding(deep); !errors.Is(err, project.ErrInvalid) {
		t.Errorf("bad id: %v", err)
	}
	if _, _, err := project.FindBinding("relative"); !errors.Is(err, project.ErrInvalid) {
		t.Errorf("relative: %v", err)
	}
	empty := folder(t)
	if _, ok, err := project.FindBinding(empty); ok || err != nil {
		t.Errorf("empty: %v %v", ok, err)
	}
	asDir := folder(t)
	if err := os.Mkdir(filepath.Join(asDir, configName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := project.FindBinding(asDir); err == nil {
		t.Error("an unreadable config was skipped")
	}
}

func TestResolveProject(t *testing.T) {
	s, _ := openStore(t)
	bound := mustProject(t, s, "bound")
	named := mustProject(t, s, "named")
	viaRoute := mustProject(t, s, "via-route")
	dir := folder(t)
	if _, err := s.Bind(bound.ID, dir); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := folder(t)
	stale := folder(t)
	writeConfig(t, stale, "[project]\nid = \"01HZY7K3M9P2Q4R6S8T0VWXYZA\"\n")

	for _, c := range []struct {
		name  string
		scope project.Scope
		want  string
	}{
		{"flag wins", project.Scope{Project: "named", Folder: sub, RouteProject: viaRoute.ID}, named.ID},
		{"folder over route", project.Scope{Folder: sub, RouteProject: viaRoute.ID}, bound.ID},
		{"at means the current one", project.Scope{Project: "@", Folder: sub}, bound.ID},
		{"route last", project.Scope{Folder: outside, RouteProject: viaRoute.ID}, viaRoute.ID},
		{"route without a folder", project.Scope{RouteProject: viaRoute.ID}, viaRoute.ID},
	} {
		got, err := s.ResolveProject(c.scope)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
		} else if got.ID != c.want {
			t.Errorf("%s: %s", c.name, got.Name)
		}
	}
	_, err := s.ResolveProject(project.Scope{Folder: outside})
	wantErr(t, err, project.ErrNotFound, "no project")
	_, err = s.ResolveProject(project.Scope{Folder: stale})
	wantErr(t, err, project.ErrNotFound, stale)
	_, err = s.ResolveProject(project.Scope{Folder: "relative"})
	wantErr(t, err, project.ErrInvalid)
}

func TestResolveTask(t *testing.T) {
	s, _ := openStore(t)
	a := mustProject(t, s, "a", "r")
	b := mustProject(t, s, "b", "r")
	ta := mustTask(t, s, a, "Same")
	tb := mustTask(t, s, b, "Same")
	dir := folder(t)
	if _, err := s.Bind(b.ID, dir); err != nil {
		t.Fatal(err)
	}
	outside := folder(t)

	got, err := s.ResolveTask(ta.ID, project.Scope{Folder: dir}, nil)
	if err != nil || got.ID != ta.ID {
		t.Fatalf("id: %+v %v", got, err)
	}
	got, err = s.ResolveTask("same", project.Scope{Folder: dir}, nil)
	if err != nil || got.ID != tb.ID {
		t.Fatalf("bound folder: %+v %v", got, err)
	}
	got, err = s.ResolveTask("same", project.Scope{Project: "a"}, nil)
	if err != nil || got.ID != ta.ID {
		t.Fatalf("flag: %+v %v", got, err)
	}
	_, err = s.ResolveTask("same", project.Scope{Folder: outside}, nil)
	wantErr(t, err, project.ErrConflict)
	_, err = s.ResolveTask("same", project.Scope{Project: "ghost"}, nil)
	wantErr(t, err, project.ErrNotFound, "ghost")
	_, err = s.ResolveTask("same", project.Scope{Folder: "relative"}, nil)
	wantErr(t, err, project.ErrInvalid)

	_, err = s.ResolveTask("@", project.Scope{Folder: dir}, nil)
	wantErr(t, err, project.ErrRefused, "worktree")
	current := func(folder string) (string, bool, error) { return tb.Slug, folder == dir, nil }
	got, err = s.ResolveTask("@", project.Scope{Folder: dir}, current)
	if err != nil || got.ID != tb.ID {
		t.Fatalf("at: %+v %v", got, err)
	}
	_, err = s.ResolveTask("@", project.Scope{Folder: outside}, current)
	wantErr(t, err, project.ErrNotFound, "worktree")
	boom := errors.New("boom")
	_, err = s.ResolveTask("@", project.Scope{Folder: dir}, func(string) (string, bool, error) { return "", false, boom })
	wantErr(t, err, boom)
}
