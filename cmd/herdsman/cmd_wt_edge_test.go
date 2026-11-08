package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoDashIsASelectorNotAFlag(t *testing.T) {
	worktreeRepo(t)
	if _, errOut, code := call(t, "go", "-"); code != 1 || !strings.Contains(errOut, "no previous") {
		t.Fatalf("%d %s", code, errOut)
	}
}

func TestConfigShellPrintsJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", "")
	out, _, code := call(t, "config", "shell", "install", "bash", "--json")
	var installed struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(out), &installed); err != nil || code != 0 || installed.Path != filepath.Join(home, ".bashrc") {
		t.Fatalf("%d %s %v", code, out, err)
	}
	out, _, code = call(t, "config", "shell", "uninstall", "--json", "bash")
	var removed struct {
		Removed bool `json:"removed"`
	}
	if err := json.Unmarshal([]byte(out), &removed); err != nil || code != 0 || !removed.Removed {
		t.Fatalf("%d %s %v", code, out, err)
	}
}
