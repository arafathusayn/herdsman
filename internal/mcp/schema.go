package mcp

import (
	"math"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// dryRunField is the argument that asks a mutating tool for its plan.
const dryRunField = "dry_run"

// schemaBuilder collects the definitions one input schema refers to.
type schemaBuilder struct {
	defs map[string]any
}

// inputSchema is the JSON Schema of a request message, in the proto field
// names. Unknown properties are refused, as the server's own decoding refuses
// them. withDryRun adds the argument for a method whose request lacks it, so
// the tool list does not change when the method learns to plan.
func inputSchema(msg protoreflect.MessageDescriptor, withDryRun bool) map[string]any {
	b := &schemaBuilder{defs: map[string]any{}}
	schema := b.object(msg)
	if withDryRun {
		schema["properties"].(map[string]any)[dryRunField] = map[string]any{"type": "boolean"}
	}
	if len(b.defs) > 0 {
		schema["$defs"] = b.defs
	}
	return schema
}

func (b *schemaBuilder) object(msg protoreflect.MessageDescriptor) map[string]any {
	properties := map[string]any{}
	fields := msg.Fields()
	for i := range fields.Len() {
		field := fields.Get(i)
		properties[string(field.Name())] = b.field(field)
	}
	return map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
}

func (b *schemaBuilder) field(field protoreflect.FieldDescriptor) map[string]any {
	switch {
	case field.IsMap():
		return map[string]any{"type": "object", "additionalProperties": b.value(field.MapValue())}
	case field.IsList():
		return map[string]any{"type": "array", "items": b.value(field)}
	}
	return b.value(field)
}

// value is the schema of one value, following the protobuf JSON mapping: 64-bit
// integers may be strings, and floats may be "NaN" or "Infinity".
func (b *schemaBuilder) value(field protoreflect.FieldDescriptor) map[string]any {
	switch field.Kind() {
	case protoreflect.BoolKind:
		return map[string]any{"type": "boolean"}
	case protoreflect.StringKind:
		return map[string]any{"type": "string"}
	case protoreflect.BytesKind:
		return map[string]any{"type": "string", "contentEncoding": "base64"}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return map[string]any{"type": "integer", "minimum": int64(math.MinInt32), "maximum": int64(math.MaxInt32)}
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return map[string]any{"type": "integer", "minimum": int64(0), "maximum": int64(math.MaxUint32)}
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind, protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return map[string]any{"type": []string{"integer", "string"}}
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return map[string]any{"type": []string{"number", "string"}}
	case protoreflect.EnumKind:
		values := field.Enum().Values()
		names := make([]string, values.Len())
		for i := range names {
			names[i] = string(values.Get(i).Name())
		}
		return map[string]any{"type": "string", "enum": names}
	}
	return b.message(field.Message())
}

// message refers to a definition, so a recursive message terminates. The
// well-known types have their own JSON forms; those not written as a string are
// left open, and the server's decoding still checks them.
func (b *schemaBuilder) message(msg protoreflect.MessageDescriptor) map[string]any {
	name := string(msg.FullName())
	if rest, ok := strings.CutPrefix(name, "google.protobuf."); ok {
		switch rest {
		case "Timestamp", "Duration", "FieldMask":
			return map[string]any{"type": "string"}
		}
		return map[string]any{}
	}
	if _, seen := b.defs[name]; !seen {
		b.defs[name] = nil
		b.defs[name] = b.object(msg)
	}
	return map[string]any{"$ref": "#/$defs/" + name}
}
