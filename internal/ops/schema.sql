-- The operation journal: what was planned, what was about to happen and what
-- is known to have happened. A step is written 'intended' before its effect
-- runs and 'done' after the effect is confirmed.
CREATE TABLE operations (
  id TEXT PRIMARY KEY,
  key TEXT UNIQUE,                  -- the caller's idempotency key, when it gave one
  kind TEXT NOT NULL,
  plan TEXT NOT NULL,
  spec_sha256 TEXT NOT NULL,        -- a repeated key must describe the same work
  preconditions TEXT NOT NULL,      -- JSON list of what the plan assumed
  state TEXT NOT NULL CHECK (state IN ('running','done','failed','conflict','stopped','stale')),
  reason TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL,
  updated INTEGER NOT NULL
) STRICT;
CREATE INDEX operations_open ON operations(state) WHERE state IN ('running','conflict','stopped');

CREATE TABLE operation_steps (
  op_id TEXT NOT NULL REFERENCES operations(id),
  ordinal INTEGER NOT NULL,
  name TEXT NOT NULL,
  effect TEXT NOT NULL,
  key TEXT NOT NULL,
  spec TEXT NOT NULL,
  retry TEXT NOT NULL CHECK (retry IN ('never','idempotent')),
  events TEXT NOT NULL,             -- JSON list, enqueued in the transaction that records 'done'
  state TEXT NOT NULL CHECK (state IN ('pending','intended','done','conflict')),
  detail TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (op_id, ordinal),
  UNIQUE (op_id, key)
) STRICT;

CREATE TABLE operation_locks (
  name TEXT PRIMARY KEY,
  op_id TEXT NOT NULL REFERENCES operations(id)
) STRICT;
CREATE INDEX operation_locks_op ON operation_locks(op_id);

CREATE TABLE outbox (
  seq INTEGER PRIMARY KEY,
  key TEXT NOT NULL UNIQUE,         -- stable across retries: consumers drop a repeat by it
  op_id TEXT NOT NULL REFERENCES operations(id),
  topic TEXT NOT NULL,
  payload TEXT NOT NULL,
  created INTEGER NOT NULL,
  delivered INTEGER
) STRICT;
CREATE INDEX outbox_undelivered ON outbox(seq) WHERE delivered IS NULL;
