-- 0001_init.sql (route): design sqlite-storage.md section 1.3.
CREATE TABLE meta (
  k TEXT PRIMARY KEY, v TEXT NOT NULL
) STRICT;
