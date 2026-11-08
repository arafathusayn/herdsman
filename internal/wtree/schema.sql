-- One database per repository, shared by all of its worktrees.

CREATE TABLE worktrees (
	task       TEXT PRIMARY KEY,
	branch     TEXT NOT NULL,
	path       TEXT NOT NULL UNIQUE,
	created_at INTEGER NOT NULL
) STRICT;

CREATE TABLE tasks (
	task       TEXT PRIMARY KEY,
	base       TEXT NOT NULL DEFAULT '',
	route      TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
) STRICT;

-- Start tells a reused process id or volume name from the one recorded.
CREATE TABLE resources (
	task          TEXT NOT NULL,
	execution     TEXT NOT NULL,
	kind          TEXT NOT NULL,
	id            TEXT NOT NULL,
	start         TEXT NOT NULL DEFAULT '',
	command       TEXT NOT NULL DEFAULT '',
	adopted       INTEGER NOT NULL DEFAULT 0,
	source        TEXT NOT NULL DEFAULT '',
	registered_at INTEGER NOT NULL,
	PRIMARY KEY (task, execution, kind, id, start)
) STRICT;

CREATE TABLE hook_runs (
	id          INTEGER PRIMARY KEY,
	task        TEXT NOT NULL,
	hook_type   TEXT NOT NULL,
	hook_name   TEXT NOT NULL DEFAULT '',
	exit_code   INTEGER,
	started_at  INTEGER NOT NULL,
	finished_at INTEGER,
	log_path    TEXT NOT NULL DEFAULT ''
) STRICT;

-- Keys are object ids only, so an entry can never go stale.
CREATE TABLE caches (
	kind       TEXT NOT NULL,
	key        TEXT NOT NULL,
	value      TEXT NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (kind, key)
) STRICT;

CREATE TABLE archives (
	id         INTEGER PRIMARY KEY,
	task       TEXT NOT NULL,
	path       TEXT NOT NULL,
	created_at INTEGER NOT NULL
) STRICT;

CREATE TABLE meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
) STRICT;
