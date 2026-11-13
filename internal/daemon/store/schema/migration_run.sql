-- migration_run.sql: the one fact a migration file may read about the run
-- itself, in the connection's temporary schema. migrate() fills it before the
-- first file and empties it after the last; nothing persists in the database.
-- fresh = 1 when this run creates the store, 0 when it upgrades one.
CREATE TEMP TABLE IF NOT EXISTS migration_run (
  fresh INTEGER NOT NULL CHECK (fresh IN (0, 1))
) STRICT;
DELETE FROM temp.migration_run;
