package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func writeProfile(t *testing.T, profile string) string {
	t.Helper()
	repo := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	body := "[schema]\nversion = \"1.0.0\"\n[policy]\nprofile = \"" + profile + "\"\n"
	if err := os.WriteFile(filepath.Join(repo, "herdsman.config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

func parsed(t *testing.T, extra ...string) upFlags {
	t.Helper()
	args := append([]string{"harbor", "-p", "x", "-o", "codex", "--no-adjutant"}, extra...)
	f, ok := parseUp(args, io.Discard)
	if !ok {
		t.Fatalf("parse %v", args)
	}
	return f
}

func TestPairDefaultsToManualUnderRestricted(t *testing.T) {
	f := parsed(t)
	if got := upPairMode(f, writeProfile(t, "restricted")); got != "manual" {
		t.Fatalf("mode = %s", got)
	}
}

func TestExplicitAutoPairIsSentAsAsked(t *testing.T) {
	f := parsed(t, "--pair", "auto")
	if got := upPairMode(f, writeProfile(t, "restricted")); got != "auto" {
		t.Fatalf("the server must see and refuse it, got %s", got)
	}
}

func TestPairStaysAutoElsewhere(t *testing.T) {
	f := parsed(t)
	if got := upPairMode(f, writeProfile(t, "trusted-local")); got != "auto" {
		t.Fatalf("mode = %s", got)
	}
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, "herdsman.config.toml"), []byte("[broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := upPairMode(f, broken); got != "auto" {
		t.Fatalf("an unreadable file is the server's to refuse, got %s", got)
	}
}
