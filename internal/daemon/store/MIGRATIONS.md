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
- Version 5 adds the installation default backend, `installation`, and the
  execution records of routes the operation journal starts, `executions`. It
  writes one row into `installation`, once: `herdr-tui` with source
  `existing-store` when it upgrades a store that already existed, and
  `herdr-tui` with source `new-store` when the store is created new, because
  herdr-core ships as an experiment in this release. Existing rows are
  untouched. An older herdsman refuses a store of version 5 with
  `ErrSchemaNewer`; going back to the Herdr TUI is the config line
  `backend = "herdr-tui"` on this version, not a downgrade. Fixture:
  `testdata/schema-4`.
- Version 6 adds the notices of herdr-core executions and what their gates
  read: `notices`, `notice_waits`, `notice_routes`, `notice_approvals`, and a
  person's typed submits, `typed_submits`. Every table starts empty and
  existing rows are untouched.

## Route

- Version 2 adds the execution records, `executions`, and the route's backend,
  `route_runtime`. Both start empty; `parties` and `launches` keep their
  columns. Fixture: `testdata/schema-1` and `testdata/schema-4`.

## Listed transformations

- account 5: added installation.*
