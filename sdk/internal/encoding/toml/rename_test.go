package toml

import (
	"strings"
	"testing"
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
