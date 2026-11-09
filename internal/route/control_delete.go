package route

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/snapshot"
)

// snapshotRoot is where a route's snapshots live, in the common git folder so
// that every worktree of the repository finds them.
func snapshotRoot(ctx context.Context, repo string) (string, error) {
	gitPath := filepath.Join(repo, ".git")
	if st, err := os.Stat(gitPath); err == nil && st.IsDir() {
		return filepath.Join(gitPath, "herdsman", "snapshots", "routes"), nil
	}
	out, err := exec.CommandContext(ctx, "git", "-C", repo, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return "", fmt.Errorf("find the common git folder: %w", err)
	}
	return filepath.Join(strings.TrimSpace(string(out)), "herdsman", "snapshots", "routes"), nil
}

// ErrNoRecovery means this build cannot undo a delete, so it refuses one.
var ErrNoRecovery = errors.New("delete needs the recovery pipeline's snapshot and undo, which this build does not have")

// SnapshotRequest asks the recovery pipeline for a snapshot of a route.
type SnapshotRequest struct {
	Route, Repo, Operation, Dest string
	Targets                      []string
}

// SnapshotTarget is one path a delete removes. Live is where its bytes are now:
// the path itself, its staged name, or empty once it is gone.
type SnapshotTarget struct{ Path, Live string }

// Recovery is the pipeline that makes a delete undoable. Verify must fail unless
// the snapshot is whole and its inventory covers every target.
type Recovery interface {
	Capture(ctx context.Context, q SnapshotRequest) error
	Verify(ctx context.Context, dest string, targets []SnapshotTarget) error
}

func stagedPath(target string) string { return target + ".deleting" }

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func liveOf(target string) string {
	switch {
	case exists(target):
		return target
	case exists(stagedPath(target)):
		return stagedPath(target)
	}
	return ""
}

func targetsOf(paths []string) []SnapshotTarget {
	out := make([]SnapshotTarget, 0, len(paths))
	for _, p := range paths {
		out = append(out, SnapshotTarget{Path: p, Live: liveOf(p)})
	}
	return out
}

// VerifySnapshot proves a snapshot with the recovery pipeline's verifier, then
// binds its inventory to the targets: each must lie inside the snapshot, have
// entries in it, have nothing left out of it, and match it wherever it still
// exists. A target with no bytes left is judged on the inventory alone.
func VerifySnapshot(ctx context.Context, dest string, targets []SnapshotTarget) error {
	if len(targets) == 0 {
		return errors.New("the verification names no target")
	}
	m, err := snapshot.Verify(ctx, dest)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if err := bindTarget(m, target); err != nil {
			return fmt.Errorf("%s: %w", target.Path, err)
		}
	}
	return nil
}

func under(path, prefix string) bool { return path == prefix || strings.HasPrefix(path, prefix+"/") }

func bindTarget(m *snapshot.Manifest, target SnapshotTarget) error {
	rel, err := filepath.Rel(m.Worktree, target.Path)
	if err != nil || !filepath.IsLocal(rel) || rel == "." {
		return errors.New("it is outside the snapshot")
	}
	rel = filepath.ToSlash(rel)
	for _, e := range m.Excluded {
		if under(string(e.Path), rel) && e.Reason != snapshot.ReasonSpecial {
			return fmt.Errorf("the snapshot left out %s (%s)", e.Path, e.Reason)
		}
	}
	files := map[string]snapshot.FileRecord{}
	for _, f := range m.Files {
		if under(string(f.Path), rel) {
			files[strings.TrimPrefix(strings.TrimPrefix(string(f.Path), rel), "/")] = f
		}
	}
	if len(files) == 0 {
		return errors.New("the snapshot's inventory does not cover it")
	}
	if target.Live == "" {
		return nil
	}
	return matchLive(target.Live, files)
}

// matchLive compares what is on disk with a target's inventory, both ways.
func matchLive(root string, files map[string]snapshot.FileRecord) error {
	seen := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		if !mode.IsRegular() && mode&fs.ModeSymlink == 0 {
			return nil // folders follow from their files; sockets hold no data
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		f, ok := files[filepath.ToSlash(name)]
		if !ok {
			return fmt.Errorf("%s is not in the snapshot", name)
		}
		if f.Size != info.Size() || f.MTimeNS != info.ModTime().UnixNano() {
			return fmt.Errorf("%s changed after the snapshot", name)
		}
		seen++
		return nil
	})
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.Mode&0o170000 == 0o040000 {
			seen++ // a kept empty folder
		}
	}
	if seen != len(files) {
		return fmt.Errorf("%d of the snapshot's %d entries are still there", seen, len(files))
	}
	return nil
}

func verifyCaptures(ctx context.Context, deps Deps, d stepData) error {
	if deps.Recovery == nil {
		return ErrNoRecovery
	}
	if err := deps.Recovery.Verify(ctx, d.Control.Snapshot, targetsOf(d.Control.Captures)); err != nil {
		return fmt.Errorf("no verified snapshot: %w", err)
	}
	return nil
}

func applySnapshot(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	if deps.Recovery == nil {
		return ErrNoRecovery
	}
	if !exists(d.Control.Snapshot) {
		if err := os.MkdirAll(filepath.Dir(d.Control.Snapshot), 0o700); err != nil {
			return err
		}
		q := SnapshotRequest{Route: d.Route, Repo: d.Repo, Operation: d.Op, Dest: d.Control.Snapshot, Targets: d.Control.Captures}
		if err := deps.Recovery.Capture(ctx, q); err != nil {
			return fmt.Errorf("take the snapshot: %w", err)
		}
	}
	return verifyCaptures(ctx, deps, d)
}

func probeSnapshot(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	if deps.Recovery == nil || !exists(d.Control.Snapshot) {
		return ops.Absent, "", nil
	}
	if err := verifyCaptures(ctx, deps, d); err != nil {
		return ops.Unknown, err.Error(), nil
	}
	return ops.Matches, "", nil
}

// applyDown frees the route's tasks, once the snapshot still verifies.
func applyDown(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	if deps.Down == nil {
		return ErrNoRecovery
	}
	if err := verifyCaptures(ctx, deps, d); err != nil {
		return err
	}
	return deps.Down(ctx, d.Repo, d.Folder)
}

// probeUnknown serves a step only its own rerun can settle.
func probeUnknown(context.Context, Deps, *herdr.Client, stepData) (ops.Verdict, string, error) {
	return ops.Unknown, "only the step itself can tell", nil
}

func applyTeardown(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	if deps.Teardown == nil {
		return errors.New("this build has no teardown")
	}
	return deps.Teardown(ctx, d.Repo, d.Folder)
}

// routeOwns reports whether a path is one of the two a route's start made.
func routeOwns(d stepData, target string) bool {
	switch target {
	case "":
		return false
	case d.Inbox:
		return true
	case d.Folder:
		return filepath.Dir(d.Folder) == filepath.Join(d.Repo, ".herdsman") && strings.HasPrefix(filepath.Base(d.Folder), d.Route+"-")
	}
	return false
}

func unstage(renamed []string) error {
	var errs []error
	for _, target := range renamed {
		errs = append(errs, os.Rename(stagedPath(target), target))
	}
	return errors.Join(errs...)
}

// applyRemove is all or nothing up to the last check: every target is verified,
// moved aside and verified again before the workspace closes and any byte goes.
// A failure before that point puts every target back.
func applyRemove(ctx context.Context, deps Deps, c *herdr.Client, d stepData) error {
	for _, target := range d.Control.Targets {
		if !routeOwns(d, target) {
			return fmt.Errorf("refusing to remove %s: the route did not create it", target)
		}
	}
	if err := verifyCaptures(ctx, deps, d); err != nil {
		return err
	}
	var renamed []string
	back := func(cause error) error { return errors.Join(cause, unstage(renamed)) }
	for _, target := range d.Control.Targets {
		if !exists(target) {
			if exists(stagedPath(target)) { // a crashed run moved it aside
				renamed = append(renamed, target)
			}
			continue
		}
		if err := os.Rename(target, stagedPath(target)); err != nil {
			return back(err)
		}
		renamed = append(renamed, target)
	}
	if err := verifyCaptures(ctx, deps, d); err != nil {
		return back(err)
	}
	if err := closeWorkspace(ctx, c, d.Workspace); err != nil {
		return back(err)
	}
	for _, target := range d.Control.Targets {
		if err := os.RemoveAll(stagedPath(target)); err != nil {
			return err
		}
	}
	return nil
}

func closeWorkspace(ctx context.Context, c *herdr.Client, workspace string) error {
	layout, err := c.Snapshot(ctx)
	if err != nil {
		return err
	}
	id := workspaceID(layout, workspace)
	if id == "" {
		return nil
	}
	if err := c.Call(ctx, "workspace.close", map[string]any{"workspace_id": id}, nil); err != nil && !isNotFound(err) {
		return err
	}
	return nil
}

// probeRemove says Matches only when the snapshot still verifies and every
// target is gone: a target's absence alone proves nothing. Every target in
// place and none staged means the step changed nothing.
func probeRemove(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	whole, gone := true, true
	for _, target := range d.Control.Targets {
		if !routeOwns(d, target) || !exists(target) || exists(stagedPath(target)) {
			whole = false
		}
		if exists(target) || exists(stagedPath(target)) {
			gone = false
		}
	}
	if whole {
		return ops.Absent, "", nil
	}
	if err := verifyCaptures(ctx, deps, d); err != nil {
		return ops.Unknown, err.Error(), nil
	}
	if gone {
		return ops.Matches, "", nil
	}
	return ops.Unknown, "some targets remain", nil
}

// SnapshotOf returns the snapshot folder a delete operation writes.
func SnapshotOf(op *ops.Operation) string {
	for _, st := range op.Steps {
		if st.Effect != effectSnapshot {
			continue
		}
		if d, err := decode(st); err == nil && d.Control != nil {
			return d.Control.Snapshot
		}
	}
	return ""
}
