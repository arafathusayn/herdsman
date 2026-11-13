package project

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

var errDisk = errors.New("disk fault")

// brokenTemp fails the named call and otherwise behaves like the real file.
type brokenTemp struct {
	tempFile
	fail string
}

func (b brokenTemp) Chmod(m os.FileMode) error {
	if b.fail == "chmod" {
		return errDisk
	}
	return b.tempFile.Chmod(m)
}

func (b brokenTemp) WriteString(s string) (int, error) {
	if b.fail == "write" {
		return 0, errDisk
	}
	return b.tempFile.WriteString(s)
}

func (b brokenTemp) Sync() error {
	if b.fail == "sync" {
		return errDisk
	}
	return b.tempFile.Sync()
}

func (b brokenTemp) Close() error {
	if b.fail == "close" {
		_ = b.tempFile.Close()
		return errDisk
	}
	return b.tempFile.Close()
}

func TestReplaceFileLeavesTheOldFileOnEveryFailure(t *testing.T) {
	real := createTemp
	t.Cleanup(func() { createTemp = real })
	for _, fail := range []string{"chmod", "write", "sync", "close"} {
		dir := t.TempDir()
		path := filepath.Join(dir, "c.toml")
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		createTemp = func(d, p string) (tempFile, error) {
			f, err := real(d, p)
			if err != nil {
				return nil, err
			}
			return brokenTemp{tempFile: f, fail: fail}, nil
		}
		if err := replaceFile(path, "new", 0o600); !errors.Is(err, errDisk) {
			t.Errorf("%s: %v", fail, err)
		}
		if b, err := os.ReadFile(path); err != nil || string(b) != "old" {
			t.Errorf("%s: %q %v", fail, b, err)
		}
		left, err := os.ReadDir(dir)
		if err != nil || len(left) != 1 {
			t.Errorf("%s: temporary file stayed: %v %v", fail, left, err)
		}
	}
}

func TestReplaceFileReportsAFailedRename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taken")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := replaceFile(path, "new", 0o600); err == nil {
		t.Fatal("replaced a folder")
	}
}

func TestReplaceFileReportsAMissingFolder(t *testing.T) {
	if err := replaceFile(filepath.Join(t.TempDir(), "gone", "c.toml"), "new", 0o600); err == nil {
		t.Fatal("wrote into a folder that does not exist")
	}
}

func TestUndoReportsWhatItCouldNotRestore(t *testing.T) {
	dir := t.TempDir()
	created := filepath.Join(dir, "created")
	edits := fileEdits{list: []*fileEdit{
		{path: created, applied: true},
		{path: filepath.Join(dir, "gone", "existing"), existed: true, before: "x", mode: 0o600, applied: true},
		{path: filepath.Join(dir, "untouched")},
	}}
	if err := edits.undo(); err == nil {
		t.Fatal("undo reported nothing")
	}
}

func TestPlanSpecNamesEachFileAndLock(t *testing.T) {
	files := fileEdits{list: []*fileEdit{{path: "/a", after: "x", mode: 0o600}}}
	spec, err := planSpec(&files, []statement{{SQL: "SELECT ?", Args: []any{1}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Steps) != 2 || len(spec.Locks) != 1 || spec.Steps[1].Effect != effectRecords {
		t.Fatalf("%+v", spec)
	}
	if _, err := planSpec(&files, []statement{{SQL: "x", Args: []any{make(chan int)}}}); err == nil {
		t.Fatal("marshalled a channel")
	}
}

func TestTwoFoldersCannotWantDifferentContentInOneFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.toml")
	var edits fileEdits
	if err := edits.add(path, "", false, "one"); err != nil {
		t.Fatal(err)
	}
	if err := edits.add(path, "", false, "one"); err != nil || len(edits.list) != 1 {
		t.Fatalf("%v %d", err, len(edits.list))
	}
	if err := edits.add(path, "", false, "two"); !errors.Is(err, ErrConflict) {
		t.Fatalf("%v", err)
	}
}
