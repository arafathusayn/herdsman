package config

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
)

// The places a value can come from, weakest first.
const (
	SourceDefault = "default"
	SourceUser    = "user"
	SourceProject = "project"
	SourceEnv     = "env"
	SourceFlag    = "flag"
)

// Shown is one effective setting and where it came from.
type Shown struct {
	Key    string
	Value  string
	Source string
}

// Show lists every effective value, sorted by key.
func (r *Result) Show() []Shown {
	var out []Shown
	var walk func(node *tree.Node, prefix string)
	walk = func(node *tree.Node, prefix string) {
		if node.Kind != tree.Table {
			out = append(out, Shown{Key: prefix, Value: renderNode(node), Source: r.Sources[prefix]})
			return
		}
		for _, key := range node.Keys() {
			item, _ := node.Get(key)
			walk(item, joinKey(prefix, key))
		}
	}
	walk(r.tree, "")
	slices.SortFunc(out, func(a, b Shown) int {
		switch {
		case a.Key < b.Key:
			return -1
		case a.Key > b.Key:
			return 1
		}
		return 0
	})
	return out
}

// Typed returns a dotted key's value as plain data and where it came from. A
// table whose entries came from several places reports "mixed".
func (r *Result) Typed(key string) (any, string, error) {
	resolved, err := resolveKey(key, configDescriptor())
	if err != nil {
		return nil, "", err
	}
	node, ok := r.tree.Lookup(resolved.segments...)
	if !ok {
		return nil, "", fmt.Errorf("%s: not set", key)
	}
	return plainValue(node), r.sourceOf(strings.Join(resolved.segments, ".")), nil
}

func (r *Result) sourceOf(key string) string {
	if source, ok := r.Sources[key]; ok {
		return source
	}
	found := ""
	for candidate, source := range r.Sources {
		if !strings.HasPrefix(candidate, key+".") {
			continue
		}
		if found != "" && found != source {
			return "mixed"
		}
		found = source
	}
	return found
}

// Encode writes the whole effective configuration in a text format.
func (r *Result) Encode(format string) ([]byte, error) { return encodeText(format, r.tree) }

// renderNode is a value as one line of text: a scalar bare, anything else as
// JSON.
func renderNode(node *tree.Node) string {
	switch node.Kind {
	case tree.String:
		return node.Str
	case tree.Integer:
		return strconv.FormatInt(node.Int, 10)
	case tree.Bool:
		return strconv.FormatBool(node.Bool)
	default:
		data, err := json.Marshal(plainValue(node))
		if err != nil {
			return fmt.Sprintf("<%s>", node.Kind)
		}
		return string(data)
	}
}

func plainValue(node *tree.Node) any {
	switch node.Kind {
	case tree.Table:
		out := map[string]any{}
		for _, key := range node.Keys() {
			item, _ := node.Get(key)
			out[key] = plainValue(item)
		}
		return out
	case tree.Array:
		out := make([]any, 0, len(node.Items))
		for _, item := range node.Items {
			out = append(out, plainValue(item))
		}
		return out
	case tree.Integer:
		return node.Int
	case tree.Float:
		return node.Flt
	case tree.Bool:
		return node.Bool
	case tree.Null:
		return nil
	default:
		return node.Str
	}
}
