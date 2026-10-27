package store

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// twoVersions is a route schema of two files: v1 creates meta and t, v2
// adds a column (and v2bad is a second file that fails halfway).
var (
	v1    = &fstest.MapFile{Data: []byte("CREATE TABLE meta (k TEXT PRIMARY KEY, v TEXT NOT NULL) STRICT;\nCREATE TABLE t (a INTEGER) STRICT;\n")}
	v2    = &fstest.MapFile{Data: []byte("ALTER TABLE t ADD COLUMN b TEXT;\nCREATE INDEX t_b ON t(b);\n")}
	v2bad = &fstest.MapFile{Data: []byte("ALTER TABLE t ADD COLUMN b TEXT;\nCREATE TABLE t (a INTEGER);\n")}
)

func schemaOf(files map[string]*fstest.MapFile) fstest.MapFS {
	m := fstest.MapFS{}
	for name, f := range files {
		m["schema/route/"+name] = f
	}
	return m
}

// writerAt opens a raw writer (no schema step) on a new file of dir.
func writerAt(t *testing.T, p string) Conn {
	t.Helper()
	f, err := openRegular(p, true)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	c, err := openDriver(p, Writer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func userVersion(t *testing.T, c Conn) int {
	t.Helper()
	var v int64
	if err := c.Query("PRAGMA user_version", nil, func(r Row) error { v = r.Int64(0); return nil }); err != nil {
		t.Fatal(err)
	}
	return int(v)
}

func metaValue(t *testing.T, c Conn, k string) string {
	t.Helper()
	v := ""
	if err := c.Query("SELECT v FROM meta WHERE k = ?", []any{k}, func(r Row) error { v = r.Text(0); return nil }); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestEmbeddedSchemas: both kinds' embedded files are numbered from 1
// without a gap, and a fresh writer of each kind reaches the newest
// version with its meta.kind and created_by.
func TestEmbeddedSchemas(t *testing.T) {
	old := BuildVersion
	BuildVersion = "v9.9.9"
	t.Cleanup(func() { BuildVersion = old })
	for _, kind := range []Kind{KindAccount, KindRoute} {
		if _, err := migrations(schemaFS, kind); err != nil {
			t.Fatal(err)
		}
		c, err := Open(filepath.Join(dir(t), "x.db"), Writer, kind)
		if err != nil {
			t.Fatal(err)
		}
		if got := userVersion(t, c); got != Newest(kind) {
			t.Errorf("%s: user_version %d, want %d", kind, got, Newest(kind))
		}
		if metaValue(t, c, "kind") != string(kind) || metaValue(t, c, "created_by") != "v9.9.9" {
			t.Errorf("%s: meta kind %q created_by %q", kind, metaValue(t, c, "kind"), metaValue(t, c, "created_by"))
		}
		_ = c.Close()
	}
}

// TestMigrationsNumbered: a gap, a stray file or no file is refused.
func TestMigrationsNumbered(t *testing.T) {
	for name, files := range map[string]map[string]*fstest.MapFile{
		"gap":   {"0001_init.sql": v1, "0003_more.sql": v2},
		"stray": {"0001_init.sql": v1, "notes.txt": v2},
		"none":  {},
	} {
		if _, err := migrations(schemaOf(files), KindRoute); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	ms, err := migrations(schemaOf(map[string]*fstest.MapFile{"0002_b.sql": v2, "0001_init.sql": v1}), KindRoute)
	if err != nil || len(ms) != 2 || ms[0].version != 1 || ms[1].version != 2 {
		t.Fatalf("ordered: %v %v", ms, err)
	}
}

// TestMigrateBacksUpFirst: migrating v1 to v2 first copies the v1 file to
// <db>.v1.bak (mode 600, with the v1 rows), then applies v2 with its
// migrated_by row; a second run is a no-op.
func TestMigrateBacksUpFirst(t *testing.T) {
	p := filepath.Join(dir(t), "r.db")
	c := writerAt(t, p)
	if err := migrate(c, p, KindRoute, schemaOf(map[string]*fstest.MapFile{"0001_init.sql": v1})); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec("INSERT INTO t(a) VALUES (7)"); err != nil {
		t.Fatal(err)
	}
	two := schemaOf(map[string]*fstest.MapFile{"0001_init.sql": v1, "0002_b.sql": v2})
	if err := migrate(c, p, KindRoute, two); err != nil {
		t.Fatal(err)
	}
	if userVersion(t, c) != 2 || metaValue(t, c, "migrated_by.2") != BuildVersion {
		t.Fatalf("user_version %d, migrated_by.2 %q", userVersion(t, c), metaValue(t, c, "migrated_by.2"))
	}
	st, err := os.Lstat(p + ".v1.bak")
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("backup: %v %v", st, err)
	}
	b := writerAt(t, p+".v1.bak")
	var a int64
	if err := b.Query("SELECT a FROM t", nil, func(r Row) error { a = r.Int64(0); return nil }); err != nil || a != 7 || userVersion(t, b) != 1 {
		t.Fatalf("backup rows: a=%d v=%d %v", a, userVersion(t, b), err)
	}
	if err := migrate(c, p, KindRoute, two); err != nil || userVersion(t, c) != 2 {
		t.Fatalf("second run: %v", err)
	}
	if err := DropBackups(p, KindRoute); err != nil {
		t.Fatal(err)
	}
}

// TestMigrationFailureRollsBack: a file that fails halfway leaves the
// database at its old version with none of the file's changes, and the
// backup stays for the human.
func TestMigrationFailureRollsBack(t *testing.T) {
	p := filepath.Join(dir(t), "r.db")
	c := writerAt(t, p)
	if err := migrate(c, p, KindRoute, schemaOf(map[string]*fstest.MapFile{"0001_init.sql": v1})); err != nil {
		t.Fatal(err)
	}
	err := migrate(c, p, KindRoute, schemaOf(map[string]*fstest.MapFile{"0001_init.sql": v1, "0002_b.sql": v2bad}))
	if !errors.Is(err, ErrMigration) {
		t.Fatalf("want ErrMigration, got %v", err)
	}
	if userVersion(t, c) != 1 {
		t.Fatalf("user_version %d after a failed migration", userVersion(t, c))
	}
	cols := 0
	if err := c.Query("SELECT count(*) FROM pragma_table_info('t')", nil, func(r Row) error { cols = int(r.Int64(0)); return nil }); err != nil || cols != 1 {
		t.Fatalf("t has %d columns after rollback (%v)", cols, err)
	}
	if _, err := os.Lstat(p + ".v1.bak"); err != nil {
		t.Fatalf("backup gone: %v", err)
	}
}

// TestSchemaRefusesNewer: a file of a newer version is refused by the
// writer and by a reader, naming the herdsman version that wrote it.
func TestSchemaRefusesNewer(t *testing.T) {
	p := filepath.Join(dir(t), "r.db")
	c := open(t, p, Writer)
	n := Newest(KindRoute) + 1
	if _, err := c.Exec("PRAGMA user_version = " + strconv.Itoa(n)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec("INSERT INTO meta(k, v) VALUES ('migrated_by.' || ?, 'v7.0.0')", n); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	for _, m := range []Mode{Writer, Reader} {
		_, err := Open(p, m, KindRoute)
		if !errors.Is(err, ErrSchemaNewer) || !strings.Contains(err.Error(), "install herdsman v7.0.0 or newer") {
			t.Errorf("%v: %v", m, err)
		}
	}
}

// TestSchemaReaderRefusesOlder: a reader never migrates: an older (here
// fresh, version 0) file is refused with the start-the-daemon message.
func TestSchemaReaderRefusesOlder(t *testing.T) {
	p := filepath.Join(dir(t), "r.db")
	_ = writerAt(t, p)
	_, err := Open(p, Reader, KindRoute)
	if !errors.Is(err, ErrSchemaOlder) || !strings.Contains(err.Error(), "start the daemon to migrate it") {
		t.Fatalf("reader of an older file: %v", err)
	}
}

// TestSchemaKindMismatch: a route file opened as an account database (or
// a versioned file without meta.kind) is corrupt for both modes.
func TestSchemaKindMismatch(t *testing.T) {
	p := filepath.Join(dir(t), "r.db")
	_ = open(t, p, Writer).Close()
	for _, m := range []Mode{Writer, Reader} {
		if _, err := Open(p, m, KindAccount); !errors.Is(err, ErrSchemaCorrupt) {
			t.Errorf("%v: %v", m, err)
		}
	}
}

// TestSchemaFreshEqualsMigrated: building version N from nothing and
// from version N-1 gives the same schema text, for the test schema and
// for every checked-in fixture of an older embedded version.
func TestSchemaFreshEqualsMigrated(t *testing.T) {
	two := schemaOf(map[string]*fstest.MapFile{"0001_init.sql": v1, "0002_b.sql": v2})
	fresh := writerAt(t, filepath.Join(dir(t), "fresh.db"))
	if err := migrate(fresh, "", KindRoute, two); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir(t), "old.db")
	old := writerAt(t, p)
	if err := migrate(old, p, KindRoute, schemaOf(map[string]*fstest.MapFile{"0001_init.sql": v1})); err != nil {
		t.Fatal(err)
	}
	if err := migrate(old, p, KindRoute, two); err != nil {
		t.Fatal(err)
	}
	if a, b := schemaText(t, fresh), schemaText(t, old); a != b {
		t.Fatalf("fresh:\n%s\nmigrated:\n%s", a, b)
	}
	fixtures, _ := filepath.Glob(filepath.Join("testdata", "*-v*.db"))
	for _, f := range fixtures {
		t.Errorf("fixture %s: compare it with a fresh build here when version 2 ships", f)
	}
}

func schemaText(t *testing.T, c Conn) string {
	t.Helper()
	var rows []string
	ws := regexp.MustCompile(`\s+`)
	err := c.Query("SELECT name, sql FROM sqlite_schema WHERE sql IS NOT NULL", nil, func(r Row) error {
		rows = append(rows, r.Text(0)+": "+ws.ReplaceAllString(r.Text(1), " "))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(rows)
	return strings.Join(rows, "\n")
}

// TestNoDDLInGo: the embedded files are the only DDL (design 1.1).
func TestNoDDLInGo(t *testing.T) {
	ddl := regexp.MustCompile(`\b(CREATE|ALTER|DROP)\s+(TABLE|INDEX|UNIQUE|TRIGGER|VIEW|VIRTUAL)\b`)
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if loc := ddl.FindIndex(b); loc != nil {
			t.Errorf("%s: DDL in Go: %q", f, b[loc[0]:loc[1]])
		}
	}
}

// TestBackupInterruptedNotAccepted (S2S3-1): an empty or foreign file at
// the backup name (an interrupted backup) stops the migration, and the
// database stays at its version; a temporary file left by an interrupted
// backup is rebuilt into a complete, published copy.
func TestBackupInterruptedNotAccepted(t *testing.T) {
	two := schemaOf(map[string]*fstest.MapFile{"0001_init.sql": v1, "0002_b.sql": v2})
	one := schemaOf(map[string]*fstest.MapFile{"0001_init.sql": v1})
	p := filepath.Join(dir(t), "r.db")
	c := writerAt(t, p)
	if err := migrate(c, p, KindRoute, one); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p+".v1.bak", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrate(c, p, KindRoute, two); !errors.Is(err, ErrMigration) {
		t.Fatalf("migration over an empty backup: %v", err)
	}
	if userVersion(t, c) != 1 {
		t.Fatalf("user_version %d", userVersion(t, c))
	}
	if err := os.Remove(p + ".v1.bak"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p+".v1.bak.tmp", []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrate(c, p, KindRoute, two); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(p + ".v1.bak.tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file left: %v", err)
	}
	f, err := os.Open(p + ".v1.bak")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if h, err := readHeader(f, p+".v1.bak"); err != nil || h.userVersion != 1 {
		t.Fatalf("published backup: %+v %v", h, err)
	}
}
