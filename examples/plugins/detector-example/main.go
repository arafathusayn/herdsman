// Command detector-example is an example resource detector: every *.pid file at
// the top of a worktree is reported as a resource.
package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/arafathusayn/herdsman/sdk/plugin"
	pluginv1 "github.com/arafathusayn/herdsman/sdk/plugin/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

type detector struct{}

func (detector) Detect(_ context.Context, r *pluginv1.DetectRequest) (*pluginv1.DetectResponse, error) {
	entries, err := os.ReadDir(r.GetWorktree())
	if err != nil {
		return nil, rpc.Errorf(rpc.InvalidArgument, "read the worktree: %v", err)
	}
	response := &pluginv1.DetectResponse{}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".pid") {
			response.Resources = append(response.Resources, &pluginv1.DetectedResource{
				Kind: "pidfile", Id: entry.Name(), Description: filepath.Join(r.GetWorktree(), entry.Name()),
			})
		}
	}
	return response, nil
}

func main() {
	manifest := &pluginv1.Manifest{
		Name: "detector-example", Version: "0.1.0", Description: "An example detector that reports pid files.",
		ExtensionPoints: []pluginv1.ExtensionPoint{pluginv1.ExtensionPoint_EXTENSION_POINT_RESOURCE_DETECTOR},
	}
	if err := plugin.Serve(context.Background(), manifest, plugin.Extensions{ResourceDetector: detector{}}); err != nil {
		log.Fatal(err)
	}
}
