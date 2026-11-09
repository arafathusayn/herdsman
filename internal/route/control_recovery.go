package route

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/snapshot"
)

// Pipeline is the Recovery a daemon runs: a snapshot registered with undo, with
// every removed folder as a scratch archive. Undo puts scratch folders back at
// their own paths, which the repository's tree alone could not do, since the
// repository is still there.
type Pipeline struct{ Recovery *ops.Recovery }

func snapshotTask(route string) string { return "route-" + route }

// dropSnapshotRef removes the ref a capture made. The bundle holds the commit,
// and no later step of the delete or its undo reads the ref: an undo restores a
// standalone tree, because the repository's branch is usually still checked out.
func dropSnapshotRef(ctx context.Context, repo, task, op string) error {
	return exec.CommandContext(ctx, "git", "-C", repo, "update-ref", "-d", "refs/herdsman/snapshot/"+task+"/"+op).Run()
}

// Capture builds the snapshot beside its destination, registers it, and only
// then publishes it, so a crash leaves either no destination or a finished one.
func (p Pipeline) Capture(ctx context.Context, q SnapshotRequest) (err error) {
	partial := q.Dest + ".partial"
	if err := os.RemoveAll(partial); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(partial)) }()
	task := snapshotTask(q.Route)
	_ = dropSnapshotRef(ctx, q.Repo, task, q.Operation) // an interrupted attempt's, if any
	if _, err := snapshot.Take(ctx, snapshot.Options{Worktree: q.Repo, Dest: partial, Task: task, Op: q.Operation}); err != nil {
		return err
	}
	var res ops.Resources
	for i, target := range q.Remove {
		name := fmt.Sprintf("scratch-%d.tar", i)
		sum, err := packFolder(target, filepath.Join(partial, name))
		if err != nil {
			return fmt.Errorf("archive %s: %w", target, err)
		}
		res.Scratch = append(res.Scratch, ops.ScratchArchive{Path: target, Archive: name, SHA256: sum})
	}
	raw, err := json.Marshal(res)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(partial, ops.ResourcesName), raw, 0o600); err != nil {
		return err
	}
	if err := p.Recovery.Register(ops.SnapshotRecord{ID: q.Operation, Task: task, Dir: q.Dest}, partial); err != nil {
		return err
	}
	if err := os.Rename(partial, q.Dest); err != nil {
		return err
	}
	return dropSnapshotRef(ctx, q.Repo, task, q.Operation)
}

// Verify proves the snapshot is registered and whole, and binds it to every
// target: through the worktree inventory inside the repository, and through a
// scratch archive wherever the delete removes the target.
func (p Pipeline) Verify(ctx context.Context, dest string, targets []SnapshotTarget) error {
	if err := p.registered(dest); err != nil {
		return err
	}
	m, err := snapshot.ReadManifest(dest)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(dest, ops.ResourcesName))
	if err != nil {
		return err
	}
	var res ops.Resources
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("%s: %w", ops.ResourcesName, err)
	}
	var inside []SnapshotTarget
	for _, target := range targets {
		rel, relErr := filepath.Rel(m.Worktree, target.Path)
		isInside := relErr == nil && filepath.IsLocal(rel) && rel != "."
		if isInside {
			inside = append(inside, target)
		}
		i := slices.IndexFunc(res.Scratch, func(a ops.ScratchArchive) bool { return a.Path == target.Path })
		if i < 0 {
			if !isInside {
				return fmt.Errorf("%s: no archive in the snapshot covers it", target.Path)
			}
			continue
		}
		if err := checkScratch(dest, res.Scratch[i], target.Live); err != nil {
			return fmt.Errorf("%s: %w", target.Path, err)
		}
	}
	return VerifySnapshot(ctx, dest, inside)
}

func (p Pipeline) registered(dest string) error {
	list, err := p.Recovery.Snapshots()
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(list, func(s ops.SnapshotRecord) bool { return s.Dir == dest }) {
		return errors.New("undo has no record of the snapshot")
	}
	return nil
}

// packFolder writes root as a tar, in the form undo unpacks, and returns the
// archive's checksum. Sockets hold no data and are left out.
func packFolder(root, archive string) (sum string, err error) {
	out, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	hash := sha256.New()
	w := tar.NewWriter(io.MultiWriter(out, hash))
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return err
		}
		return packEntry(w, path, filepath.ToSlash(rel), entry)
	})
	if err = errors.Join(err, w.Close()); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func packEntry(w *tar.Writer, path, name string, entry fs.DirEntry) error {
	info, err := entry.Info()
	if err != nil {
		return err
	}
	link := ""
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		if link, err = os.Readlink(path); err != nil {
			return err
		}
	case !info.Mode().IsRegular() && !info.IsDir():
		return nil
	}
	h, err := tar.FileInfoHeader(info, link)
	if err != nil {
		return err
	}
	h.Name = name
	if err := w.WriteHeader(h); err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// checkScratch proves an archive has its recorded bytes and, while the folder
// still exists, holds exactly what the folder holds.
func checkScratch(dest string, a ops.ScratchArchive, live string) error {
	if !filepath.IsLocal(a.Archive) {
		return fmt.Errorf("archive %q is outside the snapshot", a.Archive)
	}
	archive := filepath.Join(dest, a.Archive)
	raw, err := os.ReadFile(archive)
	if err != nil {
		return err
	}
	got := sha256.Sum256(raw)
	if hex.EncodeToString(got[:]) != a.SHA256 {
		return errors.New("its archive is damaged")
	}
	if live == "" {
		return nil
	}
	held, err := archiveListing(archive)
	if err != nil {
		return err
	}
	now, err := folderListing(live)
	if err != nil {
		return err
	}
	if !maps.Equal(held, now) {
		return errors.New("it changed after the snapshot")
	}
	return nil
}

func contentID(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return "file:" + hex.EncodeToString(h.Sum(nil)), nil
}

func archiveListing(archive string) (map[string]string, error) {
	f, err := os.Open(archive)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	reader := tar.NewReader(f)
	for {
		h, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		name := filepath.Clean(h.Name)
		switch h.Typeflag {
		case tar.TypeDir:
			out[name] = "dir"
		case tar.TypeSymlink:
			out[name] = "link:" + h.Linkname
		default:
			if out[name], err = contentID(reader); err != nil {
				return nil, err
			}
		}
	}
}

func folderListing(root string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return err
		}
		switch {
		case entry.IsDir():
			out[rel] = "dir"
		case entry.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			out[rel] = "link:" + link
		case entry.Type().IsRegular():
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			if out[rel], err = contentID(f); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}
