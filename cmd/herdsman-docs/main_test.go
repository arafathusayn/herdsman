package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdkconfig "github.com/arafathusayn/herdsman/sdk/config"
)

func TestRunWritesThePages(t *testing.T) {
	dir := t.TempDir()
	set, err := sdkconfig.DescriptorSet()
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"cli.json": []byte(`{"commands":[{"name":"alpha","summary":"First."}]}`),
		"set.bin":  set,
		"src":      []byte("package herdsman.config.v1;\n// Branch text.\nmessage Branch {\n}\n"),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(dir, "docs")
	var stderr bytes.Buffer
	args := []string{"--cli", filepath.Join(dir, "cli.json"), "--descriptor", filepath.Join(dir, "set.bin"), "--proto", filepath.Join(dir, "src"), "--out", out}
	if code := run(args, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	config, err := os.ReadFile(filepath.Join(out, "config.md"))
	if err != nil || !strings.Contains(string(config), "Branch text.") {
		t.Errorf("config.md = %q, %v", config, err)
	}
	if _, err := os.Stat(filepath.Join(out, "commands", "alpha.md")); err != nil {
		t.Error(err)
	}
}

func TestRunRefusesBadArguments(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"--unknown"}, &stderr); code != 2 {
		t.Errorf("unknown flag: exit %d", code)
	}
	if code := run(nil, &stderr); code != 2 || !strings.Contains(stderr.String(), "usage:") {
		t.Errorf("no flags: exit %d, %q", code, stderr.String())
	}
	missing := filepath.Join(t.TempDir(), "none")
	if code := run([]string{"--cli", missing, "--descriptor", missing, "--proto", missing}, &stderr); code != 1 {
		t.Errorf("missing input: exit %d", code)
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"--cli", bad, "--descriptor", bad, "--proto", bad}, &stderr); code != 1 {
		t.Errorf("bad cli input: exit %d", code)
	}
	good := filepath.Join(dir, "good")
	if err := os.WriteFile(good, []byte(`{"commands":[{"name":"a"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"--cli", good, "--descriptor", bad, "--proto", bad}, &stderr); code != 1 {
		t.Errorf("bad descriptor: exit %d", code)
	}
}
