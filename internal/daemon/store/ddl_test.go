package store

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// tables lists a database's tables with their CREATE text.
func tables(t *testing.T, c Conn) map[string]string {
	t.Helper()
	got := map[string]string{}
	err := c.Query("SELECT name, sql FROM sqlite_schema WHERE type = 'table'", nil, func(r Row) error {
		got[r.Text(0)] = r.Text(1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestSchemaV1Tables: the newest schema of each kind has exactly these
// tables (version 1's, plus the operation journal and the project records in
// the account database), every one STRICT.
func TestSchemaV1Tables(t *testing.T) {
	want := map[Kind][]string{
		KindRoute: {"acks", "alerts", "approvals", "cards", "claim_keys", "claims", "credentials", "events",
			"holds", "import_log", "launches", "messages", "meta", "notices", "parties", "questions",
			"resume_intents", "send_keys", "setup_proof", "targets", "terminals"},
		KindAccount: {"activity", "adoptions", "meta", "operation_locks", "operation_steps", "operations", "outbox", "project_commits", "project_folders", "project_repos", "projects", "routes", "snapshots", "task_labels", "task_links", "task_repos", "tasks"},
	}
	for kind, names := range want {
		c := openKind(t, kind)
		got := tables(t, c)
		var have []string
		for name, sql := range got {
			have = append(have, name)
			if !strings.HasSuffix(strings.TrimSpace(sql), "STRICT") {
				t.Errorf("%s: table %s is not STRICT", kind, name)
			}
		}
		sort.Strings(have)
		if strings.Join(have, " ") != strings.Join(names, " ") {
			t.Errorf("%s tables:\n got %v\nwant %v", kind, have, names)
		}
	}
}

func openKind(t *testing.T, kind Kind) Conn {
	t.Helper()
	c, err := Open(filepath.Join(dir(t), "x.db"), Writer, kind)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// exec runs statements in order; the last one's error is returned, any
// earlier error fails the test.
func exec(t *testing.T, c Conn, stmts ...string) error {
	t.Helper()
	for i, s := range stmts {
		_, err := c.Exec(s)
		if i == len(stmts)-1 {
			return err
		}
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return nil
}

const (
	sha = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tok = "0123456789abcdef0123456789abcdef"
)

// TestSchemaV1Constraints: the keys and bounds the operation map relies on
// (design section 3) are enforced by the schema itself. Each case's
// setup statements succeed and its last statement must be refused, and
// the allowed neighbour of each case is accepted.
func TestSchemaV1Constraints(t *testing.T) {
	party := func(name, role, state string) string {
		return "INSERT INTO parties(name, role, pane, session, generation, state, enrolled_at) VALUES ('" +
			name + "', '" + role + "', 'p', '{}', 1, '" + state + "', 1)"
	}
	approval := "INSERT INTO approvals(seq, registry_sha256, request_sha256, approved_by, result, time) VALUES (1, '" + sha + "', 'r1', 'terminal', '{}', 1)"
	message := func(id, body string) string {
		return "INSERT INTO messages(id, sender, recipient, kind, envelope, body, body_sha256, state, created_seq, created_at, origin) VALUES (" +
			id + ", 'a', 'o', 'task', '{}', " + body + ", '" + sha + "', 'ready', 1, 1, 'daemon')"
	}
	webLaunch := func(id, role string) string {
		return "INSERT INTO launches(id, via, device, name, role, pane, session, registry_rev, launch_group, created_at, expires_at) VALUES (" +
			id + ", 'web', 'd', 'n" + id + "', '" + role + "', 'p', 's', 1, 'g', 1, 2)"
	}
	claim := func(token, box string) string {
		return "INSERT INTO claims(token, box, owner, read_key, state, created_at) VALUES ('" + token + "', '" + box + "', 'o', 'k" + box + "', 'active', 1)"
	}
	cases := map[string]struct {
		kind    Kind
		ok      []string // accepted, in order
		refused string
	}{
		"two approved of a role":               {KindRoute, []string{party("a1", "adjutant", "approved"), party("a2", "adjutant", "pending")}, party("a3", "adjutant", "approved")},
		"two pending of a role":                {KindRoute, []string{party("o1", "orchestrator", "pending")}, party("o2", "orchestrator", "pending")},
		"party name too long":                  {KindRoute, nil, party(strings.Repeat("n", 33), "adjutant", "pending")},
		"unknown role":                         {KindRoute, nil, party("x", "reviewer", "pending")},
		"approval from nowhere":                {KindRoute, nil, strings.Replace(approval, "'terminal'", "'script'", 1)},
		"same request approved twice":          {KindRoute, []string{approval}, strings.Replace(approval, "VALUES (1,", "VALUES (2,", 1)},
		"web group with two adjutants":         {KindRoute, []string{webLaunch("1", "adjutant"), webLaunch("2", "orchestrator")}, webLaunch("3", "adjutant")},
		"web launch without a group":           {KindRoute, nil, "INSERT INTO launches(via, device, name, role, pane, session, registry_rev, created_at, expires_at) VALUES ('web', 'd', 'n', 'adjutant', 'p', 's', 1, 1, 2)"},
		"adjutant launch without its launcher": {KindRoute, nil, "INSERT INTO launches(via, name, role, pane, session, registry_rev, created_at, expires_at) VALUES ('adjutant', 'n', 'orchestrator', 'p', 's', 1, 1, 2)"},
		"launch used by a missing approval":    {KindRoute, nil, strings.Replace(webLaunch("1", "adjutant"), "created_at, expires_at) VALUES (1, 'web', 'd', 'n1', 'adjutant', 'p', 's', 1, 'g', 1, 2)", "created_at, expires_at, used_by_approval) VALUES (1, 'web', 'd', 'n1', 'adjutant', 'p', 's', 1, 'g', 1, 2, 9)", 1)},
		"file credential without a secret":     {KindRoute, []string{party("a", "adjutant", "approved"), approval}, "INSERT INTO credentials(name, generation, option, sha256, state, approval_seq) VALUES ('a', 1, 'file', '" + sha + "', 'active', 1)"},
		"body over 128 KiB":                    {KindRoute, []string{message("1", "zeroblob(131072)")}, message("2", "zeroblob(131073)")},
		"message of an unknown claim":          {KindRoute, nil, strings.Replace(strings.Replace(message("1", "x'00'"), "state, created_seq", "state, claim_token, created_seq", 1), "'ready', 1, 1", "'ready', '"+tok+"', 1, 1", 1)},
		"two active claims of a box":           {KindRoute, []string{"INSERT INTO claims(token, box, owner, read_key, state, created_at) VALUES ('" + tok + "', 'o', 'o', 'k1', 'active', 1)"}, "INSERT INTO claims(token, box, owner, read_key, state, created_at) VALUES ('" + strings.Repeat("f", 32) + "', 'o', 'o', 'k2', 'active', 1)"},
		"claim token neither hex nor legacy":   {KindRoute, []string{claim("0123456789abcdef0123456789abcdef", "b1"), claim(".claim.4242", "b2")}, claim(".claim.12a", "b3")},
		"claim token of 32 non-hex":            {KindRoute, nil, claim(strings.Repeat("g", 32), "b1")},
		"claim token of 32 uppercase hex":      {KindRoute, nil, claim("0123456789ABCDEF0123456789ABCDEF", "b1")},
		"claim token of 31 hex":                {KindRoute, nil, claim(strings.Repeat("a", 31), "b1")},
		"claim token of 33 hex":                {KindRoute, nil, claim(strings.Repeat("a", 33), "b1")},
		"legacy claim token over 40":           {KindRoute, nil, claim(".claim."+strings.Repeat("1", 34), "b1")},
		"legacy claim token without digits":    {KindRoute, nil, claim(".claim.", "b1")},
		"legacy claim token, other prefix":     {KindRoute, nil, claim(".claimx1234", "b1")},
		"approval by an unknown importer":      {KindRoute, []string{strings.Replace(approval, "'terminal'", "'import'", 1)}, strings.Replace(strings.Replace(approval, "'terminal'", "'imported'", 1), "VALUES (1,", "VALUES (2,", 1)},
		"send key of a missing message":        {KindRoute, nil, "INSERT INTO send_keys(key, id, recipient, kind, body_sha256, committed_at) VALUES ('" + tok + "', 5, 'o', 'task', '" + sha + "', 1)"},
		"event data over 2 KiB":                {KindRoute, nil, "INSERT INTO events(seq, epoch, time, type, data) VALUES (1, '" + tok + "', 1, 'message', '" + strings.Repeat("x", 2049) + "')"},
		"unknown event type":                   {KindRoute, nil, "INSERT INTO events(seq, epoch, time, type, data) VALUES (1, '" + tok + "', 1, 'chat', '{}')"},
		"second setup proof row":               {KindRoute, nil, "INSERT INTO setup_proof(one, version, doc, doc_sha256, written_at) VALUES (2, 3, '{}', 'h', 1)"},
		"hold reason over 300":                 {KindRoute, nil, "INSERT INTO holds(target, owner, reason, time) VALUES ('t', 'o', '" + strings.Repeat("r", 301) + "', 1)"},
		"route of an unknown state":            {KindAccount, nil, "INSERT INTO routes(route, inbox, repo, herdr_socket, db_path, created, state, open_gen, reserved_message_id, reserved_seq) VALUES ('r', 'i', 'g', 's', 'd', 1, 'paused', 'x', 0, 0)"},
		"two inboxes under one route name":     {KindAccount, []string{"INSERT INTO adoptions(inbox, route, inbox_dev, inbox_ino, confirm_pair_sha256, phase, epoch, started, staging_path) VALUES ('i1', 'r', 1, 1, 'c', 'locks', 'e', 1, 's')"}, "INSERT INTO adoptions(inbox, route, inbox_dev, inbox_ino, confirm_pair_sha256, phase, epoch, started, staging_path) VALUES ('i2', 'r', 1, 2, 'c', 'locks', 'e', 1, 's')"},
		"unknown activity source":              {KindAccount, nil, "INSERT INTO activity(source, time) VALUES ('cli', 1)"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := openKind(t, tc.kind)
			// A constraint, not a mistyped statement, must refuse it.
			if err := exec(t, c, append(tc.ok, tc.refused)...); err == nil || !strings.Contains(err.Error(), "constraint") {
				t.Fatalf("%s: %v", tc.refused, err)
			}
		})
	}
}

// TestSchemaV1ForeignKeysClean: the fresh schema has no dangling foreign
// key declarations (each references an existing table and key).
func TestSchemaV1ForeignKeysClean(t *testing.T) {
	for _, kind := range []Kind{KindRoute, KindAccount} {
		c := openKind(t, kind)
		for name := range tables(t, c) {
			if err := c.Query("SELECT * FROM pragma_foreign_key_check(?)", []any{name}, func(Row) error { return nil }); err != nil {
				t.Errorf("%s.%s: %v", kind, name, err)
			}
		}
	}
}
