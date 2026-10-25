// Package review prepares a reviewer: its own worktree, detached at the
// head under review, and Go and staticcheck caches that a sandboxed
// harness can write. Git runs as a program.
package review

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Options are the inputs of Start.
type Options struct {
	Repo      string // git dir or checkout; "" is the working directory
	Worktree  string // the reviewer's worktree
	Head      string // the commit under review (any revision git resolves)
	CacheRoot string // "" is TempRoot
}

// Result is what Start prints.
//
// JSON fields: worktree (absolute path), head (full commit id), gocache and
// staticcheck_cache (absolute folders to export as GOCACHE and
// STATICCHECK_CACHE).
type Result struct {
	Worktree         string `json:"worktree"`
	Head             string `json:"head"`
	GoCache          string `json:"gocache"`
	StaticcheckCache string `json:"staticcheck_cache"`
}

// Lines are the text output, one `name=value` per line.
func (r Result) Lines() []string {
	return []string{"worktree=" + r.Worktree, "head=" + r.Head, "GOCACHE=" + r.GoCache, "STATICCHECK_CACHE=" + r.StaticcheckCache}
}

// DirtyError refuses a worktree with changes.
type DirtyError struct {
	Worktree string
	Paths    []string
}

func (e *DirtyError) Error() string {
	return fmt.Sprintf("worktree %s has changes: %s", e.Worktree, strings.Join(e.Paths, ", "))
}

// TempRoot is the system temp folder: /private/tmp on macOS, where every
// harness's sandbox allows writes, else os.TempDir.
func TempRoot() string {
	if st, err := os.Stat("/private/tmp"); err == nil && st.IsDir() {
		return "/private/tmp"
	}
	return os.TempDir()
}

var defaultRoot = TempRoot

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errOut.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// Start makes or moves the worktree to head, detached, and makes the caches.
func Start(ctx context.Context, o Options) (Result, error) {
	if o.Worktree == "" || o.Head == "" {
		return Result{}, errors.New("--worktree and --head are required")
	}
	if o.Repo == "" {
		o.Repo = "."
	}
	wt, err := filepath.Abs(o.Worktree)
	if err != nil {
		return Result{}, err
	}
	head, err := git(ctx, o.Repo, "rev-parse", "--verify", "--quiet", "--end-of-options", o.Head+"^{commit}")
	if err != nil {
		return Result{}, fmt.Errorf("head %q is no commit in %s", o.Head, o.Repo)
	}
	if _, err := os.Lstat(wt); err != nil {
		if _, err := git(ctx, o.Repo, "worktree", "add", "--detach", "--quiet", wt, head); err != nil {
			return Result{}, err
		}
	} else if err := moveTo(ctx, wt, head); err != nil {
		return Result{}, err
	}
	root := o.CacheRoot
	if root == "" {
		root = defaultRoot()
	}
	if root, err = filepath.Abs(root); err != nil {
		return Result{}, err
	}
	r := Result{Worktree: wt, Head: head,
		GoCache:          filepath.Join(root, filepath.Base(wt)+"-go-build"),
		StaticcheckCache: filepath.Join(root, filepath.Base(wt)+"-staticcheck")}
	for _, d := range []string{r.GoCache, r.StaticcheckCache} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return Result{}, err
		}
	}
	return r, nil
}

// moveTo checks out head, detached, in an existing worktree that is its
// own top level and has no changes.
func moveTo(ctx context.Context, wt, head string) error {
	top, err := git(ctx, wt, "rev-parse", "--show-toplevel")
	real, rerr := filepath.EvalSymlinks(wt)
	if err != nil || rerr != nil || top != real {
		return fmt.Errorf("%s is not a git worktree", wt)
	}
	status, err := git(ctx, wt, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return err
	}
	var dirty []string
	entries := strings.Split(status, "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		dirty = append(dirty, e[3:])
		if e[0] == 'R' || e[0] == 'C' {
			i++ // the next entry is the original path
		}
	}
	if len(dirty) > 0 {
		return &DirtyError{Worktree: wt, Paths: dirty}
	}
	// checkout overwrites ignored files: refuse when the head tracks a path
	// that ignored local work holds (the same file, a file where the work
	// is a folder, or a folder where the work is a file).
	if dirty, err = ignoredCollisions(ctx, wt, head); err != nil {
		return err
	}
	if len(dirty) > 0 {
		return &DirtyError{Worktree: wt, Paths: dirty}
	}
	_, err = git(ctx, wt, "checkout", "--quiet", "--detach", head)
	return err
}

// ignoredCollisions lists the ignored files a checkout of head would replace.
func ignoredCollisions(ctx context.Context, wt, head string) ([]string, error) {
	ignored, err := git(ctx, wt, "status", "--porcelain=v1", "-z", "--ignored=matching", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	tracked, err := git(ctx, wt, "ls-tree", "-r", "-z", "--name-only", head)
	if err != nil {
		return nil, err
	}
	target := map[string]bool{}
	for p := range strings.SplitSeq(tracked, "\x00") {
		if p != "" {
			target[p] = true
		}
	}
	var out []string
	for e := range strings.SplitSeq(ignored, "\x00") {
		if !strings.HasPrefix(e, "!! ") {
			continue
		}
		p := strings.TrimSuffix(e[3:], "/")
		hit := target[p]
		// A tracked file at any parent folder of the ignored path.
		for d := filepath.Dir(p); !hit && d != "."; d = filepath.Dir(d) {
			hit = target[d]
		}
		// A tracked path inside an ignored file's place.
		if !hit {
			for t := range target {
				if strings.HasPrefix(t, p+"/") {
					hit = true
					break
				}
			}
		}
		if hit {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out, nil
}
