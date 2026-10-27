-- 0001_init.sql (route): design sqlite-storage.md section 1.3.
CREATE TABLE meta (
  k TEXT PRIMARY KEY, v TEXT NOT NULL
) STRICT;  -- kind=route, created_by, migrated_by.<n>, route, inbox, epoch, next_seq,
           -- last_message_id, oldest_seq, health_flags,
           -- registry_rev (raised by enroll, approve and retire; launch match, P1-4),
           -- clean_shutdown (set by a clean stop, cleared at open; quick_check rule, P1-6),
           -- store_gen (random, written at every open; restore detection, N-P1-4),
           -- delivery_hold ('paused' or 'frozen' from an adopted route; holds delivery, N-P1-5)

CREATE TABLE parties (            -- the registry: enrolled, pending and approved (7.4, 7.5)
  name TEXT PRIMARY KEY CHECK (length(name) BETWEEN 1 AND 32),
  role TEXT NOT NULL CHECK (role IN ('adjutant','orchestrator')),
  pane TEXT NOT NULL,
  session TEXT NOT NULL,          -- the full {agent,kind,source,value} identity, strict JSON
  generation INTEGER NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('pending','approved','retired')),
  enrolled_at INTEGER NOT NULL, approved_at INTEGER, retired_at INTEGER
) STRICT;
-- one approved and at most one pending party per role: a re-enrollment after a clear (7.7)
-- is pending next to the approved generation until the human approves the new pair
CREATE UNIQUE INDEX parties_approved_role ON parties(role) WHERE state = 'approved';
CREATE UNIQUE INDEX parties_pending_role ON parties(role) WHERE state = 'pending';

CREATE TABLE approvals (          -- one row per approval; replaces state/approval.json (F5)
  seq INTEGER PRIMARY KEY,        -- the event seq of its pair event
  registry_sha256 TEXT NOT NULL CHECK (length(registry_sha256) = 64),
  request_sha256 TEXT NOT NULL UNIQUE,   -- the exact request; a retry returns the result (F5)
  interaction TEXT, asked_by TEXT CHECK (asked_by IS NULL OR length(asked_by) <= 200),
  approved_by TEXT NOT NULL CHECK (approved_by IN ('terminal','web','adjutant-ask','adjutant-launch','web-launch','first-pairing')),
  result TEXT NOT NULL,           -- the 200 answer, strict JSON
  time INTEGER NOT NULL
) STRICT;

CREATE TABLE launches (           -- A3/A5 launch records (15-task-launch-pair, P1-4)
  id INTEGER PRIMARY KEY,
  via TEXT NOT NULL CHECK (via IN ('adjutant','web')),
  launcher_name TEXT,             -- the enrolled, approved adjutant that launched (via adjutant)
  launcher_generation INTEGER,    -- its generation at launch time
  device TEXT,                    -- the paired browser session (via web), from the server, never the browser
  name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 32),   -- the name the launched party enrolls as
  role TEXT NOT NULL CHECK (role IN ('adjutant','orchestrator')),
  pane TEXT NOT NULL, session TEXT NOT NULL,   -- exact identity Herdr returned
  registry_rev INTEGER NOT NULL,  -- meta.registry_rev at launch; any later registry change voids it,
                                  -- except its own web group's re-stamp by a matching enroll (N3-P0-1)
  launch_group TEXT,              -- web form: the adjutant and orchestrator launched together (N-P1-3)
  matched_party TEXT,             -- the pending party a web-group launch matched, before the group completes
  created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL,
  used_by_approval INTEGER REFERENCES approvals(seq),   -- single use
  void_reason TEXT,               -- registry changed, retired name, replaced session, expired
  CHECK ((via = 'adjutant') = (launcher_name IS NOT NULL AND launcher_generation IS NOT NULL)),
  CHECK ((via = 'web') = (device IS NOT NULL AND launch_group IS NOT NULL)),
  UNIQUE (launch_group, role)     -- a web group is one adjutant and one orchestrator launch (N4-P1-1)
) STRICT;
CREATE INDEX launches_open ON launches(name) WHERE used_by_approval IS NULL AND void_reason IS NULL;

CREATE TABLE credentials (        -- 7.5; secrets of option 'file' live here, never in the inbox
  name TEXT NOT NULL REFERENCES parties(name),
  generation INTEGER NOT NULL,
  option TEXT NOT NULL CHECK (option IN ('identity','file','keychain')),
  sha256 TEXT NOT NULL CHECK (length(sha256) = 64),
  secret BLOB CHECK ((option = 'file') = (secret IS NOT NULL)),
  state TEXT NOT NULL CHECK (state IN ('active','revoked')),
  approval_seq INTEGER NOT NULL REFERENCES approvals(seq),
  PRIMARY KEY (name, generation)
) STRICT;

CREATE TABLE messages (           -- every message: envelope, body and state (queue folders, ids/)
  id INTEGER PRIMARY KEY,         -- m%012d on the wire; never reused (meta.last_message_id)
  sender TEXT NOT NULL, recipient TEXT NOT NULL,
  kind TEXT NOT NULL, reply_to INTEGER,
  envelope TEXT NOT NULL,         -- the strict JSON header object, unchanged format
  body BLOB NOT NULL CHECK (length(body) <= 131072),
  body_sha256 TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('new','ready','cur','handled','quarantined','abandoned')),
  claim_token TEXT REFERENCES claims(token),
  created_seq INTEGER NOT NULL, created_at INTEGER NOT NULL,
  origin TEXT NOT NULL CHECK (origin IN ('daemon','imported','legacy-new'))
) STRICT;
CREATE INDEX messages_box_state ON messages(recipient, state, id);
CREATE INDEX messages_claim ON messages(claim_token) WHERE claim_token IS NOT NULL;

CREATE TABLE send_keys (          -- 5.4 idempotency; the 512-in-window bound is checked in the send txn
  key TEXT PRIMARY KEY CHECK (length(key) = 32),
  id INTEGER NOT NULL REFERENCES messages(id),
  recipient TEXT NOT NULL, kind TEXT NOT NULL, reply_to INTEGER, body_sha256 TEXT NOT NULL,
  committed_at INTEGER NOT NULL
) STRICT;
CREATE INDEX send_keys_time ON send_keys(committed_at);

CREATE TABLE claims (
  token TEXT PRIMARY KEY CHECK (length(token) = 32),
  box TEXT NOT NULL, owner TEXT NOT NULL,
  read_key TEXT NOT NULL UNIQUE,
  state TEXT NOT NULL CHECK (state IN ('active','released')),
  created_at INTEGER NOT NULL, released_at INTEGER
) STRICT;
CREATE UNIQUE INDEX claims_one_active ON claims(box) WHERE state = 'active';

CREATE TABLE claim_keys (         -- extend keys and their answers (64 per claim in the window)
  extend_key TEXT PRIMARY KEY, token TEXT NOT NULL REFERENCES claims(token),
  bound TEXT NOT NULL,            -- the ids it bound, JSON list (empty list for an empty extension)
  created_at INTEGER NOT NULL
) STRICT;

CREATE TABLE acks (               -- ack/<id>.md.json and ids/<id>/ack.json in one row
  id INTEGER PRIMARY KEY REFERENCES messages(id),
  claim TEXT NOT NULL, time INTEGER NOT NULL,
  record BLOB NOT NULL CHECK (length(record) <= 131072),   -- the encoded ack file, byte for byte
  envelope_checksum TEXT NOT NULL                          -- CRC <crc>-<length> (4.6)
) STRICT;

CREATE TABLE terminals (          -- quarantine and abandoned dispositions with their reason
  id INTEGER PRIMARY KEY REFERENCES messages(id),
  state TEXT NOT NULL, reason TEXT NOT NULL, notice_seq INTEGER
) STRICT;

CREATE TABLE holds (              -- state/hold-<target>; written under the input lock (3.7)
  target TEXT NOT NULL, owner TEXT NOT NULL, reason TEXT NOT NULL CHECK (length(reason) <= 300),
  time INTEGER NOT NULL, PRIMARY KEY (target, owner)
) STRICT;

-- No exposures table: state/boundary-tainted and state/exposure-pending stay files in the
-- inbox, written by the gate and the daemon under the input lock (section 6, P0-1).

CREATE TABLE notices (            -- <box>/notices/*.json, the sender notices (P1-7)
  id INTEGER PRIMARY KEY REFERENCES messages(id),
  recipient TEXT NOT NULL, state TEXT NOT NULL, authority TEXT, time INTEGER NOT NULL
) STRICT;

CREATE TABLE resume_intents (     -- state/resume-*, written under the input lock (3.7, P1-7)
  target TEXT PRIMARY KEY, intent TEXT NOT NULL, time INTEGER NOT NULL
) STRICT;

CREATE TABLE setup_proof (        -- 7.3 setup proof, version 3 (section 6); one row
  one INTEGER PRIMARY KEY CHECK (one = 1),
  version INTEGER NOT NULL CHECK (version = 3),
  doc TEXT NOT NULL,              -- strict JSON: rules, binary, store, boundary, pair, targets, probes
  doc_sha256 TEXT NOT NULL, written_at INTEGER NOT NULL,
  stale_reason TEXT               -- the stale latch: set once, cleared only by setup
) STRICT;

CREATE TABLE events (             -- the stored event stream (5.3); replaces events/*.ndjson
  seq INTEGER PRIMARY KEY,        -- per route, from meta.next_seq, never reused in an epoch
  epoch TEXT NOT NULL CHECK (length(epoch) = 32),
  time INTEGER NOT NULL,
  type TEXT NOT NULL CHECK (type IN ('message','receipt','notice','health','card','alert','pair','question')),
  recipient TEXT,                 -- the party a party stream filters on (5.3 table), NULL for both
  data TEXT NOT NULL CHECK (length(data) <= 2048)   -- the 5.3 type fields, strict JSON, never a body
) STRICT;

CREATE TABLE questions (          -- 6.5; replaces state/questions/<hash>.json
  question_id TEXT PRIMARY KEY, request_id TEXT NOT NULL, revision TEXT NOT NULL,
  message_id INTEGER NOT NULL REFERENCES messages(id), sender TEXT NOT NULL,
  opened INTEGER NOT NULL, deadline INTEGER NOT NULL, irreversible INTEGER NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('open','overdue_pending','decided_without_answer','answered','cancelled')),
  choice TEXT CHECK (length(choice) <= 200), reason TEXT CHECK (length(reason) <= 500),
  reversal TEXT CHECK (length(reversal) <= 300),
  decision TEXT, answer_message_id INTEGER, answered_at INTEGER, overdue_at INTEGER,
  late_answer_at INTEGER, late_answer_message_id INTEGER, cancelled_at INTEGER,
  cancel_message_id INTEGER, closed_at INTEGER
) STRICT;
CREATE INDEX questions_active ON questions(deadline) WHERE state IN ('open','overdue_pending');

CREATE TABLE alerts (             -- alerts/<key>.pending and its states
  key TEXT PRIMARY KEY, title TEXT NOT NULL, payload TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('pending','shown','resolved')), time INTEGER NOT NULL
) STRICT;

CREATE TABLE cards (              -- card-events/ journal (Phase B producer, schema now)
  episode TEXT PRIMARY KEY, source TEXT NOT NULL, headline TEXT NOT NULL,
  relayed_id INTEGER, time INTEGER NOT NULL
) STRICT;

CREATE TABLE targets (            -- watch-targets.json and resume-targets.json
  kind TEXT NOT NULL CHECK (kind IN ('watch','resume')), name TEXT NOT NULL,
  identity TEXT NOT NULL, PRIMARY KEY (kind, name)
) STRICT;

CREATE TABLE import_log (         -- what route adopt imported (section 5), one row per source file
  path TEXT PRIMARY KEY, sha256 TEXT NOT NULL, disposition TEXT NOT NULL, time INTEGER NOT NULL
) STRICT;
