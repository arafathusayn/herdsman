package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	pluginv1 "github.com/arafathusayn/herdsman/sdk/plugin/v1"
)

func TestDetectReportsPidFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "dev.pid"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), nil, 0o644)
	got, err := detector{}.Detect(context.Background(), &pluginv1.DetectRequest{Worktree: dir})
	if err != nil || len(got.GetResources()) != 1 || got.GetResources()[0].GetId() != "dev.pid" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := (detector{}).Detect(context.Background(), &pluginv1.DetectRequest{Worktree: filepath.Join(dir, "absent")}); err == nil {
		t.Fatal("no error")
	}
}
