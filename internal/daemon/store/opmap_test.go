package store

import (
	"fmt"
	"strings"
	"testing"
)

// opRow is one step of the operation map: its statements run in one
// transaction, in the order design section 3 gives them.
type opRow struct {
	op  string
	sql string
}

// TestOpMapOnFreshV1 runs the statements of every row of design section
// 3 on fresh version 1 route and account files with foreign_keys on:
// any constraint, column or syntax mismatch between the operation map
// and the schema fails here, before version 1 is frozen.
func TestOpMapOnFreshV1(t *testing.T) {
	sha := strings.Repeat("a", 64)
	tok := strings.Repeat("a", 32)
	n := 0
	evt := func(typ string) string {
		n++
		return fmt.Sprintf("INSERT INTO events(seq, epoch, time, type, recipient, data) VALUES (%d, '%s', 1, '%s', NULL, '{}'); "+
			"UPDATE meta SET v = v + 1 WHERE k = 'next_seq';", n, tok, typ)
	}
	msg := func(id int, kind string) string {
		return fmt.Sprintf("INSERT INTO messages(id, sender, recipient, kind, envelope, body, body_sha256, state, created_seq, created_at, origin) "+
			"VALUES (%d, 'a', 'o', '%s', '{}', x'01', '%s', 'ready', 1, 1, 'daemon'); UPDATE meta SET v = %d WHERE k = 'last_message_id';", id, kind, sha, id)
	}
	party := func(name, role, state string) string {
		return fmt.Sprintf("INSERT INTO parties(name, role, pane, session, generation, state, enrolled_at) VALUES ('%s', '%s', 'p', '{}', 1, '%s', 1);", name, role, state)
	}
	approval := func(seq int, by string) string {
		return fmt.Sprintf("INSERT INTO approvals(seq, registry_sha256, request_sha256, approved_by, result, time) VALUES (%d, '%s', 'r%d', '%s', '{}', 1);", seq, sha, seq, by)
	}
	cred := func(name string, seq int) string {
		return fmt.Sprintf("INSERT INTO credentials(name, generation, option, sha256, state, approval_seq) VALUES ('%s', 1, 'identity', '%s', 'active', %d);", name, sha, seq)
	}
	enrollGuard := func(name, group string) string {
		return fmt.Sprintf("SELECT NOT EXISTS (SELECT 1 FROM parties WHERE state = 'pending' AND name <> '%s' AND name NOT IN "+
			"(SELECT matched_party FROM launches WHERE launch_group = '%s' AND matched_party IS NOT NULL));", name, group)
	}
	guardedMatch := func(seq, id int, name, role string, rev int) string {
		return fmt.Sprintf("UPDATE launches SET used_by_approval = %d, matched_party = '%s' WHERE id = %d AND name = '%s' AND pane = 'p' AND session = '{}' "+
			"AND role = '%s' AND registry_rev = %d AND used_by_approval IS NULL AND void_reason IS NULL AND expires_at > 2 AND (via = 'web' OR "+
			"(role = 'orchestrator' AND EXISTS (SELECT 1 FROM parties WHERE name = launcher_name AND generation = launcher_generation AND role = 'adjutant' AND state = 'approved')));",
			seq, name, id, name, role, rev)
	}
	supersede := func(old string) string {
		return fmt.Sprintf("UPDATE parties SET state = 'retired', retired_at = 2 WHERE name = '%s'; UPDATE credentials SET state = 'revoked' WHERE name = '%s'; "+
			"UPDATE launches SET void_reason = 'superseded' WHERE name = '%s' AND used_by_approval IS NULL AND void_reason IS NULL;", old, old, old)
	}
	const voidOpen = "UPDATE launches SET void_reason = 'registry changed' WHERE used_by_approval IS NULL AND void_reason IS NULL; UPDATE meta SET v = v + 1 WHERE k = 'registry_rev';"
	const openLaunch = "used_by_approval IS NULL AND void_reason IS NULL AND expires_at > 2"
	route := []opRow{
		{"meta", "INSERT INTO meta(k, v) VALUES ('epoch', '" + tok + "'), ('next_seq', '1'), ('last_message_id', '0'), ('oldest_seq', '1'), ('registry_rev', '0'), ('health_flags', '[]'), ('store_gen', 'g');"},
		{"send: key, window, backlog", fmt.Sprintf("SELECT id, recipient, kind, reply_to, body_sha256 FROM send_keys WHERE key = '%s'; SELECT count(*) FROM send_keys WHERE committed_at > 0; "+
			"SELECT count(*) FROM messages WHERE recipient = 'o' AND state IN ('new', 'ready');", tok)},
		{"send with question", msg(1, "question") + fmt.Sprintf("INSERT INTO send_keys(key, id, recipient, kind, reply_to, body_sha256, committed_at) VALUES ('%s', 1, 'o', 'question', NULL, '%s', 1); ", tok, sha) +
			"SELECT count(*) FROM questions WHERE state IN ('open', 'overdue_pending'); INSERT INTO questions(question_id, request_id, revision, message_id, sender, opened, deadline, irreversible, state) " +
			"VALUES ('q1', 'r1', 'v1', 1, 'a', 1, 20, 1, 'open');" + evt("message")},
		{"delivery hold", "INSERT INTO meta(k, v) VALUES ('delivery_hold', 'paused') ON CONFLICT(k) DO UPDATE SET v = excluded.v; UPDATE meta SET v = 'frozen' WHERE k = 'delivery_hold'; DELETE FROM meta WHERE k = 'delivery_hold';"},
		{"claim", fmt.Sprintf("SELECT token FROM claims WHERE read_key = 'read1'; SELECT token FROM claims WHERE box = 'o' AND state = 'active'; "+
			"INSERT INTO claims(token, box, owner, read_key, state, created_at) VALUES ('%s', 'o', 'o', 'read1', 'active', 1); "+
			"UPDATE messages SET state = 'cur', claim_token = '%s' WHERE recipient = 'o' AND state = 'ready';", tok, tok)},
		{"claim extend", fmt.Sprintf("SELECT bound FROM claim_keys WHERE extend_key = 'x1'; SELECT count(*) FROM claim_keys WHERE token = '%s' AND created_at > 0; "+
			"UPDATE messages SET state = 'cur', claim_token = '%s' WHERE recipient = 'o' AND state = 'ready'; INSERT INTO claim_keys(extend_key, token, bound, created_at) VALUES ('x1', '%s', '[]', 1);", tok, tok, tok)},
		{"read manifest", fmt.Sprintf("SELECT id, envelope, body_sha256 FROM messages WHERE claim_token = '%s' AND state = 'cur' AND id NOT IN (SELECT id FROM acks) ORDER BY id;", tok)},
		{"ack", "SELECT record FROM acks WHERE id = 1; INSERT INTO acks(id, claim, time, record, envelope_checksum) VALUES (1, 'c', 1, x'7b7d', '1-2'); UPDATE messages SET state = 'handled' WHERE id = 1;" + evt("receipt")},
		{"release", "SELECT count(*) FROM messages WHERE recipient = 'o' AND state IN ('cur', 'ready') AND id NOT IN (SELECT id FROM acks); UPDATE claims SET state = 'released', released_at = 2 WHERE box = 'o' AND state = 'active';"},
		{"side_agent_data send", msg(2, "side_agent_data") + evt("message")},
		{"quarantine and notice", "UPDATE messages SET state = 'quarantined' WHERE id = 2; INSERT INTO terminals(id, state, reason, notice_seq) VALUES (2, 'quarantined', 'bad', 4); " +
			"INSERT INTO notices(id, recipient, state, authority, time) VALUES (2, 'a', 'pending', 'HUMAN', 1);" + evt("notice")},
		{"hold and unhold", "INSERT OR REPLACE INTO holds(target, owner, reason, time) VALUES ('task', 'a', 'wait', 1); SELECT owner, reason FROM holds WHERE target = 'task'; " +
			"DELETE FROM holds WHERE target = 'task' AND owner = 'a'; INSERT OR REPLACE INTO resume_intents(target, intent, time) VALUES ('task', '{}', 1);"},
		{"question overdue", "UPDATE questions SET state = 'overdue_pending', decision = 'refuse', overdue_at = 20 WHERE question_id = 'q1';" + evt("question")},
		{"user_answer", msg(3, "user_answer") + "UPDATE questions SET state = 'answered', choice = 'yes', reason = 'human', reversal = 'undo', answer_message_id = 3, answered_at = 21 WHERE question_id = 'q1'; " +
			"UPDATE questions SET late_answer_at = 22, late_answer_message_id = 3 WHERE question_id = 'q1';" + evt("question")},
		{"cancel", msg(4, "question") + "INSERT INTO questions(question_id, request_id, revision, message_id, sender, opened, deadline, irreversible, state) VALUES ('q2', 'r2', 'v2', 4, 'a', 1, 20, 0, 'open'); " +
			msg(5, "cancel") + "UPDATE questions SET state = 'cancelled', cancelled_at = 3, cancel_message_id = 5, closed_at = 3 WHERE question_id = 'q2';" + evt("question")},
		{"first pairing", party("olda", "adjutant", "approved") + party("oldo", "orchestrator", "approved") + approval(1, "first-pairing") + cred("olda", 1) + cred("oldo", 1)},
		{"re-enroll (human path)", enrollGuard("newa", "") + party("newa", "adjutant", "pending") + party("newo", "orchestrator", "pending") + "UPDATE meta SET v = v + 1 WHERE k = 'registry_rev';" + voidOpen + evt("pair")},
		{"approve a re-enrollment without a retire", "SELECT result FROM approvals WHERE request_sha256 = 'r2';" + approval(2, "terminal") + supersede("olda") + supersede("oldo") +
			"UPDATE parties SET state = 'approved', approved_at = 2 WHERE name IN ('newa', 'newo');" + cred("newa", 2) + cred("newo", 2) + voidOpen + evt("pair")},
		{"retire", "UPDATE parties SET state = 'retired', retired_at = 3 WHERE name = 'newo'; UPDATE credentials SET state = 'revoked' WHERE name = 'newo'; " +
			"UPDATE launches SET void_reason = 'retired name' WHERE name = 'newo' AND used_by_approval IS NULL AND void_reason IS NULL;" + voidOpen + evt("pair")},
		{"adjutant launch recorded", "INSERT INTO launches(id, via, launcher_name, launcher_generation, name, role, pane, session, registry_rev, created_at, expires_at) " +
			"VALUES (1, 'adjutant', 'newa', 1, 'o2', 'orchestrator', 'p', '{}', 4, 1, 1000);"},
		{"enroll, adjutant launch matches", enrollGuard("o2", "") + party("o2", "orchestrator", "pending") + "SAVEPOINT launch;" + approval(3, "adjutant-launch") + guardedMatch(3, 1, "o2", "orchestrator", 4) +
			"UPDATE parties SET state = 'approved', approved_at = 3 WHERE name = 'o2';" + cred("o2", 3) + "RELEASE launch; UPDATE meta SET v = v + 1 WHERE k = 'registry_rev';" + voidOpen + evt("pair")},
		{"enroll, launch does not match", "INSERT INTO launches(id, via, launcher_name, launcher_generation, name, role, pane, session, registry_rev, created_at, expires_at) " +
			"VALUES (2, 'adjutant', 'newa', 1, 'o3', 'orchestrator', 'p', '{}', 1, 1, 1000);" + enrollGuard("o3", "") + party("o3", "orchestrator", "pending") +
			"SAVEPOINT launch;" + approval(5, "adjutant-launch") + guardedMatch(5, 2, "o3", "orchestrator", 6) + "ROLLBACK TO launch; RELEASE launch; UPDATE meta SET v = v + 1 WHERE k = 'registry_rev';" + voidOpen + evt("pair")},
		{"human retires the pending party", "UPDATE parties SET state = 'retired', retired_at = 4 WHERE name = 'o3';" + voidOpen},
		{"web group recorded", "INSERT INTO launches(id, via, device, name, role, pane, session, registry_rev, launch_group, created_at, expires_at) " +
			"VALUES (10, 'web', 'd', 'wa', 'adjutant', 'p', '{}', 8, 'g', 1, 1000);"},
		{"web group first enroll", enrollGuard("wa", "g") + party("wa", "adjutant", "pending") + "SAVEPOINT launch;" + approval(6, "web-launch") + guardedMatch(6, 10, "wa", "adjutant", 8) +
			"ROLLBACK TO launch; UPDATE launches SET matched_party = 'wa' WHERE id = 10; RELEASE launch; UPDATE meta SET v = v + 1 WHERE k = 'registry_rev'; " +
			"UPDATE launches SET registry_rev = 9 WHERE launch_group = 'g' AND used_by_approval IS NULL AND void_reason IS NULL; " +
			"UPDATE launches SET void_reason = 'registry changed' WHERE (launch_group IS NOT 'g') AND used_by_approval IS NULL AND void_reason IS NULL;" + evt("pair")},
		{"web group second launch (exemption)", "SELECT count(*) FROM parties WHERE state = 'pending' AND name NOT IN (SELECT matched_party FROM launches WHERE launch_group = 'g' AND matched_party IS NOT NULL " +
			"AND " + openLaunch + "); INSERT INTO launches(id, via, device, name, role, pane, session, registry_rev, launch_group, created_at, expires_at) VALUES (11, 'web', 'd', 'wo', 'orchestrator', 'p', '{}', 9, 'g', 1, 1000);"},
		{"web group completes", enrollGuard("wo", "g") + party("wo", "orchestrator", "pending") + "SAVEPOINT launch;" + approval(7, "web-launch") + guardedMatch(7, 11, "wo", "orchestrator", 9) +
			"SELECT count(*) FROM launches l WHERE launch_group = 'g' AND id <> 11 AND matched_party IS NOT NULL AND " + openLaunch + " AND EXISTS (SELECT 1 FROM parties p WHERE p.name = l.matched_party AND p.state = 'pending'); " +
			supersede("newa") + supersede("o2") + "UPDATE parties SET state = 'approved', approved_at = 5 WHERE name IN ('wa', 'wo');" + cred("wa", 7) + cred("wo", 7) +
			"UPDATE launches SET used_by_approval = 7 WHERE launch_group = 'g' AND matched_party IS NOT NULL; RELEASE launch;" + voidOpen + evt("pair")},
		{"dead web group goes to the human", "INSERT INTO launches(id, via, device, name, role, pane, session, registry_rev, launch_group, created_at, expires_at, void_reason) " +
			"VALUES (20, 'web', 'd', 'xa', 'adjutant', 'p', '{}', 10, 'h', 1, 1000, 'registry changed'), (21, 'web', 'd', 'xo', 'orchestrator', 'p', '{}', 10, 'h', 1, 1000, NULL); " +
			"UPDATE launches SET matched_party = 'xa' WHERE id = 20;" + party("xo", "orchestrator", "pending") + "SAVEPOINT launch;" + approval(8, "web-launch") + guardedMatch(8, 21, "xo", "orchestrator", 10) +
			"ROLLBACK TO launch; RELEASE launch; UPDATE meta SET v = v + 1 WHERE k = 'registry_rev';" + voidOpen + evt("pair")},
		{"setup", fmt.Sprintf("INSERT OR REPLACE INTO setup_proof(one, version, doc, doc_sha256, written_at, stale_reason) VALUES (1, 3, '{}', '%s', 1, NULL); "+
			"UPDATE setup_proof SET stale_reason = 'binary changed' WHERE one = 1; DELETE FROM targets; INSERT INTO targets(kind, name, identity) VALUES ('watch', 'wa', '{}'), ('resume', 'wo', '{}');", sha) + evt("health")},
		{"alerts", "INSERT OR REPLACE INTO alerts(key, title, payload, state, time) VALUES ('k', 't', '{}', 'pending', 1); UPDATE alerts SET state = 'shown' WHERE key = 'k'; UPDATE alerts SET state = 'resolved' WHERE key = 'k';" + evt("alert")},
		{"card journal", "INSERT INTO cards(episode, source, headline, relayed_id, time) VALUES ('ep', '{}', 'h', 3, 1);" + evt("card")},
		{"health", "UPDATE meta SET v = '[\"route-paused\"]' WHERE k = 'health_flags';" + evt("health")},
		{"import log", "INSERT INTO import_log(path, sha256, disposition, time) VALUES ('ack/m1.md.json', '" + sha + "', 'acks', 1);"},
		{"retention", "DELETE FROM events WHERE seq IN (SELECT seq FROM events WHERE seq < 5 ORDER BY seq LIMIT 1000); UPDATE meta SET v = '5' WHERE k = 'oldest_seq'; " +
			"DELETE FROM send_keys WHERE key IN (SELECT key FROM send_keys WHERE committed_at < 1000 ORDER BY committed_at LIMIT 1000); " +
			"DELETE FROM claim_keys WHERE token IN (SELECT token FROM claims WHERE state = 'released' AND released_at < 1000); " +
			"DELETE FROM questions WHERE question_id IN (SELECT question_id FROM questions WHERE closed_at < 1000 ORDER BY closed_at LIMIT 1000);"},
		{"reset-events", "UPDATE meta SET v = '" + strings.Repeat("b", 32) + "' WHERE k = 'epoch'; DELETE FROM events; UPDATE meta SET v = (SELECT v FROM meta WHERE k = 'next_seq') WHERE k = 'oldest_seq';"},
	}
	account := []opRow{
		{"meta", "INSERT INTO meta(k, v) VALUES ('route_count', '0');"},
		{"route add", "INSERT INTO routes(route, inbox, repo, herdr_socket, db_path, created, state, open_gen, reserved_message_id, reserved_seq) VALUES ('r', '/inbox', '/repo', '/socket', '/r.db', 1, 'active', 'g', 1000, 1000);"},
		{"reservation and open", "UPDATE routes SET reserved_message_id = reserved_message_id + 1000 WHERE route = 'r'; UPDATE routes SET reserved_seq = reserved_seq + 1000 WHERE route = 'r'; UPDATE routes SET open_gen = 'g2' WHERE route = 'r';"},
		{"adoption", "SELECT route, inbox_dev, inbox_ino FROM adoptions WHERE inbox = '/inbox2'; INSERT INTO adoptions(inbox, route, inbox_dev, inbox_ino, confirm_pair_sha256, phase, epoch, started, staging_path) " +
			"VALUES ('/inbox2', 'r2', 1, 2, 'h', 'locks', 'e', 1, '/s.db'); UPDATE adoptions SET phase = 'drain' WHERE inbox = '/inbox2'; UPDATE adoptions SET phase = 'import' WHERE inbox = '/inbox2'; " +
			"UPDATE adoptions SET phase = 'publish' WHERE inbox = '/inbox2'; INSERT INTO routes(route, inbox, repo, herdr_socket, db_path, created, state, open_gen, reserved_message_id, reserved_seq) " +
			"VALUES ('r2', '/inbox2', '/repo', '/socket', '/r2.db', 1, 'active', 'sg', 2000, 2000); UPDATE adoptions SET moved = json_insert(moved, '$[#]', 'ack') WHERE inbox = '/inbox2'; " +
			"UPDATE adoptions SET phase = 'done' WHERE inbox = '/inbox2'; DELETE FROM adoptions WHERE inbox = '/inbox2';"},
		{"activity", "INSERT OR REPLACE INTO activity(source, time) VALUES ('web', 1), ('adjutant', 2), ('orchestrator', 3);"},
		{"route removal", "UPDATE routes SET state = 'removed' WHERE route = 'r';"},
	}
	for kind, rows := range map[Kind][]opRow{KindRoute: route, KindAccount: account} {
		c := openKind(t, kind)
		for _, r := range rows {
			if err := c.Tx(func(tx Conn) error { return execScript(tx, r.sql) }); err != nil {
				t.Fatalf("%s %s: %v", kind, r.op, err)
			}
		}
	}
}
