package main

import (
	"bytes"
	"io/fs"
	"os"
	"path"
	"strings"
	"testing"

	contracts "github.com/arafathusayn/herdsman/proto"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestSpecDescriptorMatchesGolden(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runSpec([]string{"descriptor"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	golden, err := os.ReadFile("../../sdk/testdata/descriptor.golden")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stdout.Bytes(), golden) {
		t.Fatal("spec descriptor differs from sdk/testdata/descriptor.golden: run make proto")
	}
}

func TestSpecDescriptorIsACompleteSet(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runSpec([]string{"descriptor"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(stdout.Bytes(), &set); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, file := range set.GetFile() {
		for _, dep := range file.GetDependency() {
			if !seen[dep] {
				t.Errorf("%s comes before its import %s", file.GetName(), dep)
			}
		}
		seen[file.GetName()] = true
	}
	for _, want := range []string{"google/protobuf/any.proto", "herdsman/core/v1/core.proto", "herdsman/daemon/v1/message.proto"} {
		if !seen[want] {
			t.Errorf("the set lacks %s", want)
		}
	}
}

// A contract left out of the descriptor set, or one the generator cannot place
// in the SDK module, must fail here rather than only in the generator step.
func TestSpecDescriptorCoversEveryContract(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runSpec([]string{"descriptor"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(stdout.Bytes(), &set); err != nil {
		t.Fatal(err)
	}
	inSet := map[string]*descriptorpb.FileDescriptorProto{}
	for _, file := range set.GetFile() {
		inSet[file.GetName()] = file
	}
	sources, err := fs.Glob(contracts.Files, "herdsman/*/v1/*.proto")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) == 0 {
		t.Fatal("no embedded contracts")
	}
	for _, source := range sources {
		file, ok := inSet[source]
		if !ok {
			t.Errorf("the set lacks %s", source)
			continue
		}
		want := "github.com/arafathusayn/herdsman/sdk/" + path.Base(path.Dir(path.Dir(source))) + "/v1;"
		if got := file.GetOptions().GetGoPackage(); !strings.HasPrefix(got, want) {
			t.Errorf("%s: go_package %q, want the prefix %q", source, got, want)
		}
	}
}

func TestSpecProtoPrintsEverySource(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runSpec([]string{"proto"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	for _, want := range []string{
		"// file: herdsman/core/v1/core.proto\n",
		"// file: herdsman/daemon/v1/message.proto\n",
		"package herdsman.core.v1;",
		"service MessageService",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output lacks %q", want)
		}
	}
	if strings.Index(stdout.String(), "core.proto") > strings.Index(stdout.String(), "message.proto") {
		t.Error("files are not in path order")
	}
}

func TestSpecRejectsBadUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"nope"}, {"proto", "extra"}} {
		var stdout, stderr bytes.Buffer
		if code := runSpec(args, nil, &stdout, &stderr); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
		if stdout.Len() != 0 || !strings.Contains(stderr.String(), "spec proto|descriptor") {
			t.Errorf("%v: stdout %q, stderr %q", args, stdout.String(), stderr.String())
		}
	}
}
