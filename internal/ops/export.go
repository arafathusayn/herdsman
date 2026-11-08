package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/snapshot"
)

const (
	// ExportManifestName is the file that makes a folder an export.
	ExportManifestName = "export.json"
	exportSchema       = "1.0.0"
	exportStateDir     = "state"
	exportSnapshotDir  = "snapshots"
	accountDatabase    = "daemon/account.db"
)

// ErrDestinationExists means an export would replace something.
var ErrDestinationExists = errors.New("the export destination already exists")

// exportManifest is written last inside the export.
type exportManifest struct {
	Schema    string   `json:"schema"`
	Created   int64    `json:"created"`
	Op        string   `json:"op"`
	Digest    string   `json:"digest"`
	Snapshots []string `json:"snapshots"`
}

// Export copies the whole account to dest under the account lock. Mutating
// operations and the retention sweep wait until it ends, and dest appears by
// one rename, so an export exists whole or not at all.
func (r *Recovery) Export(ctx context.Context, dest string) (*Operation, error) {
	if !filepath.IsAbs(dest) {
		return nil, fmt.Errorf("export: destination %q must be absolute", dest)
	}
	dest = filepath.Clean(dest)
	if rel, err := filepath.Rel(r.opt.StateDir, dest); err != nil || filepath.IsLocal(rel) {
		return nil, fmt.Errorf("export: destination %s is inside the state directory", dest)
	}
	if _, err := os.Lstat(dest); !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrDestinationExists, dest)
	}
	op, err := r.eng.submitBarrier(ctx, Spec{Kind: "export", Plan: "export the account to " + dest, Locks: []string{AccountLock},
		Steps: []StepSpec{{Name: "export", Effect: effectExport, Key: "export:" + dest, Spec: dest}}})
	if err != nil {
		return nil, err
	}
	return finished(r.eng.Drive(ctx, op.ID))
}

type exportEffect struct{ r *Recovery }

func stagingPath(dest, op string) string { return dest + ".tmp-" + op }

// Apply starts by removing what an interrupted attempt of this operation
// left, then builds the export beside the destination.
func (fx exportEffect) Apply(ctx context.Context, st Step) error {
	dest := st.Spec
	fx.r.eng.fault("export:barrier")
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	tmp := stagingPath(dest, st.OpID)
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	if err := os.Mkdir(tmp, 0o700); err != nil {
		return err
	}
	if err := fx.build(ctx, st.OpID, tmp); err != nil {
		return errors.Join(err, os.RemoveAll(tmp))
	}
	fx.r.eng.fault("export:staged")
	if err := os.Rename(tmp, dest); err != nil {
		return errors.Join(err, os.RemoveAll(tmp))
	}
	return syncPath(filepath.Dir(dest))
}

func (fx exportEffect) build(ctx context.Context, op, tmp string) error {
	r := fx.r
	var digest string
	var snapshots []SnapshotRecord
	err := r.eng.Read(func(c store.Conn) error {
		recs, err := store.AccountRecords(c)
		if err != nil {
			return err
		}
		digest = store.Digest(recs)
		snapshots, err = loadSnapshots(c)
		return err
	})
	if err != nil {
		return err
	}
	if err := r.copyState(r.opt.StateDir, filepath.Join(tmp, exportStateDir)); err != nil {
		return err
	}
	ids := make([]string, 0, len(snapshots))
	for _, s := range snapshots {
		copied := filepath.Join(tmp, exportSnapshotDir, s.ID)
		if err := copySnapshot(ctx, s.Dir, copied); err != nil {
			return fmt.Errorf("export: snapshot %s: %w", s.ID, err)
		}
		ids = append(ids, s.ID)
	}
	raw, err := json.MarshalIndent(exportManifest{Schema: exportSchema, Created: r.eng.now(), Op: op, Digest: digest, Snapshots: ids}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, ExportManifestName), append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return syncTree(tmp)
}

func copySnapshot(ctx context.Context, from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	if err := copyTree(from, to, nil); err != nil {
		return err
	}
	_, err := snapshot.Verify(ctx, to)
	return err
}

// Probe reads the export's own manifest: a folder at dest from anyone else is
// a conflict.
func (fx exportEffect) Probe(_ context.Context, st Step) (Verdict, string, error) {
	raw, err := os.ReadFile(filepath.Join(st.Spec, ExportManifestName))
	if errors.Is(err, fs.ErrNotExist) {
		if _, serr := os.Lstat(st.Spec); errors.Is(serr, fs.ErrNotExist) {
			return Absent, "no export at the destination", nil
		}
		return Different, st.Spec + " exists and is not an export", nil
	}
	if err != nil {
		return Unknown, "", err
	}
	var m exportManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Different, "unreadable export manifest", nil
	}
	if m.Op != st.OpID {
		return Different, "an export of operation " + m.Op, nil
	}
	return Matches, "the export is in place", nil
}

// stateSkip names the top-level folders that are not the account's data: the
// runtime's sockets and pid files, and snapshots, which are copied by record.
var stateSkip = map[string]bool{"run": true, exportSnapshotDir: true}

func sidecar(name string) bool {
	return strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm") || strings.HasSuffix(name, "-journal")
}

func (r *Recovery) copyState(from, to string) error {
	return copyTree(from, to, func(rel, source string, isDir bool) (bool, error) {
		if stateSkip[rel] || sidecar(rel) {
			return true, nil
		}
		if isDir {
			return false, nil
		}
		isDB, err := isSQLite(source)
		if err != nil || !isDB {
			return false, err
		}
		return true, r.vacuumInto(source, filepath.Join(to, rel))
	})
}

func isSQLite(name string) (bool, error) {
	f, err := os.Open(name)
	if err != nil {
		return false, err
	}
	defer f.Close()
	head := make([]byte, 16)
	if _, err := io.ReadFull(f, head); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return false, nil
		}
		return false, err
	}
	return string(head) == "SQLite format 3\x00", nil
}

// vacuumInto copies a live database consistently. The target is created
// first, private, because SQLite's checked open will not create files; the
// connection must be a writer because VACUUM INTO attaches its output with the
// flags of the connection, and it writes nothing to the source. The account
// database goes through the engine's own connection; a route's has its writer
// in another actor, so it gets a short-lived one.
func (r *Recovery) vacuumInto(source, target string) error {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if rel, err := filepath.Rel(r.opt.StateDir, source); err == nil && filepath.ToSlash(rel) == accountDatabase {
		err := r.eng.Read(func(c store.Conn) error {
			_, err := c.Exec("VACUUM INTO ?", target)
			return err
		})
		if err != nil {
			return fmt.Errorf("export: %s: %w", source, err)
		}
		return nil
	}
	c, err := store.Open(source, store.Writer, store.KindRoute)
	if err != nil {
		return fmt.Errorf("export: %s: %w", source, err)
	}
	defer c.Close()
	if _, err := c.Exec("VACUUM INTO ?", target); err != nil {
		return fmt.Errorf("export: %s: %w", source, err)
	}
	return nil
}

// copyTree copies a folder, keeping modes and links. handled, given a path
// relative to the root, may claim an entry; a file that vanishes while the
// copy runs is left out, because only mutating operations wait for the lock.
func copyTree(from, to string, handled func(rel, source string, isDir bool) (bool, error)) error {
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && p != from {
				return nil
			}
			return err
		}
		rel, err := filepath.Rel(from, p)
		if err != nil {
			return err
		}
		if rel != "." && handled != nil && (d.IsDir() || d.Type().IsRegular()) {
			done, err := handled(rel, p, d.IsDir())
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if done {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		return copyEntry(p, filepath.Join(to, rel), d)
	})
}

func copyEntry(source, target string, d fs.DirEntry) error {
	info, err := d.Info()
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	switch {
	case d.IsDir():
		if err := os.Mkdir(target, 0o700); err != nil {
			return err
		}
		return os.Chmod(target, info.Mode().Perm())
	case d.Type()&fs.ModeSymlink != 0:
		link, err := os.Readlink(source)
		if err != nil {
			return ignoreGone(err)
		}
		return os.Symlink(link, target)
	case d.Type().IsRegular():
		return ignoreGone(copyFile(source, target, info.Mode().Perm()))
	}
	return nil // sockets, pipes and devices are the runtime's, not data
}

func ignoreGone(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func copyFile(source, target string, mode fs.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err := errors.Join(err, out.Close()); err != nil {
		return err
	}
	return os.Chmod(target, mode)
}

func syncPath(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// syncTree makes the staged copy durable before the rename publishes it.
func syncTree(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Type().IsRegular() {
			return syncPath(p)
		}
		return nil
	})
}
