package wtree

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arafathusayn/herdsman/internal/gitrun"
	"github.com/arafathusayn/herdsman/internal/snapshot"
)

// Inventory says, for every path in a worktree, why deleting it is allowed.
type Inventory struct {
	Captured       int
	Rebuildable    []string
	Submodules     []string // Initialized at their recorded commit, and all on a remote.
	Named          []string
	Uncaptured     []string
	UnmatchedNames []string
	// Reasons says why a path in Uncaptured or Named is unsaved, when known.
	Reasons map[string]string
}

// ValidateNamed refuses a name that could not be a path inside a worktree.
func ValidateNamed(names []string) error {
	for _, name := range names {
		_, badGlob := path.Match(name, "")
		if name == "" || path.IsAbs(name) || badGlob != nil || strings.Contains("/"+name+"/", "/../") {
			return fmt.Errorf("--force-irreversible: %q does not name a path inside the worktree", name)
		}
	}
	return nil
}

// matchName finds the first name covering rel: the path itself, a folder above
// it, or a glob matching either.
func matchName(names []string, rel string) (string, bool) {
	parts := strings.Split(rel, "/")
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		for _, name := range names {
			// ValidateNamed has already rejected a pattern Match cannot read.
			if ok, _ := path.Match(name, prefix); ok {
				return name, true
			}
		}
	}
	return "", false
}

type inventoryWalk struct {
	root     string
	files    map[string]snapshot.FileRecord
	excluded map[string]snapshot.Exclusion
	names    []string
	used     map[string]bool
	out      *Inventory
	problems map[string]string
}

func (w *inventoryWalk) refuseOrName(rel string) {
	if name, ok := matchName(w.names, rel); ok {
		w.used[name] = true
		w.out.Named = append(w.out.Named, rel)
		return
	}
	w.out.Uncaptured = append(w.out.Uncaptured, rel)
}

// unchanged compares only what the manifest recorded for a regular file; a
// symlink's record has no comparable size or time.
func unchanged(record snapshot.FileRecord, info fs.FileInfo) bool {
	if !info.Mode().IsRegular() {
		return true
	}
	return record.Size == info.Size() && (record.MTimeNS == 0 || record.MTimeNS == info.ModTime().UnixNano())
}

func (w *inventoryWalk) visit(full string, d fs.DirEntry, err error) error {
	if err != nil {
		return err
	}
	if full == w.root {
		return nil
	}
	rel, err := filepath.Rel(w.root, full)
	if err != nil {
		return err
	}
	rel = filepath.ToSlash(rel)
	if rel == ".git" {
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	}
	if exclusion, ok := w.excluded[rel]; ok {
		problem, checked := w.problems[rel]
		switch {
		case exclusion.Reason == snapshot.ReasonRebuildable:
			w.out.Rebuildable = append(w.out.Rebuildable, rel)
		case exclusion.Reason == snapshot.ReasonSubmodule && checked && problem == "":
			w.out.Submodules = append(w.out.Submodules, rel)
		default:
			if problem != "" {
				w.out.Reasons[rel] = problem
			}
			w.refuseOrName(rel)
		}
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	}
	if d.IsDir() {
		return nil
	}
	info, err := d.Info()
	if err != nil {
		return err
	}
	if record, ok := w.files[rel]; ok && unchanged(record, info) {
		w.out.Captured++
		return nil
	}
	w.refuseOrName(rel)
	return nil
}

// TakeInventory walks the worktree and classifies every path against the
// manifest. A path changed since the snapshot counts as not captured, because
// the bytes on disk are not the bytes the snapshot holds. problems maps a
// submodule to what makes it unsaved, "" for one that can be fetched again; a
// submodule it does not list counts as unsaved.
func TakeInventory(root string, m *snapshot.Manifest, named []string, problems map[string]string) (*Inventory, error) {
	if err := ValidateNamed(named); err != nil {
		return nil, err
	}
	walk := &inventoryWalk{
		root: root, names: named, out: &Inventory{Reasons: map[string]string{}}, used: map[string]bool{}, problems: problems,
		files: make(map[string]snapshot.FileRecord, len(m.Files)), excluded: make(map[string]snapshot.Exclusion, len(m.Excluded)),
	}
	for _, record := range m.Files {
		walk.files[string(record.Path)] = record
	}
	for _, exclusion := range m.Excluded {
		walk.excluded[string(exclusion.Path)] = exclusion
	}
	if err := filepath.WalkDir(root, walk.visit); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, name := range named {
		if !walk.used[name] {
			walk.out.UnmatchedNames = append(walk.out.UnmatchedNames, name)
		}
	}
	sort.Strings(walk.out.Uncaptured)
	sort.Strings(walk.out.Named)
	sort.Strings(walk.out.Rebuildable)
	sort.Strings(walk.out.Submodules)
	return walk.out, nil
}

func ignoredIsRebuildable(rel string, patterns []string) bool {
	for _, part := range strings.Split(strings.TrimSuffix(rel, "/"), "/") {
		for _, pattern := range patterns {
			if ok, _ := path.Match(pattern, part); ok {
				return true
			}
		}
	}
	return false
}

// submoduleProblem says what in a submodule exists nowhere else, or "" when
// fetching want again would give back everything but rebuildable files.
func submoduleProblem(ctx context.Context, git *gitrun.Runner, full, want string, rebuildable []string) (string, error) {
	if _, err := os.Lstat(filepath.Join(full, ".git")); err != nil {
		entries, readErr := os.ReadDir(full)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return "", readErr
		}
		if len(entries) > 0 {
			return "it is not initialized but holds files", nil
		}
		return "", nil
	}
	head, err := git.Run(ctx, full, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(head.Stdout)) != want {
		return "it moved from the recorded commit", nil
	}
	status, err := git.Run(ctx, full, "status", "--porcelain", "-z", "--ignored")
	if err != nil {
		return "", err
	}
	for _, entry := range strings.Split(string(status.Stdout), "\x00") {
		if entry == "" {
			continue
		}
		if !strings.HasPrefix(entry, "!! ") {
			return "it has uncommitted changes", nil
		}
		if !ignoredIsRebuildable(entry[3:], rebuildable) {
			return "it holds ignored files: " + entry[3:], nil
		}
	}
	unpushed, err := git.Run(ctx, full, "rev-list", "-n", "1", "HEAD", "--not", "--remotes")
	if err != nil {
		return "", err
	}
	if len(strings.TrimSpace(string(unpushed.Stdout))) > 0 {
		return "it has commits on no remote", nil
	}
	return nestedProblem(ctx, git, full, rebuildable)
}

// nestedProblem applies the same test to the submodules of a submodule.
func nestedProblem(ctx context.Context, git *gitrun.Runner, full string, rebuildable []string) (string, error) {
	listed, err := git.Run(ctx, full, "ls-files", "-z", "-s")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(listed.Stdout), "\x00") {
		meta, rel, _ := strings.Cut(line, "\t")
		fields := strings.Fields(meta)
		if len(fields) < 2 || fields[0] != "160000" {
			continue
		}
		problem, err := submoduleProblem(ctx, git, filepath.Join(full, rel), fields[1], rebuildable)
		if err != nil {
			return "", err
		}
		if problem != "" {
			return rel + ": " + problem, nil
		}
	}
	return "", nil
}

// CheckSubmodules tests every submodule the manifest recorded.
func CheckSubmodules(ctx context.Context, git *gitrun.Runner, root string, m *snapshot.Manifest) (map[string]string, error) {
	problems := make(map[string]string, len(m.Submodules))
	for _, submodule := range m.Submodules {
		rel := string(submodule.Path)
		problem, err := submoduleProblem(ctx, git, filepath.Join(root, rel), submodule.IndexCommit, m.Rebuildable)
		if err != nil {
			return nil, err
		}
		problems[rel] = problem
	}
	return problems, nil
}
