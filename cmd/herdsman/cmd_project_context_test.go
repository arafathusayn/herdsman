package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon"
)

const contextBinding = "[project]\nid = \"01KB2A00000000000000000001\"\nname = \"sample\"\n"

// contextFolderUnderDaemon starts a real daemon, makes a bound git folder and
// moves into a subfolder of it. It returns the bound folder.
func contextFolderUnderDaemon(t *testing.T) string {
	t.Helper()
	base, err := os.MkdirTemp("", "hc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	if base, err = filepath.EvalSymlinks(base); err != nil {
		t.Fatal(err)
	}
	home, private, root := filepath.Join(base, "h"), filepath.Join(base, "p"), filepath.Join(base, "w")
	inside := filepath.Join(root, "src")
	for _, dir := range []string{home, filepath.Join(root, ".git"), inside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "herdsman.config.toml"), []byte(contextBinding), 0o600); err != nil {
		t.Fatal(err)
	}
	d := daemon.New(daemon.Config{Home: home, PrivateHome: private, Version: daemonVersion()})
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	t.Setenv("HOME", home)
	t.Setenv("HERDSMAN_PRIVATE_HOME", private)
	t.Chdir(inside)
	return root
}

// contextTestdata is the library's fixture folder, as an absolute path because
// the tests change directory.
func contextTestdata(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "internal", "context", "testdata"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func projectContext(args ...string) (string, string, int) {
	var out, errOut bytes.Buffer
	code := runProjectContext(args, nil, &out, &errOut)
	return out.String(), errOut.String(), code
}

func projectLog(args ...string) (string, string, int) {
	var out, errOut bytes.Buffer
	code := runProjectLog(args, nil, &out, &errOut)
	return out.String(), errOut.String(), code
}

func TestProjectContextVerbs(t *testing.T) {
	root := contextFolderUnderDaemon(t)

	out, errOut, code := projectContext("init")
	if code != 0 || out != root+"\n" {
		t.Fatalf("init: exit %d, stdout %q, stderr %q", code, out, errOut)
	}

	out, errOut, code = projectContext("note", "--actor", "someone", "--task", "alpha", "--repo", "core", "--repo", "web", "first", "note")
	id := strings.TrimSpace(out)
	if code != 0 || len(id) != 26 {
		t.Fatalf("note: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	written, err := os.ReadFile(filepath.Join(root, "events", id+".md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"someone", "alpha", "core", "web", "first note\n"} {
		if !strings.Contains(string(written), want) {
			t.Fatalf("event file lacks %q:\n%s", want, written)
		}
	}

	if _, errOut, code = projectContext("note", "--actor", "someone", "--parent", id, "second"); code != 0 {
		t.Fatalf("note with a parent: exit %d, stderr %q", code, errOut)
	}

	out, errOut, code = projectContext("check")
	if code != 0 || out != "2 events, no problems\n" {
		t.Fatalf("check: exit %d, stdout %q, stderr %q", code, out, errOut)
	}

	if err := os.WriteFile(filepath.Join(root, "events", "01KB1Z00000000000000000009.md"), []byte("not an event\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, code = projectContext("check")
	if code != 1 || !strings.Contains(out, "01KB1Z00000000000000000009.md") || !strings.Contains(out, "malformed") {
		t.Fatalf("check on a malformed file: exit %d, stdout %q", code, out)
	}
	if _, errOut, code = projectContext("note", "--actor", "someone", "refused"); code != 1 || !strings.Contains(errOut, "failed_precondition") {
		t.Fatalf("note on a broken history: exit %d, stderr %q", code, errOut)
	}
}

func TestProjectContextUsage(t *testing.T) {
	cases := [][]string{
		{},
		{"nope"},
		{"init", "extra"},
		{"check", "extra"},
		{"init", "--nope"},
		{"check", "--nope"},
		{"note", "--actor", "someone"},
		{"note", "text without an actor"},
		{"note", "--nope", "x"},
	}
	for _, args := range cases {
		if out, errOut, code := projectContext(args...); code != 2 || out != "" || errOut == "" {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", args, code, out, errOut)
		}
	}
	out, _, code := projectContext("--help")
	if code != 0 || !strings.Contains(out, "exit codes:") || !strings.Contains(out, "note") {
		t.Errorf("--help: exit %d, %q", code, out)
	}
}

func TestProjectLogGolden(t *testing.T) {
	testdata := contextTestdata(t)
	root := contextFolderUnderDaemon(t)
	if err := os.CopyFS(root, os.DirFS(filepath.Join(testdata, "forked"))); err != nil {
		t.Fatal(err)
	}
	for _, golden := range []struct {
		file string
		args []string
	}{
		{"forked.log.txt", nil},
		{"forked.log.json", []string{"--json"}},
	} {
		want, err := os.ReadFile(filepath.Join(testdata, golden.file))
		if err != nil {
			t.Fatal(err)
		}
		out, errOut, code := projectLog(golden.args...)
		if code != 0 || out != string(want) {
			t.Errorf("%s: exit %d, stderr %q, stdout:\n%s", golden.file, code, errOut, out)
		}
	}

	all, _, _ := projectLog("--json")
	for _, args := range [][]string{{"--since", "2026-03-03"}, {"--since", "2026-03-03T00:00:00Z"}} {
		out, errOut, code := projectLog(append(args, "--json")...)
		if code != 0 || len(out) == 0 || len(out) >= len(all) {
			t.Errorf("%q: exit %d, stderr %q, %d bytes of %d", args, code, errOut, len(out), len(all))
		}
	}
	out, errOut, code := projectLog("--task", "no-such-task", "--json")
	if code != 0 || strings.Contains(out, `"kind"`) {
		t.Errorf("--task: exit %d, stderr %q, stdout %q", code, errOut, out)
	}
}

// `project context log` is the same command as `project log`.
func TestProjectContextLogSpelling(t *testing.T) {
	testdata := contextTestdata(t)
	root := contextFolderUnderDaemon(t)
	if err := os.CopyFS(root, os.DirFS(filepath.Join(testdata, "forked"))); err != nil {
		t.Fatal(err)
	}
	for _, golden := range []struct {
		file string
		args []string
	}{
		{"forked.log.txt", []string{"log"}},
		{"forked.log.json", []string{"log", "--json"}},
	} {
		want, err := os.ReadFile(filepath.Join(testdata, golden.file))
		if err != nil {
			t.Fatal(err)
		}
		out, errOut, code := projectContext(golden.args...)
		if code != 0 || out != string(want) {
			t.Errorf("%s: exit %d, stderr %q, stdout:\n%s", golden.file, code, errOut, out)
		}
	}
	if out, _, code := projectContext("--help"); code != 0 || !strings.Contains(out, "project context log") {
		t.Errorf("--help: exit %d, %q", code, out)
	}
}

func TestProjectLogUsage(t *testing.T) {
	for _, args := range [][]string{{"--since", "yesterday"}, {"--nope"}, {"extra"}} {
		if out, errOut, code := projectLog(args...); code != 2 || out != "" || errOut == "" {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", args, code, out, errOut)
		}
	}
	out, _, code := projectLog("--help")
	if code != 0 || !strings.Contains(out, "exit codes:") {
		t.Errorf("--help: exit %d, %q", code, out)
	}
}

func TestProjectVerbsOutsideABoundFolder(t *testing.T) {
	root := contextFolderUnderDaemon(t)
	if err := os.Remove(filepath.Join(root, "herdsman.config.toml")); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := projectContext("check"); code != 1 || !strings.Contains(errOut, "not inside a folder bound") {
		t.Errorf("check: exit %d, stderr %q", code, errOut)
	}
	if _, errOut, code := projectContext("init"); code != 1 || errOut == "" {
		t.Errorf("init: exit %d, stderr %q", code, errOut)
	}
	if _, errOut, code := projectLog(); code != 1 || errOut == "" {
		t.Errorf("log: exit %d, stderr %q", code, errOut)
	}
}

func TestProjectVerbsWithoutADaemon(t *testing.T) {
	base, err := os.MkdirTemp("", "hc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	t.Setenv("HERDSMAN_PRIVATE_HOME", base)
	t.Chdir(base)
	if _, errOut, code := projectContext("check"); code != 1 || errOut == "" {
		t.Errorf("check: exit %d, stderr %q", code, errOut)
	}
	if _, errOut, code := projectLog(); code != 1 || errOut == "" {
		t.Errorf("log: exit %d, stderr %q", code, errOut)
	}
}
