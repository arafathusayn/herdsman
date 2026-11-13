package config

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// emptyListsField holds, in a binary file, the path of each list written as
// empty. The wire format cannot tell an empty repeated field from an absent
// one, and absent means the default. The number is reserved in the contract,
// so it never becomes a setting. Each record is a message of one repeated
// string, field 1: the path's segments, with a list item as its decimal index.
const emptyListsField = protowire.Number(900)

// unmarshalBin reads the binary form and takes out the empty-list records, so
// that what is left unknown is what this build truly does not define.
func unmarshalBin(data []byte) (*configv1.Config, [][]string, error) {
	msg := &configv1.Config{}
	if err := proto.Unmarshal(data, msg); err != nil {
		return nil, nil, err
	}
	var kept []byte
	var lists [][]string
	for raw := []byte(msg.ProtoReflect().GetUnknown()); len(raw) > 0; {
		number, kind, tag := protowire.ConsumeTag(raw)
		length := tag + protowire.ConsumeFieldValue(number, kind, raw[tag:])
		if number != emptyListsField {
			kept = append(kept, raw[:length]...)
			raw = raw[length:]
			continue
		}
		if kind != protowire.BytesType {
			return nil, nil, fmt.Errorf("the empty list record has the wrong wire type")
		}
		body, _ := protowire.ConsumeBytes(raw[tag:length])
		path, err := emptyListPath(body)
		if err != nil {
			return nil, nil, err
		}
		lists = append(lists, path)
		raw = raw[length:]
	}
	msg.ProtoReflect().SetUnknown(kept)
	return msg, lists, nil
}

func emptyListPath(body []byte) ([]string, error) {
	var path []string
	for len(body) > 0 {
		number, kind, tag := protowire.ConsumeTag(body)
		if tag < 0 || number != 1 || kind != protowire.BytesType {
			return nil, fmt.Errorf("an empty list record is damaged")
		}
		segment, size := protowire.ConsumeString(body[tag:])
		if size < 0 {
			return nil, fmt.Errorf("an empty list record is damaged")
		}
		path = append(path, segment)
		body = body[tag+size:]
	}
	if len(path) == 0 {
		return nil, fmt.Errorf("an empty list record has no path")
	}
	return path, nil
}

// restoreEmptyList puts one recorded list back into a decoded tree. A record
// that does not fit the tree means the file is damaged.
func restoreEmptyList(root *tree.Node, path []string) error {
	shown := strings.Join(path, ".")
	node := root
	for _, segment := range path[:len(path)-1] {
		switch node.Kind {
		case tree.Table:
			next, ok := node.Get(segment)
			if !ok {
				next = tree.NewTable()
				node.Set(segment, next)
			}
			node = next
		case tree.Array:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(node.Items) {
				return fmt.Errorf("%s: the empty list record names no such item", shown)
			}
			node = node.Items[index]
		default:
			return fmt.Errorf("%s: the empty list record goes through a value", shown)
		}
	}
	last := path[len(path)-1]
	if node.Kind != tree.Table {
		return fmt.Errorf("%s: the empty list record goes through a value", shown)
	}
	if _, taken := node.Get(last); taken {
		return fmt.Errorf("%s: the file holds this list and also records it as empty", shown)
	}
	node.Set(last, tree.NewArray())
	return nil
}

// decodeBin reads the binary form into a canonical tree, so that it goes
// through the same layers as a text file. Fields this build does not define
// have no place in the tree, so each one is reported, and the message is kept
// for whoever returns a Config that must still hold them.
func decodeBin(data []byte, rep *report) (*tree.Node, error) {
	msg, lists, err := unmarshalBin(data)
	if err != nil {
		return nil, err
	}
	rep.wire = msg
	var failure error
	walkUnknown(msg.ProtoReflect(), "", false, func(path string, safety bool) {
		if err := rep.unknownWire(path, safety); err != nil && failure == nil {
			failure = err
		}
	})
	if failure != nil {
		return nil, failure
	}
	root := messageTree(msg.ProtoReflect())
	for _, path := range lists {
		if err := restoreEmptyList(root, path); err != nil {
			return nil, err
		}
	}
	return root, nil
}

// encodeBin writes the binary form. Deterministic output keeps a regenerated
// file from showing as changed.
func encodeBin(msg *configv1.Config) ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(msg)
}

// encodeBinLists writes the binary form with a record for each empty list.
func encodeBinLists(msg *configv1.Config, lists [][]string) ([]byte, error) {
	data, err := encodeBin(msg)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(lists, slices.Compare[[]string])
	for _, path := range lists {
		var body []byte
		for _, segment := range path {
			body = protowire.AppendString(protowire.AppendTag(body, 1, protowire.BytesType), segment)
		}
		data = protowire.AppendBytes(protowire.AppendTag(data, emptyListsField, protowire.BytesType), body)
	}
	return data, nil
}

// sortedMapKeys returns a map field's keys in a stable order.
func sortedMapKeys(entries protoreflect.Map) []string {
	keys := make([]string, 0, entries.Len())
	entries.Range(func(key protoreflect.MapKey, _ protoreflect.Value) bool {
		keys = append(keys, key.String())
		return true
	})
	slices.Sort(keys)
	return keys
}

func isMessageMap(fd protoreflect.FieldDescriptor) bool {
	return fd.IsMap() && fd.MapValue().Kind() == protoreflect.MessageKind
}

func isMessageList(fd protoreflect.FieldDescriptor) bool {
	return fd.IsList() && fd.Kind() == protoreflect.MessageKind
}

func isSingularMessage(fd protoreflect.FieldDescriptor) bool {
	return !fd.IsMap() && !fd.IsList() && fd.Kind() == protoreflect.MessageKind
}

// unknownFields names every field in msg that this build does not define.
func unknownFields(msg protoreflect.Message, prefix string) []string {
	var out []string
	walkUnknown(msg, prefix, false, func(path string, _ bool) { out = append(out, path) })
	return out
}

// walkUnknown visits every undefined field, saying whether it lies inside a
// safety section.
func walkUnknown(msg protoreflect.Message, prefix string, safety bool, visit func(path string, safety bool)) {
	for raw := []byte(msg.GetUnknown()); len(raw) > 0; {
		number, _, length := protowire.ConsumeField(raw)
		if length < 0 {
			break
		}
		visit(joinKey(prefix, fmt.Sprintf("#%d", number)), safety)
		raw = raw[length:]
	}
	fields := msg.Descriptor().Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if !msg.Has(fd) {
			continue
		}
		path := joinKey(prefix, string(fd.Name()))
		value := msg.Get(fd)
		switch {
		case isMessageMap(fd):
			for _, key := range sortedMapKeys(value.Map()) {
				held := value.Map().Get(protoreflect.ValueOfString(key).MapKey())
				walkUnknown(held.Message(), joinKey(path, key), safety, visit)
			}
		case isMessageList(fd):
			for j := range value.List().Len() {
				walkUnknown(value.List().Get(j).Message(), fmt.Sprintf("%s[%d]", path, j), safety, visit)
			}
		case isSingularMessage(fd):
			walkUnknown(value.Message(), path, safety || isSafetySection(fd), visit)
		}
	}
}

// carryUnknown copies the fields this build does not define from one message
// to another of the same shape, wherever a counterpart still exists.
func carryUnknown(from, to protoreflect.Message) {
	if raw := from.GetUnknown(); len(raw) > 0 {
		to.SetUnknown(raw)
	}
	clearInventedForms(from, to)
	fields := from.Descriptor().Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if !from.Has(fd) || !to.Has(fd) {
			continue
		}
		source, target := from.Get(fd), to.Get(fd)
		switch {
		case isMessageMap(fd):
			for _, key := range sortedMapKeys(source.Map()) {
				mapKey := protoreflect.ValueOfString(key).MapKey()
				if target.Map().Has(mapKey) {
					carryUnknown(source.Map().Get(mapKey).Message(), target.Map().Get(mapKey).Message())
				}
			}
		case isMessageList(fd):
			for j := range min(source.List().Len(), target.List().Len()) {
				carryUnknown(source.List().Get(j).Message(), target.List().Get(j).Message())
			}
		case isSingularMessage(fd):
			carryUnknown(source.Message(), target.Message())
		}
	}
}

// clearInventedForms undoes a oneof the tree made up. A form this build does
// not define arrives with no case set, and the tree has no way to say that, so
// it comes back as an empty case. A case that holds something was set on
// purpose and stays.
func clearInventedForms(from, to protoreflect.Message) {
	oneofs := from.Descriptor().Oneofs()
	for i := range oneofs.Len() {
		oneof := oneofs.Get(i)
		if oneof.IsSynthetic() || from.WhichOneof(oneof) != nil {
			continue
		}
		fd := to.WhichOneof(oneof)
		if fd == nil {
			continue
		}
		blank := to.Get(fd).String() == ""
		if fd.Kind() == protoreflect.MessageKind {
			blank = proto.Size(to.Get(fd).Message().Interface()) == 0
		}
		if blank {
			to.Clear(fd)
		}
	}
}

// emptyLists names every list written as empty.
func emptyLists(layer *tree.Node, md protoreflect.MessageDescriptor, prefix []string) [][]string {
	var out [][]string
	for _, key := range layer.Keys() {
		value, _ := layer.Get(key)
		fd := md.Fields().ByName(protoreflect.Name(key))
		path := slices.Concat(prefix, []string{key})
		switch {
		case isMessageMap(fd):
			for _, name := range value.Keys() {
				item, _ := value.Get(name)
				out = append(out, emptyLists(item, fd.MapValue().Message(), slices.Concat(path, []string{name}))...)
			}
		case fd.IsList() && len(value.Items) == 0:
			out = append(out, path)
		case isMessageList(fd):
			for i, item := range value.Items {
				out = append(out, emptyLists(item, fd.Message(), slices.Concat(path, []string{strconv.Itoa(i)}))...)
			}
		case isSingularMessage(fd) && string(fd.Message().FullName()) != hookSpecName:
			out = append(out, emptyLists(value, fd.Message(), path)...)
		}
	}
	return out
}

// refuseUnknownInText fails when a binary file holds fields a text format
// could not carry.
func refuseUnknownInText(data []byte) error {
	msg, _, err := unmarshalBin(data)
	if err != nil {
		return err
	}
	if found := unknownFields(msg.ProtoReflect(), ""); len(found) > 0 {
		return fmt.Errorf("the file holds fields this build does not define (%s); upgrade herdsman or export to bin", strings.Join(found, ", "))
	}
	return nil
}
