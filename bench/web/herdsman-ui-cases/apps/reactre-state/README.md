# Reactre core + state

One application using `@reactre/core`, `@reactre/state` and the upstream
compilers from Herdsman's source submodule, pinned at
`8bae29b9fce00ab1e192f906e15191c6057550d7` (Reactre main when added).
The existing `re` entry is the separate `@re/core` library; it is unchanged.

Every fresh driver snapshot enters a compiled state actor. The upstream
`useActor` bridge connects it to core; no second writable UI model exists.
The finite benchmark contracts are directly typed, unlike Herdsman's SDK
boundary, which also needs to encode optional fields and collections.

Team, Activity and Checklist derive keyed row projections. They visit every
input row and compare that row's fields, retaining equal rows for core's
keyed rendering. Log renders directly from the actor: its key already
contains every displayed field. Native element rows let the compiler use
core's specialized list path, and a keyed selector limits selection work to
the previously and newly selected cards.
Per-lane memos retain unchanged member lists and counts. Activity bodies use
flat templates for their possible shapes, reducing nested DOM cleanup when
events leave the feed. Tests exercise same-key field changes, optional bodies,
selection and disposal through the actual compiled app factory.
This is ordinary per-key reconciliation, permitted by the contract and also
used by the other entries. It never compares whole snapshots, reads operation
names, caches operation results or skips the actor transition. Tabs dispose
their old view; card selection goes through a card's own handler.

The build uses both upstream compilers with production state compilation,
the same minification and stylesheet as the other entries. Source licenses
remain in `web/vendor/reactre`; no published or duplicated runtime is used.

From the repository root:

```sh
make web
cd bench/web/herdsman-ui-cases
bun install --frozen-lockfile
bun build.ts reactre-state herdsman-patch
bun run.ts --apps herdsman-patch,herdsman-patch-control,reactre-state
```

For the full comparison, first install the other entries' existing pinned
dependencies, then build and run every entry:

```sh
for app in apps/*; do
  if [ -f "$app/bun.lock" ]; then (cd "$app" && bun install --frozen-lockfile); fi
done
bun build.ts
bun run.ts --iterations 15
```

The unchanged runner checks canonical DOM and card behavior before timing,
interleaves entries, applies the existing CPU throttling, and reports the
identical-build A/A noise floor. Results must include all workloads and the
control; a ranking within that noise floor is not a demonstrated speed win.
