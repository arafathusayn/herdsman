package yaml

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
)

var plainKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// Encode writes a tree as block YAML, each string double-quoted.
func Encode(root *tree.Node) []byte {
	var b strings.Builder
	writeTable(&b, root, 0)
	return []byte(b.String())
}

func keyText(key string) string {
	if plainKey.MatchString(key) && !reservedWord(key) {
		return key
	}
	return quote(key)
}

// reservedWord reports a word a plain scalar would read as something else.
func reservedWord(word string) bool {
	switch strings.ToLower(word) {
	case "null", "true", "false", "y", "n", "yes", "no", "on", "off":
		return true
	}
	return false
}

// quote writes a double-quoted scalar, whose escapes match JSON's for
// everything this writer can produce.
func quote(s string) string {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(s); err != nil {
		return strconv.Quote(s)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func floatText(f float64) string {
	switch {
	case math.IsNaN(f):
		return ".nan"
	case math.IsInf(f, 1):
		return ".inf"
	case math.IsInf(f, -1):
		return "-.inf"
	}
	text := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(text, ".e") {
		text += ".0"
	}
	return text
}

func scalarText(node *tree.Node) string {
	switch node.Kind {
	case tree.Integer:
		return strconv.FormatInt(node.Int, 10)
	case tree.Float:
		return floatText(node.Flt)
	case tree.Bool:
		return strconv.FormatBool(node.Bool)
	case tree.Null:
		return "null"
	default:
		return quote(node.Str)
	}
}

func writeTable(b *strings.Builder, table *tree.Node, indent int) {
	pad := strings.Repeat(" ", indent)
	for _, key := range table.Keys() {
		value, _ := table.Get(key)
		b.WriteString(pad + keyText(key) + ":")
		writeValue(b, value, indent)
	}
}

// writeValue finishes the line after a key or list dash at indent, nesting a
// non-empty collection below it.
func writeValue(b *strings.Builder, value *tree.Node, indent int) {
	switch {
	case value.Kind == tree.Table && value.Len() == 0:
		b.WriteString(" {}\n")
	case value.Kind == tree.Array && len(value.Items) == 0:
		b.WriteString(" []\n")
	case value.Kind == tree.Table:
		b.WriteString("\n")
		writeTable(b, value, indent+2)
	case value.Kind == tree.Array:
		b.WriteString("\n")
		pad := strings.Repeat(" ", indent+2)
		for _, item := range value.Items {
			b.WriteString(pad + "-")
			writeValue(b, item, indent+2)
		}
	default:
		b.WriteString(" " + scalarText(value) + "\n")
	}
}
