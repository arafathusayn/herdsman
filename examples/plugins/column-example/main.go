// Command column-example is an example list column: the length of a task name.
package main

import (
	"context"
	"log"
	"strconv"

	"github.com/arafathusayn/herdsman/sdk/plugin"
	pluginv1 "github.com/arafathusayn/herdsman/sdk/plugin/v1"
)

type column struct{}

func (column) Compute(_ context.Context, r *pluginv1.ComputeRequest) (*pluginv1.ComputeResponse, error) {
	response := &pluginv1.ComputeResponse{Header: "NAME LENGTH"}
	for _, row := range r.GetRows() {
		response.Values = append(response.Values, strconv.Itoa(len(row.GetTask())))
	}
	return response, nil
}

func main() {
	manifest := &pluginv1.Manifest{
		Name: "column-example", Version: "0.1.0", Description: "An example list column.",
		ExtensionPoints: []pluginv1.ExtensionPoint{pluginv1.ExtensionPoint_EXTENSION_POINT_LIST_COLUMN},
	}
	if err := plugin.Serve(context.Background(), manifest, plugin.Extensions{ListColumn: column{}}); err != nil {
		log.Fatal(err)
	}
}
