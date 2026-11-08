package toml

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
)

func TestEncodeTreeOutput(t *testing.T) {
	src := `b = 1
"key with space" = "line\nbreak"
list = [1, "two", {x = 1.5, y = 2}]
tables = [{y = 2}, {y = 3}]
[nested.deep]
when = 2020-01-02
[empty]
[[things]]
name = "first"
[things.sub]
v = true
[[things]]
`
	root := mustDecode(t, src)
	out, err := Encode(root)
	if err != nil {
		t.Fatal(err)
	}
	want := `b = 1
"key with space" = "line\nbreak"
list = [1, "two", {x = 1.5, y = 2}]

[[tables]]
y = 2

[[tables]]
y = 3

[nested.deep]
when = 2020-01-02

[empty]

[[things]]
name = "first"

[things.sub]
v = true

[[things]]
`
	if string(out) != want {
		t.Fatalf("Encode() =\n%s\nwant\n%s", out, want)
	}
	if again := mustDecode(t, string(out)); !tree.Equal(root, again) {
		t.Fatal("the encoded tree decodes to different values")
	}
}

func TestEncodeValues(t *testing.T) {
	cases := []struct {
		name string
		node *tree.Node
		want string
	}{
		{"escapes", tree.NewString("\"\\\b\t\n\f\r\x01\x7fé"), `"\"\\\b\t\n\f\r\u0001\u007Fé"`},
		{"integer", tree.NewInt(-5), "-5"},
		{"true", tree.NewBool(true), "true"},
		{"false", tree.NewBool(false), "false"},
		{"whole float", tree.NewFloat(2), "2.0"},
		{"fraction", tree.NewFloat(0.5), "0.5"},
		{"exponent", tree.NewFloat(1e21), "1e+21"},
		{"negative zero", tree.NewFloat(math.Copysign(0, -1)), "-0.0"},
		{"inf", tree.NewFloat(math.Inf(1)), "inf"},
		{"minus inf", tree.NewFloat(math.Inf(-1)), "-inf"},
		{"nan", tree.NewFloat(math.NaN()), "nan"},
		{"local time", tree.NewDatetime(tree.LocalTime, "07:32:00"), "07:32:00"},
		{"empty array", tree.NewArray(), "[]"},
		{"empty table in array", tree.NewArray(tree.NewInt(1), tree.NewTable()), "[1, {}]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := tree.NewTable()
			root.Set("v", c.node)
			out, err := Encode(root)
			if err != nil {
				t.Fatal(err)
			}
			if want := "v = " + c.want + "\n"; string(out) != want {
				t.Fatalf("Encode() = %q, want %q", out, want)
			}
			if again := mustDecode(t, string(out)); !tree.Equal(root, again) {
				t.Fatalf("%q does not decode to the same value", out)
			}
		})
	}
}

func TestEncodeRejects(t *testing.T) {
	deep := tree.NewInt(1)
	for range maxDepth + 1 {
		deep = tree.NewArray(deep)
	}
	deepTable := tree.NewTable()
	for range maxDepth + 1 {
		parent := tree.NewTable()
		parent.Set("a", deepTable)
		deepTable = parent
	}
	badString := tree.NewTable()
	badString.Set("\xff", tree.NewInt(1))
	cases := []struct {
		name string
		v    *tree.Node
	}{
		{"null", tree.NewNull()},
		{"invalid utf-8 string", tree.NewString("\xff")},
		{"bad date text", tree.NewDatetime(tree.LocalDate, "nope")},
		{"date text of another kind", tree.NewDatetime(tree.LocalDate, "07:32:00")},
		{"too deep array", deep},
		{"too deep table", deepTable},
		{"invalid utf-8 key in inline table", tree.NewArray(badString, tree.NewInt(1))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := tree.NewTable()
			root.Set("v", c.v)
			var encodeErr *EncodeError
			if _, err := Encode(root); !errors.As(err, &encodeErr) || !strings.HasPrefix(err.Error(), "toml: ") {
				t.Fatalf("Encode error = %v, want *EncodeError", err)
			}
		})
	}
	var encodeErr *EncodeError
	if _, err := Encode(tree.NewInt(1)); !errors.As(err, &encodeErr) {
		t.Fatalf("Encode of a non-table root = %v, want *EncodeError", err)
	}
}

// nestedArrays wraps an integer in n arrays.
func nestedArrays(n int) *tree.Node {
	v := tree.NewInt(1)
	for range n {
		v = tree.NewArray(v)
	}
	return v
}

func parseDoc(t *testing.T, src string) *Document {
	t.Helper()
	doc, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse(%q): %v", src, err)
	}
	return doc
}

func TestCommentsSurviveASet(t *testing.T) {
	doc := parseDoc(t, "# header comment\n[merge]\n# why squash\nstrategy = \"squash\"   # squash | rebase\nverify = true\n")
	if err := doc.Set([]string{"merge", "strategy"}, tree.NewString("rebase")); err != nil {
		t.Fatal(err)
	}
	want := "# header comment\n[merge]\n# why squash\nstrategy = \"rebase\"   # squash | rebase\nverify = true\n"
	if got := string(doc.Encode()); got != want {
		t.Fatalf("Encode() =\n%s\nwant\n%s", got, want)
	}
	if v, _ := doc.Tree().Lookup("merge", "strategy"); v.Str != "rebase" {
		t.Fatalf("the tree was not updated: %+v", v)
	}
}

func TestSet(t *testing.T) {
	cases := []struct {
		name, src string
		path      []string
		value     *tree.Node
		want      string
	}{
		{"replace root", "a = 1\n", []string{"a"}, tree.NewInt(2), "a = 2\n"},
		{"replace dotted", "[x]\na.b = 1 # c\n", []string{"x", "a", "b"}, tree.NewInt(2), "[x]\na.b = 2 # c\n"},
		{"replace multi-line array keeps the line's tail", "a = [\n 1,\n] # c\n", []string{"a"}, tree.NewArray(tree.NewInt(2)), "a = [2] # c\n"},
		{"new key after last key of section", "[x]\na = 1\n# tail\n\n[y]\n", []string{"x", "b"}, tree.NewBool(true), "[x]\na = 1\nb = true\n# tail\n\n[y]\n"},
		{"new key under bare header", "[x] # c\n[y]\n", []string{"x", "b"}, tree.NewInt(1), "[x] # c\nb = 1\n[y]\n"},
		{"new key in last section without newline", "[x]\na = 1", []string{"x", "b"}, tree.NewInt(1), "[x]\na = 1\nb = 1\n"},
		{"new dotted key under the nearest section", "[x]\na = 1\n", []string{"x", "y", "z"}, tree.NewInt(1), "[x]\na = 1\ny.z = 1\n"},
		{"new section", "[x]\na = 1\n", []string{"y", "z", "k"}, tree.NewInt(1), "[x]\na = 1\n\n[y.z]\nk = 1\n"},
		{"new section in empty file", "", []string{"y", "k"}, tree.NewInt(1), "[y]\nk = 1\n"},
		{"new section without final newline", "[x]", []string{"y", "k"}, tree.NewInt(1), "[x]\n\n[y]\nk = 1\n"},
		{"new root key before first section", "# top\n[x]\n", []string{"a"}, tree.NewInt(1), "# top\na = 1\n[x]\n"},
		{"new root key after root keys", "a = 1\n\n[x]\n", []string{"b"}, tree.NewInt(1), "a = 1\nb = 1\n\n[x]\n"},
		{"new root key in empty file", "", []string{"a"}, tree.NewInt(1), "a = 1\n"},
		{"keeps crlf", "[x]\r\na = 1\r\n", []string{"x", "b"}, tree.NewInt(1), "[x]\r\na = 1\r\nb = 1\r\n"},
		{"quotes odd keys", "", []string{"a b", "c.d"}, tree.NewInt(1), "[\"a b\"]\n\"c.d\" = 1\n"},
		{"empty key is quoted", "", []string{""}, tree.NewInt(1), "\"\" = 1\n"},
		{"inside an inline table", "h = { a = 1 } # c\n", []string{"h", "b"}, tree.NewInt(2), "h = { a = 1, b = 2 } # c\n"},
		{"deep inside an inline table", "h = { a = { x = 1 } }\n", []string{"h", "a", "x"}, tree.NewInt(2), "h = { a = { x = 2 } }\n"},
		{"creates tables inside an inline table", "h = {}\n", []string{"h", "a", "b"}, tree.NewInt(2), "h = {a.b = 2}\n"},
		{"inline edit keeps a sibling's comment", "h = { a = [1, # keep\n  2], b = 3 } # outside\n", []string{"h", "b"}, tree.NewInt(4), "h = { a = [1, # keep\n  2], b = 4 } # outside\n"},
		{"inline add keeps a sibling's comment", "h = { a = [1, # keep\n  2] }\n", []string{"h", "b"}, tree.NewInt(4), "h = { a = [1, # keep\n  2], b = 4 }\n"},
		{"nested inline edit keeps a sibling's comment", "h = { a = { x = [1, # keep\n 2], y = 1 } }\n", []string{"h", "a", "y"}, tree.NewInt(2), "h = { a = { x = [1, # keep\n 2], y = 2 } }\n"},
		{"inline edit of a dotted key", "h = { a.b = 1, a.c = 2 }\n", []string{"h", "a", "c"}, tree.NewInt(3), "h = { a.b = 1, a.c = 3 }\n"},
		{"inline add beside a dotted key", "h = { a.b = 1 }\n", []string{"h", "a", "c"}, tree.NewInt(3), "h = { a.b = 1, a.c = 3 }\n"},
		{"table value goes inline", "", []string{"h"}, func() *tree.Node {
			v := tree.NewTable()
			v.Set("dev", tree.NewString("x"))
			return v
		}(), "h = {dev = \"x\"}\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := parseDoc(t, c.src)
			if err := doc.Set(c.path, c.value); err != nil {
				t.Fatal(err)
			}
			if got := string(doc.Encode()); got != c.want {
				t.Fatalf("Encode() = %q, want %q", got, c.want)
			}
			if got, ok := doc.Tree().Lookup(c.path...); !ok || !tree.Equal(got, c.value) {
				t.Fatalf("Lookup = %+v, %v", got, ok)
			}
		})
	}
}

func TestSetRefusesAndLeavesTheDocumentAlone(t *testing.T) {
	cases := []struct {
		name, src string
		path      []string
		value     *tree.Node
	}{
		{"empty path", "a = 1\n", nil, tree.NewInt(1)},
		{"a section", "[a]\nb = 1\n", []string{"a"}, tree.NewInt(1)},
		{"a dotted prefix", "a.b = 1\n", []string{"a"}, tree.NewInt(1)},
		{"under a scalar", "a = 1\n", []string{"a", "b"}, tree.NewInt(1)},
		{"under a scalar inside an inline table", "a = {b = 1}\n", []string{"a", "b", "c"}, tree.NewInt(1)},
		{"inside an array of tables", "[[a]]\nb = 1\n", []string{"a", "b"}, tree.NewInt(1)},
		{"new key under an array of tables", "[[a]]\n[a.c]\n", []string{"a", "c", "d"}, tree.NewInt(1)},
		{"the array of tables itself", "[[a]]\n", []string{"a"}, tree.NewInt(1)},
		{"not encodable", "a = 1\n", []string{"a"}, tree.NewNull()},
		{"inline value not encodable", "a = {}\n", []string{"a", "b"}, tree.NewNull()},
		{"two levels under a scalar in an inline table", "a = {b = {c = 1}}\n", []string{"a", "b", "c", "d", "e"}, tree.NewInt(1)},
		{"a dotted table inside an inline table", "a = {b.c = 1}\n", []string{"a", "b"}, tree.NewInt(1)},
		{"too deep once inside an inline table", "a = {}\n", []string{"a", "b"}, nestedArrays(maxDepth - 1)},
		{"dotted insert into an implicit table", "[a.b.c]\n[a]\n", []string{"a", "b", "z"}, tree.NewInt(1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := parseDoc(t, c.src)
			if err := doc.Set(c.path, c.value); err == nil {
				t.Fatalf("Set succeeded: %q", doc.Encode())
			}
			if got := string(doc.Encode()); got != c.src {
				t.Fatalf("the document changed: %q", got)
			}
		})
	}
}

func TestUnset(t *testing.T) {
	cases := []struct {
		name, src string
		path      []string
		removed   bool
		want      string
	}{
		{"removes the line and keeps comments", "# a\na = 1 # gone\n# b\nb = 2\n", []string{"a"}, true, "# a\n# b\nb = 2\n"},
		{"in a section", "[x]\na = 1\nb = 2\n", []string{"x", "a"}, true, "[x]\nb = 2\n"},
		{"inside an inline table", "h = {a = 1, b = 2} # c\n", []string{"h", "a"}, true, "h = {b = 2} # c\n"},
		{"first inline entry keeps a sibling's comment", "h = { a = 1, b = [2, # keep\n 3], c = 4 }\n", []string{"h", "a"}, true, "h = { b = [2, # keep\n 3], c = 4 }\n"},
		{"last inline entry keeps a sibling's comment", "h = { b = [2, # keep\n 3], c = 4 }\n", []string{"h", "c"}, true, "h = { b = [2, # keep\n 3] }\n"},
		{"only inline entry", "h = {a = 1}\n", []string{"h", "a"}, true, "h = {}\n"},
		{"nested inline entry", "h = {a = {x = 1, y = 2}}\n", []string{"h", "a", "x"}, true, "h = {a = {y = 2}}\n"},
		{"missing path under an inline table creates nothing", "h = {a = 1} # outside\n", []string{"h", "missing", "key"}, false, "h = {a = 1} # outside\n"},
		{"missing path under a nested inline table creates nothing", "h = {a = {}}\n", []string{"h", "a", "b", "c"}, false, "h = {a = {}}\n"},
		{"missing key", "a = 1\n", []string{"b"}, false, "a = 1\n"},
		{"missing key inside an inline table", "h = {a = 1}\n", []string{"h", "z"}, false, "h = {a = 1}\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := parseDoc(t, c.src)
			removed, err := doc.Unset(c.path)
			if err != nil {
				t.Fatal(err)
			}
			if removed != c.removed {
				t.Fatalf("removed = %v, want %v", removed, c.removed)
			}
			if got := string(doc.Encode()); got != c.want {
				t.Fatalf("Encode() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestUnsetOfAMissingPathLeavesTheTreeAlone(t *testing.T) {
	doc := parseDoc(t, "h = {a = 1}\n")
	removed, err := doc.Unset([]string{"h", "missing", "key"})
	if err != nil || removed {
		t.Fatalf("Unset = %v, %v", removed, err)
	}
	if h, _ := doc.Tree().Lookup("h"); h.Len() != 1 {
		t.Fatalf("the tree gained keys: %v", h.Keys())
	}
}

func TestUnsetRefuses(t *testing.T) {
	for name, c := range map[string]struct {
		src  string
		path []string
	}{
		"empty path":             {"a = 1\n", nil},
		"a section":              {"[a]\nb = 1\n", []string{"a"}},
		"array of tables":        {"[[a]]\nb = 1\n", []string{"a", "b"}},
		"under a scalar":         {"a = 1\n", []string{"a", "b"}},
		"dotted table in inline": {"a = {b.c = 1}\n", []string{"a", "b"}},
		"scalar under inline":    {"a = {b = 1}\n", []string{"a", "b", "c"}},
	} {
		t.Run(name, func(t *testing.T) {
			doc := parseDoc(t, c.src)
			if _, err := doc.Unset(c.path); err == nil {
				t.Fatalf("Unset succeeded: %q", doc.Encode())
			}
		})
	}
}
