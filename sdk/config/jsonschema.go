package config

import (
	"encoding/json"
	"strings"

	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// JSONSchema returns the schema of the text formats, generated from the
// message so that a new field needs no change here.
func JSONSchema() ([]byte, error) {
	doc := messageSchema((&configv1.Config{}).ProtoReflect().Descriptor(), false, 0)
	doc["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	doc["title"] = "herdsman configuration"
	return json.MarshalIndent(doc, "", "  ")
}

// maxSchemaDepth stops the recursion of Config inside projects.
const maxSchemaDepth = 2

func messageSchema(md protoreflect.MessageDescriptor, strict bool, depth int) map[string]any {
	properties := map[string]any{}
	fields := md.Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		name := string(fd.Name())
		schema := fieldSchema(fd, strict || depth == 0 && strictSections[name], depth)
		if schema == nil {
			continue
		}
		properties[name] = schema
		if strings.Contains(name, "_") {
			properties[strings.ReplaceAll(name, "_", "-")] = schema
		}
	}
	out := map[string]any{"type": "object", "properties": properties}
	if strict {
		out["additionalProperties"] = false
	}
	return out
}

func fieldSchema(fd protoreflect.FieldDescriptor, strict bool, depth int) map[string]any {
	switch {
	case fd.IsMap():
		if fd.MapValue().Kind() == protoreflect.MessageKind && depth+1 >= maxSchemaDepth {
			return nil
		}
		return map[string]any{"type": "object", "additionalProperties": singleSchema(fd.MapValue(), strict, depth+1)}
	case fd.IsList():
		return map[string]any{"type": "array", "items": singleSchema(fd, strict, depth)}
	default:
		return singleSchema(fd, strict, depth)
	}
}

func singleSchema(fd protoreflect.FieldDescriptor, strict bool, depth int) map[string]any {
	switch fd.Kind() {
	case protoreflect.StringKind:
		return map[string]any{"type": "string"}
	case protoreflect.BoolKind:
		return map[string]any{"type": "boolean"}
	case protoreflect.Int32Kind, protoreflect.Int64Kind:
		return map[string]any{"type": "integer"}
	case protoreflect.EnumKind:
		return map[string]any{"type": "string", "enum": enumChoices(fd.Enum())}
	default:
		if string(fd.Message().FullName()) == hookSpecName {
			return hookSchema()
		}
		return messageSchema(fd.Message(), strict, depth+1)
	}
}

func hookSchema() map[string]any {
	command := map[string]any{"type": "string"}
	named := map[string]any{"type": "object", "additionalProperties": command}
	stage := map[string]any{"oneOf": []any{command, named}}
	return map[string]any{"oneOf": []any{command, named, map[string]any{"type": "array", "items": stage}}}
}

// DescriptorSet returns the serialized FileDescriptorSet of the configuration
// contract, for tools generating an adapter in another language.
func DescriptorSet() ([]byte, error) {
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{
		protodesc.ToFileDescriptorProto(configv1.File_herdsman_config_v1_config_proto),
	}}
	return proto.MarshalOptions{Deterministic: true}.Marshal(set)
}
