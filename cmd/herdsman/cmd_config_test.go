package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/approvals"
	"github.com/arafathusayn/herdsman/sdk/config"
)

func configCmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runConfig(args, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// configHome points the user folder at an empty place and returns a file path
// beside it.
func configHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	t.Setenv("HERDSMAN_CONFIG", "")
	return filepath.Join(dir, "herdsman.config.toml")
}

func writeConfig(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestConfigShowPrintsEachValueWithItsSource(t *testing.T) {
	path := configHome(t)
	writeConfig(t, path, "[teardown]\ngrace = 2\n")
	t.Setenv("HERDSMAN_MERGE__LAND", "local")
	code, out, errText := configCmd(t, "show", "-c", path)
	if code != 0 {
		t.Fatalf("code %d: %s", code, errText)
	}
	for _, want := range []string{"file: " + path, "teardown.grace = 2 (project)", "merge.land = local (env)", "merge.strategy = squash (default)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestConfigMisspelledPolicyKeyStops(t *testing.T) {
	path := configHome(t)
	writeConfig(t, path, "[policy]\nmax_agnets = 1\n")
	code, _, errText := configCmd(t, "show", "-c", path)
	if code != 1 || !strings.Contains(errText, "max_agents") {
		t.Fatalf("code %d: %s", code, errText)
	}
}

func TestConfigShowWarnsAndStrictStops(t *testing.T) {
	path := configHome(t)
	writeConfig(t, path, "[branch]\nnmae = \"x\"\n")
	code, _, errText := configCmd(t, "show", "-c", path)
	if code != 0 || !strings.Contains(errText, "did you mean name") {
		t.Fatalf("code %d: %s", code, errText)
	}
	if code, _, _ := configCmd(t, "show", "--strict", "-c", path); code != 1 {
		t.Fatalf("strict code %d", code)
	}
}

func TestConfigGetSetUnset(t *testing.T) {
	path := configHome(t)
	writeConfig(t, path, "# note\n")
	if code, _, errText := configCmd(t, "set", "merge.strategy", "rebase", "-c", path); code != 0 {
		t.Fatalf("set: %d %s", code, errText)
	}
	if code, out, _ := configCmd(t, "get", "merge.strategy", "-c", path); code != 0 || out != "rebase\n" {
		t.Fatalf("get: %d %q", code, out)
	}
	if code, _, _ := configCmd(t, "unset", "merge.strategy", "-c", path); code != 0 {
		t.Fatalf("unset: %d", code)
	}
	if code, out, _ := configCmd(t, "get", "merge.strategy", "-c", path); code != 0 || out != "squash\n" {
		t.Fatalf("back to default: %d %q", code, out)
	}
	if code, out, _ := configCmd(t, "unset", "merge.strategy", "-c", path); code != 0 || !strings.Contains(out, "not set") {
		t.Fatalf("second unset: %d %q", code, out)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "# note") {
		t.Fatalf("file: %q %v", data, err)
	}
	if code, _, _ := configCmd(t, "get", "no.such", "-c", path); code != 1 {
		t.Fatalf("unknown get: %d", code)
	}
	if code, _, _ := configCmd(t, "set", "merge.strategy", "octopus", "-c", path); code != 1 {
		t.Fatalf("bad set: %d", code)
	}
	if code, _, _ := configCmd(t, "unset", "no.such", "-c", path); code != 1 {
		t.Fatalf("bad unset: %d", code)
	}
}

func TestConfigExportRoundTripsThroughEveryFormat(t *testing.T) {
	path := configHome(t)
	writeConfig(t, path, "[merge]\nstrategy = \"rebase\"\n[hooks]\npre-merge = [\"a\", { b = \"c\" }]\n")
	current := path
	for _, format := range []string{"yaml", "json", "bin", "toml"} {
		code, out, errText := configCmd(t, "export", "--format", format, "-c", current)
		if code != 0 {
			t.Fatalf("%s: %d %s", format, code, errText)
		}
		current = filepath.Join(t.TempDir(), "c."+format)
		writeConfig(t, current, out)
	}
	got, err := config.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	want, err := config.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.String() != want.Config.String() {
		t.Fatalf("got %v want %v", got.Config, want.Config)
	}
	if code, _, _ := configCmd(t, "export", "-c", path); code != 2 {
		t.Fatalf("no format: %d", code)
	}
	if code, _, _ := configCmd(t, "export", "--format", "ini", "-c", path); code != 1 {
		t.Fatalf("bad format: %d", code)
	}
}

func TestConfigUpdateAndInit(t *testing.T) {
	path := configHome(t)
	writeConfig(t, path, "[hooks]\npre-merge = \"a\"\n")
	if code, _, errText := configCmd(t, "update", "-c", path); code != 0 {
		t.Fatalf("update: %d %s", code, errText)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "pre-merge") {
		t.Fatalf("file: %q %v", data, err)
	}
	made := filepath.Join(t.TempDir(), "made.json")
	code, out, errText := configCmd(t, "init", "--format", "json", "-c", made)
	if code != 0 || !strings.Contains(out, "no comments") {
		t.Fatalf("init: %d %q %s", code, out, errText)
	}
	if code, _, _ := configCmd(t, "init", "--format", "json", "-c", made); code != 1 {
		t.Fatalf("second init: %d", code)
	}
	toml := filepath.Join(t.TempDir(), "made.toml")
	if code, out, _ := configCmd(t, "init", "-c", toml); code != 0 || strings.Contains(out, "no comments") {
		t.Fatalf("toml init: %d %q", code, out)
	}
}

func TestConfigInitWritesBesideTheRepository(t *testing.T) {
	configHome(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(repo))
	code, out, errText := configCmd(t, "init")
	want := filepath.Join(base, "herdsman.config.toml")
	if code != 0 || strings.TrimSpace(out) != want {
		t.Fatalf("init: %d %q %s", code, out, errText)
	}
	if code, out, _ := configCmd(t, "set", "teardown.grace", "2"); code != 0 || strings.TrimSpace(out) != want {
		t.Fatalf("discovered set: %d %q", code, out)
	}
}

func TestConfigOutsideARepository(t *testing.T) {
	configHome(t)
	t.Chdir(t.TempDir())
	if code, _, _ := configCmd(t, "init"); code != 1 {
		t.Fatalf("init: %d", code)
	}
	for _, args := range [][]string{{"set", "a.b", "c"}, {"unset", "a.b"}, {"update"}, {"export", "--format", "json"}} {
		if code, _, errText := configCmd(t, args...); code != 1 {
			t.Fatalf("%v: %d %s", args, code, errText)
		}
	}
	if code, out, _ := configCmd(t, "show"); code != 0 || !strings.Contains(out, "file: none") {
		t.Fatalf("show: %d %q", code, out)
	}
}

func TestConfigSchema(t *testing.T) {
	configHome(t)
	code, out, _ := configCmd(t, "schema", "--format", "jsonschema")
	if code != 0 || !strings.Contains(out, "2020-12") {
		t.Fatalf("jsonschema: %d", code)
	}
	if code, out, _ := configCmd(t, "schema", "--format", "descriptor"); code != 0 || out == "" {
		t.Fatalf("descriptor: %d", code)
	}
	if code, _, _ := configCmd(t, "schema"); code != 2 {
		t.Fatalf("no format: %d", code)
	}
}

func TestConfigApprovals(t *testing.T) {
	configHome(t)
	store, err := approvals.Default(os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Approve("proj", "make it"); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := configCmd(t, "approvals", "list"); code != 0 || out != "proj\tmake it\n" {
		t.Fatalf("list: %d %q", code, out)
	}
	if code, out, _ := configCmd(t, "approvals", "clear", "proj"); code != 0 || out != "removed 1\n" {
		t.Fatalf("clear: %d %q", code, out)
	}
	if code, out, _ := configCmd(t, "approvals", "clear"); code != 0 || out != "removed 0\n" {
		t.Fatalf("clear all: %d %q", code, out)
	}
	if code, _, _ := configCmd(t, "approvals", "list", "x"); code != 2 {
		t.Fatalf("list with project: %d", code)
	}
	if code, _, _ := configCmd(t, "approvals", "other"); code != 2 {
		t.Fatalf("other: %d", code)
	}
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	if code, _, _ := configCmd(t, "approvals", "list"); code != 1 {
		t.Fatalf("no home: %d", code)
	}
}

func TestConfigUsageErrors(t *testing.T) {
	configHome(t)
	for _, args := range [][]string{{}, {"nope"}, {"get"}, {"get", "a", "b"}, {"show", "-c"}, {"show", "--format"}} {
		if code, _, _ := configCmd(t, args...); code != 2 {
			t.Fatalf("%v: %d", args, code)
		}
	}
}

// gitRepo makes a repository with a remote and moves the test into it.
func gitRepo(t *testing.T, remote string) string {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", remote}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Chdir(root)
	return root
}

func userConfig(t *testing.T, text string) {
	t.Helper()
	dir := config.UserDir(os.Environ())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, filepath.Join(dir, "config.toml"), text)
}

func TestConfigAppliesTheRepositorysOverrides(t *testing.T) {
	configHome(t)
	gitRepo(t, "git@github.com:acme/app.git")
	userConfig(t, "[branch]\nname = \"general\"\n[projects.\"github.com/acme/app\".branch]\nname = \"special\"\n")
	code, out, errText := configCmd(t, "get", "branch.name")
	if code != 0 || strings.TrimSpace(out) != `"special"` && strings.TrimSpace(out) != "special" {
		t.Fatalf("code %d, out %q, err %s", code, out, errText)
	}
}

func TestConfigIgnoresAnotherRepositorysOverrides(t *testing.T) {
	configHome(t)
	gitRepo(t, "git@github.com:acme/other.git")
	userConfig(t, "[branch]\nname = \"general\"\n[projects.\"github.com/acme/app\".branch]\nname = \"special\"\n")
	if code, out, errText := configCmd(t, "get", "branch.name"); code != 0 || !strings.Contains(out, "general") {
		t.Fatalf("code %d, out %q, err %s", code, out, errText)
	}
}
