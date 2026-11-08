package toml

import (
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
)

func TestRenameTableRewritesHeadersOnly(t *testing.T) {
	const source = "# first\n  [old-name] # beside\nkey = 1\n\n[old-name.inner]\nother = 2\n\n[[old-name.many]]\nthird = 3\n\n[unrelated]\nold-name = 4\n"
	doc, err := Parse([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := doc.RenameTable([]string{"old-name"}, []string{"new_name"})
	if err != nil || !renamed {
		t.Fatalf("renamed %v, err %v", renamed, err)
	}
	const want = "# first\n  [new_name] # beside\nkey = 1\n\n[new_name.inner]\nother = 2\n\n[[new_name.many]]\nthird = 3\n\n[unrelated]\nold-name = 4\n"
	if got := string(doc.Encode()); got != want {
		t.Fatalf("got:\n%s", got)
	}
	if _, ok := doc.Tree().Lookup("new_name", "inner", "other"); !ok {
		t.Fatal("the tree did not follow the rename")
	}
}

func TestRenameTableWithoutHeader(t *testing.T) {
	doc, err := Parse([]byte("inline = { a = 1 }\n"))
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := doc.RenameTable([]string{"inline"}, []string{"other"})
	if err != nil || renamed {
		t.Fatalf("renamed %v, err %v", renamed, err)
	}
}

func TestRenameTableRefusesACollisionAndBadPaths(t *testing.T) {
	const source = "[a]\nx = 1\n[b]\ny = 2\n"
	doc, err := Parse([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doc.RenameTable([]string{"a"}, []string{"b"}); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("err = %v", err)
	}
	if string(doc.Encode()) != source {
		t.Fatalf("a refused rename changed the document:\n%s", doc.Encode())
	}
	if _, err := doc.RenameTable(nil, []string{"b"}); err == nil {
		t.Fatal("an empty source path was accepted")
	}
	if _, err := doc.RenameTable([]string{"a"}, nil); err == nil {
		t.Fatal("an empty target path was accepted")
	}
}

func TestRenameTableRewritesDottedKeys(t *testing.T) {
	doc, err := Parse([]byte("[a]\nold-name.x = 1 # beside\n  old-name.y.z = 2\nother.old-name = 3\n"))
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := doc.RenameTable([]string{"a", "old-name"}, []string{"a", "new_name"})
	if err != nil || !renamed {
		t.Fatalf("renamed %v, err %v", renamed, err)
	}
	const want = "[a]\nnew_name.x = 1 # beside\n  new_name.y.z = 2\nother.old-name = 3\n"
	if got := string(doc.Encode()); got != want {
		t.Fatalf("got:\n%s", got)
	}
}

func TestRenameLeavesKeysBelowARenamedHeaderToTheHeader(t *testing.T) {
	doc, err := Parse([]byte("[old]\nx.y = 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doc.RenameTable([]string{"old"}, []string{"new"}); err != nil {
		t.Fatal(err)
	}
	if got := string(doc.Encode()); got != "[new]\nx.y = 1\n" {
		t.Fatalf("got:\n%s", got)
	}
}

func TestUnsetTableFindsNothingAndRefusesAnEmptyPath(t *testing.T) {
	doc, err := Parse([]byte("a = 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if removed, err := doc.UnsetTable([]string{"missing"}); err != nil || removed {
		t.Fatalf("removed %v, err %v", removed, err)
	}
	if _, err := doc.UnsetTable(nil); err == nil {
		t.Fatal("an empty path was accepted")
	}
}

func TestUnsetAndSetReplaceAWholeTable(t *testing.T) {
	const source = "# top\n[a.b]\nx = 1\n[a.b.c]\ny = 2\n# about d\n[d]\nz = 3\n"
	doc, err := Parse([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	removed, err := doc.UnsetTable([]string{"a", "b"})
	if err != nil || !removed {
		t.Fatalf("removed %v, err %v", removed, err)
	}
	if got := string(doc.Encode()); got != "# top\n# about d\n[d]\nz = 3\n" {
		t.Fatalf("got:\n%s", got)
	}

	doc, err = Parse([]byte("[a]\nb.x = 1\nb.y = 2\nk = 3\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doc.UnsetTable([]string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if err := doc.Set([]string{"a", "b"}, tree.NewString("v")); err != nil {
		t.Fatal(err)
	}
	if node, ok := doc.Tree().Lookup("a", "b"); !ok || node.Str != "v" {
		t.Fatalf("got:\n%s", doc.Encode())
	}
	if _, ok := doc.Tree().Lookup("a", "k"); !ok {
		t.Fatalf("a sibling was lost:\n%s", doc.Encode())
	}
}
