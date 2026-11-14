# Reactre core + state

One application using `@reactre/core`, `@reactre/state` and the upstream
compilers from Herdsman's source submodule, pinned at
`f0cb6cb9f7d9c9a96c25fb25ece2d54adb702c27` (Reactre main verified for the update).
The existing `re` entry is the separate `@re/core` library; it is unchanged.

The [core + built-in signals entry](../reactre-signals/) uses the same
[view components and keyed projections](../../shared/reactre/), so their
comparison changes only snapshot ownership and its bridge.

Every fresh driver snapshot enters a compiled state actor. The upstream
`useActor` bridge connects it to core; no second writable UI model exists.
The benchmark contracts are directly typed. Herdsman's application snapshot now
also uses structural JSON contracts; generic request and collection adapters
retain their extended codec. This benchmark already avoided string conversion,
so upstream's immutable-data conversion measurements do not predict its speed.

Team, Activity and Checklist derive keyed row projections. Updates compare
each row's fields, retaining equal rows for core's keyed rendering. Initial
mounts defer the projection's key index until data changes; equal snapshots
reuse it without allocating replacement maps. Log renders directly from the actor: its key already
contains every displayed field. Native element rows let the compiler use
core's specialized list path, and a keyed selector limits selection work to
the previously and newly selected cards.
Per-lane memos retain unchanged member lists and counts. Activity bodies use
flat templates for their possible shapes, reducing nested DOM cleanup when
events leave the feed. Tests exercise same-key field changes, optional bodies,
selection and disposal through the actual compiled app factory.
The latest shared renderer uses typed row equality, stable lane projections and
weakly cached keys for retained event rows. Log classes compile as static holes;
empty-lane placeholders use the existing count memo. See the [paired profiles](../../TOP2-PROFILING.md)
for improvements, tradeoffs, rejected experiments and source hashes.
This is ordinary per-key reconciliation, permitted by the contract and also
used by the other entries. It never compares whole snapshots, reads operation
names, caches operation results or skips the actor transition. Tabs dispose
their old view; card selection goes through a card's own handler.

The build uses both upstream compilers with production state compilation,
the same minification and stylesheet as the other entries. Source licenses
remain in `web/vendor/reactre`; no published or duplicated runtime is used.
The source adapter resolves the vendored packages for whole-program analysis
as well as bundling. See the [mounting and cleanup profile](../../PROFILING.md)
for the compiler fallback regression and allocation optimizations.

From the repository root:

```sh
make web
cd bench/web/herdsman-ui-cases
bun install --frozen-lockfile
bun build.ts reactre-state vanillajs-lite
bun run.ts --apps vanillajs-lite,vanillajs-lite-control,reactre-state
```

For the full comparison, first install the other entries' existing pinned
dependencies, then build and run every entry:

```sh
for app in apps/*; do
  if [ -f "$app/bun.lock" ]; then (cd "$app" && bun install --frozen-lockfile); fi
done
bun build.ts
bun run.ts --iterations 16
```

The unchanged runner checks canonical DOM and card behavior before timing,
interleaves entries, applies the existing CPU throttling, and reports the
identical-build A/A noise floor. Results must include all workloads and the
control; a ranking within that noise floor is not a demonstrated speed win.
