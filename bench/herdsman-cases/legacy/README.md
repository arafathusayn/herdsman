# Retired renderer benchmark fixture

These modules preserve the renderer used by the existing herdsman-blocks,
herdsman-blocks-declarative and herdsman-patch benchmark baselines. The web
app uses native Reactre TSX instead and does not import this directory.

`make web-check` retains the fixture's strict declaration checks and all
unit/property tests. To run its tests alone, from `web/`:

```sh
bun test ../bench/herdsman-cases/legacy
```

The tests share the app's dependency-free fake DOM in `web/src/test-dom-core.ts`.
