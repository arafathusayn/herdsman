package yaml

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
)

func mapOf(pairs ...any) *tree.Node {
	out := tree.NewTable()
	for i := 0; i < len(pairs); i += 2 {
		out.Set(pairs[i].(string), pairs[i+1].(*tree.Node))
	}
	return out
}

func TestSetKeepsCommentsAndOrder(t *testing.T) {
	const source = "# head\nmerge: # beside section\n  # about land\n  land: pr # beside land\n  verify: true\n\n# about branch\nbranch:\n  name: x\n"
	for name, tc := range map[string]struct {
		path  []string
		value *tree.Node
		want  string
	}{
		"replace":     {[]string{"merge", "land"}, tree.NewString("push"), "# head\nmerge: # beside section\n  # about land\n  land: \"push\" # beside land\n  verify: true\n\n# about branch\nbranch:\n  name: x\n"},
		"new key":     {[]string{"merge", "clean"}, tree.NewBool(false), "# head\nmerge: # beside section\n  # about land\n  land: pr # beside land\n  verify: true\n  clean: false\n\n# about branch\nbranch:\n  name: x\n"},
		"new section": {[]string{"worktree", "path"}, tree.NewString("/w"), source + "worktree:\n  path: \"/w\"\n"},
		"to list":     {[]string{"branch", "name"}, tree.NewArray(tree.NewString("a")), "# head\nmerge: # beside section\n  # about land\n  land: pr # beside land\n  verify: true\n\n# about branch\nbranch:\n  name:\n    - \"a\"\n"},
	} {
		t.Run(name, func(t *testing.T) {
			doc := parseDoc(t, source)
			if err := doc.Set(tc.path, tc.value); err != nil {
				t.Fatal(err)
			}
			if got := string(doc.Encode()); got != tc.want {
				t.Fatalf("got:\n%s", got)
			}
		})
	}
}

func parseDoc(t *testing.T, source string) *Document {
	t.Helper()
	doc, err := Parse([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestSetReplacesMultiLineValuesWhole(t *testing.T) {
	doc := parseDoc(t, "a:\n  - x\n  - y # gone with the list\nb: |\n  text\n  # not a comment\nc: 1\n")
	if err := doc.Set([]string{"a"}, tree.NewString("v")); err != nil {
		t.Fatal(err)
	}
	if err := doc.Set([]string{"b"}, tree.NewInt(2)); err != nil {
		t.Fatal(err)
	}
	if got := string(doc.Encode()); got != "a: \"v\"\nb: 2\nc: 1\n" {
		t.Fatalf("got:\n%s", got)
	}
}

func TestSetKeepsAQuotedHashAndFindsAFinalComment(t *testing.T) {
	doc := parseDoc(t, "a: \"x # y\" # real\nb: [1, \"# z\"] # also\n")
	if err := doc.Set([]string{"a"}, tree.NewInt(1)); err != nil {
		t.Fatal(err)
	}
	if err := doc.Set([]string{"b"}, tree.NewInt(2)); err != nil {
		t.Fatal(err)
	}
	if got := string(doc.Encode()); got != "a: 1 # real\nb: 2 # also\n" {
		t.Fatalf("got:\n%s", got)
	}
}

func TestSetInsideAFlowValueRewritesThatValueOnly(t *testing.T) {
	doc := parseDoc(t, "# top\na: {b: 1, c: 2} # side\nd: 3\n")
	if err := doc.Set([]string{"a", "b"}, tree.NewInt(9)); err != nil {
		t.Fatal(err)
	}
	if got := string(doc.Encode()); got != "# top\na: # side\n  b: 9\n  c: 2\nd: 3\n" {
		t.Fatalf("got:\n%s", got)
	}
}

func TestSetIntoAnEmptyTableAndAnEmptyFile(t *testing.T) {
	doc := parseDoc(t, "a: {}\n")
	if err := doc.Set([]string{"a", "b"}, tree.NewInt(1)); err != nil {
		t.Fatal(err)
	}
	if got := string(doc.Encode()); got != "a:\n  b: 1\n" {
		t.Fatalf("got:\n%s", got)
	}
	for _, source := range []string{"", "# only\n", "---\n# only\n"} {
		doc := parseDoc(t, source)
		if err := doc.Set([]string{"a", "b"}, mapOf("c", tree.NewInt(1))); err != nil {
			t.Fatalf("%q: %v", source, err)
		}
		if _, ok := doc.Tree().Lookup("a", "b", "c"); !ok {
			t.Fatalf("%q: got:\n%s", source, doc.Encode())
		}
		if !strings.HasPrefix(string(doc.Encode()), source) {
			t.Fatalf("%q: lost the start:\n%s", source, doc.Encode())
		}
	}
}

func TestSetRefusesToGoThroughAValue(t *testing.T) {
	const source = "a: 1\n"
	doc := parseDoc(t, source)
	if err := doc.Set([]string{"a", "b"}, tree.NewInt(1)); err == nil {
		t.Fatal("went through a scalar")
	}
	if string(doc.Encode()) != source {
		t.Fatalf("changed:\n%s", doc.Encode())
	}
}

func TestSetFollowsTheFilesOwnIndentAndKeys(t *testing.T) {
	doc := parseDoc(t, "\"a\":\n    'b c': 1\n    d: 2\n")
	if err := doc.Set([]string{"a", "b c"}, tree.NewInt(5)); err != nil {
		t.Fatal(err)
	}
	if err := doc.Set([]string{"a", "e"}, tree.NewInt(6)); err != nil {
		t.Fatal(err)
	}
	if got := string(doc.Encode()); got != "\"a\":\n    'b c': 5\n    d: 2\n    e: 6\n" {
		t.Fatalf("got:\n%s", got)
	}
}

func TestSetKeepsCRLF(t *testing.T) {
	doc := parseDoc(t, "# c\r\na: 1\r\n")
	if err := doc.Set([]string{"b"}, tree.NewInt(2)); err != nil {
		t.Fatal(err)
	}
	if got := string(doc.Encode()); got != "# c\r\na: 1\r\nb: 2\r\n" {
		t.Fatalf("got: %q", got)
	}
}

func TestUnsetKeepsTheRest(t *testing.T) {
	doc := parseDoc(t, "# head\na:\n  # first\n  b: 1 # gone\n  c: 2\nd:\n  e: 1\n# tail\n")
	removed, err := doc.Unset([]string{"a", "b"})
	if err != nil || !removed {
		t.Fatalf("removed %v, err %v", removed, err)
	}
	if got := string(doc.Encode()); got != "# head\na:\n  # first\n  c: 2\nd:\n  e: 1\n# tail\n" {
		t.Fatalf("got:\n%s", got)
	}
	if removed, err := doc.Unset([]string{"a", "missing"}); err != nil || removed {
		t.Fatalf("removed %v, err %v", removed, err)
	}
	if removed, err := doc.Unset([]string{"x", "y"}); err != nil || removed {
		t.Fatalf("removed %v, err %v", removed, err)
	}
}

func TestUnsetOfTheLastEntryLeavesAnEmptyTable(t *testing.T) {
	doc := parseDoc(t, "a: # side\n  b: 1\nc: 2\n")
	if _, err := doc.Unset([]string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if got := string(doc.Encode()); got != "a: {} # side\nc: 2\n" {
		t.Fatalf("got:\n%s", got)
	}
	doc = parseDoc(t, "a: 1\n")
	if _, err := doc.Unset([]string{"a"}); err != nil {
		t.Fatal(err)
	}
	if doc.Tree().Len() != 0 {
		t.Fatalf("got:\n%s", doc.Encode())
	}
}

func TestUnsetInsideAFlowValue(t *testing.T) {
	doc := parseDoc(t, "a: {b: 1, c: 2}\n")
	if removed, err := doc.Unset([]string{"a", "b"}); err != nil || !removed {
		t.Fatalf("removed %v, err %v", removed, err)
	}
	if got := string(doc.Encode()); got != "a:\n  c: 2\n" {
		t.Fatalf("got:\n%s", got)
	}
}

func TestRenameChangesOnlyTheKey(t *testing.T) {
	doc := parseDoc(t, "# c\nhooks:\n  pre-merge: a # beside\n  other: b\n")
	if err := doc.Rename([]string{"hooks", "pre-merge"}, "pre_merge"); err != nil {
		t.Fatal(err)
	}
	if got := string(doc.Encode()); got != "# c\nhooks:\n  pre_merge: a # beside\n  other: b\n" {
		t.Fatalf("got:\n%s", got)
	}
}

func TestRenameKeepsPlaceInsideAFlowValue(t *testing.T) {
	doc := parseDoc(t, "hooks: {pre-merge: a, other: b}\n")
	if err := doc.Rename([]string{"hooks", "pre-merge"}, "pre_merge"); err != nil {
		t.Fatal(err)
	}
	if got := string(doc.Encode()); got != "hooks:\n  pre_merge: \"a\"\n  other: \"b\"\n" {
		t.Fatalf("got:\n%s", got)
	}
}

func TestRenameOfAMissingKeyFails(t *testing.T) {
	doc := parseDoc(t, "a: 1\n")
	for _, path := range [][]string{{"b"}, {"a", "b"}, {"x", "y"}} {
		if err := doc.Rename(path, "z"); err == nil {
			t.Fatalf("%v renamed", path)
		}
	}
}

func TestDocumentsTheEditorCannotPreserve(t *testing.T) {
	for _, source := range []string{"{a: 1}\n", "--- {a: 1}\n", "- a\n"} {
		if _, err := Parse([]byte(source)); !errors.Is(err, ErrNotEditable) {
			t.Fatalf("%q: err = %v", source, err)
		}
	}
	if _, err := Parse([]byte("a: [\n")); err == nil || errors.Is(err, ErrNotEditable) {
		t.Fatalf("err = %v", err)
	}
}

func TestEndMarkerBoundsTheDocument(t *testing.T) {
	doc := parseDoc(t, "---\na: 1\n...\n")
	if err := doc.Set([]string{"b"}, tree.NewInt(2)); err != nil {
		t.Fatal(err)
	}
	if got := string(doc.Encode()); got != "---\na: 1\nb: 2\n...\n" {
		t.Fatalf("got:\n%s", got)
	}
}

func inf() float64 { return math.Inf(1) }

func TestEncodeWritesEveryKind(t *testing.T) {
	root := mapOf(
		"s", tree.NewString("x"), "yes", tree.NewBool(true), "i", tree.NewInt(-3),
		"f", tree.NewFloat(1.5), "whole", tree.NewFloat(2), "inf", tree.NewFloat(inf()), "n", tree.NewNull(),
		"e", tree.NewTable(), "l", tree.NewArray(), "nest", mapOf("a b", tree.NewArray(mapOf("k", tree.NewInt(1)), tree.NewArray(tree.NewInt(2)))),
	)
	data := Encode(root)
	back, err := Decode(data)
	if err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	if !tree.Equal(root, back) {
		t.Fatalf("not the same:\n%s", data)
	}
}
