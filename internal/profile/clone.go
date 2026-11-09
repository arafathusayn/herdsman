package profile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CloneOptions says what to copy and where.
type CloneOptions struct {
	// Source is the repository, an absolute path.
	Source string
	// Dest is the new repository. It must not exist.
	Dest string
	// Base is the revision to start from, the source's HEAD when empty.
	Base string
	// Branch is created at Base; empty leaves HEAD detached there.
	Branch string
}

// afterClone runs on the finished clone before it is checked; tests use it.
var afterClone = func(string) error { return nil }

// Clone makes a repository that owns all its objects. --reference alone would
// leave the copy borrowing from Source, where deleting a branch and letting
// maintenance run can remove objects the copy still needs. --dissociate ends
// the borrowing once the copy is done. Dest appears whole or not at all.
func Clone(ctx context.Context, o CloneOptions) error {
	if !filepath.IsAbs(o.Source) || !filepath.IsAbs(o.Dest) {
		return fmt.Errorf("clone: source %q and destination %q must be absolute", o.Source, o.Dest)
	}
	if _, err := os.Lstat(o.Dest); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clone: destination %s is taken or unreadable", o.Dest)
	}
	base := o.Base
	if base == "" {
		base = "HEAD"
	}
	commit, err := runGit(ctx, o.Source, "rev-parse", "--verify", "--end-of-options", base+"^{commit}")
	if err != nil {
		return err
	}
	if o.Branch != "" {
		if _, err := runGit(ctx, o.Source, "check-ref-format", "--branch", o.Branch); err != nil {
			return err
		}
	}
	parent := filepath.Dir(o.Dest)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".herdsman-clone-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	repo := filepath.Join(tmp, "repo")
	if _, err := runGit(ctx, tmp, "clone", "--quiet", "--no-checkout", "--no-hardlinks", "--reference", o.Source, "--dissociate", "--", o.Source, repo); err != nil {
		return err
	}
	switchArgs := []string{"switch", "--quiet", "--detach", commit}
	if o.Branch != "" {
		switchArgs = []string{"switch", "--quiet", "-C", o.Branch, commit}
	}
	if _, err := runGit(ctx, repo, switchArgs...); err != nil {
		return err
	}
	if err := afterClone(repo); err != nil {
		return err
	}
	if err := refuseBorrowed(repo); err != nil {
		return err
	}
	return os.Rename(repo, o.Dest)
}

func refuseBorrowed(repo string) error {
	b, err := os.ReadFile(filepath.Join(repo, ".git", "objects", "info", "alternates"))
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(bytes.TrimSpace(b)) == 0) {
		return nil
	}
	return errors.New("clone: the copy still borrows objects")
}

// runGit ignores any repository location the caller's environment names, as a
// git hook's would, since an inherited alternate would undo the isolation.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_PREFIX", "LC_ALL":
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	cmd.Env = append(cmd.Env, "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}
