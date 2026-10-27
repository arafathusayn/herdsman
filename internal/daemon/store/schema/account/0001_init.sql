-- 0001_init.sql (account): design sqlite-storage.md section 1.4.
CREATE TABLE meta (k TEXT PRIMARY KEY, v TEXT NOT NULL) STRICT;   -- kind=account, created_by, …
CREATE TABLE routes (             -- replaces daemon/routes.json
  route TEXT PRIMARY KEY, inbox TEXT NOT NULL UNIQUE, repo TEXT NOT NULL,
  herdr_socket TEXT NOT NULL, db_path TEXT NOT NULL UNIQUE, created INTEGER NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('active','adopting','removed')),
  open_gen TEXT NOT NULL,          -- the route's meta.store_gen written at its last open (N-P1-4)
  reserved_message_id INTEGER NOT NULL,  -- ids up to here may be issued; raised in blocks of 1000
  reserved_seq INTEGER NOT NULL    -- event seqs up to here may be issued; raised in blocks of 1000
) STRICT;
CREATE TABLE adoptions (          -- replaces daemon/adopting.json and state/adopting.json
  inbox TEXT PRIMARY KEY, route TEXT NOT NULL UNIQUE,  -- one route name per inbox and per adoption (section 5)
  inbox_dev INTEGER NOT NULL, inbox_ino INTEGER NOT NULL, confirm_pair_sha256 TEXT NOT NULL,
  phase TEXT NOT NULL CHECK (phase IN ('locks','import','drain','publish','done')),
  epoch TEXT NOT NULL, started INTEGER NOT NULL, staging_path TEXT NOT NULL,
  moved TEXT NOT NULL DEFAULT '[]'  -- JSON list of top-level names already moved to legacy (P1-7, P1-5)
) STRICT;
-- No credential index (P1-3): a credential is looked up in the route DB of the request's path;
-- wrong_route is only an error classification (section 6).
CREATE TABLE activity (           -- 6.5 last-message time per source
  source TEXT PRIMARY KEY CHECK (source IN ('web','adjutant','orchestrator')), time INTEGER NOT NULL
) STRICT;
