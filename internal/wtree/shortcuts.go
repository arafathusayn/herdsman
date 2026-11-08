package wtree

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

// SelectorKind says what a worktree selector refers to.
type SelectorKind int

const (
	SelectName         SelectorKind = iota + 1 // A task, a branch or a path.
	SelectDefault                              // ^ the default branch.
	SelectCurrent                              // @ the worktree around the current folder.
	SelectPrevious                             // - the worktree used last.
	SelectPullRequest                          // pr:<n> or a pull request link.
	SelectMergeRequest                         // mr:<n> or a merge request link.
)

// Selector is a parsed command-line worktree reference.
type Selector struct {
	Kind   SelectorKind
	Value  string // The name, for SelectName.
	Number int    // The request number.
	Repo   string // owner/name from a link; empty for pr:<n> and mr:<n>.
}

// ErrNeedsFetch reports a selector that names a remote request: the caller has
// to fetch it before any local worktree can answer.
var ErrNeedsFetch = errors.New("the selector names a remote request; fetch it first")

// ParseSelector reads one selector.
func ParseSelector(text string) (Selector, error) {
	switch text {
	case "":
		return Selector{}, errors.New("empty worktree selector")
	case "^":
		return Selector{Kind: SelectDefault}, nil
	case "@":
		return Selector{Kind: SelectCurrent}, nil
	case "-":
		return Selector{Kind: SelectPrevious}, nil
	}
	if rest, ok := strings.CutPrefix(text, "pr:"); ok {
		return requestSelector(SelectPullRequest, rest, "", text)
	}
	if rest, ok := strings.CutPrefix(text, "mr:"); ok {
		return requestSelector(SelectMergeRequest, rest, "", text)
	}
	if strings.HasPrefix(text, "https://") || strings.HasPrefix(text, "http://") {
		return parseRequestURL(text)
	}
	return Selector{Kind: SelectName, Value: text}, nil
}

func requestSelector(kind SelectorKind, number, repo, original string) (Selector, error) {
	if number == "" || strings.Trim(number, "0123456789") != "" {
		return Selector{}, fmt.Errorf("invalid request number in %q", original)
	}
	n, err := strconv.Atoi(number)
	if err != nil || n <= 0 {
		return Selector{}, fmt.Errorf("invalid request number in %q", original)
	}
	return Selector{Kind: kind, Number: n, Repo: repo}, nil
}

// parseRequestURL accepts any host, so that self-hosted forges work. Pages
// under a request, such as /files, name the same request.
func parseRequestURL(text string) (Selector, error) {
	parsed, err := url.Parse(text)
	if err != nil {
		return Selector{}, fmt.Errorf("invalid link %q: %w", text, err)
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i, segment := range segments {
		if segment == "pull" && i >= 2 && i+1 < len(segments) {
			return requestSelector(SelectPullRequest, segments[i+1], strings.Join(segments[:i], "/"), text)
		}
		if segment == "merge_requests" && i >= 3 && segments[i-1] == "-" && i+1 < len(segments) {
			return requestSelector(SelectMergeRequest, segments[i+1], strings.Join(segments[:i-1], "/"), text)
		}
	}
	return Selector{}, fmt.Errorf("%q is not a pull request or merge request link", text)
}

// SelectContext is what a selector is resolved against.
type SelectContext struct {
	Rows     []Row
	Default  string // Short name of the default branch.
	Current  string // The caller's folder, absolute.
	Previous string // Task or branch used last.
}

// ResolveSelector finds the worktree a selector means. A name is tried as a
// task, then a branch, then a path. Request selectors return ErrNeedsFetch.
func ResolveSelector(sel Selector, ctx SelectContext) (Row, error) {
	switch sel.Kind {
	case SelectDefault:
		if ctx.Default == "" {
			return Row{}, errors.New("this repository has no default branch to select")
		}
		if row, ok := findBranch(ctx.Rows, ctx.Default); ok {
			return row, nil
		}
		return Row{}, fmt.Errorf("no worktree has the default branch %q checked out", ctx.Default)
	case SelectCurrent:
		if row, ok := findPath(ctx.Rows, ctx.Current); ok {
			return row, nil
		}
		return Row{}, fmt.Errorf("%q is not inside a worktree of this repository", ctx.Current)
	case SelectPrevious:
		if ctx.Previous == "" {
			return Row{}, errors.New("no previous worktree yet")
		}
		return findName(ctx.Rows, ctx.Previous)
	case SelectPullRequest, SelectMergeRequest:
		return Row{}, fmt.Errorf("%w: number %d", ErrNeedsFetch, sel.Number)
	default:
		return findName(ctx.Rows, sel.Value)
	}
}

func findName(rows []Row, name string) (Row, error) {
	for _, row := range rows {
		if row.Task == name {
			return row, nil
		}
	}
	if row, ok := findBranch(rows, name); ok {
		return row, nil
	}
	if filepath.IsAbs(name) {
		if row, ok := findPath(rows, name); ok {
			return row, nil
		}
	}
	return Row{}, fmt.Errorf("no worktree matches %q", name)
}

func findBranch(rows []Row, branch string) (Row, bool) {
	for _, row := range rows {
		if row.Branch == branch {
			return row, true
		}
	}
	return Row{}, false
}

// findPath returns the deepest worktree containing path, comparing whole path
// components so that /src/abc is not inside /src/ab.
func findPath(rows []Row, path string) (Row, bool) {
	var found Row
	matched := false
	for _, row := range rows {
		if path != row.Path && !strings.HasPrefix(path, strings.TrimSuffix(row.Path, "/")+"/") {
			continue
		}
		if !matched || len(row.Path) > len(found.Path) {
			found, matched = row, true
		}
	}
	return found, matched
}
