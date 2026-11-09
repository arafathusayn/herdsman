package main

import (
	"io"
	"path/filepath"
	"testing"
)

func TestUpConfigMaySupplyTheCommands(t *testing.T) {
	if _, ok := parseUp([]string{"harbor", "-p", "go", "-c", "route.toml"}, io.Discard); !ok {
		t.Fatal("refused a config in place of -a and -o")
	}
	if _, ok := parseUp([]string{"harbor", "-p", "go"}, io.Discard); ok {
		t.Fatal("accepted no commands and no config")
	}
	if _, ok := parseUp([]string{"harbor", "-c", "route.toml"}, io.Discard); ok {
		t.Fatal("accepted no prompt")
	}
}

func TestUpSendsAnAbsoluteConfigPath(t *testing.T) {
	f, ok := parseUp([]string{"harbor", "-p", "go", "-c", "route.toml"}, io.Discard)
	want, err := filepath.Abs("route.toml")
	if !ok || err != nil || f.config != want {
		t.Fatalf("config %q, want %q (%v %v)", f.config, want, ok, err)
	}
	f, ok = parseUp([]string{"harbor", "--continue", "-c", "route.toml"}, io.Discard)
	if !ok || f.config != want {
		t.Fatalf("resume: config %q, want %q", f.config, want)
	}
}
