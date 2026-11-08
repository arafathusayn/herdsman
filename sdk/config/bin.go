package config

import (
	"fmt"
	"slices"
	"strings"

	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// decodeBin reads the binary form into a canonical tree, so that it goes
// through the same layers as a text file. Fields this build does not define
// have no place in the tree, so each one is reported.
func decodeBin(data []byte, rep *report) (*tree.Node, error) {
	msg := &configv1.Config{}
	if err := proto.Unmarshal(data, msg); err != nil {
		return nil, err
	}
	var failure error
	walkUnknown(msg.ProtoReflect(), "", false, func(path string, safety bool) {
		if err := rep.unknownWire(path, safety); err != nil && failure == nil {
			failure = err
		}
	})
	if failure != nil {
		return nil, failure
	}
	return messageTree(msg.ProtoReflect()), nil
}

// encodeBin writes the binary form. Deterministic output keeps a regenerated
// file from showing as changed.
func encodeBin(msg *configv1.Config) ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(msg)
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

// refuseEmptyLists fails on a list written as empty. The wire format cannot
// tell an empty repeated field from an absent one, and absent means the
// default, so the binary form would silently change the setting.
func refuseEmptyLists(layer *tree.Node, md protoreflect.MessageDescriptor, prefix string) error {
	for _, key := range layer.Keys() {
		value, _ := layer.Get(key)
		fd := md.Fields().ByName(protoreflect.Name(key))
		path := joinKey(prefix, key)
		switch {
		case isMessageMap(fd):
			for _, name := range value.Keys() {
				item, _ := value.Get(name)
				if err := refuseEmptyLists(item, fd.MapValue().Message(), joinKey(path, name)); err != nil {
					return err
				}
			}
		case fd.IsList() && len(value.Items) == 0:
			return fmt.Errorf("%s: the binary format cannot hold an empty list; use a text format", path)
		case isMessageList(fd):
			for i, item := range value.Items {
				if err := refuseEmptyLists(item, fd.Message(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		case isSingularMessage(fd) && string(fd.Message().FullName()) != hookSpecName:
			if err := refuseEmptyLists(value, fd.Message(), path); err != nil {
				return err
			}
		}
	}
	return nil
}

// refuseUnknownInText fails when a binary file holds fields a text format
// could not carry.
func refuseUnknownInText(data []byte) error {
	msg := &configv1.Config{}
	if err := proto.Unmarshal(data, msg); err != nil {
		return err
	}
	if found := unknownFields(msg.ProtoReflect(), ""); len(found) > 0 {
		return fmt.Errorf("the file holds fields this build does not define (%s); upgrade herdsman or export to bin", strings.Join(found, ", "))
	}
	return nil
}
