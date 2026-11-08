package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Branches every repository fixture holds, one per integration state.
const (
	branchAhead       = "state-ahead"
	branchBehind      = "state-behind"
	branchDiverged    = "state-diverged"
	branchIntegrated  = "state-integrated"
	branchConflicting = "state-conflicting"
	branchRequest     = "pr-topic"
	branchTeardown    = "teardown-target"

	requestNumber = 7
	remoteName    = "acme/repo"
	cacheMarker   = "pkg-0000/blob-0000"
)

var fixedBranches = []string{branchAhead, branchBehind, branchDiverged, branchIntegrated, branchConflicting, branchRequest, branchTeardown}

// fixtureGit runs git in a sandbox with its own identity and no user config.
type fixtureGit struct {
	home string
}

func (g fixtureGit) env() []string {
	return append(cleanEnv(os.Environ()), "HOME="+g.home, "XDG_CONFIG_HOME="+filepath.Join(g.home, ".config"),
		"GIT_CONFIG_NOSYSTEM=1")
}

func (g fixtureGit) run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = g.env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

func (g fixtureGit) script(dir string, commands ...[]string) error {
	for _, args := range commands {
		if _, err := g.run(dir, args...); err != nil {
			return err
		}
	}
	return nil
}

func writeFile(path, body string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), mode)
}

func (g fixtureGit) commit(dir, name, body, message string) error {
	if err := writeFile(filepath.Join(dir, name), body, 0o644); err != nil {
		return err
	}
	return g.script(dir, []string{"add", "."}, []string{"commit", "-q", "-m", message})
}

// writeHome gives both tools the same identity, and turns the fixture's GitHub
// address into a local path so that a stray fetch cannot reach the network.
func writeHome(home, origin string) error {
	config := fmt.Sprintf("[user]\n\tname = bench\n\temail = bench@example.test\n[init]\n\tdefaultBranch = main\n[url %q]\n\tinsteadOf = https://github.com/%s.git\n", origin, remoteName)
	return writeFile(filepath.Join(home, ".gitconfig"), config, 0o644)
}

// fakeGHScript answers the two calls both tools make for a pull request, the
// repository lookup and the read of the pull request, and an empty list for
// the rest, such as the CI cells of a full listing.
func fakeGHScript(sha string) string {
	body := fmt.Sprintf(`{"number":%d,"state":"open","title":"topic","draft":false,"html_url":"https://github.com/%s/pull/%d","user":{"login":"bench"},"base":{"ref":"main","repo":{"name":"repo","full_name":%q,"owner":{"login":"acme"}}},"head":{"ref":%q,"sha":%q,"label":"acme:%s","repo":{"name":"repo","full_name":%q,"clone_url":"https://github.com/%s.git","owner":{"login":"acme"}}}}`,
		requestNumber, remoteName, requestNumber, remoteName, branchRequest, sha, branchRequest, remoteName, remoteName)
	return fmt.Sprintf(`#!/bin/sh
case "$*" in
  "repo set-default --view") echo %s ;;
  *pulls/%d*) printf '%%s\n' '%s' ;;
  *) echo '[]' ;;
esac
`, remoteName, requestNumber, body)
}

// writeIgnoredTree fills node_modules with incompressible files of 1 MiB, so
// that a copy costs what a real dependency tree costs.
func writeIgnoredTree(repo string, megabytes int) error {
	block := make([]byte, 1<<20)
	for i := range megabytes {
		if _, err := rand.Read(block); err != nil {
			return err
		}
		path := filepath.Join(repo, "node_modules", fmt.Sprintf("pkg-%04d", i/32), fmt.Sprintf("blob-%04d", i))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, block, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// branchStates creates one branch per integration state, relative to main.
func (g fixtureGit) branchStates(repo string) error {
	first, err := g.run(repo, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if err := g.commit(repo, "shared.txt", "main\n", "main moves on"); err != nil {
		return err
	}
	type edit struct{ branch, start, file, body string }
	for _, e := range []edit{
		{branchBehind, first, "", ""},
		{branchAhead, "main", "ahead.txt", "ahead\n"},
		{branchDiverged, first, "diverged.txt", "diverged\n"},
		{branchConflicting, first, "shared.txt", "conflict\n"},
		{branchIntegrated, "main", "integrated.txt", "integrated\n"},
		{branchRequest, "main", "topic.txt", "topic\n"},
		{branchTeardown, "main", "", ""},
	} {
		if err := g.script(repo, []string{"checkout", "-q", "-b", e.branch, e.start}); err != nil {
			return err
		}
		if e.file != "" {
			if err := g.commit(repo, e.file, e.body, "work on "+e.branch); err != nil {
				return err
			}
		}
	}
	return g.script(repo,
		[]string{"checkout", "-q", "main"},
		[]string{"merge", "-q", "--no-ff", "-m", "land integrated", branchIntegrated})
}

// buildRepository creates a repository with the state branches, its origin,
// and exactly worktrees linked worktrees, the state branches among them.
func buildRepository(root string, g fixtureGit, worktrees, ignoredMB int) (string, error) {
	if worktrees < len(fixedBranches) {
		return "", fmt.Errorf("a repository needs at least %d worktrees", len(fixedBranches))
	}
	repo := filepath.Join(root, "repo-"+strconv.Itoa(worktrees))
	origin := repo + "-origin.git"
	if err := os.MkdirAll(repo, 0o755); err != nil {
		return "", err
	}
	if err := g.script(root, []string{"init", "-q", "--bare", "-b", "main", origin}); err != nil {
		return "", err
	}
	if err := g.script(repo, []string{"init", "-q", "-b", "main"}); err != nil {
		return "", err
	}
	if err := writeFile(filepath.Join(repo, ".gitignore"), "node_modules/\n", 0o644); err != nil {
		return "", err
	}
	if err := g.commit(repo, "shared.txt", "base\n", "first commit"); err != nil {
		return "", err
	}
	if err := g.branchStates(repo); err != nil {
		return "", err
	}
	branches := append([]string{}, fixedBranches...)
	for i := len(branches); i < worktrees; i++ {
		name := fmt.Sprintf("task-%02d", i)
		if err := g.script(repo, []string{"branch", name, "main"}); err != nil {
			return "", err
		}
		branches = append(branches, name)
	}
	for _, branch := range branches {
		if err := g.script(repo, []string{"worktree", "add", "-q", filepath.Join(root, filepath.Base(repo)+"."+branch), branch}); err != nil {
			return "", err
		}
	}
	if err := g.script(repo,
		[]string{"remote", "add", "origin", "https://github.com/" + remoteName + ".git"},
		[]string{"config", "remote.origin.pushurl", origin},
		[]string{"push", "-q", origin, "main", branchRequest},
		[]string{"fetch", "-q", origin, "+refs/heads/*:refs/remotes/origin/*"},
	); err != nil {
		return "", err
	}
	return repo, writeIgnoredTree(repo, ignoredMB)
}

// Fixtures is everything the cases run against, under one disposable root.
type Fixtures struct {
	Root   string
	Home   string
	Bin    string
	Repo20 string
	Repo50 string
	git    fixtureGit
}

// BuildFixtures creates both repositories. Only the first carries the ignored
// tree, the only case that copies it creates from there.
func BuildFixtures(root string, small, large, ignoredMB int) (*Fixtures, error) {
	f := &Fixtures{Root: root, Home: filepath.Join(root, "home"), Bin: filepath.Join(root, "bin")}
	f.git = fixtureGit{home: f.Home}
	var err error
	if err = writeHome(f.Home, filepath.Join(root, fmt.Sprintf("repo-%d-origin.git", small))); err != nil {
		return nil, err
	}
	if f.Repo20, err = buildRepository(root, f.git, small, ignoredMB); err != nil {
		return nil, err
	}
	if f.Repo50, err = buildRepository(root, f.git, large, 0); err != nil {
		return nil, err
	}
	sha, err := f.git.run(f.Repo20, "rev-parse", branchRequest)
	if err != nil {
		return nil, err
	}
	return f, writeFile(filepath.Join(f.Bin, "gh"), fakeGHScript(sha), 0o755)
}
