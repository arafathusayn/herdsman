package main

import (
	"fmt"
	"io"
	"io/fs"
	"sort"

	contracts "github.com/arafathusayn/herdsman/proto"
	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	suggestv1 "github.com/arafathusayn/herdsman/sdk/suggest/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

const specUsage = "usage: herdsman spec proto|descriptor\n"

const specHelp = specUsage + `
proto prints every contract source, each after a "// file: <path>" line.
descriptor prints the contracts' serialized FileDescriptorSet, with every
import before the file that needs it.

exit codes:
  0  the contracts were printed
  1  the contracts could not be read or written
  2  usage error
`

func init() {
	register(command{name: "spec", summary: "Print the SDK contracts: spec proto|descriptor.", help: specHelp, run: runSpec})
}

func runSpec(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 || (args[0] != "proto" && args[0] != "descriptor") {
		fmt.Fprint(stderr, specUsage)
		return 2
	}
	write := writeSpecDescriptor
	if args[0] == "proto" {
		write = writeSpecProto
	}
	if err := write(stdout); err != nil {
		fmt.Fprintf(stderr, "herdsman spec %s: %v\n", args[0], err)
		return 1
	}
	return 0
}

// writeSpecProto prints the embedded sources in path order.
func writeSpecProto(w io.Writer) error {
	paths, err := fs.Glob(contracts.Files, "herdsman/*/v1/*.proto")
	if err != nil {
		return err
	}
	sort.Strings(paths)
	for _, path := range paths {
		data, err := contracts.Files.ReadFile(path)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "// file: %s\n%s", path, data); err != nil {
			return err
		}
	}
	return nil
}

// writeSpecDescriptor prints the deterministic FileDescriptorSet of the
// contracts compiled into the binary.
func writeSpecDescriptor(w io.Writer) error {
	set := &descriptorpb.FileDescriptorSet{}
	seen := map[string]bool{}
	for _, file := range []protoreflect.FileDescriptor{configv1.File_herdsman_config_v1_config_proto, corev1.File_herdsman_core_v1_core_proto, daemonv1.File_herdsman_daemon_v1_message_proto, suggestv1.File_herdsman_suggest_v1_suggest_proto} {
		appendFile(set, seen, file)
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(set)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// appendFile adds file after everything it imports, once.
func appendFile(set *descriptorpb.FileDescriptorSet, seen map[string]bool, file protoreflect.FileDescriptor) {
	if seen[file.Path()] {
		return
	}
	seen[file.Path()] = true
	imports := file.Imports()
	for i := range imports.Len() {
		appendFile(set, seen, imports.Get(i).FileDescriptor)
	}
	set.File = append(set.File, protodesc.ToFileDescriptorProto(file))
}
