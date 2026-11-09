-- What live detection last saw of a record. Kept apart from the columns that
-- grant authority: nothing that decides what may be stopped reads these.
ALTER TABLE resources ADD COLUMN evidence TEXT NOT NULL DEFAULT '';
ALTER TABLE resources ADD COLUMN detail TEXT NOT NULL DEFAULT '';
ALTER TABLE resources ADD COLUMN seen_at INTEGER NOT NULL DEFAULT 0;
