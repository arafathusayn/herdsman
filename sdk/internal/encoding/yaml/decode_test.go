package yaml

import (
	"errors"
	"math"
	"runtime"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
)

func table(pairs ...any) *tree.Node {
	t := tree.NewTable()
	for i := 0; i < len(pairs); i += 2 {
		t.Set(pairs[i].(string), pairs[i+1].(*tree.Node))
	}
	return t
}

func str(s string) *tree.Node { return tree.NewString(s) }

func TestDecode(t *testing.T) {
	cases := []struct {
		name, src string
		want      *tree.Node
	}{
		{"empty", "", tree.NewNull()},
		{"only comment", "# nothing\n", tree.NewNull()},
		{"bom", "\xef\xbb\xbfa: 1", table("a", tree.NewInt(1))},
		{"crlf", "a: 1\r\nb: 2\r\n", table("a", tree.NewInt(1), "b", tree.NewInt(2))},
		{"bare cr", "a: 1\rb: 2", table("a", tree.NewInt(1), "b", tree.NewInt(2))},
		{"document marker", "---\na: 1\n...\n", table("a", tree.NewInt(1))},
		{"scalar after marker", "--- hello\n", str("hello")},
		{"empty document", "---\n", tree.NewNull()},
		{"marker then end marker", "---\n...\n# bye\n", tree.NewNull()},
		{"plain top scalar folds", "a\n  b\n\n  c\n", str("a b\nc")},
		{"mapping", "a: x\nb:   y  # c\n", table("a", str("x"), "b", str("y"))},
		{"nested", "a:\n  b:\n    c: 1\n  d: 2\ne: 3\n", table("a", table("b", table("c", tree.NewInt(1)), "d", tree.NewInt(2)), "e", tree.NewInt(3))},
		{"null value", "a:\nb: ~\nc: null\n", table("a", tree.NewNull(), "b", tree.NewNull(), "c", tree.NewNull())},
		{"null at end", "a:", table("a", tree.NewNull())},
		{"sequence", "- a\n- b\n", tree.NewArray(str("a"), str("b"))},
		{"empty item", "-\n- b\n", tree.NewArray(tree.NewNull(), str("b"))},
		{"sequence in mapping at same indent", "a:\n- 1\n- 2\nb: 3\n", table("a", tree.NewArray(tree.NewInt(1), tree.NewInt(2)), "b", tree.NewInt(3))},
		{"compact nested sequence", "- - a\n  - b\n- c\n", tree.NewArray(tree.NewArray(str("a"), str("b")), str("c"))},
		{"compact mapping", "- a: 1\n  b: 2\n- c: 3\n", tree.NewArray(table("a", tree.NewInt(1), "b", tree.NewInt(2)), table("c", tree.NewInt(3)))},
		{"item on the next line", "-\n  a: 1\n", tree.NewArray(table("a", tree.NewInt(1)))},
		{"comment between entries", "a: 1\n# c\n\nb: 2\n", table("a", tree.NewInt(1), "b", tree.NewInt(2))},
		{"multi-line plain value", "a: b\n  c\nd: e\n", table("a", str("b c"), "d", str("e"))},
		{"plain with colon and hash", "a: b:c d#e\n", table("a", str("b:c d#e"))},
		{"plain starting with dash", "a: -b\nc: ?d\ne: :f\n", table("a", str("-b"), "c", str("?d"), "e", str(":f"))},
		{"quoted keys", "\"a b\": 1\n'c''d': 2\n", table("a b", tree.NewInt(1), "c'd", tree.NewInt(2))},
		{"numeric key keeps its text", "1: a\n", table("1", str("a"))},
		{"url value", "a: http://x.y/z\n", table("a", str("http://x.y/z"))},
		{"single quoted", "a: 'it''s # x'\n", table("a", str("it's # x"))},
		{"single multi-line", "a: 'x\n  y\n\n  z'\n", table("a", str("x y\nz"))},
		{"double escapes", `a: "\0\a\b\t\	\n\v\f\r\e\ \"\/\\\N\_\L\P\x41\u00e9\U0001F600"`,
			table("a", str("\x00\a\b\t\t\n\v\f\r\x1b \"/\\\u0085\u00a0\u2028\u2029Aé😀"))},
		{"double multi-line", "a: \"x  \n  y\\\n  z \\  \n\n  w\"\n", table("a", str("x yz  \nw"))},
		{"quoted stays string", "a: \"1\"\nb: 'true'\n", table("a", str("1"), "b", str("true"))},
		{"literal", "a: |\n  x\n   y\n\n  z\nb: 1\n", table("a", str("x\n y\n\nz\n"), "b", tree.NewInt(1))},
		{"literal strip", "a: |-\n  x\n\n", table("a", str("x"))},
		{"literal keep", "a: |+\n  x\n\n", table("a", str("x\n\n"))},
		{"literal explicit indent", "a: |2\n    x\n  y\n", table("a", str("  x\ny\n"))},
		{"indent then chomp", "a: |1-\n  x\n", table("a", str(" x"))},
		{"chomp then indent", "a: |-1\n  x\n", table("a", str(" x"))},
		{"header comment", "a: | # c\n  x\n", table("a", str("x\n"))},
		{"no final newline", "a: |\n  x", table("a", str("x"))},
		{"empty block scalar", "a: |\nb: 1\n", table("a", str(""), "b", tree.NewInt(1))},
		{"keep with only blanks", "a: |+\n\n\n", table("a", str("\n\n"))},
		{"leading blank in block", "a: |\n\n  x\n", table("a", str("\nx\n"))},
		{"folded", "a: >\n  x\n  y\n\n  z\n", table("a", str("x y\nz\n"))},
		{"folded more indented", "a: >\n  x\n    y\n  z\n", table("a", str("x\n  y\nz\n"))},
		{"folded more indented blank", "a: >\n  x\n\n    y\n\n  z\n", table("a", str("x\n\n  y\n\nz\n"))},
		{"folded leading blank", "a: >\n\n  x\n", table("a", str("\nx\n"))},
		{"whitespace line longer than indent", "a: |\n  x\n    \n  y\n", table("a", str("x\n  \ny\n"))},
		{"block scalar stops at marker", "--- |\nx\n...\n", str("x\n")},
		{"block scalar in sequence", "- |\n  x\n- y\n", tree.NewArray(str("x\n"), str("y"))},
		{"flow sequence", "[a, 'b', \"c\", 1, [x], {y: z},]", tree.NewArray(str("a"), str("b"), str("c"), tree.NewInt(1), tree.NewArray(str("x")), table("y", str("z")))},
		{"flow mapping", "{a: 1, b, c: , \"d\":2, e: [x]}", table("a", tree.NewInt(1), "b", tree.NewNull(), "c", tree.NewNull(), "d", tree.NewInt(2), "e", tree.NewArray(str("x")))},
		{"flow empty", "a: []\nb: {}\n", table("a", tree.NewArray(), "b", table())},
		{"flow multi-line", "a: [x, # c\n  y,\n  z\n  w]\n", table("a", tree.NewArray(str("x"), str("y"), str("z w")))},
		{"flow single pair", "[a: 1, 'b': 2]", tree.NewArray(table("a", tree.NewInt(1)), table("b", tree.NewInt(2)))},
		{"flow single pair empty value", "[a: , b]", tree.NewArray(table("a", tree.NewNull()), str("b"))},
		{"flow plain with colon", "[a:b, c:d]", tree.NewArray(str("a:b"), str("c:d"))},
		{"flow plain ends at bracket", "{a: b}", table("a", str("b"))},
		{"flow pair colon before flow indicator", "{a:, b: 1}", table("a", tree.NewNull(), "b", tree.NewInt(1))},
		{"tab before top flow", "\t[\n\t]\n", tree.NewArray()},
		{"tab after space indent", "a:\n \tb\n", table("a", str("b"))},
		{"spaces-only last line", "a: |\n  x\n   ", table("a", str("x\n \n"))},
		{"escaped break before blank line", "\"a\\\n\n  b\"", str("a\nb")},
		{"front matter block", "ship: Kestrel\ntonnage: 340\nports:\n  - Bergen\n  - Cork\ncargo:\n  wool: true\n",
			table("ship", str("Kestrel"), "tonnage", tree.NewInt(340), "ports", tree.NewArray(str("Bergen"), str("Cork")),
				"cargo", table("wool", tree.NewBool(true)))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Decode([]byte(c.src))
			if err != nil {
				t.Fatalf("Decode(%q): %v", c.src, err)
			}
			if !tree.Equal(got, c.want) {
				t.Fatalf("got %s, want %s", show(got), show(c.want))
			}
		})
	}
}

func show(n *tree.Node) string {
	switch n.Kind {
	case tree.Table:
		var parts []string
		for _, k := range n.Keys() {
			v, _ := n.Get(k)
			parts = append(parts, k+": "+show(v))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case tree.Array:
		var parts []string
		for _, item := range n.Items {
			parts = append(parts, show(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case tree.String:
		return "\"" + n.Str + "\""
	}
	return n.Kind.String()
}

func TestResolvesCoreSchema(t *testing.T) {
	inf, nan := math.Inf(1), math.NaN()
	cases := map[string]*tree.Node{
		"null": tree.NewNull(), "Null": tree.NewNull(), "NULL": tree.NewNull(), "~": tree.NewNull(),
		"true": tree.NewBool(true), "True": tree.NewBool(true), "TRUE": tree.NewBool(true),
		"false": tree.NewBool(false), "False": tree.NewBool(false), "FALSE": tree.NewBool(false),
		"0": tree.NewInt(0), "12": tree.NewInt(12), "+12": tree.NewInt(12), "-12": tree.NewInt(-12),
		"0o14": tree.NewInt(12), "0xC": tree.NewInt(12),
		"1.5": tree.NewFloat(1.5), "-1.5": tree.NewFloat(-1.5), ".5": tree.NewFloat(.5), "1.": tree.NewFloat(1),
		"1e3": tree.NewFloat(1000), "1.5E+3": tree.NewFloat(1500), "2e-1": tree.NewFloat(.2),
		"99999999999999999999": tree.NewFloat(1e20),
		".inf":                 tree.NewFloat(inf), ".Inf": tree.NewFloat(inf), ".INF": tree.NewFloat(inf), "+.inf": tree.NewFloat(inf),
		"-.inf": tree.NewFloat(-inf), ".nan": tree.NewFloat(nan), ".NaN": tree.NewFloat(nan), ".NAN": tree.NewFloat(nan),
		"yes": str("yes"), "nUll": str("nUll"), "0x": str("0x"), "0xG": str("0xG"), "0o8": str("0o8"), "0o": str("0o"),
		"1.2.3": str("1.2.3"), "1e": str("1e"), "1e+": str("1e+"), ".": str("."), "+": str("+"), "-.": str("-."),
		"1_000": str("1_000"), ".e1": str(".e1"), "0xFFFFFFFFFFFFFFFFFF": str("0xFFFFFFFFFFFFFFFFFF"),
		"1e999": str("1e999"),
	}
	for text, want := range cases {
		t.Run(text, func(t *testing.T) {
			if got := resolve(text); !tree.Equal(got, want) {
				t.Fatalf("resolve(%q) = %+v, want %+v", text, got, want)
			}
		})
	}
}

func TestDecodeRejects(t *testing.T) {
	cases := map[string]string{
		"too large":                 "# " + strings.Repeat("x", maxInputBytes),
		"invalid utf-8":             "a: \xff",
		"control character":         "a: \x01",
		"del is rejected":           "a: \x7f",
		"too deep flow":             strings.Repeat("[", maxDepth+1),
		"too deep block":            strings.Repeat("- ", maxDepth+1) + "a",
		"duplicate key":             "a: 1\na: 2\n",
		"duplicate flow key":        "{a: 1, a: 2}",
		"bad indent":                "a: 1\n  b: 2\n",
		"item deeper":               "- a: 1\n  - b\n",
		"misaligned item":           "- a: 1\n b: 2\n",
		"not a key":                 "a: 1\nb\n",
		"trailing after sequence":   "- a\nb\n",
		"nested on one line":        "a: b: c\n",
		"sequence on key line":      "a: - b\n",
		"continuation with key":     "a\nb: 1\n",
		"tab indent":                "a:\n\tb: 1\n",
		"dash after mapping":        "a: 1\n- b\n",
		"deeper after quoted value": "a: \"x\"\n  b: 2\n",
		"tab indents top mapping":   "\ta: 1\n",
		"tab indents top sequence":  "\t- a\n",
		"comment without space":     "\"a\"#c\n",
		"junk after quoted":         "\"a\" b\n",
		"junk after flow":           "[a] b\n",
		"bad plain start percent":   "a: %b\n",
		"bad plain start at":        "a: @b\n",
		"bad plain start tick":      "a: `b\n",
		"bad plain start comma":     "a: ,b\n",
		"bad plain start bracket":   "a: ]\n",
		"unterminated double":       "\"a",
		"unterminated single":       "'a",
		"bad escape":                `"\q"`,
		"short hex escape":          `"\x4"`,
		"non-hex escape":            `"\xZZ"`,
		"surrogate escape":          `"\uD800"`,
		"too big escape":            `"\UFFFFFFFF"`,
		"escape at the end":         `"\`,
		"marker inside double":      "\"a\n--- b\"\n",
		"marker inside single":      "'a\n... b'\n",
		"quoted under indent":       "a:\n  b: \"x\ny\"\n",
		"block scalar bad header":   "a: |x\n  y\n",
		"block scalar two chomps":   "a: |+-\n  y\n",
		"block scalar two digits":   "a: |12\n  y\n",
		"block scalar zero":         "a: |0\n  y\n",
		"blank wider than text":     "a: |\n     \n  x\n",
		"flow unterminated":         "[a",
		"flow map unterminated":     "{a: 1",
		"flow missing comma":        "[\"a\" 'b']",
		"flow map missing comma":    "{a: [] b: 1}",
		"flow lone comma":           "[,]",
		"flow under indent":         "a:\n  b: [x,\ny]\n",
		"flow marker":               "[a,\n---\n]",
		"close without open":        "]",
		"mapping on marker line":    "--- a: 1\n",
		"sequence on marker line":   "--- - a\n",
		"single pair key split":     "[ a\n  : b ]",
		"quoted pair key split":     "[ \"a\"\n  :b ]",
		"tab continues quoted":      "a: \"b\n\tc\"\n",
		"tab continues flow":        "- [\n\ta]\n",
		"tab indents scalar":        "a:\n\tb\n",
		"tab in block scalar":       "a: |\n\t\nb: 1\n",
		"tab before nested dash":    "-\t- a\n",
		"colon after comment":       "[a #c\n:b]",
		"comment needs space":       "[a,#c\nb]",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Decode([]byte(src))
			var syntax *Error
			if !errors.As(err, &syntax) {
				t.Fatalf("Decode(%.60q) error = %v, want *Error", src, err)
			}
			if syntax.Line < 1 || syntax.Column < 1 || !strings.HasPrefix(syntax.Error(), "yaml: line ") {
				t.Fatalf("bad position in %q", syntax.Error())
			}
			if errors.Is(err, ErrUnsupported) {
				t.Fatalf("%q reported as unsupported, want a syntax error: %v", src, err)
			}
		})
	}
}

func TestUnsupportedConstructsSayIt(t *testing.T) {
	for name, src := range map[string]string{
		"anchor":             "a: &x 1\n",
		"alias":              "a: *x\n",
		"tag":                "a: !t 1\n",
		"anchor at top":      "&x a: 1\n",
		"alias item":         "- *x\n",
		"directive":          "%YAML 1.2\n---\na\n",
		"two documents":      "a\n---\nb\n",
		"complex key":        "? a\n: b\n",
		"empty key":          ": a\n",
		"flow key":           "[a]: b\n",
		"flow map flow key":  "{[a]: b}\n",
		"flow seq flow key":  "[[a]: b]\n",
		"flow complex":       "[? a]\n",
		"complex key later":  "a: 1\n? b\n: c\n",
		"empty key later":    "a: 1\n: b\n",
		"flow empty key":     "[: a]\n",
		"flow map no value":  "{[a]}\n",
		"flow anchor":        "[&x a]\n",
		"flow map complex":   "{? a: b}\n",
		"document after end": "a\n...\n---\nb\n",
		"content after end":  "---\na\n...\nb\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode([]byte(src)); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("Decode(%q) error = %v, want ErrUnsupported", src, err)
			}
		})
	}
}

// TestScalarAllocationIsLinear guards against a line of input costing work
// proportional to everything before or after it: four times the input must
// not cost anywhere near sixteen times the memory.
func TestScalarAllocationIsLinear(t *testing.T) {
	cases := map[string]func(lines int) string{
		"literal": func(n int) string { return "a: |\n" + strings.Repeat("  x\n", n) },
		"folded":  func(n int) string { return "a: >\n" + strings.Repeat("  x\n", n) },
		"double":  func(n int) string { return "a: \"x\n" + strings.Repeat("  y\n", n) + "  \"\n" },
		"single":  func(n int) string { return "a: 'x\n" + strings.Repeat("  y\n", n) + "  '\n" },
	}
	measure := func(t *testing.T, src string) uint64 {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		if _, err := Decode([]byte(src)); err != nil {
			t.Fatal(err)
		}
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			small := measure(t, build(8192))
			large := measure(t, build(4*8192))
			if large > 8*small {
				t.Fatalf("allocated %d bytes, then %d for four times the input", small, large)
			}
			if limit := uint64(64 * len(build(4*8192))); large > limit {
				t.Fatalf("allocated %d bytes, more than %d", large, limit)
			}
		})
	}
}

func TestQuotedBlanks(t *testing.T) {
	for src, want := range map[string]string{
		"\"a  \"":        "a  ",
		"'a  '":          "a  ",
		"\"a  b\"":       "a  b",
		"\"a  \tb\"":     "a  \tb",
		"'a  ''b'":       "a  'b",
		"\"a  \\\n  b\"": "a  b",
		"\"a  \n  b\"":   "a b",
		"\"  \n  b\"":    " b",
	} {
		got, err := Decode([]byte(src))
		if err != nil {
			t.Fatalf("Decode(%q): %v", src, err)
		}
		if got.Str != want {
			t.Errorf("Decode(%q) = %q, want %q", src, got.Str, want)
		}
	}
}

func TestErrorPosition(t *testing.T) {
	_, err := Decode([]byte("a: 1\nb: @\n"))
	var syntax *Error
	if !errors.As(err, &syntax) || syntax.Line != 2 || syntax.Column != 4 {
		t.Fatalf("err = %#v, want line 2 column 4", err)
	}
}
