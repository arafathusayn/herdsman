package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
)

// ErrStateNotEmpty means an import would mix with existing state.
var ErrStateNotEmpty = errors.New("the state directory is not empty")

// Import rebuilds an account from an export into an empty state directory,
// which appears by one rename. No daemon may be serving that directory.
func Import(ctx context.Context, src, state string) error {
	if !filepath.IsAbs(src) || !filepath.IsAbs(state) {
		return fmt.Errorf("import: paths must be absolute: %q, %q", src, state)
	}
	state = filepath.Clean(state)
	m, err := readExportManifest(src)
	if err != nil {
		return err
	}
	free, err := pathFree(state)
	if err != nil {
		return err
	}
	if !free {
		return fmt.Errorf("%w: %s", ErrStateNotEmpty, state)
	}
	if err := os.MkdirAll(filepath.Dir(state), 0o700); err != nil {
		return err
	}
	tmp := state + ".import-tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := copyTree(filepath.Join(src, exportStateDir), tmp, nil); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o700); err != nil {
		return err
	}
	for _, id := range m.Snapshots {
		if err := copySnapshot(ctx, filepath.Join(src, exportSnapshotDir, id), filepath.Join(tmp, exportSnapshotDir, id)); err != nil {
			return fmt.Errorf("import: snapshot %s: %w", id, err)
		}
	}
	if err := adoptDatabase(filepath.Join(tmp, filepath.FromSlash(accountDatabase)), state, m); err != nil {
		return err
	}
	if err := syncTree(tmp); err != nil {
		return err
	}
	if err := os.Remove(state); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(tmp, state); err != nil {
		return err
	}
	return syncPath(filepath.Dir(state))
}

func readExportManifest(src string) (*exportManifest, error) {
	raw, err := os.ReadFile(filepath.Join(src, ExportManifestName))
	if err != nil {
		return nil, fmt.Errorf("import: %s is not an export: %w", src, err)
	}
	var m exportManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("import: %s: %w", ExportManifestName, err)
	}
	if !strings.HasPrefix(m.Schema, "1.") {
		return nil, fmt.Errorf("import: unsupported export schema %q", m.Schema)
	}
	return &m, nil
}

// adoptDatabase checks the copied records against the export, repoints the
// snapshot rows at their new home, and ends the export's own operation: the
// copy was taken while it was running, and a restart must not run it again.
func adoptDatabase(path, state string, m *exportManifest) error {
	c, err := store.Open(path, store.Writer, store.KindAccount)
	if err != nil {
		return fmt.Errorf("import: account database: %w", err)
	}
	defer c.Close()
	return c.Tx(func(tx store.Conn) error {
		recs, err := store.AccountRecords(tx)
		if err != nil {
			return err
		}
		if got := store.Digest(recs); got != m.Digest {
			return fmt.Errorf("import: the account records have digest %s, the export recorded %s", got, m.Digest)
		}
		rows, err := loadSnapshots(tx)
		if err != nil {
			return err
		}
		for _, s := range rows {
			if !slices.Contains(m.Snapshots, s.ID) {
				return fmt.Errorf("import: snapshot %s is recorded but not in the export", s.ID)
			}
			if _, err := tx.Exec("UPDATE snapshots SET dir = ? WHERE id = ?", filepath.Join(state, exportSnapshotDir, s.ID), s.ID); err != nil {
				return err
			}
		}
		return endExportOperation(tx, m.Op)
	})
}

func endExportOperation(tx store.Conn, op string) error {
	if _, err := tx.Exec("UPDATE operation_steps SET state = 'done' WHERE op_id = ?", op); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE operations SET state = 'done' WHERE id = ?", op); err != nil {
		return err
	}
	return releaseLocks(tx, op)
}
