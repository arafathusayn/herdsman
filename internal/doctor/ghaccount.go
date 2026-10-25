package doctor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Account is one GitHub account gh knows and whether it can read the
// repository.
//
// JSON fields: name, active (gh's active account) and can_read.
type Account struct {
	Name    string `json:"name"`
	Active  bool   `json:"active"`
	CanRead bool   `json:"can_read"`
}

// GitHubAccess is the probe of a repository's GitHub accounts (A32).
//
// JSON fields: repo ("owner/name"), accounts, and suggested (the account to
// confirm: the active one when it can read, else the first that can; "" when
// none can).
type GitHubAccess struct {
	Repo      string    `json:"repo"`
	Accounts  []Account `json:"accounts"`
	Suggested string    `json:"suggested"`
}

var (
	githubRemote = regexp.MustCompile(`^(?:https://(?:[^@/]+@)?github\.com/|ssh://git@github\.com/|git@github\.com:)([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?/?$`)
	loggedIn     = regexp.MustCompile(`Logged in to github\.com (?:account|as) ([A-Za-z0-9-]+)`)
	activeLine   = regexp.MustCompile(`Active account: (true|false)`)
)

// RepoOf reads "owner/name" from a GitHub remote URL.
func RepoOf(remote string) (string, bool) {
	m := githubRemote.FindStringSubmatch(strings.TrimSpace(remote))
	if m == nil {
		return "", false
	}
	return m[1] + "/" + m[2], true
}

// parseAccounts reads the github.com accounts of `gh auth status`, each with
// its "Active account" line; output without those lines marks the first
// account active.
func parseAccounts(out string) []Account {
	var accts []Account
	for _, line := range strings.Split(out, "\n") {
		if m := loggedIn.FindStringSubmatch(line); m != nil {
			accts = append(accts, Account{Name: m[1]})
			continue
		}
		if m := activeLine.FindStringSubmatch(line); m != nil && len(accts) > 0 {
			accts[len(accts)-1].Active = m[1] == "true"
		}
	}
	anyActive := false
	for _, a := range accts {
		anyActive = anyActive || a.Active
	}
	if !anyActive && len(accts) > 0 {
		accts[0].Active = true
	}
	return accts
}

// ProbeGitHub reads the repository of dir's origin remote, the accounts gh
// knows, and which of them can read the repository.
func ProbeGitHub(ctx context.Context, p Probes, dir string) (GitHubAccess, error) {
	remote, err := p.GitRemote(ctx, dir)
	if err != nil {
		return GitHubAccess{}, fmt.Errorf("no origin remote: %v", err)
	}
	repo, ok := RepoOf(remote)
	if !ok {
		return GitHubAccess{}, fmt.Errorf("origin %s is not a GitHub repository", remote)
	}
	gh, err := p.LookPath("gh")
	if err != nil {
		return GitHubAccess{Repo: repo}, errors.New("gh not found on PATH")
	}
	accts, err := p.GhAccounts(ctx, gh)
	if err != nil {
		return GitHubAccess{Repo: repo}, err
	}
	g := GitHubAccess{Repo: repo, Accounts: accts}
	for i := range g.Accounts {
		a := &g.Accounts[i]
		a.CanRead = p.GhCanRead(ctx, gh, a.Name, repo) == nil
		if a.CanRead && (g.Suggested == "" || a.Active) {
			g.Suggested = a.Name
		}
	}
	return g, nil
}

func checkGitHub(ctx context.Context, p Probes, add adder) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	g, err := ProbeGitHub(cctx, p, ".")
	if err != nil {
		add(Warn, "gh-account", err.Error())
		return
	}
	var can, cannot []string
	active := ""
	for _, a := range g.Accounts {
		name := a.Name
		if a.Active {
			name += " (active)"
			active = a.Name
		}
		if a.CanRead {
			can = append(can, name)
		} else {
			cannot = append(cannot, name)
		}
	}
	detail := g.Repo + ": readable by " + orNone(strings.Join(can, ", "))
	if len(cannot) > 0 {
		detail += "; not by " + strings.Join(cannot, ", ")
	}
	switch {
	case g.Suggested == "":
		add(Warn, "gh-account", detail+"; log in with an account that can read it (gh auth login)")
	case g.Suggested != active:
		add(Warn, "gh-account", detail+"; the route will confirm "+g.Suggested+" (gh auth switch --user "+g.Suggested+")")
	default:
		add(OK, "gh-account", detail+"; the route will confirm "+g.Suggested)
	}
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// gitRemote runs `git -C dir remote get-url origin`.
func gitRemote(ctx context.Context, dir string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "remote", "get-url", "origin").Output()
	return strings.TrimSpace(string(out)), err
}

// ghAccounts runs `gh auth status` and reads its accounts; gh prints the
// status to stdout or stderr depending on its version.
func ghAccounts(ctx context.Context, gh string) ([]Account, error) {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, gh, "auth", "status")
	cmd.Stdout, cmd.Stderr = &out, &out
	_ = cmd.Run() // a failing account makes gh exit 1; its lines still list the accounts
	accts := parseAccounts(out.String())
	if len(accts) == 0 {
		return nil, errors.New("gh knows no github.com account (gh auth login)")
	}
	return accts, nil
}

// ghCanRead asks GitHub for the repository as one account: its token goes
// from `gh auth token --user` to the child's GH_TOKEN only.
func ghCanRead(ctx context.Context, gh, user, repo string) error {
	tok, err := exec.CommandContext(ctx, gh, "auth", "token", "--user", user).Output()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, gh, "api", "repos/"+repo, "--silent")
	cmd.Env = append(os.Environ(), "GH_TOKEN="+strings.TrimSpace(string(tok)))
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	return cmd.Run()
}
