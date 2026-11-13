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
