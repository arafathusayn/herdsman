# Schema migrations

Every change a migration makes to data that already exists is listed here, one
line per change, in the form the fixture test reads:

    - <kind> <version>: <changed|removed|added> <table>.<column>

`migrate_fixture_test.go` migrates a database of every released schema forward
and fails on any difference this file does not name, so a migration can never
rewrite a payload or an association silently.

A migration that only adds empty tables or indexes changes no existing data and
needs no line. Those are noted below for the reader.

## Account

- Version 2 adds the operation journal: `operations`, `operation_steps`,
  `operation_locks` and `outbox`. Existing rows are untouched.
- Version 4 adds the snapshot records: `snapshots`. Existing rows are untouched.

## Listed transformations

None yet.
