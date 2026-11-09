package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func automationsCall(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	a := &AdminEnv{Stdout: &stdout, Stderr: &stderr, Version: "test"}
	all := append([]string{"--private-home", privateHomeFor(t)}, append([]string{"automations"}, args...)...)
	code := RunDaemon(a, all)
	return code, stdout.String(), stderr.String()
}

func privateHomeFor(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ph")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func automationsRepo(t *testing.T, run string) string {
	t.Helper()
	repo := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	writeAutomationConfig(t, repo, run)
	return repo
}

func TestAutomationsApproveThenDryRun(t *testing.T) {
	repo := automationsRepo(t, "touch {{ route }}")
	code, out, _ := automationsCall(t, "list", "--project", repo)
	if code != 0 || !strings.Contains(out, "route.started unapproved shell") {
		t.Fatalf("%d %q", code, out)
	}
	code, out, _ = automationsCall(t, "dry-run", "route.started", "--project", repo, "--value", "route=harbor")
	if code != 1 || !strings.Contains(out, "unapproved") {
		t.Fatalf("%d %q", code, out)
	}
	if code, out, _ = automationsCall(t, "approve", "route.started", "--project", repo); code != 0 || !strings.Contains(out, "sha256:") {
		t.Fatalf("%d %q", code, out)
	}
	code, out, errText := automationsCall(t, "dry-run", "route.started", "--project", repo, "--value", "route=harbor")
	if code != 0 || !strings.Contains(out, "would-run") || !strings.Contains(errText, "experimental") {
		t.Fatalf("%d %q %q", code, out, errText)
	}
	if _, err := os.Stat(filepath.Join(repo, "harbor")); err == nil {
		t.Fatal("a dry run ran the action")
	}
	if code, out, _ = automationsCall(t, "list", "--project", repo); code != 0 || !strings.Contains(out, "route.started approved") {
		t.Fatalf("%d %q", code, out)
	}
	writeAutomationConfig(t, repo, "touch edited")
	if code, out, _ = automationsCall(t, "dry-run", "route.started", "--project", repo); code != 1 || !strings.Contains(out, "unapproved") {
		t.Fatalf("an edit kept its approval: %d %q", code, out)
	}
}

func TestAutomationsDefaultsToTheEnclosingRepository(t *testing.T) {
	repo := automationsRepo(t, "true")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(inner)
	if code, out, _ := automationsCall(t, "list"); code != 0 || !strings.Contains(out, "route.started") {
		t.Fatalf("%d %q", code, out)
	}
}

func TestAutomationsOutsideARepositoryAsksForOne(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	if code, _, errText := automationsCall(t, "list"); code != 1 || !strings.Contains(errText, "--project") {
		t.Fatalf("%d %q", code, errText)
	}
}

func TestAutomationsRefusals(t *testing.T) {
	repo := automationsRepo(t, "true")
	for _, args := range [][]string{{}, {"bogus", "x"}, {"approve"}, {"approve", "--project", repo}, {"list", "extra"}, {"dry-run", "e", "--value", "noequals"}, {"dry-run", "e", "--value", "=x"}} {
		if code, _, _ := automationsCall(t, args...); code != 2 {
			t.Fatalf("%v: exit %d", args, code)
		}
	}
	if code, _, errText := automationsCall(t, "approve", "task.done", "--project", repo); code != 1 || !strings.Contains(errText, "no action") {
		t.Fatalf("%d %q", code, errText)
	}
	if code, _, errText := automationsCall(t, "dry-run", "task.done", "--project", repo); code != 1 || !strings.Contains(errText, "no enabled action") {
		t.Fatalf("%d %q", code, errText)
	}
}

func TestAutomationsReportsABrokenConfig(t *testing.T) {
	repo := automationsRepo(t, "true")
	if err := os.WriteFile(filepath.Join(repo, "herdsman.config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"list", "--project", repo}, {"approve", "e", "--project", repo}} {
		if code, _, _ := automationsCall(t, args...); code != 1 {
			t.Fatalf("%v: exit %d", args, code)
		}
	}
}
