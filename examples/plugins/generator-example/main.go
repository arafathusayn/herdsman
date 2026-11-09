// Command generator-example is an example message generator. It uses no model:
// each text comes from the first line of the prompt.
package main

import (
	"context"
	"log"
	"regexp"
	"strings"

	"github.com/arafathusayn/herdsman/sdk/plugin"
	pluginv1 "github.com/arafathusayn/herdsman/sdk/plugin/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

var notSlug = regexp.MustCompile(`[^a-z0-9]+`)

type generator struct{}

func (generator) Generate(_ context.Context, r *pluginv1.GenerateRequest) (*pluginv1.GenerateResponse, error) {
	first, _, _ := strings.Cut(strings.TrimSpace(r.GetPrompt()), "\n")
	switch r.GetKind() {
	case "commit":
		return &pluginv1.GenerateResponse{Text: "chore: " + first}, nil
	case "branch":
		return &pluginv1.GenerateResponse{Text: strings.Trim(notSlug.ReplaceAllString(strings.ToLower(first), "-"), "-")}, nil
	case "summary":
		return &pluginv1.GenerateResponse{Text: first}, nil
	}
	return nil, rpc.Errorf(rpc.InvalidArgument, "unknown kind %q", r.GetKind())
}

func main() {
	manifest := &pluginv1.Manifest{
		Name: "generator-example", Version: "0.1.0", Description: "An example generator that needs no model.",
		ExtensionPoints: []pluginv1.ExtensionPoint{pluginv1.ExtensionPoint_EXTENSION_POINT_MESSAGE_GENERATOR},
	}
	if err := plugin.Serve(context.Background(), manifest, plugin.Extensions{MessageGenerator: generator{}}); err != nil {
		log.Fatal(err)
	}
}
