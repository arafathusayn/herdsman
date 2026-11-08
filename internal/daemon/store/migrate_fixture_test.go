package store

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// writeFixturesEnv rebuilds the committed databases when set. It is only run
// by hand, against the schema version being released.
const writeFixturesEnv = "HERDSMAN_WRITE_STORE_FIXTURES"

// schemaUpTo is kind's shipped files through version v.
func schemaUpTo(t *testing.T, kind Kind, v int) fstest.MapFS {
	t.Helper()
	m := fstest.MapFS{}
	dir := "schema/" + string(kind)
	ents, err := fs.ReadDir(schemaFS, dir)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range ents {
		if i >= v {
			break
		}
		b, err := fs.ReadFile(schemaFS, dir+"/"+e.Name())
		if err != nil {
			t.Fatal(err)
		}
		m[dir+"/"+e.Name()] = &fstest.MapFile{Data: b}
	}
	return m
}

func privateDir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "s")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func mustExec(t *testing.T, c Conn, q string, args ...any) {
	t.Helper()
	if _, err := c.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func seedAccount(t *testing.T, c Conn) {
	mustExec(t, c, `INSERT INTO routes(route, inbox, repo, herdr_socket, db_path, created, state, open_gen, reserved_message_id, reserved_seq)
		VALUES ('alpha', '/srv/fixture/alpha', 'repo-a', '/srv/fixture/herdr.sock', '/srv/fixture/alpha.db', 1000, 'active', 'gen-1', 1000, 2000),
		       ('beta', '/srv/fixture/beta', 'repo-b', '/srv/fixture/herdr.sock', '/srv/fixture/beta.db', 2000, 'removed', 'gen-2', 3000, 4000)`)
	mustExec(t, c, `INSERT INTO activity(source, time) VALUES ('web', 1234)`)
}

func seedRoute(t *testing.T, c Conn) {
	mustExec(t, c, `INSERT INTO messages(id, sender, recipient, kind, reply_to, envelope, body, body_sha256, state, created_seq, created_at, origin)
		VALUES (1, 'lead', 'worker', 'task', NULL, '{"subject":"one"}', X'68656C6C6F', 'sha-one', 'ready', 1, 1000, 'daemon'),
		       (2, 'worker', 'lead', 'reply', 1, '{"subject":"two"}', X'776F726C64', 'sha-two', 'handled', 2, 1001, 'imported')`)
}

func TestWriteFixtures(t *testing.T) {
	if os.Getenv(writeFixturesEnv) == "" {
		t.Skip("rebuilds testdata only on request")
	}
	const released = 1
	dir := filepath.Join("testdata", fmt.Sprintf("schema-%d", released))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct {
		kind Kind
		seed func(*testing.T, Conn)
	}{{KindAccount, seedAccount}, {KindRoute, seedRoute}} {
		p := filepath.Join(privateDir(t), string(f.kind)+".db")
		c := writerAt(t, p)
		if err := migrate(c, p, f.kind, schemaUpTo(t, f.kind, released)); err != nil {
			t.Fatal(err)
		}
		f.seed(t, c)
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, string(f.kind)+".db"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// dump is every value of a database: table, row key, column, quoted value.
type dump map[string]map[string]map[string]string

func takeDump(t *testing.T, c Conn) dump {
	t.Helper()
	d := dump{}
	var tables []string
	if err := c.Query("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name", nil, func(r Row) error {
		tables = append(tables, r.Text(0))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		var cols, pk []string
		if err := c.Query("SELECT name, pk FROM pragma_table_info(?) ORDER BY cid", []any{table}, func(r Row) error {
			cols = append(cols, r.Text(0))
			if r.Int64(1) > 0 {
				pk = append(pk, r.Text(0))
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if len(pk) == 0 {
			pk = []string{"rowid"}
		}
		sel := make([]string, 0, len(cols)+1)
		sel = append(sel, "CAST("+strings.Join(pk, " AS TEXT) || '/' || CAST(")+" AS TEXT)")
		for _, col := range cols {
			sel = append(sel, "quote("+col+")")
		}
		rows := map[string]map[string]string{}
		if err := c.Query("SELECT "+strings.Join(sel, ", ")+" FROM "+table, nil, func(r Row) error {
			row := map[string]string{}
			for i, col := range cols {
				row[col] = r.Text(i + 1)
			}
			rows[r.Text(0)] = row
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		d[table] = rows
	}
	return d
}

// differences names every change to data that already existed, and every
// value a migration invented. New empty tables are not data.
func differences(before, after dump) []string {
	seen := map[string]bool{}
	for table, rows := range before {
		afterRows, ok := after[table]
		if !ok {
			if len(rows) > 0 {
				seen["removed "+table+".*"] = true
			}
			continue
		}
		for key, row := range rows {
			got, ok := afterRows[key]
			if !ok {
				seen["removed "+table+".*"] = true
				continue
			}
			for col, v := range row {
				gv, ok := got[col]
				if !ok {
					seen["removed "+table+"."+col] = true
				} else if gv != v {
					seen["changed "+table+"."+col] = true
				}
			}
			for col, v := range got {
				if _, ok := row[col]; !ok && v != "NULL" {
					seen["added "+table+"."+col] = true
				}
			}
		}
		for key := range afterRows {
			if _, ok := rows[key]; !ok {
				seen["added "+table+".*"] = true
			}
		}
	}
	for table, rows := range after {
		if _, ok := before[table]; !ok && len(rows) > 0 && table != "meta" {
			seen["added "+table+".*"] = true
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

// bookkeeping is what every migration writes about itself.
func bookkeeping(d dump) {
	for key := range d["meta"] {
		if strings.HasPrefix(key, "migrated_by.") {
			delete(d["meta"], key)
		}
	}
}

// unlisted returns the differences MIGRATIONS.md does not name for kind.
func unlisted(diffs []string, kind Kind, doc string) []string {
	var out []string
	for _, diff := range diffs {
		re := regexp.MustCompile(`(?m)^- ` + string(kind) + ` [0-9]+: ` + regexp.QuoteMeta(diff) + `$`)
		if !re.MatchString(doc) {
			out = append(out, diff)
		}
	}
	return out
}

// loadFixture copies a committed database into a private folder.
func loadFixture(t *testing.T, version int, kind Kind) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", fmt.Sprintf("schema-%d", version), string(kind)+".db"))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(privateDir(t), string(kind)+".db")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func rawDump(t *testing.T, p string, fsys fs.FS, kind Kind, migrateIt bool) dump {
	t.Helper()
	c, err := openDriver(p, Writer)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if migrateIt {
		if err := migrate(c, p, kind, fsys); err != nil {
			t.Fatal(err)
		}
	}
	d := takeDump(t, c)
	bookkeeping(d)
	return d
}

func fixtureVersions(t *testing.T) []int {
	t.Helper()
	ents, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	var vs []int
	for _, e := range ents {
		var v int
		if _, err := fmt.Sscanf(e.Name(), "schema-%d", &v); err == nil {
			vs = append(vs, v)
		}
	}
	if len(vs) == 0 {
		t.Fatal("no fixture databases")
	}
	return vs
}

func TestFixturesMigrateForwardWithNothingUnlisted(t *testing.T) {
	doc, err := os.ReadFile("MIGRATIONS.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range fixtureVersions(t) {
		for _, kind := range []Kind{KindAccount, KindRoute} {
			t.Run(fmt.Sprintf("%s-%d", kind, v), func(t *testing.T) {
				p := loadFixture(t, v, kind)
				before := rawDump(t, p, nil, kind, false)
				digestBefore := fixtureDigest(t, p, kind)

				c, err := Open(p, Writer, kind)
				if err != nil {
					t.Fatal(err)
				}
				if got := userVersion(t, c); got != Newest(kind) {
					t.Fatalf("version %d, want %d", got, Newest(kind))
				}
				after := takeDump(t, c)
				bookkeeping(after)
				if err := c.Close(); err != nil {
					t.Fatal(err)
				}
				if miss := unlisted(differences(before, after), kind, string(doc)); len(miss) != 0 {
					t.Fatalf("migration changes MIGRATIONS.md does not list: %v", miss)
				}
				if got := fixtureDigest(t, p, kind); got != digestBefore {
					t.Fatalf("digest moved: %s to %s", digestBefore, got)
				}
				if len(after["routes"])+len(after["messages"]) != 2 {
					t.Fatalf("representative rows lost: %v", after)
				}
			})
		}
	}
}

func fixtureDigest(t *testing.T, p string, kind Kind) string {
	t.Helper()
	c, err := openDriver(p, Writer)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var recs []Record
	if kind == KindAccount {
		recs, err = AccountRecords(c)
	} else {
		recs, err = RouteRecords("fixture", c)
	}
	if err != nil {
		t.Fatal(err)
	}
	return Digest(recs)
}

func TestFixtureCheckCatchesAnUnlistedChange(t *testing.T) {
	cases := map[string]string{
		"a rewritten payload":      "UPDATE routes SET repo = repo || '-moved' WHERE route = 'alpha';\n",
		"a lost association":       "DELETE FROM routes WHERE route = 'beta';\n",
		"an invented column value": "ALTER TABLE routes ADD COLUMN extra TEXT DEFAULT 'made-up';\n",
	}
	for name, sql := range cases {
		t.Run(name, func(t *testing.T) {
			p := loadFixture(t, 1, KindAccount)
			before := rawDump(t, p, nil, KindAccount, false)
			fsys := schemaUpTo(t, KindAccount, Newest(KindAccount))
			bad := fmt.Sprintf("schema/account/%04d_planted.sql", Newest(KindAccount)+1)
			fsys[bad] = &fstest.MapFile{Data: []byte(sql)}
			after := rawDump(t, p, fsys, KindAccount, true)
			diffs := differences(before, after)
			if len(diffs) == 0 {
				t.Fatal("the planted migration left no difference")
			}
			if len(unlisted(diffs, KindAccount, "")) == 0 {
				t.Fatalf("not reported: %v", diffs)
			}
			line := "- account 3: " + diffs[0]
			if miss := unlisted(diffs[:1], KindAccount, line+"\n"); len(miss) != 0 {
				t.Fatalf("a listed change is still reported: %v", miss)
			}
		})
	}
}

func TestDigestCanonical(t *testing.T) {
	a := []Record{{Kind: "route", ID: "b", Fields: []string{"1"}}, {Kind: "route", ID: "a", Fields: []string{"2"}}}
	b := []Record{a[1], a[0]}
	if Digest(a) != Digest(b) {
		t.Fatal("digest depends on input order")
	}
	if Digest([]Record{{Kind: "route", ID: "a", Fields: []string{"ab", "c"}}}) == Digest([]Record{{Kind: "route", ID: "a", Fields: []string{"a", "bc"}}}) {
		t.Fatal("field boundaries do not matter")
	}
	dup := append(a, Record{Kind: "route", ID: "a", Fields: []string{"other"}})
	if Digest(dup) == "" || Count(dup, "route") != 2 || Digest(dup) != Digest(a) {
		t.Fatal("a duplicate id must keep the first source")
	}
}
