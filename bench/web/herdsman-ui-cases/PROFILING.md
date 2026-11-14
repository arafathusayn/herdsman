# Reactre mounting and cleanup

For the current three-pair mounting and propagation comparison, including
rejected experiments and measured regressions, see [TOP2-PROFILING.md](TOP2-PROFILING.md).
The findings and runs below document the earlier compiler integration work.

Both Reactre entries use the same compiled renderer. The signals entry owns its
snapshot in a core signal; the state entry owns it in a state-machine actor. The
workloads, fresh-snapshot contract, DOM, competitors and timing method are unchanged.

## Reproduce

```sh
cd bench/web/herdsman-ui-cases
bun build.ts
bun profile-reactre.ts mounting
bun run.ts --iterations 3
```

Run the profiler and benchmark sequentially. The profiler writes readable,
syntax-minified diagnostic bundles, Chrome `.cpuprofile` files and a JSON summary
under `results/profiles/mounting/`. Open a CPU profile in Chrome DevTools. Use a
new label for each comparison; results are ignored generated artifacts.

The profiler warms each tab pair for 30 cycles, then records 300 cycles at a
100-microsecond sampling interval. Each CPU trace includes both the outgoing
view's cleanup and the incoming view's mount, in both directions. `targetMs`
counts only the target click's synchronous JavaScript, divided by `cycles` in
the table below. It includes the common driver snapshot and excludes a wait for
layout/paint. It is diagnostic evidence, **not** the benchmark's commit-time
score. Profiler sampling, GC and workstation load affect these small durations.

## Findings and changes

1. **Whole-program analysis could not resolve the source submodule.** The core
   compiler was running, but its default `Bun.resolveSync` host could not resolve
   the vendored packages' internal `@reactre/*` imports. Analysis returned `null`,
   and compilation conservatively treated imported plain data as possible stores.
   Generated lists copied arrays and installed dynamic bindings for plain fields.
   The source adapter now uses the same pinned-source resolver for bundling,
   upstream program analysis and runtime selection. An unresolved build graph
   fails explicitly. Runtime tests, which have no build entrypoints, retain the
   upstream per-file analysis behavior.
2. **Static labels still created content slots.** Imported record lookups in JSX
   were conservatively classified as possible node content. Each card and lane
   heading therefore allocated a slot effect and cleanup. Lane labels are now
   scalar fields of the existing read-only column projection; cards read their
   column's label. Event-body labels arrive as a scalar argument. Generated
   templates fill these text holes directly, eliminating the extra slot lifecycle
   and late text-node insertion. No extra application state or handwritten DOM
   renderer was introduced.
3. **Tab replacement has one control-flow owner.** A `Switch` with four `Match`
   branches expresses the mutually exclusive views, replacing four independent
   `Show` branches. Disposal of the outgoing branch precedes building its
   replacement. The diagnostic runs did not establish a separate speed benefit
   for this change.
4. **Row indexes are created only when needed.** A newly mounted projection has
   no old rows to reconcile, so it copies the input array without eagerly
   computing all keys and a second map beside the core's own list index. On
   unchanged snapshots it compares every row's fields and reuses the projection,
   avoiding replacement maps and key strings just before a tab is disposed.
   Changed snapshots still reconcile every key and preserve unchanged row
   identity. This is a data-derived optimization with no knowledge of operations.

The generated code and CPU samples point to template cloning, insertion/removal,
owned slots, cleanup and allocation/GC. The common snapshot driver also consumes
substantial time. There is still per-row ownership for conditional content;
the optimization does not eliminate that required lifecycle.

## Diagnostic measurements

Chrome 157.0.8103.78, no CPU throttling in this diagnostic. Mean synchronous
milliseconds per target click, 300 warmed cycles, on the same workstation:

| Entry          | Team before | Team after | Checklist before | Checklist after | Activity before | Activity after |
| -------------- | ----------: | ---------: | ---------------: | --------------: | --------------: | -------------: |
| core + signals |       0.374 |      0.323 |            0.242 |           0.211 |           0.480 |          0.411 |
| core + state   |       0.353 |      0.329 |            0.247 |           0.211 |           0.475 |          0.405 |
| vanillajs-lite |       0.197 |      0.191 |            0.145 |           0.151 |           0.272 |          0.284 |
| sonnet         |       0.260 |      0.288 |            0.206 |           0.226 |           0.460 |          0.432 |
| doohtml-dp     |       0.268 |      0.282 |            0.209 |           0.229 |           0.515 |          0.505 |

Before is source `746020a`, using the initial temporary version of this profiling
harness. After is source `46394278`, recorded under
`results/profiles/lazy-row-index/`. The intermediate scalar-label profile is
retained under `results/profiles/scalar-labels/`. Competitor variation illustrates why these
small JavaScript-only differences must not be presented as full-workload wins.

The first full three-iteration run, before the scalar-label change,
`2026-11-13T16-14-15-251Z`, ranked signals third (1.093800) and state fifth
(1.105687) among 15 implementations. `re` scored 1.138438. The A/A control gap
was 0.245%; every correctness gate passed and there were no invalid entries or
timing errors. This intermediate run is retained rather than discarded in favor
of a later ranking.

The second full three-iteration run, after scalar labels but before lazy indexes,
`2026-11-13T16-26-39-548Z`, ranked signals sixth (1.118819) and state fourth
(1.114055). `re` scored 1.147871. All gates passed without timing errors, but the
A/A gap was **4.603%**, larger than either entry's gap from `re`. This run does
not establish those speed differences. The README reports the latest complete
run from committed source `46394278`.

The final run, `2026-11-13T16-37-12-579Z`, ranks signals 3 and state 4
of 15, with a 2.795% A/A gap and no invalid entries or timing errors.
The signals top-two target was not met; the state top-five target was met in this run. See the README for all 15 scores.

## Validation

The compiled integration fixture imports plain rows with counting label getters.
Changing selection must update classes without re-reading those labels. Running
that fixture with the original resolver reads two labels again; with the fixed
resolver it reads none again. It also
checks keyed identity, changed row contents and disposal. The existing benchmark
tests exercise optional tasks, same-key event-body changes, every event shape,
selection and post-disposal updates for both owners. A projection regression
covers empty initialization, equal snapshots, reordering, changed fields,
removal and reintroduction. The full runner independently
checks canonical DOM/card behavior and records retained nodes for all entries.
