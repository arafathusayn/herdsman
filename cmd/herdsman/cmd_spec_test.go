package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

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
