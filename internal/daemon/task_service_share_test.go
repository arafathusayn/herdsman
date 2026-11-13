package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shareRepo is a repository whose lock file makes target a shared folder, and
// which needs no install command to set up.
func shareRepo(t *testing.T, ignore string) (*WorktreeService, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "init", "-q", "-b", "main")
	writeShareFile(t, filepath.Join(repo, "Cargo.lock"), "")
	writeShareFile(t, filepath.Join(repo, ".gitignore"), ignore)
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "first")
	return &WorktreeService{HerdrSocket: filepath.Join(root, "no-herdr.sock")}, repo
}

func writeShareFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newShareTask(t *testing.T, s *WorktreeService, repo string, noSetup bool) WorktreeNewResponse {
	t.Helper()
	resp, err := s.New(context.Background(), WorktreeNewRequest{Dir: repo, Task: "one", Base: "main", NoSetup: noSetup})
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestNewBringsTheIgnoredFolderOver(t *testing.T) {
	s, repo := shareRepo(t, "target/\n")
	writeShareFile(t, filepath.Join(repo, "target", "deep", "cache"), "built")
	resp := newShareTask(t, s, repo, false)
	got, err := os.ReadFile(filepath.Join(resp.Path, "target", "deep", "cache"))
	if err != nil || string(got) != "built" {
		t.Fatalf("got %q, %v", got, err)
	}
	if len(resp.Warnings) != 0 {
		t.Fatalf("got %v", resp.Warnings)
	}
}

func TestNewWithoutSetupLeavesTheIgnoredFolder(t *testing.T) {
	s, repo := shareRepo(t, "target/\n")
	writeShareFile(t, filepath.Join(repo, "target", "cache"), "built")
	resp := newShareTask(t, s, repo, true)
	if _, err := os.Stat(filepath.Join(resp.Path, "target")); !os.IsNotExist(err) {
		t.Fatalf("got %v", err)
	}
}

func TestNewBringsOverOnlyIgnoredFolders(t *testing.T) {
	s, repo := shareRepo(t, "")
	writeShareFile(t, filepath.Join(repo, "target", "private"), "not ignored")
	resp := newShareTask(t, s, repo, false)
	if _, err := os.Stat(filepath.Join(resp.Path, "target")); !os.IsNotExist(err) {
		t.Fatalf("an untracked folder that is not ignored must stay: %v", err)
	}
}

func TestNewKeepsAFolderTheCheckoutMade(t *testing.T) {
	s, repo := shareRepo(t, "")
	writeShareFile(t, filepath.Join(repo, "target", "tracked"), "tracked")
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "track it")
	writeShareFile(t, filepath.Join(repo, ".gitignore"), "target/\n")
	resp := newShareTask(t, s, repo, false)
	got, err := os.ReadFile(filepath.Join(resp.Path, "target", "tracked"))
	if err != nil || string(got) != "tracked" || len(resp.Warnings) != 0 {
		t.Fatalf("got %q, %v, %v", got, err, resp.Warnings)
	}
}

func TestNewSetsUpWithNoFolderToBringOver(t *testing.T) {
	s, repo := shareRepo(t, "target/\n")
	resp := newShareTask(t, s, repo, false)
	if _, err := os.Stat(filepath.Join(resp.Path, "target")); !os.IsNotExist(err) || len(resp.Warnings) != 0 {
		t.Fatalf("got %v, %v", err, resp.Warnings)
	}
}

func TestNewWarnsWhenTheFolderCannotBeBroughtOver(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit=%t", explicit), func(t *testing.T) {
			s, repo := shareRepo(t, "target/\n")
			if explicit {
				writeSetup(t, repo, "[setup]\nenv = [\"target\"]\nshare = [\"./target\", \"target\"]\n")
			}
			locked := filepath.Join(repo, "target", "locked")
			writeShareFile(t, filepath.Join(locked, "cache"), "built")
			if err := os.Chmod(locked, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
			resp := newShareTask(t, s, repo, false)
			if len(resp.Warnings) != 1 || !strings.Contains(resp.Warnings[0], "target") {
				t.Fatalf("a cache that did not come over must warn exactly once: %v", resp.Warnings)
			}
		})
	}
}
