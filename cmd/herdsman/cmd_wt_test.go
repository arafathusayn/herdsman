package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// worktreeRepo makes a repository, enters it, and keeps every outside service
// and user file out of reach.
func worktreeRepo(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "work", "repo")
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(root, "none.sock"))
	t.Setenv("HERDR_SESSION", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("HERDSMAN_DIRECTIVE_FILE", "")
	t.Setenv("HERDSMAN_STATE_DIR", filepath.Join(root, "state"))
	os.MkdirAll(repo, 0o755)
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "first"},
	} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	t.Chdir(repo)
	return repo
}

func TestNewListGoThroughTheCommandLine(t *testing.T) {
	repo := worktreeRepo(t)
	out, errOut, code := call(t, "new", "one", "--no-setup")
	if code != 0 {
		t.Fatalf("%s %s", out, errOut)
	}
	path := filepath.Join(filepath.Dir(repo), "repo.worktrees", "one")
	if !strings.Contains(out, path) {
		t.Fatal(out)
	}
	out, _, code = call(t, "ls")
	if code != 0 || !strings.Contains(out, "feat/one") || !strings.Contains(out, "TASK") {
		t.Fatalf("%d %s", code, out)
	}
	directive := filepath.Join(t.TempDir(), "d")
	t.Setenv("HERDSMAN_DIRECTIVE_FILE", directive)
	out, _, code = call(t, "go", "one")
	if code != 0 || strings.TrimSpace(out) != path {
		t.Fatalf("%d %s", code, out)
	}
	data, _ := os.ReadFile(directive)
	if string(data) != path+"\n" {
		t.Fatalf("%q", data)
	}
}

func TestGoNoCDLeavesTheDirectiveFileAlone(t *testing.T) {
	worktreeRepo(t)
	call(t, "new", "one", "--no-setup")
	directive := filepath.Join(t.TempDir(), "d")
	t.Setenv("HERDSMAN_DIRECTIVE_FILE", directive)
	if _, _, code := call(t, "go", "one", "--no-cd"); code != 0 {
		t.Fatal(code)
	}
	if _, err := os.Stat(directive); err == nil {
		t.Fatal("wrote a directive")
	}
}

func TestVerbsPrintJSON(t *testing.T) {
	worktreeRepo(t)
	out, _, code := call(t, "new", "one", "--no-setup", "--json")
	var created struct {
		Task string `json:"task"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || code != 0 || created.Task != "one" {
		t.Fatalf("%s %v", out, err)
	}
	out, _, _ = call(t, "ls", "--json")
	var list struct {
		Rows []struct {
			Task string `json:"task"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil || len(list.Rows) != 2 {
		t.Fatalf("%s %v", out, err)
	}
	out, _, _ = call(t, "go", "one", "--json")
	if !json.Valid([]byte(out)) {
		t.Fatal(out)
	}
	out, _, _ = call(t, "hook", "pre-start", "--dry-run", "--json")
	if !json.Valid([]byte(out)) {
		t.Fatal(out)
	}
}

func TestListFlags(t *testing.T) {
	repo := worktreeRepo(t)
	exec.Command("git", "-C", repo, "branch", "loose").Run()
	out, _, _ := call(t, "ls", "--all")
	if !strings.Contains(out, "loose") {
		t.Fatal(out)
	}
	out, _, _ = call(t, "ls", "--full")
	if !strings.Contains(out, repo) {
		t.Fatal(out)
	}
	out, _, _ = call(t, "ls", "--statusline")
	if strings.TrimSpace(out) != "main" {
		t.Fatalf("%q", out)
	}
	out, _, _ = call(t, "ls", "--statusline", "--format", "json")
	if !strings.Contains(out, `"statusline":"main"`) {
		t.Fatal(out)
	}
	if _, _, code := call(t, "ls", "--format", "json"); code != 2 {
		t.Fatal(code)
	}
	if _, _, code := call(t, "ls", "--statusline", "--format", "nope"); code != 2 {
		t.Fatal(code)
	}
}

func TestStatuslineReadsTheHarnessFolder(t *testing.T) {
	repo := worktreeRepo(t)
	t.Chdir(t.TempDir())
	var out, errOut strings.Builder
	input := `{"workspace":{"current_dir":"` + repo + `"}}`
	code := commands["ls"].run([]string{"--statusline", "--format", "claude-code"}, strings.NewReader(input), &out, &errOut)
	if code != 0 || strings.TrimSpace(out.String()) != "main" {
		t.Fatalf("%d %q %q", code, out.String(), errOut.String())
	}
	if code := commands["ls"].run([]string{"--statusline", "--format", "claude-code"}, strings.NewReader("{"), &out, &errOut); code != 1 {
		t.Fatal(code)
	}
}

func TestGoWithoutASelector(t *testing.T) {
	worktreeRepo(t)
	call(t, "new", "one", "--no-setup")
	var out, errOut strings.Builder
	if code := commands["go"].run(nil, strings.NewReader(""), &out, &errOut); code != 0 || !strings.Contains(out.String(), "one") {
		t.Fatalf("%d %q", code, out.String())
	}
	original := stdinIsTerminal
	defer func() { stdinIsTerminal = original }()
	stdinIsTerminal = func(any) bool { return true }
	out.Reset()
	if code := commands["go"].run(nil, strings.NewReader("one\n"), &out, &errOut); code != 0 || !strings.Contains(out.String(), filepath.Join("repo.worktrees", "one")) {
		t.Fatalf("%d %q %q", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := commands["go"].run(nil, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("%d", code)
	}
}

func TestHookVerb(t *testing.T) {
	repo := worktreeRepo(t)
	os.WriteFile(filepath.Join(filepath.Dir(repo), "herdsman.config.json"), []byte(`{"hooks":{"pre-commit":"echo {{ task }} > hook.txt"}}`), 0o644)
	out, _, code := call(t, "hook", "pre-commit", "--dry-run")
	if code != 0 || !strings.Contains(out, "echo repo > hook.txt") {
		t.Fatalf("%d %s", code, out)
	}
	if _, errOut, code := call(t, "hook", "pre-commit"); code != 1 || !strings.Contains(errOut, "not approved") {
		t.Fatalf("%d %s", code, errOut)
	}
	if out, _, code := call(t, "hook", "pre-commit", "--approve"); code != 0 || !strings.Contains(out, "approved") {
		t.Fatalf("%d %s", code, out)
	}
	if _, errOut, code := call(t, "hook", "pre-commit"); code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(repo, "hook.txt")); err != nil {
		t.Fatal(err)
	}
	if out, _, code := call(t, "hook", "show"); code != 0 || !strings.Contains(out, "pre-commit") {
		t.Fatalf("%d %s", code, out)
	}
	if out, _, code := call(t, "hook", "show", "pre-commit"); code != 0 || !strings.Contains(out, "pre-commit") {
		t.Fatalf("%d %s", code, out)
	}
}

func TestVerbUsageErrors(t *testing.T) {
	worktreeRepo(t)
	for _, args := range [][]string{
		{"new"}, {"new", "a", "b"}, {"new", "--nope"}, {"ls", "extra"}, {"ls", "--nope"}, {"go", "a", "b"}, {"go", "--nope"},
		{"hook"}, {"hook", "--nope"}, {"hook", "a", "b"}, {"hook", "show", "a", "b"},
		{"config"}, {"config", "other"}, {"config", "shell"}, {"config", "shell", "other"}, {"config", "shell", "install", "a", "b"},
	} {
		if _, _, code := call(t, args...); code != 2 {
			t.Errorf("%v: %d", args, code)
		}
	}
}

func TestVerbFailuresExitOne(t *testing.T) {
	worktreeRepo(t)
	for _, args := range [][]string{
		{"new", "Bad Name"}, {"go", "nothing"}, {"hook", "pre-launch"},
	} {
		if _, errOut, code := call(t, args...); code != 1 || errOut == "" {
			t.Errorf("%v: %d %q", args, code, errOut)
		}
	}
	t.Chdir(t.TempDir())
	if _, _, code := call(t, "ls"); code != 1 {
		t.Fatal(code)
	}
}

func TestNewPassesThePromptAndAgent(t *testing.T) {
	worktreeRepo(t)
	out, errOut, code := call(t, "new", "one", "--no-setup", "--agent", "claude", "--", "do", "it")
	if code != 0 || !strings.Contains(out, "claude") || !strings.Contains(errOut, "not started") {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
}

func TestHelpDocumentsTheJSONPipe(t *testing.T) {
	out, _, code := call(t, "ls", "--help")
	if code != 0 || !strings.Contains(out, "herdsman ls --json | jq") {
		t.Fatal(out)
	}
	for _, verb := range []string{"new", "go", "hook", "config"} {
		if out, _, code := call(t, verb, "--help"); code != 0 || !strings.Contains(out, "usage: herdsman "+verb) {
			t.Errorf("%s: %s", verb, out)
		}
	}
}

func TestConfigShellInstallAndUninstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/bash")
	t.Setenv("ZDOTDIR", "")
	out, _, code := call(t, "config", "shell", "install")
	rc := filepath.Join(home, ".bashrc")
	if code != 0 || !strings.Contains(out, rc) {
		t.Fatalf("%d %s", code, out)
	}
	data, _ := os.ReadFile(rc)
	if !strings.Contains(string(data), "HERDSMAN_DIRECTIVE_FILE") {
		t.Fatalf("%s", data)
	}
	if _, _, code := call(t, "config", "shell", "install", "zsh"); code != 0 {
		t.Fatal(code)
	}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); err != nil {
		t.Fatal(err)
	}
	out, _, code = call(t, "config", "shell", "uninstall")
	if code != 0 || !strings.Contains(out, "removed") {
		t.Fatalf("%d %s", code, out)
	}
	out, _, code = call(t, "config", "shell", "uninstall")
	if code != 0 || !strings.Contains(out, "nothing") {
		t.Fatalf("%d %s", code, out)
	}
	if _, _, code := call(t, "config", "shell", "install", "tcsh"); code != 1 {
		t.Fatal(code)
	}
	t.Setenv("SHELL", "/bin/tcsh")
	if _, _, code := call(t, "config", "shell", "install"); code != 1 {
		t.Fatal(code)
	}
	if _, _, code := call(t, "config", "shell", "uninstall"); code != 1 {
		t.Fatal(code)
	}
}
