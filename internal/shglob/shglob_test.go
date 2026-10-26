package shglob

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGlobTakesTheFolderLiterally(t *testing.T) {
	for _, name := range []string{"plain", "skill[", "skill[x]", "a*b?"} {
		dir := filepath.Join(t.TempDir(), name)
		_ = os.MkdirAll(dir, 0o755)
		for _, f := range []string{"b.sh", "a.sh", ".hidden.sh", "c.txt"} {
			_ = os.WriteFile(filepath.Join(dir, f), nil, 0o644)
		}
		got, err := Glob(dir, "*.sh")
		want := []string{filepath.Join(dir, "a.sh"), filepath.Join(dir, "b.sh")}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v %v, want %v", name, got, err, want)
		}
		got, _ = Glob(dir, ".*.sh")
		if !reflect.DeepEqual(got, []string{filepath.Join(dir, ".hidden.sh")}) {
			t.Errorf("%s: a pattern that starts with a dot: %v", name, got)
		}
	}
}

func TestGlobErrors(t *testing.T) {
	if _, err := Glob(filepath.Join(t.TempDir(), "none"), "*"); err == nil {
		t.Error("a missing folder lists")
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "x"), nil, 0o644)
	if _, err := Glob(dir, "["); err == nil {
		t.Error("a bad pattern lists a folder with entries")
	}
}

// R4 finding 3: a bad pattern is an error before any entry is read, so an
// empty folder and a folder of hidden entries refuse it too.
func TestGlobBadPatternWithoutEntries(t *testing.T) {
	if _, err := Glob(t.TempDir(), "["); err == nil {
		t.Error("an empty folder accepts a bad pattern")
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ".hidden"), nil, 0o644)
	if _, err := Glob(dir, "*["); err == nil {
		t.Error("a folder of hidden entries accepts a bad pattern")
	}
}
