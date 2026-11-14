# First candidate: mounting and propagation

Baseline: `fbd7b60dbc6a7e88b9cfe886718e2e2780732594`.
Candidate: `8e28bc1628864a377deb4622fe23673bb8fc9671`.
Both use Reactre `f0cb6cb9f7d9c9a96c25fb25ece2d54adb702c27`.

## Method

Three paired diagnostic runs alternate baseline/candidate, candidate/baseline,
then baseline/candidate. Each builds named, syntax-minified bundles with the
production compiler and runtime. Run `bun profile-reactre.ts LABEL all` after
`bun build.ts`; swap only `shared/reactre/view.tsx` and `rows.ts` to the source
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
[TOP2-FIRST-PROFILING.json](TOP2-FIRST-PROFILING.json). Full named bundles and CPU traces
remain under ignored `results/profiles/top2-pair-{1,2,3}-{base,candidate}/`.

## Changes retained

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
| reactre-signals | team      |  0.3223 |       0.3033 |      5.9% |          3/3 |
| reactre-signals | checklist |  0.2247 |       0.2093 |      6.8% |          2/3 |
| reactre-signals | activity  |  0.4157 |       0.4167 |     -0.2% |          1/3 |
| reactre-signals | log       |  0.2677 |       0.2580 |      3.6% |          3/3 |
| reactre-signals | poll      |  0.0900 |       0.0863 |      4.1% |          3/3 |
| reactre-signals | status    |  0.1010 |       0.0957 |      5.3% |          3/3 |
| reactre-signals | select    |  0.0910 |       0.0873 |      4.0% |          3/3 |
| reactre-signals | toggle    |  0.0970 |       0.0900 |      7.2% |          3/3 |
| reactre-signals | event     |  0.2020 |       0.1667 |     17.5% |          3/3 |
| reactre-signals | burst     |  0.2487 |       0.2150 |     13.5% |          3/3 |
| reactre-signals | logline   |  0.1880 |       0.1953 |     -3.9% |          0/3 |
| reactre-state   | team      |  0.3223 |       0.2980 |      7.5% |          3/3 |
| reactre-state   | checklist |  0.2197 |       0.2223 |     -1.2% |          2/3 |
| reactre-state   | activity  |  0.4110 |       0.4213 |     -2.5% |          1/3 |
| reactre-state   | log       |  0.2687 |       0.2547 |      5.2% |          2/3 |
| reactre-state   | poll      |  0.0900 |       0.0880 |      2.2% |          3/3 |
| reactre-state   | status    |  0.1013 |       0.0977 |      3.6% |          3/3 |
| reactre-state   | select    |  0.0923 |       0.0900 |      2.5% |          3/3 |
| reactre-state   | toggle    |  0.0977 |       0.0913 |      6.5% |          3/3 |
| reactre-state   | event     |  0.1987 |       0.1677 |     15.6% |          3/3 |
| reactre-state   | burst     |  0.2460 |       0.2190 |     11.0% |          3/3 |
| reactre-state   | logline   |  0.1920 |       0.1983 |     -3.3% |          1/3 |
| vanillajs-lite  | team      |  0.1933 |       0.2037 |     -5.3% |          0/3 |
| vanillajs-lite  | checklist |  0.1457 |       0.1510 |     -3.7% |          1/3 |
| vanillajs-lite  | activity  |  0.2950 |       0.3067 |     -4.0% |          1/3 |
| vanillajs-lite  | log       |  0.2333 |       0.2333 |      0.0% |          2/3 |
| vanillajs-lite  | poll      |  0.0897 |       0.0900 |     -0.4% |          1/3 |
| vanillajs-lite  | status    |  0.0913 |       0.0923 |     -1.1% |          1/3 |
| vanillajs-lite  | select    |  0.0923 |       0.0913 |      1.1% |          2/3 |
| vanillajs-lite  | toggle    |  0.0847 |       0.0853 |     -0.8% |          1/3 |
| vanillajs-lite  | event     |  0.1533 |       0.1510 |      1.5% |          2/3 |
| vanillajs-lite  | burst     |  0.1793 |       0.1807 |     -0.7% |          1/3 |
| vanillajs-lite  | logline   |  0.1830 |       0.1817 |      0.7% |          2/3 |
| sonnet          | team      |  0.2927 |       0.2793 |      4.6% |          2/3 |
| sonnet          | checklist |  0.2393 |       0.2230 |      6.8% |          3/3 |
| sonnet          | activity  |  0.4430 |       0.4473 |     -1.0% |          2/3 |
| sonnet          | log       |  0.3820 |       0.3793 |      0.7% |          3/3 |
| sonnet          | poll      |  0.0973 |       0.0967 |      0.7% |          1/3 |
| sonnet          | status    |  0.1020 |       0.1007 |      1.3% |          3/3 |
| sonnet          | select    |  0.0967 |       0.0973 |     -0.7% |          1/3 |
| sonnet          | toggle    |  0.0947 |       0.0960 |     -1.4% |          1/3 |
| sonnet          | event     |  0.1647 |       0.1657 |     -0.6% |          0/3 |
| sonnet          | burst     |  0.2097 |       0.2090 |      0.3% |          1/3 |
| sonnet          | logline   |  0.2057 |       0.2070 |     -0.6% |          2/3 |
| doohtml-dp      | team      |  0.2603 |       0.2620 |     -0.6% |          0/3 |
| doohtml-dp      | checklist |  0.2137 |       0.2107 |      1.4% |          2/3 |
| doohtml-dp      | activity  |  0.5043 |       0.5063 |     -0.4% |          1/3 |
| doohtml-dp      | log       |  0.3900 |       0.3933 |     -0.9% |          1/3 |
| doohtml-dp      | poll      |  0.0957 |       0.0970 |     -1.4% |          2/3 |
| doohtml-dp      | status    |  0.1017 |       0.1020 |     -0.3% |          0/3 |
| doohtml-dp      | select    |  0.0970 |       0.0963 |      0.7% |          1/3 |
| doohtml-dp      | toggle    |  0.0943 |       0.0940 |      0.4% |          2/3 |
| doohtml-dp      | event     |  0.1673 |       0.1670 |      0.2% |          1/3 |
| doohtml-dp      | burst     |  0.2113 |       0.2120 |     -0.3% |          1/3 |
| doohtml-dp      | logline   |  0.2060 |       0.2090 |     -1.5% |          1/3 |

Team remounts and poll/status/select/toggle/event/burst updates improve in all
three pairs for both Reactre entries. Event updates are 15.6–17.5% lower and
bursts 11.0–13.5% lower in these diagnostics. Activity mounting is approximately
flat or slower; Checklist is mixed. Log mounting improves, while log-line updates
are 3.3–3.9% slower by the median. Removing per-line observers therefore has a
mount/update tradeoff, not an across-the-board speedup. Controls also vary.
These results do not establish a full-framework ranking; use the separate
five-iteration run and its identical-build control for that comparison.

## Rejected experiments

- Flat whole-row functions lost the compiler's specialized element-list path:
  Checklist and Activity replacement became about 65–90% slower in the initial
  diagnostic (`top2-flat-rows`). The experiment was reverted.
- Mapping every log line into a new object with a precomputed class increased
  update time by about 6% (`top2-projections`, `top2-updates-projections`).
- A visibility guard on outgoing tab projections added dependencies and API
  complexity without a clear gain (`top2-visible`, `top2-updates-visible`).

Baseline discovery runs (`top2-baseline`, `top2-updates-base`) and all experiment
outputs remain in ignored `results/profiles/`; none is substituted for the final
paired results above. Initial exploratory runs do not establish causal timings.

## Validation

`make web-check`: 351 tests, type/format checks and ownership/SDK guards pass.
`make web-e2e`: 316 production browser checks against the mock API pass.
`make build`: the embedded web application and Go binary build successfully.
The complete first five-iteration benchmark is in [TOP2-FIRST-RESULTS.md](TOP2-FIRST-RESULTS.md).
