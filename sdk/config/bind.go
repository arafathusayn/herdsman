package config

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
	"google.golang.org/protobuf/reflect/protoreflect"
)

var upperBoundary = regexp.MustCompile(`([a-z0-9])([A-Z])`)

// enumPrefix is what every value of an enum starts with.
func enumPrefix(ed protoreflect.EnumDescriptor) string {
	return strings.ToUpper(upperBoundary.ReplaceAllString(string(ed.Name()), "${1}_${2}")) + "_"
}

// enumText is the spelling of a value in a file: squash, on-clean.
func enumText(ed protoreflect.EnumDescriptor, value protoreflect.EnumNumber) string {
	v := ed.Values().ByNumber(value)
	if v == nil {
		return strconv.Itoa(int(value))
	}
	return strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(string(v.Name()), enumPrefix(ed))), "_", "-")
}

func enumChoices(ed protoreflect.EnumDescriptor) []string {
	var out []string
	values := ed.Values()
	for i := range values.Len() {
		if number := values.Get(i).Number(); number != 0 {
			out = append(out, enumText(ed, number))
		}
	}
	return out
}

func enumNumber(ed protoreflect.EnumDescriptor, text string) (protoreflect.EnumNumber, bool) {
	name := enumPrefix(ed) + strings.ToUpper(canonicalKey(text))
	v := ed.Values().ByName(protoreflect.Name(name))
	if v == nil || v.Number() == 0 {
		return 0, false
	}
	return v.Number(), true
}

// bindMessage fills msg from a canonical table.
func bindMessage(in *tree.Node, msg protoreflect.Message, prefix string) error {
	md := msg.Descriptor()
	for _, key := range in.Keys() {
		value, _ := in.Get(key)
		fd := md.Fields().ByName(protoreflect.Name(key))
		if fd == nil {
			return fmt.Errorf("%s: no such field", joinKey(prefix, key))
		}
		if err := bindField(value, msg, fd, joinKey(prefix, key)); err != nil {
			return err
		}
	}
	return nil
}

func bindField(value *tree.Node, msg protoreflect.Message, fd protoreflect.FieldDescriptor, path string) error {
	switch {
	case fd.IsMap():
		return bindMap(value, msg, fd, path)
	case fd.IsList():
		if value.Kind != tree.Array {
			return fmt.Errorf("%s: expected an array, found %s", path, value.Kind)
		}
		list := msg.Mutable(fd).List()
		for i, item := range value.Items {
			element := fmt.Sprintf("%s[%d]", path, i)
			if fd.Kind() == protoreflect.MessageKind {
				entry := list.NewElement()
				if err := bindMessage(item, entry.Message(), element); err != nil {
					return err
				}
				list.Append(entry)
				continue
			}
			scalar, err := bindScalar(item, fd, element)
			if err != nil {
				return err
			}
			list.Append(scalar)
		}
		return nil
	case fd.Kind() == protoreflect.MessageKind:
		if string(fd.Message().FullName()) == hookSpecName {
			return bindHook(value, msg.Mutable(fd).Message().Interface().(*configv1.HookSpec), path)
		}
		return bindMessage(value, msg.Mutable(fd).Message(), path)
	default:
		scalar, err := bindScalar(value, fd, path)
		if err != nil {
			return err
		}
		msg.Set(fd, scalar)
		return nil
	}
}

func bindMap(value *tree.Node, msg protoreflect.Message, fd protoreflect.FieldDescriptor, path string) error {
	if value.Kind != tree.Table {
		return fmt.Errorf("%s: expected a table, found %s", path, value.Kind)
	}
	entries := msg.Mutable(fd).Map()
	for _, key := range value.Keys() {
		item, _ := value.Get(key)
		entry := joinKey(path, key)
		mapKey := protoreflect.ValueOfString(key).MapKey()
		if fd.MapValue().Kind() == protoreflect.MessageKind {
			held := entries.NewValue()
			if err := bindMessage(item, held.Message(), entry); err != nil {
				return err
			}
			entries.Set(mapKey, held)
			continue
		}
		scalar, err := bindScalar(item, fd.MapValue(), entry)
		if err != nil {
			return err
		}
		entries.Set(mapKey, scalar)
	}
	return nil
}

func bindScalar(value *tree.Node, fd protoreflect.FieldDescriptor, path string) (protoreflect.Value, error) {
	mismatch := func(want string) (protoreflect.Value, error) {
		return protoreflect.Value{}, fmt.Errorf("%s: expected %s, found %s", path, want, value.Kind)
	}
	switch fd.Kind() {
	case protoreflect.StringKind:
		if value.Kind != tree.String {
			return mismatch("a string")
		}
		return protoreflect.ValueOfString(value.Str), nil
	case protoreflect.BoolKind:
		if value.Kind != tree.Bool {
			return mismatch("true or false")
		}
		return protoreflect.ValueOfBool(value.Bool), nil
	case protoreflect.Int32Kind:
		if value.Kind != tree.Integer {
			return mismatch("an integer")
		}
		if value.Int < math.MinInt32 || value.Int > math.MaxInt32 {
			return protoreflect.Value{}, fmt.Errorf("%s: %d is out of range", path, value.Int)
		}
		return protoreflect.ValueOfInt32(int32(value.Int)), nil
	case protoreflect.Int64Kind:
		if value.Kind != tree.Integer {
			return mismatch("an integer")
		}
		return protoreflect.ValueOfInt64(value.Int), nil
	case protoreflect.EnumKind:
		if value.Kind != tree.String {
			return mismatch("a string")
		}
		number, ok := enumNumber(fd.Enum(), value.Str)
		if !ok {
			return protoreflect.Value{}, fmt.Errorf("%s: %q is not one of %s", path, value.Str, strings.Join(enumChoices(fd.Enum()), ", "))
		}
		return protoreflect.ValueOfEnum(number), nil
	default:
		return protoreflect.Value{}, fmt.Errorf("%s: unsupported field type %s", path, fd.Kind())
	}
}

// namedCommands reads a table of command strings.
func namedCommands(value *tree.Node, path string) (*configv1.NamedCommands, error) {
	named := &configv1.NamedCommands{Commands: map[string]string{}}
	for _, key := range value.Keys() {
		item, _ := value.Get(key)
		if item.Kind != tree.String {
			return nil, fmt.Errorf("%s: expected a command string, found %s", joinKey(path, key), item.Kind)
		}
		named.Commands[key] = item.Str
	}
	return named, nil
}

// bindHook reads the three written forms of a hook.
func bindHook(value *tree.Node, hook *configv1.HookSpec, path string) error {
	switch value.Kind {
	case tree.String:
		hook.Form = &configv1.HookSpec_Command{Command: value.Str}
		return nil
	case tree.Table:
		named, err := namedCommands(value, path)
		if err != nil {
			return err
		}
		hook.Form = &configv1.HookSpec_Named{Named: named}
		return nil
	case tree.Array:
		pipeline := &configv1.Pipeline{}
		for i, item := range value.Items {
			element := fmt.Sprintf("%s[%d]", path, i)
			stage := &configv1.PipelineStage{}
			switch item.Kind {
			case tree.String:
				stage.Form = &configv1.PipelineStage_Command{Command: item.Str}
			case tree.Table:
				named, err := namedCommands(item, element)
				if err != nil {
					return err
				}
				stage.Form = &configv1.PipelineStage_Named{Named: named}
			default:
				return fmt.Errorf("%s: expected a command or a table of commands, found %s", element, item.Kind)
			}
			pipeline.Stages = append(pipeline.Stages, stage)
		}
		hook.Form = &configv1.HookSpec_Pipeline{Pipeline: pipeline}
		return nil
	default:
		return fmt.Errorf("%s: a hook is a string, a table or an array, found %s", path, value.Kind)
	}
}

// messageTree is the inverse of bindMessage: every populated field, in field
// order, so that the output is the same on every run.
func messageTree(msg protoreflect.Message) *tree.Node {
	out := tree.NewTable()
	fields := msg.Descriptor().Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if msg.Has(fd) {
			out.Set(string(fd.Name()), fieldTree(msg.Get(fd), fd))
		}
	}
	return out
}

func fieldTree(value protoreflect.Value, fd protoreflect.FieldDescriptor) *tree.Node {
	switch {
	case fd.IsMap():
		out := tree.NewTable()
		var keys []string
		value.Map().Range(func(key protoreflect.MapKey, _ protoreflect.Value) bool {
			keys = append(keys, key.String())
			return true
		})
		slices.Sort(keys)
		for _, key := range keys {
			out.Set(key, singleTree(value.Map().Get(protoreflect.ValueOfString(key).MapKey()), fd.MapValue()))
		}
		return out
	case fd.IsList():
		out := tree.NewArray()
		for i := range value.List().Len() {
			out.Items = append(out.Items, singleTree(value.List().Get(i), fd))
		}
		return out
	default:
		return singleTree(value, fd)
	}
}

func singleTree(value protoreflect.Value, fd protoreflect.FieldDescriptor) *tree.Node {
	switch fd.Kind() {
	case protoreflect.StringKind:
		return tree.NewString(value.String())
	case protoreflect.BoolKind:
		return tree.NewBool(value.Bool())
	case protoreflect.Int32Kind, protoreflect.Int64Kind:
		return tree.NewInt(value.Int())
	case protoreflect.EnumKind:
		return tree.NewString(enumText(fd.Enum(), value.Enum()))
	default:
		if hook, ok := value.Message().Interface().(*configv1.HookSpec); ok {
			return hookTree(hook)
		}
		return messageTree(value.Message())
	}
}

func namedTree(named *configv1.NamedCommands) *tree.Node {
	out := tree.NewTable()
	keys := make([]string, 0, len(named.GetCommands()))
	for key := range named.GetCommands() {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		out.Set(key, tree.NewString(named.GetCommands()[key]))
	}
	return out
}

func hookTree(hook *configv1.HookSpec) *tree.Node {
	switch form := hook.GetForm().(type) {
	case *configv1.HookSpec_Command:
		return tree.NewString(form.Command)
	case *configv1.HookSpec_Named:
		return namedTree(form.Named)
	case *configv1.HookSpec_Pipeline:
		out := tree.NewArray()
		for _, stage := range form.Pipeline.GetStages() {
			if named := stage.GetNamed(); named != nil {
				out.Items = append(out.Items, namedTree(named))
				continue
			}
			out.Items = append(out.Items, tree.NewString(stage.GetCommand()))
		}
		return out
	default:
		return tree.NewTable()
	}
}

// resolved is a dotted key matched against the message.
type resolved struct {
	segments []string
	field    protoreflect.FieldDescriptor
	// hookEntry is true for one named command inside a hook, and mapEntry for
	// one value of a map; either way the value's type is not the field's.
	hookEntry bool
	mapEntry  bool
}

// splitKey cuts a dotted key, honouring double quotes around a segment.
func splitKey(key string) ([]string, error) {
	var out []string
	var current strings.Builder
	quoted := false
	for _, r := range key {
		switch {
		case r == '"':
			quoted = !quoted
		case r == '.' && !quoted:
			out = append(out, current.String())
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	if quoted {
		return nil, fmt.Errorf("%s: unbalanced quote", key)
	}
	out = append(out, current.String())
	if slices.Contains(out, "") {
		return nil, fmt.Errorf("%q: empty key segment", key)
	}
	return out, nil
}

// resolveKey matches a dotted key to the message and spells it canonically.
func resolveKey(key string, md protoreflect.MessageDescriptor) (*resolved, error) {
	parts, err := splitKey(key)
	if err != nil {
		return nil, err
	}
	return resolveParts(key, parts, md, canonicalKey)
}

// envFieldName is the field an upper-case variable segment stands for.
func envFieldName(part string) string { return canonicalKey(strings.ToLower(part)) }

// inSafetySection reports whether a path runs through policy or launch, so
// that an unknown key there can stop the command. Only field names are folded:
// the segment after a map is the user's own text.
func inSafetySection(parts []string, md protoreflect.MessageDescriptor, name func(string) string) bool {
	for i := 0; i < len(parts); i++ {
		fd := md.Fields().ByName(protoreflect.Name(name(parts[i])))
		switch {
		case fd == nil:
			return false
		case isSafetySection(fd):
			return true
		case isMessageMap(fd):
			md = fd.MapValue().Message()
			i++
		case isSingularMessage(fd):
			md = fd.Message()
		default:
			return false
		}
	}
	return false
}

// resolveParts walks segments through the message. fieldName maps a written
// segment to a field name; a map key or a hook's own name is kept as written.
func resolveParts(key string, parts []string, md protoreflect.MessageDescriptor, fieldName func(string) string) (*resolved, error) {
	if slices.Contains(parts, "") {
		return nil, fmt.Errorf("%q: empty key segment", key)
	}
	out := &resolved{}
	for i := 0; i < len(parts); i++ {
		name := fieldName(parts[i])
		fd := md.Fields().ByName(protoreflect.Name(name))
		if fd == nil {
			return nil, unknownKeyError(key, parts[i], md)
		}
		out.segments = append(out.segments, name)
		out.field = fd
		rest := parts[i+1:]
		switch {
		case fd.IsMap():
			if len(rest) == 0 {
				return out, nil
			}
			out.segments = append(out.segments, rest[0])
			if fd.MapValue().Kind() != protoreflect.MessageKind {
				if len(rest) > 1 {
					return nil, fmt.Errorf("%s: %s holds plain values", key, name)
				}
				out.mapEntry = true
				return out, nil
			}
			if len(rest) == 1 {
				return nil, fmt.Errorf("%s: name a key inside this entry", key)
			}
			md = fd.MapValue().Message()
			i++
		case fd.Kind() == protoreflect.MessageKind && string(fd.Message().FullName()) == hookSpecName:
			if len(rest) > 1 {
				return nil, fmt.Errorf("%s: a hook holds named commands, one level deep", key)
			}
			if len(rest) == 1 {
				out.segments = append(out.segments, rest[0])
				out.hookEntry = true
			}
			return out, nil
		case fd.Kind() == protoreflect.MessageKind && !fd.IsList():
			if len(rest) == 0 {
				return nil, fmt.Errorf("%s: name a key inside this section", key)
			}
			md = fd.Message()
		default:
			if len(rest) > 0 {
				return nil, fmt.Errorf("%s: %s has no inner keys", key, name)
			}
			return out, nil
		}
	}
	return out, nil
}

func unknownKeyError(key, part string, md protoreflect.MessageDescriptor) error {
	if s := suggest(part, md); s != "" {
		return fmt.Errorf("unknown key %s (did you mean %s?)", key, s)
	}
	return fmt.Errorf("unknown key %s", key)
}

// textNode turns the text of a flag or variable into the field's value.
func textNode(r *resolved, text string) (*tree.Node, error) {
	fd := r.field
	path := strings.Join(r.segments, ".")
	if r.hookEntry || r.mapEntry && fd.MapValue().Kind() == protoreflect.StringKind {
		return tree.NewString(text), nil
	}
	if fd.IsMap() {
		return nil, fmt.Errorf("%s: set one entry, as %s.<name>", path, path)
	}
	if fd.IsList() {
		if fd.Kind() != protoreflect.StringKind {
			return nil, fmt.Errorf("%s: this list cannot be set as text", path)
		}
		out := tree.NewArray()
		for item := range strings.SplitSeq(text, ",") {
			if item = strings.TrimSpace(item); item != "" {
				out.Items = append(out.Items, tree.NewString(item))
			}
		}
		return out, nil
	}
	switch fd.Kind() {
	case protoreflect.BoolKind:
		parsed, err := strconv.ParseBool(text)
		if err != nil {
			return nil, fmt.Errorf("%s: expected true or false, found %q", path, text)
		}
		return tree.NewBool(parsed), nil
	case protoreflect.Int32Kind, protoreflect.Int64Kind:
		parsed, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: expected an integer, found %q", path, text)
		}
		return tree.NewInt(parsed), nil
	case protoreflect.MessageKind:
		if string(fd.Message().FullName()) != hookSpecName {
			return nil, fmt.Errorf("%s: this section cannot be set as text", path)
		}
		return tree.NewString(text), nil
	default:
		return tree.NewString(text), nil
	}
}

// setPath stores value at path, creating tables on the way.
func setPath(root *tree.Node, path []string, value *tree.Node) error {
	node := root
	for _, key := range path[:len(path)-1] {
		next, ok := node.Get(key)
		if !ok {
			next = tree.NewTable()
			node.Set(key, next)
		}
		if next.Kind != tree.Table {
			return fmt.Errorf("%s: holds a %s, not a table", key, next.Kind)
		}
		node = next
	}
	node.Set(path[len(path)-1], value)
	return nil
}
