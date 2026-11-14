# Reactre benchmark mounting and propagation

Baseline: `fbd7b60dbc6a7e88b9cfe886718e2e2780732594`.
Reactre stays pinned to `f0cb6cb9f7d9c9a96c25fb25ece2d54adb702c27`.
Target: both Reactre entries in the first two places of the full benchmark;
report the actual ranking and control gap even if this target is not achieved.

1. Profile the compiled shared renderer's tab replacement and update propagation.
2. Preserve native element lists; compare typed row fields, stable derived lists,
   retained event keys and static log classes. Keep actor transitions and ownership.
3. Exercise same-key updates, optional bodies/lanes, remounting, prefix edge cases,
   keyed node retention and disposal for both entries; run full downstream checks.
4. Compare three alternating baseline/candidate diagnostic pairs, then run all
   implementations for five iterations with unchanged gates, workloads and controls.
5. Record all results, update PR #123, and check its latest head. Do not merge.

The baseline CPU profiles locate controllable work in cloning, insertion/removal,
row ownership, generic field equality and key construction. Common driver JSON
copying is substantial but is outside the implementation being optimized.

Rejected experiments remain in ignored `results/profiles/`: flat whole-row
functions lost the compiler's specialized list path and regressed tab replacement;
mapping every log row to add a class field increased allocation/update time;
an outgoing-tab projection guard added complexity without a clear gain.

The retained changes are data-derived. They do not inspect operation names,
compare whole snapshots, suppress actor transitions, retain hidden tab DOM,
change competitors or hand-render nodes outside Reactre.

## Measured outcome

Final implementation: `2c968c99dd801d30f51cbe4f0caa87df6b0c6cdc`. Web validation passes
351 tests and 5145 assertions, normal/strict types, both benchmark types and
format/ownership checks. All 316 production browser checks and the embedded
build pass. Final three-pair profiles verify equal canonical DOM hashes and
node counts in every case; source/bundle hashes remain stable on each side.

The final full five-iteration run passes 960 samples, all 16 gates and 192
retained-node checks. Signals ranks first (1.061728), state third (1.084408).
**Both-top-two remains unmet.** State's 0.62% gap to second is smaller than the
3.995% control gap. The apparent first-place signals result is not a demonstrated
repeatable win. Both completed benchmark runs and all profiling samples are
retained; no run was repeated just to obtain a favorable rank.

See [final results](../bench/web/herdsman-ui-cases/TOP2-RESULTS.md),
[paired profiles and tradeoffs](../bench/web/herdsman-ui-cases/TOP2-PROFILING.md),
and [the earlier candidate](../bench/web/herdsman-ui-cases/TOP2-FIRST-RESULTS.md).
Further work should address measured template/owned-row mounting costs and
establish ordering with a stronger balanced run. The supported child-component
syntax still produces owned slots; this task does not copy or alter upstream
compiler/runtime implementations to bypass their cleanup semantics.
