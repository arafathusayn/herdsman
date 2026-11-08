package ops

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
	"os"
	"path/filepath"
	"strings"

	"github.com/arafathusayn/herdsman/internal/snapshot"
)

// ResourcesName is the file beside a snapshot's manifest that lists what was
// captured outside the worktree. A snapshot without one has no such resources.
const ResourcesName = "resources.json"

// ErrNoVolumeRuntime means a snapshot holds volumes and nothing can import them.
var ErrNoVolumeRuntime = errors.New("the snapshot holds volumes and no container runtime is configured")

// ScratchArchive is one scratch folder, as a tar in the snapshot folder.
type ScratchArchive struct {
	Path    string `json:"path"`
	Archive string `json:"archive"`
	SHA256  string `json:"sha256"`
}

// VolumeArchive is one exported volume, as a tar in the snapshot folder.
type VolumeArchive struct {
	Name    string `json:"name"`
	Archive string `json:"archive"`
	SHA256  string `json:"sha256"`
}

// Resources lists what a snapshot holds beyond the worktree.
type Resources struct {
	Scratch []ScratchArchive `json:"scratch,omitempty"`
	Volumes []VolumeArchive  `json:"volumes,omitempty"`
}

// VolumeStore re-imports volumes under their names.
type VolumeStore interface {
	// Compare reports whether the volume exists and holds what archive holds.
	Compare(ctx context.Context, name, archive string) (exists, same bool, err error)
	Import(ctx context.Context, name, archive string) error
}

func readResources(dir string) (*Resources, error) {
	raw, err := os.ReadFile(filepath.Join(dir, ResourcesName))
	if errors.Is(err, fs.ErrNotExist) {
		return &Resources{}, nil
	}
	if err != nil {
		return nil, err
	}
	var res Resources
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("%s: %w", ResourcesName, err)
	}
	return &res, nil
}

// checkArchive refuses a name outside the snapshot folder or changed bytes.
func checkArchive(dir, name, want string) (string, error) {
	if !filepath.IsLocal(name) {
		return "", fmt.Errorf("%w: archive %q", snapshot.ErrUnsafePath, name)
	}
	file := filepath.Join(dir, name)
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return "", fmt.Errorf("archive %s: checksum %s, the snapshot recorded %s", name, got, want)
	}
	return file, nil
}

func (r *Recovery) checkResources(dir string, res *Resources) error {
	for _, a := range res.Scratch {
		if _, err := checkArchive(dir, a.Archive, a.SHA256); err != nil {
			return err
		}
	}
	for _, a := range res.Volumes {
		if _, err := checkArchive(dir, a.Archive, a.SHA256); err != nil {
			return err
		}
	}
	if len(res.Volumes) > 0 && r.opt.Volumes == nil {
		return fmt.Errorf("%w: %s", ErrNoVolumeRuntime, res.Volumes[0].Name)
	}
	return nil
}

// UndoRequest names a snapshot and, optionally, where it goes.
type UndoRequest struct {
	Snapshot string `json:"snapshot"`
	// Target is where to restore; empty means the original path when it is
	// free and a new path beside it otherwise.
	Target string `json:"target,omitempty"`
}

// UndoResult says where the snapshot went.
type UndoResult struct {
	Op       *Operation `json:"operation"`
	Target   string     `json:"target"`
	Original bool       `json:"original"`
}

type undoSpec struct {
	Snapshot string `json:"snapshot"`
	Target   string `json:"target"`
}

// pathFree is true for a missing path or an empty folder.
func pathFree(p string) (bool, error) {
	info, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, nil
	}
	entries, err := os.ReadDir(p)
	return err == nil && len(entries) == 0, err
}

func chooseTarget(q UndoRequest, id, original string) (string, bool, error) {
	if q.Target != "" {
		if !filepath.IsAbs(q.Target) {
			return "", false, fmt.Errorf("undo: target %q must be absolute", q.Target)
		}
		free, err := pathFree(q.Target)
		if err != nil {
			return "", false, err
		}
		if !free {
			return "", false, fmt.Errorf("%w: %s holds other files and the snapshot %s would replace them", snapshot.ErrTargetOccupied, q.Target, id)
		}
		return q.Target, q.Target == original, nil
	}
	free, err := pathFree(original)
	if err != nil {
		return "", false, err
	}
	if free {
		return original, true, nil
	}
	return original + ".undo-" + strings.ToLower(id[len(id)-6:]), false, nil
}

// Undo restores a snapshot. Everything that can refuse is checked here, before
// the operation is journaled, so a refusal changes nothing.
func (r *Recovery) Undo(ctx context.Context, q UndoRequest) (*UndoResult, error) {
	rec, err := r.snapshotByID(q.Snapshot)
	if err != nil {
		return nil, err
	}
	m, err := snapshot.Verify(ctx, rec.Dir)
	if err != nil {
		return nil, err
	}
	res, err := readResources(rec.Dir)
	if err != nil {
		return nil, err
	}
	if err := r.checkResources(rec.Dir, res); err != nil {
		return nil, err
	}
	target, original, err := chooseTarget(q, rec.ID, m.Worktree)
	if err != nil {
		return nil, err
	}
	spec, err := json.Marshal(undoSpec{Snapshot: rec.ID, Target: target})
	if err != nil {
		return nil, err
	}
	op, err := r.run(ctx, Spec{Kind: "undo", Plan: "restore snapshot " + rec.ID + " to " + target,
		Locks: []string{TaskLock(rec.Task), SnapshotLock(rec.ID)},
		Steps: []StepSpec{{Name: "restore " + rec.ID, Effect: effectUndo, Key: "undo:" + target, Spec: string(spec)}}})
	if err != nil {
		return nil, err
	}
	return &UndoResult{Op: op, Target: target, Original: original}, nil
}

type undoEffect struct{ r *Recovery }

func (fx undoEffect) load(st Step) (*undoSpec, *SnapshotRecord, *snapshot.Manifest, error) {
	var spec undoSpec
	if err := json.Unmarshal([]byte(st.Spec), &spec); err != nil {
		return nil, nil, nil, err
	}
	rec, err := fx.r.snapshotByID(spec.Snapshot)
	if err != nil {
		return nil, nil, nil, err
	}
	m, err := snapshot.ReadManifest(rec.Dir)
	if err != nil {
		return nil, nil, nil, err
	}
	return &spec, rec, m, nil
}

// Apply puts the resources back first and publishes the tree last, by one
// rename: the tree's presence means the undo finished.
func (fx undoEffect) Apply(ctx context.Context, st Step) error {
	spec, rec, m, err := fx.load(st)
	if err != nil {
		return err
	}
	if free, err := pathFree(spec.Target); err != nil {
		return err
	} else if !free {
		return fmt.Errorf("%w: %s", snapshot.ErrTargetOccupied, spec.Target)
	}
	res, err := readResources(rec.Dir)
	if err != nil {
		return err
	}
	if err := fx.restoreResources(ctx, rec.Dir, res); err != nil {
		return err
	}
	staging := filepath.Join(filepath.Dir(spec.Target), ".herdsman-undo-"+st.OpID)
	holder := staging + "-link"
	if err := errors.Join(os.RemoveAll(staging), os.RemoveAll(holder)); err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	defer os.RemoveAll(holder)
	if err := snapshot.Restore(ctx, rec.Dir, staging); err != nil {
		return err
	}
	if repoAvailable(ctx, rec.Repo) && m.Head != "" {
		if err := linkWorktree(ctx, rec, m, staging, holder, spec.Target); err != nil {
			return err
		}
	}
	fx.r.eng.fault("undo:staged")
	if err := os.Remove(spec.Target); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(staging, spec.Target); err != nil {
		return err
	}
	fx.r.eng.fault("undo:published")
	return nil
}

func repoAvailable(ctx context.Context, repo string) bool {
	if repo == "" {
		return false
	}
	_, err := runGit(ctx, repo, "rev-parse", "--git-common-dir")
	return err == nil
}

// linkWorktree turns the standalone restored repository into a worktree of
// the source repository: the objects and the branch go into the repository,
// and the folder keeps only the link and its index. The worktree's record is
// written with its final path, so nothing has to change after the rename.
func linkWorktree(ctx context.Context, rec *SnapshotRecord, m *snapshot.Manifest, staging, holder, target string) error {
	bundle := filepath.Join(rec.Dir, snapshot.BundleName)
	if _, err := runGit(ctx, rec.Repo, "fetch", "-q", "--no-auto-maintenance", bundle, "+"+m.SnapshotRef+":"+m.SnapshotRef); err != nil {
		return err
	}
	// An interrupted attempt leaves a record whose folder never appeared.
	if _, err := runGit(ctx, rec.Repo, "worktree", "prune"); err != nil {
		return err
	}
	add := []string{"worktree", "add", "-q", "--no-checkout"}
	if m.Branch == "" {
		add = append(add, "--detach", holder, m.Head)
	} else {
		if err := recreateBranch(ctx, rec.Repo, m); err != nil {
			return err
		}
		add = append(add, holder, strings.TrimPrefix(m.Branch, "refs/heads/"))
	}
	if _, err := runGit(ctx, rec.Repo, add...); err != nil {
		return err
	}
	admin, err := runGit(ctx, holder, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	if err := copyFile(filepath.Join(staging, ".git", "index"), filepath.Join(admin, "index"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(admin, "gitdir"), []byte(filepath.Join(target, ".git")+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(staging, ".git")); err != nil {
		return err
	}
	return os.Rename(filepath.Join(holder, ".git"), filepath.Join(staging, ".git"))
}

// recreateBranch creates the branch, accepts it at the recorded commit and
// refuses it anywhere else: a branch that moved holds work the undo must not
// overwrite.
func recreateBranch(ctx context.Context, repo string, m *snapshot.Manifest) error {
	current, err := runGit(ctx, repo, "for-each-ref", "--format=%(objectname)", m.Branch)
	if err != nil {
		return err
	}
	switch current {
	case m.Head:
		return nil
	case "":
		_, err := runGit(ctx, repo, "update-ref", m.Branch, m.Head, "")
		return err
	}
	return fmt.Errorf("undo: branch %s is at %s and the snapshot recorded %s; move or delete the branch, or restore elsewhere", m.Branch, current, m.Head)
}

func (fx undoEffect) restoreResources(ctx context.Context, dir string, res *Resources) error {
	if err := fx.r.checkResources(dir, res); err != nil {
		return err
	}
	for _, a := range res.Scratch {
		if err := restoreScratch(filepath.Join(dir, a.Archive), a.Path); err != nil {
			return err
		}
	}
	for _, a := range res.Volumes {
		archive := filepath.Join(dir, a.Archive)
		exists, same, err := fx.r.opt.Volumes.Compare(ctx, a.Name, archive)
		if err != nil {
			return err
		}
		if exists && !same {
			return fmt.Errorf("undo: volume %s exists and differs from the snapshot's copy; nothing was overwritten", a.Name)
		}
		if !exists {
			if err := fx.r.opt.Volumes.Import(ctx, a.Name, archive); err != nil {
				return err
			}
		}
	}
	return nil
}

// restoreScratch unpacks beside path and renames. A folder already holding
// the same content counts as restored, which also makes a retry safe.
func restoreScratch(archive, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".herdsman-undo-tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := unpackTar(archive, tmp); err != nil {
		return err
	}
	free, err := pathFree(path)
	if err != nil {
		return err
	}
	if !free {
		same, err := sameTree(tmp, path)
		if err != nil {
			return err
		}
		if !same {
			return fmt.Errorf("undo: the scratch folder %s exists and differs from the snapshot's copy; nothing was overwritten", path)
		}
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Rename(tmp, path)
}

func unpackTar(archive, dest string) error {
	if err := os.Mkdir(dest, 0o700); err != nil {
		return err
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	reader := tar.NewReader(f)
	for {
		h, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := unpackEntry(root, reader, h); err != nil {
			return fmt.Errorf("%s: %w", h.Name, err)
		}
	}
}

func unpackEntry(root *os.Root, content io.Reader, h *tar.Header) error {
	name := filepath.Clean(h.Name)
	if !filepath.IsLocal(name) {
		return snapshot.ErrUnsafePath
	}
	if parent := filepath.Dir(name); parent != "." {
		if err := root.MkdirAll(parent, 0o700); err != nil {
			return err
		}
	}
	switch h.Typeflag {
	case tar.TypeDir:
		return root.MkdirAll(name, fs.FileMode(h.Mode).Perm())
	case tar.TypeSymlink:
		return root.Symlink(h.Linkname, name)
	case tar.TypeReg:
		out, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fs.FileMode(h.Mode).Perm())
		if err != nil {
			return err
		}
		_, err = io.Copy(out, content)
		return errors.Join(err, out.Close())
	}
	return fmt.Errorf("unsupported entry type %q", h.Typeflag)
}

func treeListing(root string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." {
			return err
		}
		switch {
		case d.IsDir():
			out[rel] = "dir"
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			out[rel] = "link:" + target
			return err
		default:
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			h := sha256.Sum256(b)
			out[rel] = "file:" + hex.EncodeToString(h[:])
		}
		return nil
	})
	return out, err
}

func sameTree(a, b string) (bool, error) {
	left, err := treeListing(a)
	if err != nil {
		return false, err
	}
	right, err := treeListing(b)
	if err != nil || len(left) != len(right) {
		return false, err
	}
	for k, v := range left {
		if right[k] != v {
			return false, nil
		}
	}
	return true, nil
}

// Probe compares the target's head with the snapshot, not only its presence:
// another tree at that path is a conflict for a person.
func (fx undoEffect) Probe(ctx context.Context, st Step) (Verdict, string, error) {
	spec, _, m, err := fx.load(st)
	if err != nil {
		return Unknown, "", err
	}
	free, err := pathFree(spec.Target)
	if err != nil {
		return Unknown, "", err
	}
	if free {
		return Absent, spec.Target + " is free", nil
	}
	head, err := runGit(ctx, spec.Target, "rev-parse", "HEAD")
	if err != nil || head != m.Head {
		return Different, fmt.Sprintf("%s holds another tree (head %q)", spec.Target, head), nil
	}
	return Matches, "the tree is restored", nil
}
