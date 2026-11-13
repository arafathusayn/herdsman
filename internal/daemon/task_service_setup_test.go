package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/sdk/config"
)

func TestLegacyJSONDoesNotHideSafetyConfigErrors(t *testing.T) {
	for _, section := range []string{"policy", "launch", "runtime"} {
		t.Run(section, func(t *testing.T) {
			s, repo := shareRepo(t, "")
			writeShareFile(t, filepath.Join(filepath.Dir(repo), "herdsman.config.json"), fmt.Sprintf(`{"%s":{"misspelled_safety_key":true}}`, section))
			_, err := s.New(t.Context(), WorktreeNewRequest{Dir: repo, Task: "one", Base: "main", NoSetup: true})
			var strict *config.StrictError
			if !errors.As(err, &strict) {
				t.Fatalf("expected strict config error, got %v", err)
			}
			cmd := exec.Command("git", "branch", "--list", "one")
			cmd.Dir = repo
			got, err := cmd.CombinedOutput()
			if err != nil || strings.TrimSpace(string(got)) != "" {
				t.Fatalf("created branch despite invalid config: %s", got)
			}
		})
	}
}

func writeSetup(t *testing.T, repo, body string) {
	t.Helper()
	writeShareFile(t, filepath.Join(filepath.Dir(repo), "herdsman.config.toml"), body)
}

func TestConfiguredFilesAndFoldersComeOver(t *testing.T) {
	s, repo := shareRepo(t, ".env\ncache/\n")
	writeShareFile(t, filepath.Join(repo, ".env"), "SECRET=1\n")
	writeShareFile(t, filepath.Join(repo, "cache", "blob"), "data")
	writeSetup(t, repo, "[setup]\nenv = [\".env\"]\nshare = [\"cache\"]\n")
	resp := newShareTask(t, s, repo, false)
	for path, want := range map[string]string{".env": "SECRET=1\n", "cache/blob": "data"} {
		if got, err := os.ReadFile(filepath.Join(resp.Path, path)); err != nil || string(got) != want {
			t.Fatalf("%s: %q %v", path, got, err)
		}
	}
}

func TestNoSetupSkipsConfiguredFiles(t *testing.T) {
	s, repo := shareRepo(t, ".env\n")
	writeShareFile(t, filepath.Join(repo, ".env"), "x")
	writeSetup(t, repo, "[setup]\nenv = [\".env\"]\n")
	resp := newShareTask(t, s, repo, true)
	if _, err := os.Stat(filepath.Join(resp.Path, ".env")); err == nil {
		t.Fatal("copied")
	}
}

func TestAListedFileThatIsNotIgnoredIsLeftAndSaid(t *testing.T) {
	s, repo := shareRepo(t, "")
	writeShareFile(t, filepath.Join(repo, "notes"), "mine")
	writeSetup(t, repo, "[setup]\nenv = [\"notes\"]\n")
	resp := newShareTask(t, s, repo, false)
	if _, err := os.Stat(filepath.Join(resp.Path, "notes")); err == nil {
		t.Fatal("copied")
	}
	if !strings.Contains(strings.Join(resp.Warnings, "\n"), "notes") {
		t.Fatalf("%v", resp.Warnings)
	}
}

func TestAListedPathOutsideTheRepositoryIsRefused(t *testing.T) {
	s, repo := shareRepo(t, "")
	outside := filepath.Join(filepath.Dir(repo), "outside")
	writeShareFile(t, outside, "secret")
	writeSetup(t, repo, "[setup]\nenv = [\"../outside\"]\n")
	resp := newShareTask(t, s, repo, false)
	if _, err := os.Stat(filepath.Join(filepath.Dir(resp.Path), "outside")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(resp.Warnings, "\n"), "outside") {
		t.Fatalf("%v", resp.Warnings)
	}
}

func TestTheConfiguredPortBlockIsReservedAndNamed(t *testing.T) {
	s, repo := shareRepo(t, "")
	writeSetup(t, repo, "[setup]\nports = 3\n")
	resp := newShareTask(t, s, repo, false)
	want := []int{resp.Port, resp.Port + 1, resp.Port + 2}
	if !slices.Equal(resp.Ports, want) {
		t.Fatalf("%v, want %v", resp.Ports, want)
	}
}

func TestAPortBlockLargerThanATasksShareIsRefused(t *testing.T) {
	s, repo := shareRepo(t, "")
	writeSetup(t, repo, "[setup]\nports = 100000\n")
	if _, err := s.open(t.Context(), repo); err == nil {
		t.Fatal("accepted")
	}
}

func TestHooksReadTheBlockByIndex(t *testing.T) {
	s, repo := shareRepo(t, "")
	writeSetup(t, repo, "[setup]\nports = 2\n[hooks]\npre_start = \"echo {{ port \\\"1\\\" }} > second\"\n")
	s.Approvals = filepath.Join(t.TempDir(), "a.json")
	if _, err := s.Hook(t.Context(), WorktreeHookRequest{Dir: repo, Approve: true}); err != nil {
		t.Fatal(err)
	}
	resp := newShareTask(t, s, repo, false)
	got, _ := os.ReadFile(filepath.Join(resp.Path, "second"))
	if strings.TrimSpace(string(got)) == "" || strings.TrimSpace(string(got)) != fmt.Sprint(resp.Port+1) {
		t.Fatalf("%q, port %d", got, resp.Port)
	}
}
