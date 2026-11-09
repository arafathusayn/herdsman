package ops

import (
	"bytes"
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
	// A private folder of this attempt: a fixed name beside the state could
	// be someone else's, and this code removes what it staged.
	stage, err := os.MkdirTemp(filepath.Dir(state), ".herdsman-import-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	tmp := filepath.Join(stage, "state")
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
	if err := rebaseFiles(tmp, m.StateRoot, state); err != nil {
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
	for _, id := range m.Snapshots {
		if id == "" || id == "." || strings.ContainsAny(id, `/\`) || !filepath.IsLocal(id) {
			return nil, fmt.Errorf("import: unsafe snapshot id %q", id)
		}
	}
	return &m, nil
}

// rebase moves a path under the old state root to the new one. A path outside
// it, an inbox kept in a repository for example, stays as it is.
func rebase(p, from, to string) string {
	if from == "" || !filepath.IsAbs(p) {
		return p
	}
	rel, err := filepath.Rel(from, p)
	if err != nil || !filepath.IsLocal(rel) {
		return p
	}
	return filepath.Join(to, rel)
}

// rebaseFiles rewrites the inbox paths of the two registry files.
func rebaseFiles(root, from, to string) error {
	dir := filepath.Join(root, "daemon")
	if err := rebaseJSON(filepath.Join(dir, "routes.json"), func(doc map[string]any) {
		routes, _ := doc["routes"].(map[string]any)
		for _, v := range routes {
			rebaseField(v, from, to)
		}
	}); err != nil {
		return err
	}
	return rebaseJSON(filepath.Join(dir, "adopting.json"), func(doc map[string]any) {
		inboxes, _ := doc["inboxes"].([]any)
		for _, v := range inboxes {
			rebaseField(v, from, to)
		}
	})
}

func rebaseField(entry any, from, to string) {
	if m, ok := entry.(map[string]any); ok {
		if p, ok := m["inbox"].(string); ok {
			m["inbox"] = rebase(p, from, to)
		}
	}
}

func rebaseJSON(name string, edit func(map[string]any)) error {
	raw, err := os.ReadFile(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber() // created times stay exact integers
	var doc map[string]any
	if err := decoder.Decode(&doc); err != nil {
		return fmt.Errorf("import: %s: %w", filepath.Base(name), err)
	}
	edit(doc)
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(name, append(out, '\n'), 0o600)
}

// rebaseTable rewrites one path column, row by row, since the mapping is not
// expressible in SQL for paths that must stay when outside the root.
func rebaseTable(tx store.Conn, table, key, column, from, to string) error {
	type row struct{ id, path string }
	var rows []row
	query := fmt.Sprintf("SELECT %s, %s FROM %s", key, column, table)
	if err := tx.Query(query, nil, func(r store.Row) error { rows = append(rows, row{r.Text(0), r.Text(1)}); return nil }); err != nil {
		return err
	}
	update := fmt.Sprintf("UPDATE %s SET %s = ? WHERE %s = ?", table, column, key)
	for _, r := range rows {
		if next := rebase(r.path, from, to); next != r.path {
			if _, err := tx.Exec(update, next, r.id); err != nil {
				return err
			}
		}
	}
	return nil
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
		for _, column := range []struct{ table, key, name string }{
			{"routes", "route", "inbox"},
			{"routes", "route", "db_path"},
			{"adoptions", "route", "inbox"},
			{"adoptions", "route", "staging_path"},
		} {
			if err := rebaseTable(tx, column.table, column.key, column.name, m.StateRoot, state); err != nil {
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
