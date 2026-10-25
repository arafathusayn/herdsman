package doctor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoOf(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:owner/repo.git":          "owner/repo",
		"https://github.com/owner/repo.git":      "owner/repo",
		"https://github.com/owner/repo":          "owner/repo",
		"https://x@github.com/owner/re.po.git\n": "owner/re.po",
		"ssh://git@github.com/owner/repo.git":    "owner/repo",
	} {
		if got, ok := RepoOf(in); !ok || got != want {
			t.Errorf("%q: %q %v", in, got, ok)
		}
	}
	for _, in := range []string{"https://gitlab.com/o/r.git", "/local/path", "https://github.com/o"} {
		if _, ok := RepoOf(in); ok {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestParseAccounts(t *testing.T) {
	out := "github.com\n  ✓ Logged in to github.com account user-a (keyring)\n  - Active account: false\n  ✓ Logged in to github.com account user-b (keyring)\n  - Active account: true\nother.host\n  ✓ Logged in to other.host account user-c (keyring)\n"
	got := parseAccounts(out)
	if len(got) != 2 || got[0] != (Account{Name: "user-a"}) || got[1] != (Account{Name: "user-b", Active: true}) {
		t.Errorf("%+v", got)
	}
	// Old gh: no active lines; the one account is the active one.
	if got := parseAccounts("  ✓ Logged in to github.com as user-x (oauth_token)\n"); len(got) != 1 || !got[0].Active {
		t.Errorf("%+v", got)
	}
}

func TestGhAccountCheck(t *testing.T) {
	p := fakes(t, nil)
	one(t, run(t, p), "gh-account", OK, "owner-a/repo-a: readable by user-a (active); not by user-b; the route will confirm user-a")

	p.GhCanRead = func(_ context.Context, _, user, _ string) error {
		if user == "user-b" {
			return nil
		}
		return errors.New("404")
	}
	one(t, run(t, p), "gh-account", Warn, "readable by user-b; not by user-a (active); the route will confirm user-b (gh auth switch --user user-b)")

	p.GhCanRead = func(context.Context, string, string, string) error { return errors.New("404") }
	one(t, run(t, p), "gh-account", Warn, "readable by none; not by user-a (active), user-b; log in with an account")

	p = fakes(t, nil)
	p.GitRemote = func(context.Context, string) (string, error) { return "", errors.New("exit status 2") }
	one(t, run(t, p), "gh-account", Warn, "no origin remote: exit status 2")
	p.GitRemote = func(context.Context, string) (string, error) { return "https://gitlab.com/o/r.git", nil }
	one(t, run(t, p), "gh-account", Warn, "is not a GitHub repository")

	p = fakes(t, nil)
	p.LookPath = func(name string) (string, error) {
		if name == "gh" {
			return "", exec.ErrNotFound
		}
		return "/x/herdsman", nil
	}
	one(t, run(t, p), "gh-account", Warn, "gh not found on PATH")

	p = fakes(t, nil)
	p.GhAccounts = func(context.Context, string) ([]Account, error) { return nil, errors.New("no account") }
	one(t, run(t, p), "gh-account", Warn, "no account")
}

// The real probes run git and gh as programs: a temp repository and a fake
// gh that knows two accounts, one of which can read the repository.
func TestRealGitHubProbes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	for _, args := range [][]string{{"init", "-q", dir}, {"-C", dir, "remote", "add", "origin", "git@github.com:owner-a/repo-a.git"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
	}
	if got, err := gitRemote(context.Background(), dir); err != nil || got != "git@github.com:owner-a/repo-a.git" {
		t.Fatalf("%q %v", got, err)
	}
	gh := filepath.Join(t.TempDir(), "gh")
	script := `#!/bin/sh
case "$1 $2" in
"auth status") printf '  ✓ Logged in to github.com account user-a (keyring)\n  - Active account: true\n  ✓ Logged in to github.com account user-b (keyring)\n  - Active account: false\n' >&2; exit 1;;
"auth token") echo "tok-$4";;
"api repos/owner-a/repo-a") [ "$GH_TOKEN" = tok-user-b ] || exit 1;;
*) exit 9;;
esac
`
	if err := os.WriteFile(gh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	accts, err := ghAccounts(context.Background(), gh)
	if err != nil || len(accts) != 2 || !accts[0].Active {
		t.Fatalf("%+v %v", accts, err)
	}
	if err := ghCanRead(context.Background(), gh, "user-b", "owner-a/repo-a"); err != nil {
		t.Errorf("user-b: %v", err)
	}
	if err := ghCanRead(context.Background(), gh, "user-a", "owner-a/repo-a"); err == nil {
		t.Error("user-a can read")
	}
	if err := ghCanRead(context.Background(), filepath.Join(dir, "none"), "u", "o/r"); err == nil {
		t.Error("a missing gh can read")
	}
	if _, err := ghAccounts(context.Background(), filepath.Join(dir, "none")); err == nil || !strings.Contains(err.Error(), "no github.com account") {
		t.Errorf("%v", err)
	}
	p := Default()
	p.LookPath = func(string) (string, error) { return gh, nil }
	g, err := ProbeGitHub(context.Background(), p, dir)
	if err != nil || g.Repo != "owner-a/repo-a" || g.Suggested != "user-b" {
		t.Errorf("%+v %v", g, err)
	}
}

// P0: a remote's credentials never reach the doctor's output.
func TestRemoteCredentialsAreRedacted(t *testing.T) {
	for in, want := range map[string]string{
		"https://user:SECRET@github.enterprise.invalid/o/r.git": "https://github.enterprise.invalid/o/r.git",
		"https://gitlab.com/o/r.git?token=SECRET#frag":          "https://gitlab.com/o/r.git",
		"deploy:SECRET@host.invalid:o/r.git":                    "host.invalid:o/r.git",
		"bad\nline\x1bSECRET@x":                                 "x",
		"https://host.invalid/" + strings.Repeat("a", 300):      "https://host.invalid/" + strings.Repeat("a", 179) + "…",
	} {
		if got := RedactRemote(in); got != want || strings.Contains(got, "SECRET") {
			t.Errorf("%q: %q", in, got)
		}
	}
	p := fakes(t, nil)
	p.GitRemote = func(context.Context, string) (string, error) {
		return "https://user:SECRET@github.enterprise.invalid/o/r.git?x=SECRET", nil
	}
	cs := run(t, p)
	one(t, cs, "gh-account", Warn, "origin https://github.enterprise.invalid/o/r.git is not a GitHub repository")
	for _, c := range cs {
		if strings.Contains(c.Detail, "SECRET") {
			t.Errorf("leak: %+v", c)
		}
	}
	if _, err := ProbeGitHub(context.Background(), p, "."); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("%v", err)
	}
}
