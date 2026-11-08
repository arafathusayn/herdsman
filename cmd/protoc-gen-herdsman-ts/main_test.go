package main

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

func scalarField(name string, number int32, kind descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name:     proto.String(name),
		JsonName: proto.String(jsonName(name)),
		Number:   proto.Int32(number),
		Type:     kind.Enum(),
		Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
	}
}

func jsonName(name string) string {
	parts := strings.Split(name, "_")
	for i := 1; i < len(parts); i++ {
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

func typedField(name string, number int32, kind descriptorpb.FieldDescriptorProto_Type, typeName string) *descriptorpb.FieldDescriptorProto {
	field := scalarField(name, number, kind)
	field.TypeName = proto.String(typeName)
	return field
}

func repeated(field *descriptorpb.FieldDescriptorProto) *descriptorpb.FieldDescriptorProto {
	field.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	return field
}

const (
	typeMessage = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	typeEnum    = descriptorpb.FieldDescriptorProto_TYPE_ENUM
)

// request builds a generator request for demo/v1/demo.proto with every shape
// the JSON mapping treats differently.
func request(t *testing.T, streaming bool, extra ...*descriptorpb.FieldDescriptorProto) *protogen.Plugin {
	t.Helper()
	wkt := func(name string, messages ...*descriptorpb.DescriptorProto) *descriptorpb.FileDescriptorProto {
		return &descriptorpb.FileDescriptorProto{
			Name: proto.String("google/protobuf/" + name), Package: proto.String("google.protobuf"), Syntax: proto.String("proto3"),
			Options:     &descriptorpb.FileOptions{GoPackage: proto.String("example.test/wkt;wkt")},
			MessageType: messages,
		}
	}
	anyFile := wkt("any.proto", &descriptorpb.DescriptorProto{Name: proto.String("Any")})
	stamp := wkt("timestamp.proto", &descriptorpb.DescriptorProto{Name: proto.String("Timestamp")})
	boolValue := wkt("wrappers.proto", &descriptorpb.DescriptorProto{Name: proto.String("BoolValue"), Field: []*descriptorpb.FieldDescriptorProto{
		scalarField("value", 1, descriptorpb.FieldDescriptorProto_TYPE_BOOL),
	}})
	shared := &descriptorpb.FileDescriptorProto{
		Name: proto.String("demo/shared/v1/shared.proto"), Package: proto.String("demo.shared.v1"), Syntax: proto.String("proto3"),
		Options:     &descriptorpb.FileOptions{GoPackage: proto.String("example.test/shared;sharedv1")},
		MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String("Shared")}},
	}
	fields := []*descriptorpb.FieldDescriptorProto{
		scalarField("display_name", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING),
		scalarField("count", 2, descriptorpb.FieldDescriptorProto_TYPE_INT32),
		scalarField("big", 3, descriptorpb.FieldDescriptorProto_TYPE_UINT64),
		scalarField("flag", 4, descriptorpb.FieldDescriptorProto_TYPE_BOOL),
		scalarField("blob", 5, descriptorpb.FieldDescriptorProto_TYPE_BYTES),
		repeated(scalarField("ratios", 6, descriptorpb.FieldDescriptorProto_TYPE_DOUBLE)),
		typedField("mode", 7, typeEnum, ".demo.v1.Mode"),
		typedField("inner", 8, typeMessage, ".demo.v1.Ping.Inner"),
		repeated(typedField("labels", 9, typeMessage, ".demo.v1.Ping.LabelsEntry")),
		typedField("shared", 10, typeMessage, ".demo.shared.v1.Shared"),
		typedField("detail", 11, typeMessage, ".google.protobuf.Any"),
		typedField("at", 12, typeMessage, ".google.protobuf.Timestamp"),
		typedField("maybe", 13, typeMessage, ".google.protobuf.BoolValue"),
	}
	fields = append(fields, extra...)
	entry := &descriptorpb.DescriptorProto{
		Name: proto.String("LabelsEntry"),
		Field: []*descriptorpb.FieldDescriptorProto{
			scalarField("key", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING),
			scalarField("value", 2, descriptorpb.FieldDescriptorProto_TYPE_INT64),
		},
		Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
	}
	method := &descriptorpb.MethodDescriptorProto{
		Name:       proto.String("GetPing"),
		InputType:  proto.String(".demo.v1.Ping"),
		OutputType: proto.String(".demo.shared.v1.Shared"),
	}
	if streaming {
		method.ServerStreaming = proto.Bool(true)
	}
	file := &descriptorpb.FileDescriptorProto{
		Name: proto.String("demo/v1/demo.proto"), Package: proto.String("demo.v1"), Syntax: proto.String("proto3"),
		Dependency: []string{"demo/shared/v1/shared.proto", "google/protobuf/any.proto", "google/protobuf/timestamp.proto", "google/protobuf/wrappers.proto"},
		Options:    &descriptorpb.FileOptions{GoPackage: proto.String("example.test/demo;demov1")},
		EnumType: []*descriptorpb.EnumDescriptorProto{{Name: proto.String("Mode"), Value: []*descriptorpb.EnumValueDescriptorProto{
			{Name: proto.String("MODE_UNSPECIFIED"), Number: proto.Int32(0)},
			{Name: proto.String("MODE_FAST"), Number: proto.Int32(1)},
		}}},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Ping"), Field: fields,
			NestedType: []*descriptorpb.DescriptorProto{{Name: proto.String("Inner")}, entry},
		}},
		Service: []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("DemoService"), Method: []*descriptorpb.MethodDescriptorProto{method}}},
		SourceCodeInfo: &descriptorpb.SourceCodeInfo{Location: []*descriptorpb.SourceCodeInfo_Location{
			{Path: []int32{4, 0}, Span: []int32{0, 0, 1}, LeadingComments: proto.String(" Ping asks.\n Ends */ safely.\n")},
		}},
	}
	gen, err := protogen.Options{}.New(&pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{"demo/v1/demo.proto"},
		ProtoFile:      []*descriptorpb.FileDescriptorProto{anyFile, stamp, boolValue, shared, file},
	})
	if err != nil {
		t.Fatal(err)
	}
	return gen
}

func generated(gen *protogen.Plugin) map[string]string {
	files := map[string]string{}
	for _, f := range gen.Response().GetFile() {
		files[f.GetName()] = f.GetContent()
	}
	return files
}

func TestGenerateFollowsTheJSONMapping(t *testing.T) {
	gen := request(t, false)
	if err := generate(gen); err != nil {
		t.Fatal(err)
	}
	files := generated(gen)
	if _, ok := files["demo/shared/v1/shared.ts"]; ok {
		t.Error("an imported file was generated")
	}
	out, ok := files["demo/v1/demo.ts"]
	if !ok {
		t.Fatalf("no file in %v", files)
	}
	for _, want := range []string{
		`import type * as demo_shared_v1_shared from "../shared/v1/shared";`,
		`export type Mode = "MODE_UNSPECIFIED" | "MODE_FAST" | number;`,
		"export interface Ping {",
		"export interface Ping_Inner {",
		"displayName?: string;",
		"count?: number;",
		"big?: string;",
		"flag?: boolean;",
		"blob?: string;",
		`ratios?: (number | "NaN" | "Infinity" | "-Infinity")[];`,
		"mode?: Mode;",
		"inner?: Ping_Inner;",
		"labels?: Record<string, string>;",
		"shared?: demo_shared_v1_shared.Shared;",
		`detail?: { "@type": string; [key: string]: unknown };`,
		"at?: string;",
		"maybe?: boolean | null;",
		`Ends *\/ safely.`,
		`typeName: "demo.v1.DemoService",`,
		"getPing: {",
		`path: "/rpc/demo.v1.DemoService/GetPing",`,
		"signature: undefined as unknown as (request: Ping) => demo_shared_v1_shared.Shared,",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "LabelsEntry") {
		t.Error("a map entry became a type")
	}
}

func TestGenerateQuotesNamesThatAreNotIdentifiers(t *testing.T) {
	odd := scalarField("odd", 14, descriptorpb.FieldDescriptorProto_TYPE_STRING)
	odd.JsonName = proto.String("odd-name")
	gen := request(t, false, odd)
	if err := generate(gen); err != nil {
		t.Fatal(err)
	}
	if out := generated(gen)["demo/v1/demo.ts"]; !strings.Contains(out, `"odd-name"?: string;`) {
		t.Errorf("output %s", out)
	}
}

func TestGenerateRefusesStreamingMethods(t *testing.T) {
	err := generate(request(t, true))
	if err == nil || !strings.Contains(err.Error(), "streaming methods are not supported") {
		t.Fatalf("error %v", err)
	}
}

func TestGenerateRefusesUnknownWellKnownTypes(t *testing.T) {
	if _, err := wellKnown("google.protobuf.FileOptions"); err == nil {
		t.Fatal("no error")
	}
}
