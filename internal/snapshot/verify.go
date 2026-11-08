package snapshot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// VerifyError names the part of a snapshot that failed.
type VerifyError struct {
	Artifact string
	Err      error
}

func (e *VerifyError) Error() string {
	return "snapshot verification failed: " + e.Artifact + ": " + e.Err.Error()
}

func (e *VerifyError) Unwrap() error { return e.Err }

func failure(artifact string, err error) error {
	return &VerifyError{Artifact: artifact, Err: err}
}

var objectID = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// absPaths resolves each path now: git runs in other folders later.
func absPaths(paths ...*string) error {
	for _, p := range paths {
		resolved, err := osAbs(*p)
		if err != nil {
			return err
		}
		*p = resolved
	}
	return nil
}

// Verify proves a snapshot is whole and restorable: the folder is private,
// each checksum holds, every bundle object is re-read in a scratch repository,
// the recorded HEAD and both trees match the manifest, and the archive holds
// exactly what is listed, restorable together with the tree and with no entry
// that could leave a restore folder.
func Verify(ctx context.Context, dir string) (*Manifest, error) {
	if err := absPaths(&dir); err != nil {
		return nil, failure(ManifestName, err)
	}
	m, err := ReadManifest(dir)
	if err != nil {
		return nil, failure(ManifestName, err)
	}
	if err := checkManifest(ctx, m); err != nil {
		return nil, failure(ManifestName, err)
	}
	info, err := osStat(dir)
	if err != nil {
		return nil, failure(FolderArtifact, err)
	}
	if info.Mode().Perm() != 0o700 {
		return nil, failure(FolderArtifact, fmt.Errorf("mode is %o, want 700", info.Mode().Perm()))
	}
	gitlinks, err := verifyBundle(ctx, filepath.Join(dir, BundleName), m)
	if err != nil {
		return nil, failure(BundleName, err)
	}
	preserved := filepath.Join(dir, PreservedName)
	if err := checkSum(preserved, m.Preserved); err != nil {
		return nil, failure(PreservedName, err)
	}
	if err := checkPreserved(preserved, m); err != nil {
		return nil, failure(PreservedName, err)
	}
	if err := checkLayout(m, gitlinks); err != nil {
		return nil, failure(PreservedName, err)
	}
	return m, nil
}

// checkManifest rejects values that git would read as options or that could
// leave the restore folder. Preserved paths are checked with their tar
// entries, which must match them exactly.
func checkManifest(ctx context.Context, m *Manifest) error {
	if m.ObjectFormat != "sha1" && m.ObjectFormat != "sha256" {
		return fmt.Errorf("unknown object format %q", m.ObjectFormat)
	}
	for _, id := range []string{m.SnapshotCommit, m.StagedTree, m.WorkingTree} {
		if !objectID.MatchString(id) {
			return fmt.Errorf("invalid object id %q", id)
		}
	}
	if m.Head != "" && !objectID.MatchString(m.Head) {
		return fmt.Errorf("invalid head %q", m.Head)
	}
	if m.Head == "" && m.Branch == "" {
		return errors.New("neither head nor branch recorded")
	}
	if m.Branch != "" {
		if !strings.HasPrefix(m.Branch, "refs/heads/") {
			return fmt.Errorf("invalid branch %q", m.Branch)
		}
		if _, err := runGit(ctx, "", nil, nil, "check-ref-format", m.Branch); err != nil {
			return err
		}
	}
	for _, f := range m.Files {
		if f.Source == SourceTree {
			if err := safeRel(string(f.Path)); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkSum(file string, want Artifact) error {
	sum, size, err := checksum(file)
	if err != nil {
		return err
	}
	if sum != want.SHA256 || size != want.Size {
		return fmt.Errorf("checksum mismatch: file is %s (%d bytes), manifest says %s (%d bytes)", sum, size, want.SHA256, want.Size)
	}
	return nil
}

// verifyBundle fetches into a scratch repository because `git bundle verify`
// reads only the header and prerequisites; a fetch hashes every object. It
// returns the working tree's gitlink paths.
func verifyBundle(ctx context.Context, file string, m *Manifest) ([]string, error) {
	if err := checkSum(file, m.Bundle); err != nil {
		return nil, err
	}
	scratch, err := os.MkdirTemp("", "herdsman-verify-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	if _, err := runGit(ctx, scratch, nil, nil, "init", "-q", "--bare", "--template=", "--object-format="+m.ObjectFormat); err != nil {
		return nil, err
	}
	if _, err := runGit(ctx, scratch, nil, nil, "bundle", "verify", "-q", file); err != nil {
		return nil, err
	}
	if err := fetchBundle(ctx, scratch, file); err != nil {
		return nil, err
	}
	return checkTrees(ctx, scratch, m)
}

func fetchBundle(ctx context.Context, repo, file string) error {
	_, err := runGit(ctx, repo, nil, nil, "fetch", "-q", "--no-auto-maintenance", "--update-head-ok", file, "+refs/*:refs/*")
	return err
}

type treeRow struct {
	mode, kind, oid, path string
}

func listTree(ctx context.Context, repo string, args ...string) ([]treeRow, error) {
	raw, err := runGit(ctx, repo, nil, nil, append([]string{"ls-tree"}, args...)...)
	if err != nil {
		return nil, err
	}
	var rows []treeRow
	for _, line := range splitNUL(raw) {
		meta, name, _ := strings.Cut(line, "\t")
		fields := strings.Fields(meta)
		rows = append(rows, treeRow{mode: fields[0], kind: fields[1], oid: fields[2], path: name})
	}
	return rows, nil
}

func checkTrees(ctx context.Context, repo string, m *Manifest) ([]string, error) {
	if m.Head != "" {
		if _, err := runGit(ctx, repo, nil, nil, "cat-file", "-e", m.Head+"^{commit}"); err != nil {
			return nil, err
		}
	}
	top, err := listTree(ctx, repo, "-z", m.SnapshotCommit)
	if err != nil {
		return nil, err
	}
	found := map[string]string{}
	for _, row := range top {
		found[row.path] = row.oid
	}
	if found["staged"] != m.StagedTree || found["working"] != m.WorkingTree {
		return nil, fmt.Errorf("snapshot commit holds trees %v, manifest says staged %s, working %s", found, m.StagedTree, m.WorkingTree)
	}
	rows, err := listTree(ctx, repo, "-r", "-z", m.WorkingTree)
	if err != nil {
		return nil, err
	}
	return compareWorking(rows, m)
}

// compareWorking returns the gitlink paths, which the manifest's file list
// does not hold.
func compareWorking(rows []treeRow, m *Manifest) ([]string, error) {
	want := map[string]uint32{}
	for _, f := range m.Files {
		if f.Source == SourceTree {
			want[string(f.Path)] = f.Mode
		}
	}
	blobs := 0
	var gitlinks []string
	for _, row := range rows {
		if row.kind != "blob" {
			gitlinks = append(gitlinks, row.path)
			continue
		}
		blobs++
		// ls-tree prints valid octal, so the parse cannot fail.
		mode, _ := strconv.ParseUint(row.mode, 8, 32)
		if listed, ok := want[row.path]; !ok || uint64(listed) != mode {
			return nil, fmt.Errorf("working tree entry %q (mode %s) does not match the manifest", row.path, row.mode)
		}
	}
	if blobs != len(want) {
		return nil, fmt.Errorf("working tree holds %d files, manifest lists %d", blobs, len(want))
	}
	return gitlinks, nil
}
