// Package gitdiff reads what a branch or worktree changed against its base
// branch, without writing anything: no index refresh, no lock files, no
// hooks, no external diff or textconv programs.
package gitdiff

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// MaxPatch bounds the patch text of one diff; the file list stays complete.
const MaxPatch = 2 << 20

// maxOutput bounds every other git output, such as a file list.
const maxOutput = 4 << 20

// File is one changed file.
type File struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"` // set on renames and copies
	// Status is A, M, D, R, C or T as git reports it, or "?" for an
	// untracked file of a worktree.
	Status string `json:"status"`
	Add    int    `json:"add"`
	Del    int    `json:"del"`
	Binary bool   `json:"binary,omitempty"`
}

// Diff is the change of one worktree or branch against its base.
type Diff struct {
	Base      string `json:"base"`       // the base branch, such as origin/main
	MergeBase string `json:"merge_base"` // the commit the change starts from
	Head      string `json:"head"`       // the branch name, or a short commit
	Files     []File `json:"files"`
	Patch     string `json:"patch"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ErrNotRepo is returned for a folder outside any git work tree.
var ErrNotRepo = errors.New("not a git work tree")

// Worktree diffs the work tree at dir, uncommitted changes included, against
// the merge base of HEAD and base. An empty base picks the default branch.
func Worktree(ctx context.Context, dir, base string) (*Diff, error) {
	if out, err := git(ctx, dir, maxOutput, "rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(out) != "true" {
		return nil, ErrNotRepo
	}
	return diff(ctx, dir, base, "HEAD", true)
}

// Branch diffs the commits of branch, in the repository at dir, against the
// merge base of branch and base.
func Branch(ctx context.Context, dir, branch, base string) (*Diff, error) {
	if strings.HasPrefix(branch, "-") {
		return nil, fmt.Errorf("bad branch name %q", branch)
	}
	if _, err := git(ctx, dir, maxOutput, "rev-parse", "--git-dir"); err != nil {
		return nil, ErrNotRepo
	}
	if _, err := git(ctx, dir, maxOutput, "rev-parse", "--verify", "--quiet", branch+"^{commit}"); err != nil {
		return nil, fmt.Errorf("no branch %s", branch)
	}
	return diff(ctx, dir, base, branch, false)
}

func diff(ctx context.Context, dir, base, head string, worktree bool) (*Diff, error) {
	if base == "" {
		base = DefaultBranch(ctx, dir)
	}
	if base == "" {
		return nil, errors.New("no base branch (origin/HEAD, main or master)")
	}
	mb, err := git(ctx, dir, maxOutput, "merge-base", head, base)
	if err != nil {
		return nil, fmt.Errorf("no common history with %s", base)
	}
	d := &Diff{Base: base, MergeBase: strings.TrimSpace(mb), Head: headName(ctx, dir, head)}
	// The work tree side is the folder itself; a branch side is its tip.
	revs := []string{d.MergeBase}
	if !worktree {
		revs = append(revs, head)
	}
	opts := []string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "-M"}

	status, err := git(ctx, dir, maxOutput, append(append(opts, "--name-status", "-z"), append(revs, "--")...)...)
	if err != nil {
		return nil, err
	}
	numstat, err := git(ctx, dir, maxOutput, append(append(opts, "--numstat", "-z"), append(revs, "--")...)...)
	if err != nil {
		return nil, err
	}
	d.Files = mergeStats(parseNameStatus(status), parseNumstat(numstat))

	patch, err := git(ctx, dir, MaxPatch, append(opts, append(revs, "--")...)...)
	if errors.Is(err, errTooLong) {
		d.Truncated = true
	} else if err != nil {
		return nil, err
	}
	d.Patch = patch

	if worktree {
		others, err := git(ctx, dir, maxOutput, "ls-files", "--others", "--exclude-standard", "-z")
		if err != nil {
			return nil, err
		}
		for p := range strings.SplitSeq(others, "\x00") {
			if p != "" {
				d.Files = append(d.Files, File{Path: p, Status: "?"})
			}
		}
	}
	return d, nil
}

// DefaultBranch is origin's default branch, or else main or master, or "".
func DefaultBranch(ctx context.Context, dir string) string {
	if out, err := git(ctx, dir, maxOutput, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimSpace(out)
	}
	for _, b := range []string{"origin/main", "main", "origin/master", "master"} {
		if _, err := git(ctx, dir, maxOutput, "rev-parse", "--verify", "--quiet", b+"^{commit}"); err == nil {
			return b
		}
	}
	return ""
}

func headName(ctx context.Context, dir, head string) string {
	if head != "HEAD" {
		return head
	}
	if out, err := git(ctx, dir, maxOutput, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		return strings.TrimSpace(out)
	}
	out, _ := git(ctx, dir, maxOutput, "rev-parse", "--short", "HEAD")
	return strings.TrimSpace(out)
}

// parseNameStatus reads `--name-status -z`: "M\0path\0" or "R100\0old\0new\0".
func parseNameStatus(out string) []File {
	f := strings.Split(out, "\x00")
	var files []File
	for i := 0; i < len(f) && f[i] != ""; {
		st := f[i]
		if (st[0] == 'R' || st[0] == 'C') && i+2 < len(f) {
			files = append(files, File{Status: st[:1], OldPath: f[i+1], Path: f[i+2]})
			i += 3
			continue
		}
		if i+1 >= len(f) {
			break
		}
		files = append(files, File{Status: st[:1], Path: f[i+1]})
		i += 2
	}
	return files
}

type stat struct {
	add, del int
	binary   bool
}

// parseNumstat reads `--numstat -z`: "1\t2\tpath\0", or for a rename
// "1\t2\t\0old\0new\0"; binary files count "-\t-".
func parseNumstat(out string) map[string]stat {
	f := strings.Split(out, "\x00")
	stats := map[string]stat{}
	for i := 0; i < len(f) && f[i] != ""; i++ {
		parts := strings.SplitN(f[i], "\t", 3)
		if len(parts) != 3 {
			continue
		}
		path := parts[2]
		if path == "" && i+2 < len(f) { // rename: the paths follow
			path = f[i+2]
			i += 2
		}
		a, errA := strconv.Atoi(parts[0])
		d, errD := strconv.Atoi(parts[1])
		stats[path] = stat{add: a, del: d, binary: errA != nil || errD != nil}
	}
	return stats
}

func mergeStats(files []File, stats map[string]stat) []File {
	for i := range files {
		if s, ok := stats[files[i].Path]; ok {
			files[i].Add, files[i].Del, files[i].Binary = s.add, s.del, s.binary
		}
	}
	return files
}

var errTooLong = errors.New("output too long")

// git runs one read-only git command in dir and returns at most limit bytes of
// its output; a longer output comes back cut to limit with errTooLong.
func git(ctx context.Context, dir string, limit int, args ...string) (string, error) {
	base := []string{
		"--no-pager",
		// Nothing a repository's config names may run or write: no fsmonitor
		// daemon and no untracked cache here, no external diff or textconv
		// programs in diff's own flags.
		"-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false",
		// Paths come out as they are, not C-quoted, so the patch headers
		// match the file list.
		"-c", "core.quotePath=false",
		"-C", dir,
	}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_OPTIONAL_LOCKS=0", // never take the index lock an agent may need
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"LC_ALL=C",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	out, readErr := io.ReadAll(io.LimitReader(pipe, int64(limit)+1))
	cut := len(out) > limit
	if cut {
		out = out[:limit]
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	switch {
	case cut:
		return string(out), errTooLong
	case readErr != nil:
		return "", readErr
	case waitErr != nil:
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = waitErr.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return string(out), nil
}

// WorktreeInfo is one entry of `git worktree list`.
type WorktreeInfo struct {
	Path     string `json:"path"`
	Head     string `json:"head"`
	Branch   string `json:"branch,omitempty"` // short name; empty when detached
	Main     bool   `json:"main,omitempty"`   // the repository's main work tree
	Locked   bool   `json:"locked,omitempty"`
	Prunable bool   `json:"prunable,omitempty"` // its folder is gone
}

// Worktrees lists the work trees of the repository at dir, the main one first.
func Worktrees(ctx context.Context, dir string) ([]WorktreeInfo, error) {
	out, err := git(ctx, dir, maxOutput, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return parseWorktrees(out), nil
}

func parseWorktrees(out string) []WorktreeInfo {
	var list []WorktreeInfo
	var cur *WorktreeInfo
	seen := 0 // entries read, a bare one included: only the first is the main one
	for line := range strings.SplitSeq(out, "\n") {
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			list = append(list, WorktreeInfo{Path: val, Main: seen == 0})
			cur = &list[len(list)-1]
			seen++
		case "HEAD":
			if cur != nil {
				cur.Head = val
			}
		case "branch":
			if cur != nil {
				cur.Branch = strings.TrimPrefix(val, "refs/heads/")
			}
		case "locked":
			if cur != nil {
				cur.Locked = true
			}
		case "prunable":
			if cur != nil {
				cur.Prunable = true
			}
		case "bare":
			if cur != nil { // a bare repository has no files to diff
				list = list[:len(list)-1]
				cur = nil
			}
		}
	}
	return list
}
