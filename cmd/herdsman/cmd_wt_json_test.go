package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStatuslineHonorsJSON(t *testing.T) {
	worktreeRepo(t)
	out, stderr, code := call(t, "ls", "--statusline", "--json")
	var got struct {
		Statusline string `json:"statusline"`
	}
	if err := json.Unmarshal([]byte(out), &got); code != 0 || err != nil || got.Statusline == "" {
		t.Fatalf("%d %q %q %v", code, out, stderr, err)
	}
}

func TestGoJSONWithoutASelectorPrintsOnlyJSON(t *testing.T) {
	worktreeRepo(t)
	call(t, "new", "one", "--no-setup")
	out, stderr, code := call(t, "go", "--json")
	var got struct {
		Rows []struct {
			Task string `json:"task"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &got); code != 0 || err != nil || len(got.Rows) != 2 {
		t.Fatalf("%d %q %q %v", code, out, stderr, err)
	}
}

func TestGoJSONOnATerminalKeepsThePickerOffStandardOutput(t *testing.T) {
	worktreeRepo(t)
	call(t, "new", "one", "--no-setup")
	original := stdinIsTerminal
	defer func() { stdinIsTerminal = original }()
	stdinIsTerminal = func(any) bool { return true }
	var out, errOut strings.Builder
	if code := runGo([]string{"--json"}, strings.NewReader("one\n"), &out, &errOut); code != 0 {
		t.Fatalf("%d %q", code, errOut.String())
	}
	var got struct {
		Task string `json:"task"`
	}
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil || got.Task != "one" {
		t.Fatalf("%q %v", out.String(), err)
	}
	if !strings.Contains(errOut.String(), "go to:") {
		t.Fatalf("%q", errOut.String())
	}
}
