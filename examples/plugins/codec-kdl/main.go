// Command codec-kdl is an example config codec for a small part of KDL: one
// node per line, a string, number or boolean value, and children in braces.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/arafathusayn/herdsman/sdk/plugin"
	pluginv1 "github.com/arafathusayn/herdsman/sdk/plugin/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

var keyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

type codec struct{}

func (codec) Decode(_ context.Context, r *pluginv1.DecodeRequest) (*pluginv1.DecodeResponse, error) {
	lines := strings.Split(string(r.GetSource()), "\n")
	tree, _, err := parseBlock(lines, 0, true)
	if err != nil {
		return nil, rpc.Errorf(rpc.InvalidArgument, "%s: %v", r.GetPath(), err)
	}
	data, err := json.Marshal(tree)
	if err != nil {
		return nil, err
	}
	return &pluginv1.DecodeResponse{Json: data}, nil
}

// parseBlock reads nodes from line start until the end of the file at the top
// level or a closing brace below it, and returns the next line to read.
func parseBlock(lines []string, start int, top bool) (map[string]any, int, error) {
	tree := map[string]any{}
	for i := start; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if line == "}" {
			if top {
				return nil, 0, fmt.Errorf("line %d: unexpected }", i+1)
			}
			return tree, i + 1, nil
		}
		key, rest, _ := strings.Cut(line, " ")
		if !keyPattern.MatchString(key) {
			return nil, 0, fmt.Errorf("line %d: bad node name", i+1)
		}
		rest = strings.TrimSpace(rest)
		if rest == "{" {
			child, next, err := parseBlock(lines, i+1, false)
			if err != nil {
				return nil, 0, err
			}
			tree[key] = child
			i = next - 1
			continue
		}
		value, err := parseValue(rest)
		if err != nil {
			return nil, 0, fmt.Errorf("line %d: %w", i+1, err)
		}
		tree[key] = value
	}
	if !top {
		return nil, 0, fmt.Errorf("missing }")
	}
	return tree, len(lines), nil
}

func parseValue(text string) (any, error) {
	switch {
	case text == "true":
		return true, nil
	case text == "false":
		return false, nil
	case strings.HasPrefix(text, `"`):
		return strconv.Unquote(text)
	}
	return strconv.ParseFloat(text, 64)
}

func (codec) Encode(_ context.Context, r *pluginv1.EncodeRequest) (*pluginv1.EncodeResponse, error) {
	var tree map[string]any
	if err := json.Unmarshal(r.GetJson(), &tree); err != nil {
		return nil, rpc.Errorf(rpc.InvalidArgument, "the tree is not a JSON object")
	}
	var out strings.Builder
	if err := writeBlock(&out, tree, ""); err != nil {
		return nil, rpc.Errorf(rpc.InvalidArgument, "%v", err)
	}
	return &pluginv1.EncodeResponse{Source: []byte(out.String())}, nil
}

func writeBlock(out *strings.Builder, tree map[string]any, indent string) error {
	keys := make([]string, 0, len(tree))
	for key := range tree {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		switch value := tree[key].(type) {
		case map[string]any:
			fmt.Fprintf(out, "%s%s {\n", indent, key)
			if err := writeBlock(out, value, indent+"  "); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s}\n", indent)
		case string:
			fmt.Fprintf(out, "%s%s %s\n", indent, key, strconv.Quote(value))
		case bool:
			fmt.Fprintf(out, "%s%s %t\n", indent, key, value)
		case float64:
			fmt.Fprintf(out, "%s%s %s\n", indent, key, strconv.FormatFloat(value, 'f', -1, 64))
		default:
			return fmt.Errorf("%s: this example codec cannot write that value", key)
		}
	}
	return nil
}

// Set is optional, and this example leaves it out.
func (codec) Set(context.Context, *pluginv1.SetRequest) (*pluginv1.SetResponse, error) {
	return nil, rpc.Errorf(rpc.Unimplemented, "codec-kdl has no format-preserving edit")
}

func main() {
	manifest := &pluginv1.Manifest{
		Name: "codec-kdl", Version: "0.1.0", Description: "An example config codec for a small part of KDL.",
		ExtensionPoints: []pluginv1.ExtensionPoint{pluginv1.ExtensionPoint_EXTENSION_POINT_CONFIG_CODEC},
		FileExtensions:  []string{"kdl"},
	}
	if err := plugin.Serve(context.Background(), manifest, plugin.Extensions{ConfigCodec: codec{}}); err != nil {
		log.Fatal(err)
	}
}
