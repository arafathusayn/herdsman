-- 0006_notices.sql (account): the notices of herdr-core executions, their
-- waits, the route states and approvals their gates read, and a person's typed
-- submits (herdr-core backend spec, sections 10 and 12). Each notice, route
-- state, approval and submit is one JSON document beside the columns its
-- queries need. Every table starts empty.
CREATE TABLE notices (
  id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 3 AND 64),
  key TEXT NOT NULL UNIQUE,                     -- one operation step, one notice
  execution_id TEXT NOT NULL,
  repo TEXT NOT NULL, route TEXT NOT NULL,
  seq INTEGER NOT NULL UNIQUE CHECK (seq >= 1), -- issue order
  kind TEXT NOT NULL CHECK (kind IN ('brief','continue','dispatch','checkpoint','task','final_report')),
  state TEXT NOT NULL CHECK (state IN ('issued','claimed','handled','returned')),
  doc TEXT NOT NULL
) STRICT;
CREATE INDEX notices_execution ON notices(execution_id, seq);
CREATE INDEX notices_route ON notices(repo, route, seq);

-- one row per wait id an execution's waits used; the owner is the highest
-- generation, so a restart changes no owner
CREATE TABLE notice_waits (
  execution_id TEXT NOT NULL,
  wait_id TEXT NOT NULL CHECK (length(wait_id) BETWEEN 1 AND 64),
  generation INTEGER NOT NULL CHECK (generation >= 1),
  created INTEGER NOT NULL,
  PRIMARY KEY (execution_id, wait_id),
  UNIQUE (execution_id, generation)
) STRICT;

CREATE TABLE notice_routes (
  repo TEXT NOT NULL, route TEXT NOT NULL,
  doc TEXT NOT NULL,
  PRIMARY KEY (repo, route)
) STRICT;

CREATE TABLE notice_approvals (
  execution_id TEXT PRIMARY KEY,
  doc TEXT NOT NULL
) STRICT;

-- a person's typed submits; never retired by time
CREATE TABLE typed_submits (
  id TEXT PRIMARY KEY,
  execution_id TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('queued','pending_in_core','typed','rejected_before_write','unresolved')),
  doc TEXT NOT NULL,
  created INTEGER NOT NULL, updated INTEGER NOT NULL
) STRICT;
CREATE INDEX typed_submits_execution ON typed_submits(execution_id, state);
