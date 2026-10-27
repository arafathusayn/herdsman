package store

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"syscall"
)

// The numbered migration files are the only DDL in the program (design
// section 1.1); each file's number is the schema version it produces.
//
//go:embed schema/account/*.sql schema/route/*.sql
var schemaFS embed.FS

// BuildVersion is the herdsman version written to meta.created_by and
// meta.migrated_by.<n>; the daemon sets it before its first Open.
var BuildVersion = "dev"

// Schema refusals (design section 1.1). Each error wraps one of them.
var (
	ErrSchemaOlder   = errors.New("schema older than this herdsman")
	ErrSchemaNewer   = errors.New("schema newer than this herdsman")
	ErrSchemaCorrupt = errors.New("schema marker corrupt")
	ErrMigration     = errors.New("schema migration failed")
)

var migrationName = regexp.MustCompile(`^([0-9]{4})_[a-z0-9_]+\.sql$`)

// migration is one numbered file of a kind.
type migration struct {
	version int
	name    string
	sql     string
}

// migrations reads kind's files from fsys; their numbers must run 1, 2, …
// without a gap or a stray file.
func migrations(fsys fs.FS, kind Kind) ([]migration, error) {
	dir := path.Join("schema", string(kind))
	ents, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("store: schema %s: %w", kind, err)
	}
	var ms []migration
	for _, e := range ents {
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			return nil, fmt.Errorf("store: schema %s: stray file %s", kind, e.Name())
		}
		b, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("store: schema %s: %w", kind, err)
		}
		n, _ := strconv.Atoi(m[1])
		ms = append(ms, migration{version: n, name: e.Name(), sql: string(b)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i, m := range ms {
		if m.version != i+1 {
			return nil, fmt.Errorf("store: schema %s: %s, want version %d", kind, m.name, i+1)
		}
	}
	if len(ms) == 0 {
		return nil, fmt.Errorf("store: schema %s: no migration files", kind)
	}
	return ms, nil
}

// Newest is the schema version this binary writes for kind.
func Newest(kind Kind) int {
	ms, err := migrations(schemaFS, kind)
	if err != nil {
		panic(err) // the embedded files are checked by TestEmbeddedSchemas
	}
	return len(ms)
}

// readSchema reads user_version and meta.kind (and the newest writer's
// version) in one read; a fresh file is version 0.
func readSchema(c Conn, kind Kind) (version int, writtenBy string, err error) {
	read := func(c Conn) error {
		if err := c.Query("PRAGMA user_version", nil, func(r Row) error { version = int(r.Int64(0)); return nil }); err != nil {
			return err
		}
		if version <= 0 {
			return nil
		}
		got := map[string]string{}
		q := "SELECT k, v FROM meta WHERE k IN ('kind', 'created_by', 'migrated_by.' || ?)"
		if err := c.Query(q, []any{version}, func(r Row) error { got[r.Text(0)] = r.Text(1); return nil }); err != nil {
			return fmt.Errorf("%w: %v", ErrSchemaCorrupt, err)
		}
		if got["kind"] != string(kind) {
			return fmt.Errorf("%w: user_version %d with meta.kind %q, want %q", ErrSchemaCorrupt, version, got["kind"], kind)
		}
		writtenBy = got["migrated_by."+strconv.Itoa(version)]
		if writtenBy == "" {
			writtenBy = got["created_by"]
		}
		return nil
	}
	err = c.Tx(read)
	if err != nil && !errors.Is(err, ErrSchemaCorrupt) {
		err = fmt.Errorf("store: read schema: %w", err)
	} else if err != nil {
		err = fmt.Errorf("store: %w", err)
	}
	return version, writtenBy, err
}

// checkSchema refuses a schema this connection may not use: newer for
// everyone, older for a reader (design section 1.1).
func checkSchema(c Conn, m Mode, kind Kind, newest int) (int, error) {
	v, by, err := readSchema(c, kind)
	switch {
	case err != nil:
		return v, err
	case v < 0:
		return v, fmt.Errorf("store: %w: user_version %d", ErrSchemaCorrupt, v)
	case v > newest:
		return v, fmt.Errorf("store: %w: schema %d is newer than this herdsman (%d); install herdsman %s or newer", ErrSchemaNewer, v, newest, by)
	case v < newest && m == Reader:
		return v, fmt.Errorf("store: %w: schema %d is older than this herdsman (%d); start the daemon to migrate it", ErrSchemaOlder, v, newest)
	}
	return v, nil
}

// migrate brings the writer's database at dbPath to the newest version of
// fsys: a backup of a non-empty file first (VACUUM INTO, mode 600, synced
// with its folder), then each file with its user_version and meta rows in
// one transaction. A failed file rolls back and keeps the backup.
func migrate(c Conn, dbPath string, kind Kind, fsys fs.FS) error {
	ms, err := migrations(fsys, kind)
	if err != nil {
		return err
	}
	v, err := checkSchema(c, Writer, kind, len(ms))
	if err != nil || v == len(ms) {
		return err
	}
	if v > 0 {
		if err := backup(c, backupPath(dbPath, v)); err != nil {
			return err
		}
	}
	for _, m := range ms[v:] {
		err := c.Tx(func(c Conn) error {
			if err := execScript(c, m.sql); err != nil {
				return err
			}
			if m.version == 1 {
				if _, err := c.Exec("INSERT INTO meta(k, v) VALUES ('kind', ?), ('created_by', ?)", string(kind), BuildVersion); err != nil {
					return err
				}
			} else if _, err := c.Exec("INSERT OR REPLACE INTO meta(k, v) VALUES ('migrated_by.' || ?, ?)", m.version, BuildVersion); err != nil {
				return err
			}
			_, err := c.Exec("PRAGMA user_version = " + strconv.Itoa(m.version))
			return err
		})
		if err != nil {
			return fmt.Errorf("store: %w: %s %s: %v", ErrMigration, kind, m.name, err)
		}
	}
	return nil
}

// backupPath is the copy taken before migrating from version v.
func backupPath(dbPath string, v int) string {
	return dbPath + ".v" + strconv.Itoa(v) + ".bak"
}

// backup copies the database with VACUUM INTO into a new mode 600 file and
// syncs it and its folder (F_FULLFSYNC on darwin through Go's Sync). An
// existing backup of the same version is kept: it is the older copy.
func backup(c Conn, to string) error {
	f, err := os.OpenFile(to, os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: backup %s: %w", to, err)
	}
	defer f.Close()
	if _, err := c.Exec("VACUUM INTO ?", to); err != nil {
		_ = os.Remove(to)
		return fmt.Errorf("store: backup %s: %w", to, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("store: backup %s: %w", to, err)
	}
	return syncDir(filepath.Dir(to))
}

// execScript runs a file of several statements through the adapter.
func execScript(c Conn, sql string) error {
	sc, ok := c.(interface{ script(string) error })
	if !ok {
		return errors.New("store: connection cannot run a script")
	}
	return sc.script(sql)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("store: sync %s: %w", dir, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("store: sync %s: %w", dir, err)
	}
	return nil
}

// DropBackups removes the pre-migration backups of dbPath once a start of
// the current version succeeded (design section 1.2).
func DropBackups(dbPath string, kind Kind) error {
	for v := 1; v < Newest(kind); v++ {
		if err := os.Remove(backupPath(dbPath, v)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("store: %w", err)
		}
	}
	return nil
}
