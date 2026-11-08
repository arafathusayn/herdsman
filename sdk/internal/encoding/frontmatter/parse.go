// Package frontmatter splits a Markdown file into its metadata block and its
// body. A block between --- fences is YAML; one between +++ fences is TOML.
package frontmatter

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/arafathusayn/herdsman/sdk/internal/encoding/toml"
	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
	"github.com/arafathusayn/herdsman/sdk/internal/encoding/yaml"
)

// Format names the language of a metadata block.
type Format string

// The formats. None means the file has no metadata block.
const (
	None Format = ""
	YAML Format = "yaml"
	TOML Format = "toml"
)

const bom = "\xef\xbb\xbf"

// Document is a parsed file. Meta is always a table, empty when the file has
// no block; Raw is the block's source and Body everything after it.
type Document struct {
	Format Format
	Meta   *tree.Node
	Raw    string
	Body   []byte
}

// nextLine splits off the first line and returns it without its line ending,
// with the rest of the input.
func nextLine(data []byte) (line, rest []byte) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return bytes.TrimSuffix(data[:i], []byte("\r")), data[i+1:]
	}
	return data, nil
}

// isFence reports whether a line is exactly the fence, allowing trailing
// blanks.
func isFence(line []byte, fence string) bool {
	return strings.TrimRight(string(line), " \t") == fence
}

// Parse splits data. A file that does not open with a fence on its first line
// has no block; a block that opens and never closes is an error.
func Parse(data []byte) (*Document, error) {
	input := bytes.TrimPrefix(data, []byte(bom))
	first, rest := nextLine(input)
	var format Format
	var fence string
	switch {
	case isFence(first, "---"):
		format, fence = YAML, "---"
	case isFence(first, "+++"):
		format, fence = TOML, "+++"
	}
	if format == None || rest == nil {
		return &Document{Meta: tree.NewTable(), Body: data}, nil
	}

	blockStart := len(input) - len(rest)
	for remaining := rest; remaining != nil; {
		lineStart := len(input) - len(remaining)
		var line []byte
		line, remaining = nextLine(remaining)
		if !isFence(line, fence) {
			continue
		}
		raw := string(input[blockStart:lineStart])
		meta, err := decode(format, raw)
		if err != nil {
			return nil, err
		}
		return &Document{Format: format, Meta: meta, Raw: raw, Body: remaining}, nil
	}
	return nil, errors.New("frontmatter: no closing fence for the metadata block")
}

func decode(format Format, raw string) (*tree.Node, error) {
	var meta *tree.Node
	var err error
	if format == TOML {
		meta, err = toml.Decode([]byte(raw))
	} else {
		meta, err = yaml.Decode([]byte(raw))
	}
	if err != nil {
		return nil, fmt.Errorf("frontmatter: %w", err)
	}
	switch meta.Kind {
	case tree.Table:
		return meta, nil
	case tree.Null:
		return tree.NewTable(), nil
	}
	return nil, fmt.Errorf("frontmatter: the metadata block must be a mapping, not a %s", meta.Kind)
}
