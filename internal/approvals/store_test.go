package approvals

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApproveIsPerExactTemplateAndProject(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "nested", FileName))
	if ok, err := store.Approved("p", "make a"); err != nil || ok {
		t.Fatalf("before: %v %v", ok, err)
	}
	for range 2 {
		if err := store.Approve("p", "make a"); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := store.Approved("p", "make a"); err != nil || !ok {
		t.Fatalf("after: %v %v", ok, err)
	}
	for _, other := range [][2]string{{"p", "make a "}, {"q", "make a"}} {
		if ok, err := store.Approved(other[0], other[1]); err != nil || ok {
			t.Fatalf("%v: %v %v", other, ok, err)
		}
	}
	list, err := store.List()
	if err != nil || len(list["p"]) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}
}

func TestFileIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := Open(path).Approve("p", "t"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
}

func TestClear(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), FileName))
	for _, pair := range [][2]string{{"a", "1"}, {"a", "2"}, {"b", "3"}} {
		if err := store.Approve(pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := store.Clear("none"); err != nil || n != 0 {
		t.Fatalf("none: %d %v", n, err)
	}
	if n, err := store.Clear("a"); err != nil || n != 2 {
		t.Fatalf("a: %d %v", n, err)
	}
	if ok, _ := store.Approved("b", "3"); !ok {
		t.Fatal("clearing a touched b")
	}
	if n, err := store.Clear(""); err != nil || n != 1 {
		t.Fatalf("all: %d %v", n, err)
	}
}

func TestBrokenFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("[[["), 0o600); err != nil {
		t.Fatal(err)
	}
	store := Open(path)
	if _, err := store.List(); err == nil {
		t.Fatal("list")
	}
	if _, err := store.Approved("p", "t"); err == nil {
		t.Fatal("approved")
	}
	if err := store.Approve("p", "t"); err == nil {
		t.Fatal("approve")
	}
	if _, err := store.Clear(""); err == nil {
		t.Fatal("clear")
	}
}

func TestDefaultLocation(t *testing.T) {
	if _, err := Default([]string{}); err == nil {
		t.Fatal("no home accepted")
	}
	store, err := Default([]string{"HOME=/h", "XDG_CONFIG_HOME=/x"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(store.path) != FileName {
		t.Fatalf("path %s", store.path)
	}
}

func TestPathNamesTheFile(t *testing.T) {
	if got := Open("/x/y").Path(); got != "/x/y" {
		t.Fatalf("%q", got)
	}
}

func TestStoreFailures(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Open(filepath.Join(blocker, "sub", FileName)).Approve("p", "t"); err == nil {
		t.Fatal("wrote under a file")
	}
	folder := filepath.Join(dir, "folder")
	if err := os.MkdirAll(filepath.Join(folder, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(folder).List(); err == nil {
		t.Fatal("read a folder")
	}
	if err := Open(folder).Approve("p", "t"); err == nil {
		t.Fatal("replaced a folder")
	}
	if _, err := Open(folder).Clear(""); err == nil {
		t.Fatal("cleared a folder")
	}
	if _, err := Open(folder).Approved("p", "t"); err == nil {
		t.Fatal("read a folder")
	}
	if err := Open(filepath.Join(folder, "held.toml")).write(map[string][]string{"p": {"t"}}); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(dir, "locked", FileName)
	if err := os.MkdirAll(locked+".lock", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Open(locked).Approve("p", "t"); err == nil {
		t.Fatal("a folder served as the lock")
	}
}

type failingTemp struct {
	*os.File
	fail string
}

func (f failingTemp) Write(data []byte) (int, error) {
	if f.fail == "write" {
		return 0, errors.New("disk failed")
	}
	return f.File.Write(data)
}

func (f failingTemp) Close() error {
	err := f.File.Close()
	if f.fail == "close" {
		return errors.New("disk failed")
	}
	return err
}

func TestDiskFailureKeepsTheApprovals(t *testing.T) {
	old := createTemp
	t.Cleanup(func() { createTemp = old })
	for _, fail := range []string{"create", "write", "close"} {
		t.Run(fail, func(t *testing.T) {
			createTemp = old
			path := filepath.Join(t.TempDir(), FileName)
			store := Open(path)
			if err := store.Approve("p", "first"); err != nil {
				t.Fatal(err)
			}
			createTemp = func(dir, pattern string) (tempFile, error) {
				if fail == "create" {
					return nil, errors.New("disk failed")
				}
				file, err := os.CreateTemp(dir, pattern)
				return failingTemp{file, fail}, err
			}
			if err := store.Approve("p", "second"); err == nil {
				t.Fatal("no error")
			}
			createTemp = old
			if ok, err := store.Approved("p", "first"); err != nil || !ok {
				t.Fatalf("%v %v", ok, err)
			}
		})
	}
}
