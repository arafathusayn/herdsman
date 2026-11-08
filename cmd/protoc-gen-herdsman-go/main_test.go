package main

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

// plugin builds a generator request for one file with one method, and one
// imported file that must not be generated.
func plugin(t *testing.T, streaming, withService bool) *protogen.Plugin {
	t.Helper()
	file := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("demo/v1/demo.proto"),
		Package: proto.String("demo.v1"),
		Syntax:  proto.String("proto3"),
		Options: &descriptorpb.FileOptions{GoPackage: proto.String("example.test/demo/v1;demov1")},
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("PingRequest")},
			{Name: proto.String("PingResponse")},
		},
	}
	if withService {
		method := &descriptorpb.MethodDescriptorProto{
			Name:       proto.String("Ping"),
			InputType:  proto.String(".demo.v1.PingRequest"),
			OutputType: proto.String(".demo.v1.PingResponse"),
		}
		if streaming {
			method.ServerStreaming = proto.Bool(true)
		}
		file.Service = []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("DemoService"), Method: []*descriptorpb.MethodDescriptorProto{method}}}
	}
	imported := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("demo/v1/other.proto"),
		Package: proto.String("demo.v1"),
		Syntax:  proto.String("proto3"),
		Options: &descriptorpb.FileOptions{GoPackage: proto.String("example.test/demo/v1;demov1")},
	}
	gen, err := protogen.Options{}.New(&pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{"demo/v1/demo.proto"},
		ProtoFile:      []*descriptorpb.FileDescriptorProto{imported, file},
	})
	if err != nil {
		t.Fatal(err)
	}
	return gen
}

func outputs(gen *protogen.Plugin) map[string]string {
	files := map[string]string{}
	for _, f := range gen.Response().GetFile() {
		files[f.GetName()] = f.GetContent()
	}
	return files
}

func TestGenerateWritesMessagesAndService(t *testing.T) {
	gen := plugin(t, false, true)
	if err := generate(gen); err != nil {
		t.Fatal(err)
	}
	if msg := gen.Response().GetError(); msg != "" {
		t.Fatal(msg)
	}
	files := outputs(gen)
	if _, ok := files["example.test/demo/v1/demo.pb.go"]; !ok {
		t.Fatalf("no message file in %v", files)
	}
	if _, ok := files["example.test/demo/v1/other.pb.go"]; ok {
		t.Fatal("an imported file was generated")
	}
	service := files["example.test/demo/v1/demo_rpc.pb.go"]
	for _, want := range []string{
		"type DemoService interface",
		"func NewDemoServiceHandler(impl DemoService) http.Handler",
		"rpc.NewMux(",
		`rpc.Unary(DemoServiceName, "Ping", impl.Ping)`,
		"func NewDemoServiceClient(hc *http.Client, base string) DemoService",
		`"/rpc/demo.v1.DemoService/Ping"`,
		`DemoServiceName = "demo.v1.DemoService"`,
	} {
		if !strings.Contains(service, want) {
			t.Errorf("generated service lacks %q", want)
		}
	}
}

func TestGenerateSkipsServiceFileWithoutServices(t *testing.T) {
	gen := plugin(t, false, false)
	if err := generate(gen); err != nil {
		t.Fatal(err)
	}
	if _, ok := outputs(gen)["example.test/demo/v1/demo_rpc.pb.go"]; ok {
		t.Fatal("a service file was written for a file with no services")
	}
}

func TestGenerateRefusesStreamingMethods(t *testing.T) {
	gen := plugin(t, true, true)
	if err := generate(gen); err != nil {
		t.Fatal(err)
	}
	if msg := gen.Response().GetError(); !strings.Contains(msg, "streaming methods are not supported") {
		t.Fatalf("error %q", msg)
	}
}
