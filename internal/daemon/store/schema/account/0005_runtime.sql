-- 0005_runtime.sql (account): the installation default backend and the
-- execution records of routes the operation journal starts (herdr-core backend
-- spec, sections 5 and 6). Those routes have no route store, so their records
-- live beside their journal, in the shape of the route store's executions.
--
-- The default is written once: herdr-tui with source existing-store when this
-- migration upgrades a store that already existed, and herdr-tui with source
-- new-store when the store is created new. herdr-core ships as an experiment
-- in this release (spec section 5, the fallback), so no store selects it
-- unasked; the record, not a constant, is what a later release that selects
-- core for new installations writes for the stores it creates. migrate()
-- tells the two apart through temp.migration_run.
CREATE TABLE installation (
  k TEXT PRIMARY KEY CHECK (k IN ('runtime.backend')),
  v TEXT NOT NULL CHECK (v IN ('herdr-core','herdr-tui')),
  source TEXT NOT NULL CHECK (source IN ('existing-store','new-store','set')),
  written_at INTEGER NOT NULL
) STRICT;
INSERT INTO installation(k, v, source, written_at)
SELECT 'runtime.backend',
       'herdr-tui',
       CASE WHEN fresh = 1 THEN 'new-store' ELSE 'existing-store' END,
       CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)
FROM temp.migration_run;

CREATE TABLE executions (
  execution_id TEXT PRIMARY KEY CHECK (length(execution_id) = 36),   -- random UUID
  schema_version INTEGER NOT NULL CHECK (schema_version >= 1),
  repo TEXT NOT NULL, route TEXT NOT NULL,
  agent TEXT NOT NULL CHECK (length(agent) BETWEEN 1 AND 200),       -- the logical agent
  role TEXT NOT NULL, task_id TEXT NOT NULL DEFAULT '',
  launch_intent_id TEXT NOT NULL UNIQUE,                              -- the durable intent it fulfils
  generation INTEGER NOT NULL CHECK (generation >= 1),
  backend TEXT NOT NULL CHECK (backend IN ('herdr-core','herdr-tui')),
  endpoint_id TEXT NOT NULL,                                          -- core identity or TUI socket
  terminal_address TEXT NOT NULL DEFAULT '',                          -- core address, never a name
  pane TEXT NOT NULL DEFAULT '',                                      -- herdr-tui only
  initial_created_ms INTEGER,
  observed_pid INTEGER, observed_process_start TEXT,                  -- process evidence
  argv_digest TEXT NOT NULL DEFAULT '', cwd TEXT NOT NULL DEFAULT '',
  harness_kind TEXT NOT NULL, harness_version TEXT NOT NULL DEFAULT '',
  policy_digest TEXT NOT NULL DEFAULT '', launch_guard_registration TEXT NOT NULL DEFAULT '',
  native_conversation TEXT,                                           -- strict JSON {value, provenance}
  desired_state TEXT NOT NULL CHECK (desired_state IN ('running','stopped')),
  -- the independent observation fields of section 9
  process_state TEXT NOT NULL CHECK (process_state IN ('starting','live','lost','replaced','finished','unknown')),
  readiness TEXT NOT NULL CHECK (readiness IN ('waiting','ready','needs_attention','identity_unknown')),
  program_status TEXT NOT NULL CHECK (program_status IN ('idle','working','blocked','done','error','unknown')),
  raw_program_status TEXT NOT NULL DEFAULT '',
  visibility TEXT NOT NULL CHECK (visibility IN ('unverified','pending','verified','missing')),
  app_state TEXT NOT NULL DEFAULT '' CHECK (app_state IN ('','open','closed','unreachable')),
  orchestration TEXT NOT NULL CHECK (orchestration IN ('active','paused','stopping','stopped','failed')),
  task_outcome TEXT NOT NULL DEFAULT '' CHECK (task_outcome IN ('','pending','review_required','accepted','rejected')),
  observed_at INTEGER NOT NULL,
  app_binding TEXT,                                                   -- strict JSON (section 7)
  tui_binding TEXT,                                                   -- strict JSON
  last_verified_at INTEGER,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
  UNIQUE (repo, route, agent, generation)
) STRICT;
-- one running execution per process: (pid, start time) is the identity
CREATE UNIQUE INDEX executions_process ON executions(observed_pid, observed_process_start)
  WHERE observed_pid IS NOT NULL AND desired_state = 'running';
