# Final Reactre renderer: mounting and propagation

Baseline: `fbd7b60dbc6a7e88b9cfe886718e2e2780732594`.
Candidate: `2c968c99dd801d30f51cbe4f0caa87df6b0c6cdc`.
Both use Reactre `f0cb6cb9f7d9c9a96c25fb25ece2d54adb702c27`.

## Method

Three paired diagnostic runs alternate baseline/candidate, candidate/baseline,
then baseline/candidate. Each builds named, syntax-minified bundles with the
production compiler and runtime. Run `bun profile-reactre.ts LABEL all` after
`bun build.ts`; swap `shared/reactre/view.tsx`, `rows.ts`, and both entries' `app.tsx` files to the source
revision being measured. Restore the candidate before the next validation/run.
Do not overlap profiling with builds or tests.

Chrome 157.0.8103.78, headless, no CPU throttling; 30 warmup cycles and 300
measured cycles per case, with a 100-microsecond CPU sampling interval. Tab
profiles include both outgoing disposal and incoming mounting in both directions;
the timer counts only the target click. Update profiles click the named operation
on its visible tab. They include the common driver's fresh JSON snapshot.
These are synchronous script diagnostics, not the official commit-time score.

All 55 app/case combinations have identical canonical DOM hashes and element
counts across every side and pair. The three unchanged contenders illustrate
workstation and sampling variation. The raw per-run means, source and bundle
hashes, canonical hashes, and profile-summary SHA-256s are recorded in
[TOP2-PROFILING.json](TOP2-PROFILING.json). Full named bundles and CPU traces
remain under ignored `results/profiles/top2-final-{1,2,3}-{base,candidate}/`.

## Changes retained

- Both entries read the current snapshot directly under their existing mount
  guard. Removing the retained-value callback eliminates two generated memos
  per entry. The view machine, initial empty UI, every render event, and stop
  behavior stay unchanged.

- Fixed-shape Member, Event and Check projections compare their typed fields
  directly. Every row is still examined; generic projections retain their
  complete field/own-property equality. No whole-snapshot shortcut is used.
- Native core memos retain identical derived lane arrays. They replace the
  extra effect/version-signal bridge used by configurable-equality memos.
  Lane visibility also retains identity when only a card's fields change.
- Activity reuses a retained immutable row's canonical event key through a
  view-scoped WeakMap. Incoming snapshots still use the complete event key;
  a new row object computes its own key. Entries cannot retain removed rows.
- Empty lanes derive their placeholder from the existing count memo instead
  of adding a separate mutable empty-list notification signal.
- Log's fixed prefix is expressed as plain string reads. Generated code fills
  the class with the other static holes, removing the per-line observer caused
  by conservative analysis of `startsWith`. Empty/short strings, case sensitivity,
  keyed reordering, remounts and disposal have regressions.

The shared renderer stays compiler-generated JSX; native keyed lists, card
handlers and all actor transitions remain. No hidden tab DOM, manual DOM renderer,
operation-name knowledge, competing application store or upstream source edit is
introduced. This change targets the two benchmark entries, not a measured
production-application latency claim.

## Results

Median of three per-run means, milliseconds. Positive reduction means less time;
faster pairs are descriptive counts, not confidence intervals. Include neutral
and negative results rather than selecting only favorable cases.

| App             | Case      | Base ms | Candidate ms | Reduction | Faster pairs |
| --------------- | --------- | ------: | -----------: | --------: | -----------: |
| reactre-signals | team      |  0.3207 |       0.3067 |      4.4% |          3/3 |
| reactre-signals | checklist |  0.2150 |       0.2150 |     -0.0% |          1/3 |
| reactre-signals | activity  |  0.4000 |       0.4080 |     -2.0% |          1/3 |
| reactre-signals | log       |  0.2650 |       0.2500 |      5.7% |          3/3 |
| reactre-signals | poll      |  0.0903 |       0.0847 |      6.3% |          3/3 |
| reactre-signals | status    |  0.1000 |       0.0947 |      5.3% |          3/3 |
| reactre-signals | select    |  0.0903 |       0.0887 |      1.8% |          3/3 |
| reactre-signals | toggle    |  0.0980 |       0.0900 |      8.2% |          3/3 |
| reactre-signals | event     |  0.2023 |       0.1657 |     18.1% |          3/3 |
| reactre-signals | burst     |  0.2450 |       0.2123 |     13.3% |          3/3 |
| reactre-signals | logline   |  0.1887 |       0.1830 |      3.0% |          2/3 |
| reactre-state   | team      |  0.3190 |       0.2980 |      6.6% |          3/3 |
| reactre-state   | checklist |  0.2127 |       0.1997 |      6.1% |          3/3 |
| reactre-state   | activity  |  0.4057 |       0.4007 |      1.2% |          2/3 |
| reactre-state   | log       |  0.2643 |       0.2540 |      3.9% |          3/3 |
| reactre-state   | poll      |  0.0887 |       0.0860 |      3.0% |          3/3 |
| reactre-state   | status    |  0.0997 |       0.0953 |      4.3% |          3/3 |
| reactre-state   | select    |  0.0910 |       0.0883 |      2.9% |          2/3 |
| reactre-state   | toggle    |  0.0970 |       0.0897 |      7.6% |          3/3 |
| reactre-state   | event     |  0.2003 |       0.1667 |     16.8% |          3/3 |
| reactre-state   | burst     |  0.2427 |       0.2153 |     11.3% |          3/3 |
| reactre-state   | logline   |  0.1890 |       0.1950 |     -3.2% |          1/3 |
| vanillajs-lite  | team      |  0.1963 |       0.1990 |     -1.4% |          1/3 |
| vanillajs-lite  | checklist |  0.1490 |       0.1463 |      1.8% |          2/3 |
| vanillajs-lite  | activity  |  0.2870 |       0.2967 |     -3.4% |          0/3 |
| vanillajs-lite  | log       |  0.2217 |       0.2317 |     -4.5% |          0/3 |
| vanillajs-lite  | poll      |  0.0883 |       0.0887 |     -0.4% |          0/3 |
| vanillajs-lite  | status    |  0.0907 |       0.0910 |     -0.4% |          1/3 |
| vanillajs-lite  | select    |  0.0910 |       0.0910 |      0.0% |          2/3 |
| vanillajs-lite  | toggle    |  0.0850 |       0.0863 |     -1.6% |          0/3 |
| vanillajs-lite  | event     |  0.1517 |       0.1513 |      0.2% |          2/3 |
| vanillajs-lite  | burst     |  0.1817 |       0.1777 |      2.2% |          3/3 |
| vanillajs-lite  | logline   |  0.1890 |       0.1830 |      3.2% |          2/3 |
| sonnet          | team      |  0.2887 |       0.2883 |      0.1% |          2/3 |
| sonnet          | checklist |  0.2217 |       0.2247 |     -1.4% |          1/3 |
| sonnet          | activity  |  0.4463 |       0.4520 |     -1.3% |          1/3 |
| sonnet          | log       |  0.3680 |       0.3740 |     -1.6% |          1/3 |
| sonnet          | poll      |  0.0953 |       0.0950 |      0.3% |          2/3 |
| sonnet          | status    |  0.1000 |       0.1010 |     -1.0% |          0/3 |
| sonnet          | select    |  0.0957 |       0.0963 |     -0.7% |          1/3 |
| sonnet          | toggle    |  0.0940 |       0.0943 |     -0.4% |          2/3 |
| sonnet          | event     |  0.1640 |       0.1647 |     -0.4% |          0/3 |
| sonnet          | burst     |  0.2067 |       0.2053 |      0.6% |          2/3 |
| sonnet          | logline   |  0.2030 |       0.2030 |     -0.0% |          0/3 |
| doohtml-dp      | team      |  0.2603 |       0.2613 |     -0.4% |          1/3 |
| doohtml-dp      | checklist |  0.2053 |       0.2123 |     -3.4% |          0/3 |
| doohtml-dp      | activity  |  0.5047 |       0.5007 |      0.8% |          2/3 |
| doohtml-dp      | log       |  0.3807 |       0.3813 |     -0.2% |          1/3 |
| doohtml-dp      | poll      |  0.0953 |       0.0950 |      0.3% |          1/3 |
| doohtml-dp      | status    |  0.1020 |       0.1003 |      1.6% |          2/3 |
| doohtml-dp      | select    |  0.0957 |       0.0957 |      0.0% |          1/3 |
| doohtml-dp      | toggle    |  0.0930 |       0.0930 |      0.0% |          1/3 |
| doohtml-dp      | event     |  0.1643 |       0.1637 |      0.4% |          2/3 |
| doohtml-dp      | burst     |  0.2077 |       0.2073 |      0.2% |          2/3 |
| doohtml-dp      | logline   |  0.2057 |       0.2077 |     -1.0% |          1/3 |

Team and Log remounts improve in all three pairs for both entries. Event updates
are 16.8–18.1% lower and bursts 11.3–13.3% lower, also improving in all pairs.
Activity mounting is mixed, and Checklist is mixed for signals. State log-line
updates are 3.2% slower by the median; signals log-line updates are 3.0% lower.
The earlier three-pair candidate regressed both log-line medians. This is a
mount/update tradeoff and limited sampling, not an across-the-board speedup.
Controls vary too; small changes need stronger evidence. These script profiles
do not establish a full-framework ranking. Use the separate five-iteration run
and its identical-build control for that comparison.

## Rejected experiments

- Flat whole-row functions lost the compiler's specialized element-list path:
  Checklist and Activity replacement became about 65–90% slower in the initial
  diagnostic (`top2-flat-rows`). The experiment was reverted.
- Mapping every log line into a new object with a precomputed class increased
  update time by about 6% (`top2-projections`, `top2-updates-projections`).
- A visibility guard on outgoing tab projections added dependencies and API
  complexity without a clear gain (`top2-visible`, `top2-updates-visible`).

- Child components inside native rows still compiled to owned slots. The
  experiment (`top2-components`) did not remove the expected lifecycle work.
- Reusing a mutable keyed index added retained entry records and a generation
  sweep without consistent timing gains (`top2-reused-index`); it was reverted.

The [first candidate's paired profiles](TOP2-FIRST-PROFILING.md) and
[completed benchmark](TOP2-FIRST-RESULTS.md) remain available. That benchmark
ranked state third and signals fifth, so the target was not met. The final
bridge simplification is measured independently; no samples were discarded.

Baseline discovery runs (`top2-baseline`, `top2-updates-base`) and all experiment
outputs remain in ignored `results/profiles/`; none is substituted for the final
paired results above. Initial exploratory runs do not establish causal timings.

## Validation

`make web-check`: 351 tests, type/format checks and ownership/SDK guards pass.
`make web-e2e`: 316 production browser checks against the mock API pass.
`make build`: the embedded web application and Go binary build successfully.
The final full benchmark is in [TOP2-RESULTS.md](TOP2-RESULTS.md).
