package main

import (
	"context"
	"testing"

	pluginv1 "github.com/arafathusayn/herdsman/sdk/plugin/v1"
)

func TestComputeGivesOneValuePerRow(t *testing.T) {
	got, err := column{}.Compute(context.Background(), &pluginv1.ComputeRequest{Rows: []*pluginv1.ColumnRow{{Task: "abc"}, {}}})
	if err != nil || got.GetHeader() == "" || got.GetValues()[0] != "3" || got.GetValues()[1] != "0" {
		t.Fatalf("%v %v", got, err)
	}
}
