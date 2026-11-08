package approvals

import (
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
