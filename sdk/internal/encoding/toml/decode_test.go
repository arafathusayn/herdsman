package toml

import (
	"bytes"
	"errors"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
)

func mustDecode(t *testing.T, src string) *tree.Node {
	t.Helper()
	root, err := Decode([]byte(src))
	if err != nil {
		t.Fatalf("Decode(%q): %v", src, err)
	}
	return root
}

func TestDecodeScalars(t *testing.T) {
	cases := []struct {
		name, value string
		want        *tree.Node
	}{
		{"basic", `"a\tb"`, tree.NewString("a\tb")},
		{"all short escapes", `"\b\t\n\f\r\"\\"`, tree.NewString("\b\t\n\f\r\"\\")},
		{"unicode escapes", `"\u00e9\U0001F600"`, tree.NewString("é😀")},
		{"tab inside", "\"a\tb\"", tree.NewString("a\tb")},
		{"literal", `'C:\path'`, tree.NewString(`C:\path`)},
		{"empty basic", `""`, tree.NewString("")},
		{"empty literal", `''`, tree.NewString("")},
		{"multi basic", "\"\"\"\na\nb\"\"\"", tree.NewString("a\nb")},
		{"multi basic crlf", "\"\"\"\r\na\r\nb\"\"\"", tree.NewString("a\nb")},
		{"multi basic continuation", "\"\"\"a \\  \n   \n  b\"\"\"", tree.NewString("a b")},
		{"multi basic escape", "\"\"\"\\u0041\"\"\"", tree.NewString("A")},
		{"multi basic inner quotes", `"""a""b"""`, tree.NewString(`a""b`)},
		{"multi basic one closing quote", `"""a""""`, tree.NewString(`a"`)},
		{"multi basic two closing quotes", `"""a"""""`, tree.NewString(`a""`)},
		{"multi literal", "'''\na\\n'''", tree.NewString(`a\n`)},
		{"multi literal crlf", "'''a\r\nb'''", tree.NewString("a\nb")},
		{"multi literal two closing quotes", `'''a'''''`, tree.NewString(`a''`)},
		{"integer", "+1_000", tree.NewInt(1000)},
		{"negative", "-17", tree.NewInt(-17)},
		{"zero", "0", tree.NewInt(0)},
		{"signed zero", "-0", tree.NewInt(0)},
		{"hex", "0xDEAD_beef", tree.NewInt(0xdeadbeef)},
		{"octal", "0o755", tree.NewInt(0o755)},
		{"binary", "0b1010", tree.NewInt(10)},
		{"min int64", "-9223372036854775808", tree.NewInt(math.MinInt64)},
		{"float", "3.14", tree.NewFloat(3.14)},
		{"float exponent", "5e+2", tree.NewFloat(500)},
		{"float both", "6.626E-34", tree.NewFloat(6.626e-34)},
		{"float underscores", "1_000.000_1", tree.NewFloat(1000.0001)},
		{"float zero-padded exponent", "1e007", tree.NewFloat(1e7)},
		{"inf", "inf", tree.NewFloat(math.Inf(1))},
		{"plus inf", "+inf", tree.NewFloat(math.Inf(1))},
		{"minus inf", "-inf", tree.NewFloat(math.Inf(-1))},
		{"nan", "nan", tree.NewFloat(math.NaN())},
		{"plus nan", "+nan", tree.NewFloat(math.NaN())},
		{"minus nan", "-nan", tree.NewFloat(math.NaN())},
		{"true", "true", tree.NewBool(true)},
		{"false", "false", tree.NewBool(false)},
		{"offset utc", "1979-05-27T07:32:00Z", tree.NewDatetime(tree.OffsetDateTime, "1979-05-27T07:32:00Z")},
		{"offset lowercase", "1979-05-27t07:32:00z", tree.NewDatetime(tree.OffsetDateTime, "1979-05-27t07:32:00z")},
		{"offset hours", "1979-05-27T00:32:00.999-07:00", tree.NewDatetime(tree.OffsetDateTime, "1979-05-27T00:32:00.999-07:00")},
		{"offset space separator", "1979-05-27 07:32:00+01:30", tree.NewDatetime(tree.OffsetDateTime, "1979-05-27 07:32:00+01:30")},
		{"local datetime", "1979-05-27T07:32:00", tree.NewDatetime(tree.LocalDateTime, "1979-05-27T07:32:00")},
		{"local date", "1979-05-27", tree.NewDatetime(tree.LocalDate, "1979-05-27")},
		{"local time", "07:32:00.5", tree.NewDatetime(tree.LocalTime, "07:32:00.5")},
		{"leap day", "2000-02-29", tree.NewDatetime(tree.LocalDate, "2000-02-29")},
		{"thirty-day month", "2020-04-30", tree.NewDatetime(tree.LocalDate, "2020-04-30")},
		{"leap second", "23:59:60", tree.NewDatetime(tree.LocalTime, "23:59:60")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := mustDecode(t, "v = "+c.value+"\n")
			got, _ := root.Get("v")
			if !tree.Equal(got, c.want) {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestDecodeStructure(t *testing.T) {
	root := mustDecode(t, strings.Join([]string{
		"\xef\xbb\xbf# leading BOM and comment",
		`top = 1`,
		`a.b."c d".'e' = 2`,
		`[t]`,
		`arr = [ 1, # one`,
		`  [2, "x"], {k = 3},`,
		`]`,
		`empty = []`,
		`inline = { x = 1, y.z = 2, w = {} }`,
		`none = {}`,
		`[t.sub.leaf]`,
		`v = true`,
		`[t.sub]`,
		`w = false`,
		`[[list]]`,
		`n = 1`,
		`[list.child]`,
		`c = 1`,
		`[[list.nested]]`,
		`d = 1`,
		`[[list]]`,
		`n = 2`,
		`[ spaced . "header" ]`,
		"crlf = 1\r",
		`[fruit]`,
		`apple.color = "red"`,
		`apple.taste.sweet = true`,
		`[fruit.apple.texture]`,
		`smooth = true`,
	}, "\n"))

	lookups := []struct {
		path []string
		want *tree.Node
	}{
		{[]string{"top"}, tree.NewInt(1)},
		{[]string{"a", "b", "c d", "e"}, tree.NewInt(2)},
		{[]string{"t", "inline", "y", "z"}, tree.NewInt(2)},
		{[]string{"t", "sub", "leaf", "v"}, tree.NewBool(true)},
		{[]string{"t", "sub", "w"}, tree.NewBool(false)},
		{[]string{"spaced", "header", "crlf"}, tree.NewInt(1)},
		{[]string{"fruit", "apple", "taste", "sweet"}, tree.NewBool(true)},
		{[]string{"fruit", "apple", "texture", "smooth"}, tree.NewBool(true)},
	}
	for _, l := range lookups {
		got, ok := root.Lookup(l.path...)
		if !ok || !tree.Equal(got, l.want) {
			t.Errorf("Lookup(%v) = %+v, %v", l.path, got, ok)
		}
	}
	arr, _ := root.Lookup("t", "arr")
	if len(arr.Items) != 3 || arr.Items[1].Items[1].Str != "x" {
		t.Errorf("arr = %+v", arr)
	}
	list, _ := root.Lookup("list")
	if len(list.Items) != 2 {
		t.Fatalf("list has %d elements", len(list.Items))
	}
	if _, ok := list.Items[0].Lookup("child", "c"); !ok {
		t.Error("[list.child] did not land in the last element")
	}
	if nested, ok := list.Items[0].Lookup("nested"); !ok || len(nested.Items) != 1 {
		t.Errorf("[[list.nested]] = %+v, %v", nested, ok)
	}
}

func TestDecodeRejects(t *testing.T) {
	cases := map[string]string{
		"no value":                 "a =",
		"no equals":                "a 1",
		"no key":                   "= 1",
		"trailing dot in key":      "a. = 1",
		"garbage after value":      "a = 1 b",
		"garbage after header":     "[a] b",
		"unclosed header":          "[a",
		"unclosed array header":    "[[a]",
		"newline in header":        "[a\n]",
		"duplicate key":            "a = 1\na = 2",
		"duplicate table":          "[a]\n[a]",
		"table over value":         "a = 1\n[a]",
		"key under value":          "a = 1\na.b = 2",
		"header through value":     "a = 1\n[a.b]",
		"table over dotted":        "a.b = 1\n[a]",
		"array table over table":   "[a]\n[[a]]",
		"table over array table":   "[[a]]\n[a]",
		"array table over static":  "a = []\n[[a]]",
		"header through static":    "a = [{}]\n[a.b]",
		"extend inline by header":  "a = {}\n[a.b]",
		"extend inline by dotted":  "a = {}\na.b = 1",
		"replace inline by header": "a = {}\n[a]",
		"dotted into header table": "[a.b.c]\n[a]\nb.c.t = 1",
		"duplicate in inline":      "a = {b = 1, b = 2}",
		"inline trailing comma":    "a = {b = 1,}",
		"inline newline":           "a = {b = 1\n}",
		"inline missing comma":     "a = {b = 1 c = 2}",
		"inline missing equals":    "a = {b}",
		"inline only comma":        "a = {,}",
		"array missing comma":      "a = [1 2]",
		"array unclosed":           "a = [1",
		"array only comma":         "a = [,]",
		"too deep":                 "a = " + strings.Repeat("[", maxDepth+1) + strings.Repeat("]", maxDepth+1),
		"too deep key":             strings.TrimSuffix(strings.Repeat("a.", maxDepth+1), ".") + " = 1",
		"too deep header":          "[" + strings.TrimSuffix(strings.Repeat("a.", maxDepth+1), ".") + "]",
		"too large":                "# " + strings.Repeat("x", maxInputBytes),
		"invalid utf-8":            "a = \"\xff\"",
		"bare cr":                  "a = 1\rb = 2",
		"control in comment":       "# a\x01b",
		"del in comment":           "# a\x7fb",
		"bare cr in comment":       "# a\rb",
		"unclosed basic":           `a = "x`,
		"newline in basic":         "a = \"x\ny\"",
		"control in basic":         "a = \"\x01\"",
		"del in basic":             "a = \"\x7f\"",
		"bad escape":               `a = "\q"`,
		"short unicode":            `a = "\u12"`,
		"non-hex unicode":          `a = "\u12GZ"`,
		"surrogate":                `a = "\uD800"`,
		"beyond unicode":           `a = "\U00110000"`,
		"escape at end":            `a = "\`,
		"unclosed literal":         `a = 'x`,
		"newline in literal":       "a = 'x\ny'",
		"control in literal":       "a = '\x01'",
		"unclosed multi basic":     `a = """x`,
		"six quotes after text":    `a = """x""""""`,
		"control in multi":         "a = \"\"\"\x01\"\"\"",
		"bare cr in multi":         "a = \"\"\"\rb\"\"\"",
		"bad continuation":         "a = \"\"\"\\ x\"\"\"",
		"unclosed multi literal":   `a = '''x`,
		"control in multi literal": "a = '''\x01'''",
		"six single quotes":        `a = '''x''''''`,
		"bad key char":             "a$ = 1",
		"unclosed quoted key":      `"a = 1`,
		"multi-line key":           `"""a""" = 1`,
		"literal multi-line key":   `'''a''' = 1`,
		"leading zero":             "a = 01",
		"signed leading zero":      "a = +01",
		"double underscore":        "a = 1__0",
		"leading underscore":       "a = _1",
		"trailing underscore":      "a = 1_",
		"overflow":                 "a = 9223372036854775808",
		"hex overflow":             "a = 0xFFFFFFFFFFFFFFFF",
		"signed hex":               "a = +0x1",
		"bad hex digit":            "a = 0xG",
		"empty hex":                "a = 0x",
		"bad octal digit":          "a = 0o8",
		"bad binary digit":         "a = 0b2",
		"bare word":                "a = hello",
		"no int part":              "a = .5",
		"no fraction":              "a = 1.",
		"no exponent":              "a = 1e",
		"signed empty exponent":    "a = 1e+",
		"leading zero float":       "a = 01.5",
		"float overflow":           "a = 1e999",
		"capital inf":              "a = Inf",
		"capital bool":             "a = True",
		"bad month":                "a = 2020-13-01",
		"zero month":               "a = 2020-00-01",
		"bad day":                  "a = 2021-02-29",
		"century not leap":         "a = 1900-02-29",
		"zero day":                 "a = 2020-01-00",
		"day 31 of april":          "a = 2020-04-31",
		"two signs":                "a = +-1",
		"short date":               "a = 2020-1-01",
		"bad date separator":       "a = 2020-01-01X07:32:00",
		"bad hour":                 "a = 24:00:00",
		"bad minute":               "a = 07:60:00",
		"bad second":               "a = 07:32:61",
		"no seconds":               "a = 07:32",
		"letters in time":          "a = 07:3a:00",
		"empty fraction":           "a = 07:32:00.",
		"bad fraction":             "a = 07:32:00.x",
		"offset on time":           "a = 07:32:00Z",
		"bad offset":               "a = 1979-05-27T07:32:00+25:00",
		"bad offset minute":        "a = 1979-05-27T07:32:00+01:60",
		"short offset":             "a = 1979-05-27T07:32:00+01",
		"junk after date":          "a = 1979-05-27x",
		"date space word":          "a = 1979-05-27 7",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Decode([]byte(src))
			var syntax *Error
			if !errors.As(err, &syntax) {
				t.Fatalf("Decode(%.60q) error = %v, want *Error", src, err)
			}
			if syntax.Line < 1 || syntax.Column < 1 || !strings.HasPrefix(syntax.Error(), "toml: line ") {
				t.Fatalf("bad position in %q", syntax.Error())
			}
		})
	}
}

func TestErrorPosition(t *testing.T) {
	_, err := Decode([]byte("a = 1\nb = é$\n"))
	var syntax *Error
	if !errors.As(err, &syntax) || syntax.Line != 2 || syntax.Column != 5 {
		t.Fatalf("err = %#v, want line 2 column 5", err)
	}
}

func TestDecodeAcceptsDepthLimit(t *testing.T) {
	// The value sits one level under the root, so maxDepth-1 brackets reach
	// exactly the limit.
	n := maxDepth - 1
	mustDecode(t, "a = "+strings.Repeat("[", n)+strings.Repeat("]", n))
}

func TestSampleFileRoundTripsByteForByte(t *testing.T) {
	src, err := os.ReadFile("testdata/sample-config.toml")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Encode(); !bytes.Equal(got, src) {
		t.Fatalf("round trip changed the file:\n%s", got)
	}
	for _, c := range []struct {
		path []string
		want string
	}{
		{[]string{"radio", "callsign"}, "ORCH-7"},
		{[]string{"sensors", "soil", "kind"}, "capacitive"},
		{[]string{"elevation", "datum"}, "EGM96"},
	} {
		if got, ok := doc.Tree().Lookup(c.path...); !ok || got.Str != c.want {
			t.Errorf("Lookup(%v) = %+v, %v", c.path, got, ok)
		}
	}
}

func TestParseKeepsEveryByte(t *testing.T) {
	for _, src := range []string{
		"",
		"\n\n",
		"# only a comment",
		"\xef\xbb\xbf# bom\n",
		"  a  =  1  # c\r\n\t[ x ]  # h\n",
		"a = [\n  1, # one\n  # alone\n  2,\n]\n",
		"[[x]]\n# between\n[[x]] # after",
		"s = \"\"\"\n# not a comment\n\"\"\"\n",
	} {
		doc, err := Parse([]byte(src))
		if err != nil {
			t.Fatalf("Parse(%q): %v", src, err)
		}
		if got := string(doc.Encode()); got != src {
			t.Errorf("Encode() = %q, want %q", got, src)
		}
	}
}
