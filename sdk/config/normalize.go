package config

import (
	"fmt"
	"strings"

	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const hookSpecName = "herdsman.config.v1.HookSpec"

// canonicalKey is the proto field name a written key stands for.
func canonicalKey(key string) string { return strings.ReplaceAll(key, "-", "_") }

func joinKey(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// normalize copies a decoded table into canonical field names, reporting keys
// the message does not have. Map keys and hook names are the user's own text
// and pass through unchanged.
func normalize(in *tree.Node, md protoreflect.MessageDescriptor, prefix string, safety bool, rep *report) (*tree.Node, error) {
	if in.Kind != tree.Table {
		return nil, fmt.Errorf("%s: expected a table, found %s", displayKey(prefix), in.Kind)
	}
	out := tree.NewTable()
	for _, key := range in.Keys() {
		value, _ := in.Get(key)
		name := canonicalKey(key)
		path := joinKey(prefix, name)
		fd := md.Fields().ByName(protoreflect.Name(name))
		if fd == nil {
			if err := rep.unknown(joinKey(prefix, key), key, md, safety); err != nil {
				return nil, err
			}
			continue
		}
		if _, dup := out.Get(name); dup {
			return nil, fmt.Errorf("%s: written twice under different spellings", path)
		}
		converted, err := normalizeField(value, fd, path, safety || isSafetySection(fd), rep)
		if err != nil {
			return nil, err
		}
		out.Set(name, converted)
	}
	return out, nil
}

func normalizeField(value *tree.Node, fd protoreflect.FieldDescriptor, path string, safety bool, rep *report) (*tree.Node, error) {
	switch {
	case fd.IsMap():
		if value.Kind != tree.Table {
			return nil, fmt.Errorf("%s: expected a table, found %s", path, value.Kind)
		}
		if fd.MapValue().Kind() != protoreflect.MessageKind {
			return value, nil
		}
		out := tree.NewTable()
		for _, key := range value.Keys() {
			item, _ := value.Get(key)
			converted, err := normalize(item, fd.MapValue().Message(), joinKey(path, key), safety, rep)
			if err != nil {
				return nil, err
			}
			out.Set(key, converted)
		}
		return out, nil
	case fd.Kind() == protoreflect.MessageKind && fd.IsList():
		if value.Kind != tree.Array {
			return nil, fmt.Errorf("%s: expected an array, found %s", path, value.Kind)
		}
		out := tree.NewArray()
		for i, item := range value.Items {
			converted, err := normalize(item, fd.Message(), fmt.Sprintf("%s[%d]", path, i), safety, rep)
			if err != nil {
				return nil, err
			}
			out.Items = append(out.Items, converted)
		}
		return out, nil
	case fd.Kind() == protoreflect.MessageKind && string(fd.Message().FullName()) != hookSpecName:
		return normalize(value, fd.Message(), path, safety, rep)
	default:
		return value, nil
	}
}

func displayKey(key string) string {
	if key == "" {
		return "the file"
	}
	return key
}

// rename is one key to rewrite to its canonical spelling.
type rename struct {
	from, to []string
	value    *tree.Node
}

// renames lists the keys under in that are not canonical, outermost first. A
// moved table is listed before the keys inside it, whose paths already use
// the table's new name, so the list must be applied in order.
func renames(in *tree.Node, md protoreflect.MessageDescriptor, written, canonical []string) []rename {
	var out []rename
	for _, key := range in.Keys() {
		value, _ := in.Get(key)
		name := canonicalKey(key)
		fd := md.Fields().ByName(protoreflect.Name(name))
		if fd == nil {
			continue // not ours to rewrite; an unknown key stays as written
		}
		from := append(append([]string{}, written...), key)
		to := append(append([]string{}, canonical...), name)
		if key != name {
			out = append(out, rename{from: from, to: to, value: value})
			from = to
		}
		out = append(out, fieldRenames(value, fd, from, to)...)
	}
	return out
}

func fieldRenames(value *tree.Node, fd protoreflect.FieldDescriptor, from, to []string) []rename {
	switch {
	case fd.IsMap():
		if value.Kind != tree.Table || fd.MapValue().Kind() != protoreflect.MessageKind {
			return nil
		}
		var out []rename
		for _, key := range value.Keys() {
			item, _ := value.Get(key)
			if item.Kind == tree.Table {
				out = append(out, renames(item, fd.MapValue().Message(), append(append([]string{}, from...), key), append(append([]string{}, to...), key))...)
			}
		}
		return out
	case fd.IsList() || fd.Kind() != protoreflect.MessageKind || string(fd.Message().FullName()) == hookSpecName || value.Kind != tree.Table:
		return nil
	default:
		return renames(value, fd.Message(), from, to)
	}
}
