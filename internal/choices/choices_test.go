package choices

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func store(t *testing.T) *Store {
	t.Helper()
	return &Store{Path: filepath.Join(t.TempDir(), "cfg", "herdsman", "choices.json")}
}

var at = time.Date(2026, 10, 25, 13, 0, 0, 0, time.UTC)

func TestSaveAndLoad(t *testing.T) {
	s := store(t)
	f, err := s.Load()
	if err != nil || len(f.Picks) != 0 {
		t.Fatalf("empty store: %+v %v", f, err)
	}
	rv := Pick{Role: "reviewer", Runner: "herdr", Harness: "codex", Model: "model-a", Effort: "effortx", Route: "r-1", At: at}
	im := Pick{Role: "implementer", Runner: "subagent", Harness: "claude", Model: "model-b", Effort: "", At: at}
	if err := s.Save(rv); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(im); err != nil {
		t.Fatal(err)
	}
	rv2 := rv
	rv2.Model = "model-c"
	if err := s.Save(rv2); err != nil {
		t.Fatal(err)
	}
	f, err = s.Load()
	if err != nil || len(f.Picks) != 2 || f.Picks[0] != im || f.Picks[1] != rv2 {
		t.Fatalf("%+v %v", f, err)
	}
	st, _ := os.Stat(s.Path)
	dst, _ := os.Stat(filepath.Dir(s.Path))
	if st.Mode().Perm() != 0o600 || dst.Mode().Perm() != 0o700 {
		t.Errorf("modes %v %v", st.Mode(), dst.Mode())
	}
	entries, _ := os.ReadDir(filepath.Dir(s.Path))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".choices-") {
			t.Errorf("temp file left: %s", e.Name())
		}
	}
}

// Concurrent saves of different roles all land.
func TestConcurrentSaves(t *testing.T) {
	s := store(t)
	var wg sync.WaitGroup
	for _, r := range Roles {
		wg.Go(func() {
			if err := s.Save(Pick{Role: r, Runner: "herdr", Harness: "codex", Model: "model-a", At: at}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	f, err := s.Load()
	if err != nil || len(f.Picks) != len(Roles) {
		t.Fatalf("%+v %v", f, err)
	}
}

func TestRefusesBadPicks(t *testing.T) {
	s := store(t)
	for _, p := range []Pick{
		{Role: "boss", Runner: "herdr", Harness: "codex"},
		{Role: "reviewer", Runner: "cloud", Harness: "codex"},
		{Role: "reviewer", Runner: "herdr", Harness: "Bad Kind"},
		{Role: "reviewer", Runner: "herdr", Harness: "codex", Model: "has space"},
		{Role: "reviewer", Runner: "herdr", Harness: "codex", Effort: "HIGH1"},
		{Role: "reviewer", Runner: "herdr", Harness: "codex", Route: "a/b"},
	} {
		if err := s.Save(p); err == nil {
			t.Errorf("%+v saved", p)
		}
	}
	if _, err := os.Stat(s.Path); err == nil {
		t.Error("a refused pick wrote the file")
	}
}

func TestStrictFile(t *testing.T) {
	for _, body := range []string{
		`{"picks":[],"picks":[]}`,
		`{"picks":[{"role":"reviewer","runner":"herdr","harness":"codex","model":"","effort":"","route":"","at":"2026-10-25T13:00:00Z","extra":1}]}`,
		`{"picks":"no"}`,
		`{"picks":[{"role":"boss","runner":"herdr","harness":"codex","model":"","effort":"","route":"","at":"2026-10-25T13:00:00Z"}]}`,
		`{"picks":[{"role":"reviewer","runner":"herdr","harness":"codex","model":"","effort":"","route":"","at":"2026-10-25T13:00:00Z"},{"role":"reviewer","runner":"herdr","harness":"codex","model":"","effort":"","route":"","at":"2026-10-25T13:00:00Z"}]}`,
		`not json`,
	} {
		s := store(t)
		if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.Path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Load(); err == nil {
			t.Errorf("%s: loaded", body)
		}
		if err := s.Save(Pick{Role: "reviewer", Runner: "herdr", Harness: "codex", At: at}); err == nil {
			t.Errorf("%s: saved over a bad file", body)
		}
	}
	s := store(t)
	if err := os.MkdirAll(s.Path, 0o700); err != nil { // a folder where the file goes
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Error("a folder loaded")
	}
	s = store(t)
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path, []byte(`{"picks":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if f, err := s.Load(); err != nil || f.Picks == nil {
		t.Errorf("%+v %v", f, err)
	}
}

func TestSaveErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Store{Path: filepath.Join(file, "x", "choices.json")}
	if err := s.Save(Pick{Role: "reviewer", Runner: "herdr", Harness: "codex", At: at}); err == nil {
		t.Error("saved under a file")
	}
	// The lock file cannot be made: a folder stands in its place.
	s = store(t)
	if err := os.MkdirAll(filepath.Join(filepath.Dir(s.Path), ".choices.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(Pick{Role: "reviewer", Runner: "herdr", Harness: "codex", At: at}); err == nil {
		t.Error("saved without a lock")
	}
	// The rename fails: a folder stands where the file goes.
	s = store(t)
	if err := os.MkdirAll(filepath.Join(s.Path, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(Pick{Role: "reviewer", Runner: "herdr", Harness: "codex", At: at}); err == nil {
		t.Error("saved over a folder")
	}
}

func TestDefault(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", cfg)
	s, err := Default()
	if err != nil || filepath.Base(s.Path) != "choices.json" || filepath.Base(filepath.Dir(s.Path)) != "herdsman" {
		t.Errorf("%+v %v", s, err)
	}
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	if _, err := Default(); err == nil {
		t.Error("no error without a home folder")
	}
}

func FuzzLoad(f *testing.F) {
	f.Add(`{"picks":[]}`)
	f.Add(`{"picks":[{"role":"reviewer","runner":"herdr","harness":"codex","model":"m","effort":"e","route":"","at":"2026-10-25T13:00:00Z"}]}`)
	f.Add(`{"picks":[],"picks":[]}`)
	f.Fuzz(func(t *testing.T, body string) {
		dir := t.TempDir()
		s := &Store{Path: filepath.Join(dir, "choices.json")}
		if err := os.WriteFile(s.Path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := s.Load()
		if err != nil {
			return
		}
		for _, p := range f.Picks {
			if p.Check() != nil {
				t.Errorf("loaded an invalid pick %+v", p)
			}
		}
	})
}

// The folder cannot take the new file: the save fails and leaves nothing.
func TestSaveUnwritableFolder(t *testing.T) {
	s := store(t)
	if err := s.Save(Pick{Role: "reviewer", Runner: "herdr", Harness: "codex", At: at}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(s.Path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	err := s.Save(Pick{Role: "reviewer", Runner: "herdr", Harness: "claude", At: at})
	if err == nil {
		t.Skip("running as a user who writes into mode-500 folders")
	}
	if f, _ := s.Load(); len(f.Picks) != 1 || f.Picks[0].Harness != "codex" {
		t.Errorf("%+v", f)
	}
}
