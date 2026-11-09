-- Snapshots the account can undo from. The id is the operation that took it;
-- dir is where the folder lives now, which an import changes.
CREATE TABLE snapshots (
  id TEXT PRIMARY KEY,
  task TEXT NOT NULL,
  dir TEXT NOT NULL,
  repo TEXT NOT NULL DEFAULT '',    -- the common git directory, empty when unknown
  size INTEGER NOT NULL,
  created INTEGER NOT NULL
) STRICT;
CREATE INDEX snapshots_created ON snapshots(created);
