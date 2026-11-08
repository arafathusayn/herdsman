package snapshot

import (
	"fmt"
	"path"
	"strings"
)

// DefaultRebuildable returns the build and dependency folder names of common
// language ecosystems. Take applies only the patterns its caller passes, because a
// pattern is the authority to lose what it matches.
func DefaultRebuildable() []string {
	return []string{"node_modules", "target", "dist", "build", ".next", ".turbo", ".venv", "__pycache__", ".cache"}
}

// checkRebuildable rejects a pattern that is malformed or could not name a
// path inside the worktree.
func checkRebuildable(patterns []string) error {
	for _, p := range patterns {
		_, badGlob := path.Match(p, "")
		if p == "" || path.IsAbs(p) || badGlob != nil || hasDotDot(p) {
			return fmt.Errorf("snapshot: invalid rebuildable pattern %q", p)
		}
	}
	return nil
}

func hasDotDot(p string) bool {
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// matchRebuildable finds the first pattern covering rel. A pattern without a
// slash matches one path component at any depth; a pattern with a slash
// matches leading components from the worktree root. It returns the matched
// prefix, which is what the manifest lists: one line for a whole folder.
func matchRebuildable(patterns []string, rel string) (string, string, bool) {
	parts := strings.Split(rel, "/")
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		for _, p := range patterns {
			subject := prefix
			if !strings.Contains(p, "/") {
				subject = parts[i]
			}
			// A pattern was validated by checkRebuildable, so Match cannot fail.
			if ok, _ := path.Match(p, subject); ok {
				return prefix, p, true
			}
		}
	}
	return "", "", false
}
