// Command protoc-gen-herdsman-go generates the SDK's Go code: the messages,
// through protobuf-go's own generator, and for each service an interface, a
// server handler and a client.
//
// One plugin does both so that generation needs only the vendored protobuf-go,
// with no separately installed protoc-gen-go to keep in step with it.
package main

import (
	"google.golang.org/protobuf/cmd/protoc-gen-go/internal_gengo"
	"google.golang.org/protobuf/compiler/protogen"
)

func main() {
	protogen.Options{}.Run(generate)
}

// generate writes every requested file's messages and services.
func generate(gen *protogen.Plugin) error {
	gen.SupportedFeatures = internal_gengo.SupportedFeatures
	gen.SupportedEditionsMinimum = internal_gengo.SupportedEditionsMinimum
	gen.SupportedEditionsMaximum = internal_gengo.SupportedEditionsMaximum
	for _, file := range gen.Files {
		if !file.Generate {
			continue
		}
		internal_gengo.GenerateFile(gen, file)
		if len(file.Services) > 0 {
			generateServices(gen, file)
		}
	}
	return nil
}
