package main

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

func contractFile(name string, dependency []string, messages ...*descriptorpb.DescriptorProto) *descriptorpb.FileDescriptorProto {
	return &descriptorpb.FileDescriptorProto{
		Name: proto.String(name), Package: proto.String("demo.v1"), Syntax: proto.String("proto3"), Dependency: dependency,
		Options:     &descriptorpb.FileOptions{GoPackage: proto.String("example.test/demo;demov1")},
		MessageType: messages,
	}
}

// runGenerator runs the generator over files, the last one being the target.
func runGenerator(t *testing.T, files ...*descriptorpb.FileDescriptorProto) (string, error) {
	t.Helper()
	plugin, err := protogen.Options{}.New(&pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{files[len(files)-1].GetName()},
		ProtoFile:      files,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := generate(plugin); err != nil {
		return "", err
	}
	return plugin.Response().GetFile()[0].GetContent(), nil
}

func TestGenerateRefusesAnUnknownWellKnownTypeWhereverItIsUsed(t *testing.T) {
	options := &descriptorpb.FileDescriptorProto{
		Name: proto.String("google/protobuf/descriptor.proto"), Package: proto.String("google.protobuf"), Syntax: proto.String("proto3"),
		Options:     &descriptorpb.FileOptions{GoPackage: proto.String("example.test/wkt;wkt")},
		MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String("FileOptions")}},
	}
	unknown := func(name string, number int32) *descriptorpb.FieldDescriptorProto {
		return typedField(name, number, typeMessage, ".google.protobuf.FileOptions")
	}
	for name, message := range map[string]*descriptorpb.DescriptorProto{
		"a field":        {Name: proto.String("Bad"), Field: []*descriptorpb.FieldDescriptorProto{unknown("bad", 1)}},
		"a nested field": {Name: proto.String("Bad"), NestedType: []*descriptorpb.DescriptorProto{{Name: proto.String("Inner"), Field: []*descriptorpb.FieldDescriptorProto{unknown("bad", 1)}}}},
		"a map value": {
			Name:  proto.String("Bad"),
			Field: []*descriptorpb.FieldDescriptorProto{repeated(typedField("entries", 1, typeMessage, ".demo.v1.Bad.EntriesEntry"))},
			NestedType: []*descriptorpb.DescriptorProto{{
				Name:    proto.String("EntriesEntry"),
				Field:   []*descriptorpb.FieldDescriptorProto{scalarField("key", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING), unknown("value", 2)},
				Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
			}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := runGenerator(t, options, contractFile("demo/v1/bad.proto", []string{options.GetName()}, message))
			if err == nil || !strings.Contains(err.Error(), "no JSON mapping") {
				t.Fatalf("error %v", err)
			}
		})
	}
}

func TestNullValueIsNullInJSON(t *testing.T) {
	structFile := &descriptorpb.FileDescriptorProto{
		Name: proto.String("google/protobuf/struct.proto"), Package: proto.String("google.protobuf"), Syntax: proto.String("proto3"),
		Options: &descriptorpb.FileOptions{GoPackage: proto.String("example.test/wkt;wkt")},
		EnumType: []*descriptorpb.EnumDescriptorProto{{Name: proto.String("NullValue"), Value: []*descriptorpb.EnumValueDescriptorProto{
			{Name: proto.String("NULL_VALUE"), Number: proto.Int32(0)},
		}}},
	}
	out, err := runGenerator(t, structFile, contractFile("demo/v1/null.proto", []string{structFile.GetName()}, &descriptorpb.DescriptorProto{
		Name: proto.String("Holder"), Field: []*descriptorpb.FieldDescriptorProto{typedField("nothing", 1, typeEnum, ".google.protobuf.NullValue")},
	}))
	if err != nil || !strings.Contains(out, "nothing?: null;") {
		t.Fatalf("error %v\n%s", err, out)
	}
}

func TestAnImportBesideTheFileStartsWithDotSlash(t *testing.T) {
	sibling := contractFile("demo/v1/sibling.proto", nil, &descriptorpb.DescriptorProto{Name: proto.String("Sibling")})
	out, err := runGenerator(t, sibling, contractFile("demo/v1/uses.proto", []string{sibling.GetName()}, &descriptorpb.DescriptorProto{
		Name: proto.String("Uses"), Field: []*descriptorpb.FieldDescriptorProto{typedField("other", 1, typeMessage, ".demo.v1.Sibling")},
	}))
	if err != nil || !strings.Contains(out, "from \"./sibling") {
		t.Fatalf("error %v\n%s", err, out)
	}
}

func TestAnImportThatCannotBeMadeRelativeIsAnError(t *testing.T) {
	sibling := contractFile("demo/v1/sibling.proto", nil, &descriptorpb.DescriptorProto{Name: proto.String("Sibling")})
	_, err := runGenerator(t, sibling, contractFile("../outside/uses.proto", []string{sibling.GetName()}, &descriptorpb.DescriptorProto{
		Name: proto.String("Uses"), Field: []*descriptorpb.FieldDescriptorProto{typedField("other", 1, typeMessage, ".demo.v1.Sibling")},
	}))
	if err == nil || !strings.Contains(err.Error(), "cannot import") {
		t.Fatalf("error %v", err)
	}
}

func TestANestedEnumIsWritten(t *testing.T) {
	out, err := runGenerator(t, contractFile("demo/v1/nested.proto", nil, &descriptorpb.DescriptorProto{
		Name: proto.String("Holder"),
		EnumType: []*descriptorpb.EnumDescriptorProto{{Name: proto.String("State"), Value: []*descriptorpb.EnumValueDescriptorProto{
			{Name: proto.String("STATE_UNSPECIFIED"), Number: proto.Int32(0)},
		}}},
	}))
	if err != nil || !strings.Contains(out, "Holder_State") {
		t.Fatalf("error %v\n%s", err, out)
	}
}
