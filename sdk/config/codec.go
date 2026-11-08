package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/arafathusayn/herdsman/sdk/internal/encoding/toml"
	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
	"github.com/arafathusayn/herdsman/sdk/internal/encoding/yaml"
)

// The formats a file can be in.
const (
	FormatTOML = "toml"
	FormatYAML = "yaml"
	FormatJSON = "json"
	FormatBin  = "bin"
)

// formatOf names a file's format from its extension.
func formatOf(path string) (string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		return FormatTOML, nil
	case ".yaml", ".yml":
		return FormatYAML, nil
	case ".json":
		return FormatJSON, nil
	case ".bin":
		return FormatBin, nil
	default:
		return "", fmt.Errorf("%s: unknown configuration format", path)
	}
}

// decodeText reads one of the three text formats.
func decodeText(format string, data []byte) (*tree.Node, error) {
	switch format {
	case FormatTOML:
		return toml.Decode(data)
	case FormatYAML:
		if len(bytes.TrimSpace(data)) == 0 {
			return tree.NewTable(), nil
		}
		root, err := yaml.Decode(data)
		if err != nil {
			return nil, err
		}
		if root.Kind == tree.Null {
			return tree.NewTable(), nil
		}
		return root, nil
	case FormatJSON:
		return decodeJSON(data)
	default:
		return nil, fmt.Errorf("%s is not a text format", format)
	}
}

// encodeText writes one of the three text formats.
func encodeText(format string, root *tree.Node) ([]byte, error) {
	switch format {
	case FormatTOML:
		return toml.Encode(root)
	case FormatYAML:
		var b strings.Builder
		writeYAMLTable(&b, root, 0)
		return []byte(b.String()), nil
	case FormatJSON:
		var b bytes.Buffer
		if err := writeJSON(&b, root, ""); err != nil {
			return nil, err
		}
		b.WriteByte('\n')
		return b.Bytes(), nil
	default:
		return nil, fmt.Errorf("%s is not a text format", format)
	}
}

// decodeJSON builds a tree with the token reader, which keeps integers exact
// and sees a duplicate key; the two-word forms of a hook are not expressible
// through the message's own JSON mapping.
func decodeJSON(data []byte) (*tree.Node, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	root, err := readJSON(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected content after the JSON value")
	}
	return root, nil
}

func readJSON(decoder *json.Decoder) (*tree.Node, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch value := token.(type) {
	case json.Delim:
		if value == '[' {
			out := tree.NewArray()
			for decoder.More() {
				item, err := readJSON(decoder)
				if err != nil {
					return nil, err
				}
				out.Items = append(out.Items, item)
			}
			_, err := decoder.Token()
			return out, err
		}
		out := tree.NewTable()
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key := keyToken.(string) // the decoder only yields strings for keys
			if _, dup := out.Get(key); dup {
				return nil, fmt.Errorf("duplicate key %q", key)
			}
			item, err := readJSON(decoder)
			if err != nil {
				return nil, err
			}
			out.Set(key, item)
		}
		_, err := decoder.Token()
		return out, err
	case string:
		return tree.NewString(value), nil
	case bool:
		return tree.NewBool(value), nil
	case json.Number:
		if i, err := strconv.ParseInt(value.String(), 10, 64); err == nil {
			return tree.NewInt(i), nil
		}
		f, err := value.Float64()
		if err != nil {
			return nil, err
		}
		return tree.NewFloat(f), nil
	default:
		return tree.NewNull(), nil
	}
}

func writeJSONString(b *bytes.Buffer, s string) error {
	encoder := json.NewEncoder(b)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(s); err != nil {
		return err
	}
	b.Truncate(b.Len() - 1) // Encode ends with a newline
	return nil
}

func writeJSON(b *bytes.Buffer, node *tree.Node, indent string) error {
	inner := indent + "  "
	switch node.Kind {
	case tree.Table:
		if node.Len() == 0 {
			b.WriteString("{}")
			return nil
		}
		b.WriteString("{\n")
		for i, key := range node.Keys() {
			if i > 0 {
				b.WriteString(",\n")
			}
			b.WriteString(inner)
			if err := writeJSONString(b, key); err != nil {
				return err
			}
			b.WriteString(": ")
			item, _ := node.Get(key)
			if err := writeJSON(b, item, inner); err != nil {
				return err
			}
		}
		b.WriteString("\n" + indent + "}")
	case tree.Array:
		if len(node.Items) == 0 {
			b.WriteString("[]")
			return nil
		}
		b.WriteString("[\n")
		for i, item := range node.Items {
			if i > 0 {
				b.WriteString(",\n")
			}
			b.WriteString(inner)
			if err := writeJSON(b, item, inner); err != nil {
				return err
			}
		}
		b.WriteString("\n" + indent + "]")
	case tree.String:
		return writeJSONString(b, node.Str)
	default:
		b.WriteString(renderNode(node))
	}
	return nil
}

var plainYAMLKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

func yamlKey(key string) string {
	if plainYAMLKey.MatchString(key) && !yamlReserved(key) {
		return key
	}
	return yamlQuote(key)
}

// yamlReserved reports a word a plain scalar would read as something else.
func yamlReserved(word string) bool {
	switch strings.ToLower(word) {
	case "null", "true", "false", "y", "n", "yes", "no", "on", "off":
		return true
	}
	return false
}

// yamlQuote writes a double-quoted scalar, whose escapes match JSON's for
// everything this writer can produce.
func yamlQuote(s string) string {
	var b bytes.Buffer
	if err := writeJSONString(&b, s); err != nil {
		return strconv.Quote(s)
	}
	return b.String()
}

func yamlScalar(node *tree.Node) string {
	if node.Kind == tree.String {
		return yamlQuote(node.Str)
	}
	return renderNode(node)
}

func writeYAMLTable(b *strings.Builder, table *tree.Node, depth int) {
	pad := strings.Repeat("  ", depth)
	for _, key := range table.Keys() {
		value, _ := table.Get(key)
		b.WriteString(pad + yamlKey(key) + ":")
		writeYAMLValue(b, value, depth)
	}
}

// writeYAMLValue finishes the line after a key or list dash, nesting a
// non-empty collection below it.
func writeYAMLValue(b *strings.Builder, value *tree.Node, depth int) {
	switch {
	case value.Kind == tree.Table && value.Len() == 0:
		b.WriteString(" {}\n")
	case value.Kind == tree.Array && len(value.Items) == 0:
		b.WriteString(" []\n")
	case value.Kind == tree.Table:
		b.WriteString("\n")
		writeYAMLTable(b, value, depth+1)
	case value.Kind == tree.Array:
		b.WriteString("\n")
		pad := strings.Repeat("  ", depth+1)
		for _, item := range value.Items {
			b.WriteString(pad + "-")
			writeYAMLValue(b, item, depth+1)
		}
	default:
		b.WriteString(" " + yamlScalar(value) + "\n")
	}
}
