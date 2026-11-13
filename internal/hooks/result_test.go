package hooks

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestResultPublicationPreservesAnOpenSnapshot(t *testing.T) {
	dir := t.TempDir()
	result := Result{Hook: "pre-start", Task: "before", OK: true, File: filepath.Join(dir, "result.json")}
	if err := writeResult(&result); err != nil {
		t.Fatal(err)
	}
	old, err := os.Open(result.File)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	result.Task = "after"
	if err := writeResult(&result); err != nil {
		t.Fatal(err)
	}
	before, err := io.ReadAll(old)
	if err != nil {
		t.Fatal(err)
	}
	var prior Result
	if err := json.Unmarshal(before, &prior); err != nil || prior.Task != "before" {
		t.Fatalf("open snapshot changed: %q (%v)", before, err)
	}
	after, err := os.ReadFile(result.File)
	if err != nil {
		t.Fatal(err)
	}
	var current Result
	if err := json.Unmarshal(after, &current); err != nil || current.Task != "after" {
		t.Fatalf("new snapshot missing: %q (%v)", after, err)
	}
	info, err := os.Stat(result.File)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private result permissions: %v %v", info, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary result leaked: %v %v", entries, err)
	}
}

func TestFailedResultPublicationCleansTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeResult(&Result{File: path}); err == nil {
		t.Fatal("replacing a directory succeeded")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("failed publication changed destination or leaked a file: %v %v", entries, err)
	}
}
