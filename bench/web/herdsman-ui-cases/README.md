# herdsman-cases benchmark

Measures DOM libraries on the screens herdsman web renders, not on the
js-framework-benchmark table of 1,000 rows. Each contender is one app in
`apps/<name>/` that renders the same four route tabs from the same data:

| Tab       | Content                       | Size       |
| --------- | ----------------------------- | ---------- |
| Team      | status lanes of agent cards   | 32 cards   |
| Activity  | event feed, newest first      | 200 events |
| Checklist | route checklist lines         | 80 lines   |
| Log       | run-state lines, newest first | 300 lines  |

## The contract

`shared/state.ts` holds the data, the state type and the operations.
`shared/driver.ts` holds the page shell. An app's entry calls
`start(createApp)`; `createApp(root, onSelect)` returns `{ render(state) }`.

- Every toolbar click hands `render` a **fresh snapshot**: equal values, new
  objects, as a poll parses fresh JSON. The app renders from that snapshot
  alone. It must not learn which button was clicked, and it must not compare
  whole snapshots to skip work (for example by `JSON.stringify` equality).
  Diffing per keyed item, the way the library does it idiomatically, is the
  point of the benchmark and is allowed.
- Lists are keyed: cards by `pane`, events by `eventKey`, checklist lines by
  `checkKey`, log lines by `lineKey`.
- A card's click calls `onSelect(m.pane)` through a handler on that card (or
  the library's own event delegation).
- The DOM under `#main` must match `apps/vanillajs-lite` exactly in the canonical form
  `window.__bench.canon()` gives after every operation. The canonical form
  ignores `data-key`, empty `class` attributes, comments, blank text, the
  order of attributes and classes, and how text is split into text nodes.
  Everything else counts: tags, nesting, attributes, text.
- A tab switch replaces the tab's view; the lanes, cards and items of one
  tab never morph into another tab's elements.

The reference markup is `apps/vanillajs-lite/main.ts`. In short:

```html
<div class="team">
  <div class="kanban">
    <section class="lane s-{status}" aria-label="{Lane title}">
      <h2>
        <span class="lane-dot" aria-hidden="true"></span>{Lane title}<span
          class="count"
          >{n}</span
        >
      </h2>
      <button
        class="card r-{role} s-{status}[ selected]"
        title="Open {title} ({pane})"
        data-pane="{pane}"
      >
        <div class="card-top">
          <span class="card-title">{title}</span>{pill}
        </div>
        <div class="card-task">{task}</div>
        <!-- only when task !== null -->
        <div class="card-sub">{sub}</div>
        <div class="card-foot">
          <code>{pane}</code><span class="muted">{tab}</span
          ><span class="muted clip" title="{term}">{term}</span>
        </div>
      </button>
      <!-- or, for a lane with no members: <p class="lane-empty">None</p> -->
    </section>
  </div>
</div>

<!-- pill -->
<span class="pill s-{status}"
  ><span class="dot" aria-hidden="true"></span>{statusLabel}</span
>

<ol class="timeline activity">
  <li class="{source}">
    <time datetime="{at}">{clock}</time>
    <p class="event">{body}</p>
  </li>
</ol>
<!-- body: source "web": <span class="chip accent">web</span><span>{text}</span>
           status set:   <strong>{who}</strong>{pill}
           otherwise:    <span class="chip">{label}</span><strong>{who}</strong>  (strong only when who !== null) -->

<ul class="checklist">
  <li class="done|skipped|">
    <!-- done wins over skipped -->
    <span class="box" aria-label="Done|Not applicable|Open">✓|–|(empty)</span>
    <code>{id}</code>
    <!-- only when id is not empty -->
    <span>{text}</span>
  </li>
</ul>

<ol class="timeline">
  <li class="web|">
    <time>{time}</time>
    <p>{text}</p>
  </li>
  <!-- "web" when text starts with "web:" -->
</ol>
```

Lanes come from `lanes(members)` in `shared/state.ts`.

## Reactre core + built-in signals

`apps/reactre-signals` uses `@reactre/core` and its built-in `signal`, from
upstream revision `f0cb6cb9f7d9c9a96c25fb25ece2d54adb702c27`. See its
[source and reproduction notes](apps/reactre-signals/README.md).
Each fresh snapshot is written to a signal. It shares the exact view components,
keyed projections and core compiler with the combined core + state entry below.
Only the state owner and subscription bridge differ; this entry has no state
machine runtime. Herdsman's production UI retains actor-owned state.

## Reactre core + state

`apps/reactre-state` is one application combining the current vendored
`@reactre/core` and `@reactre/state`, with snapshots owned by a compiled actor
and core's native keyed rendering. See its [source and reproduction notes](apps/reactre-state/README.md).
It uses the same driver, data, canonical DOM gate, workloads and A/A control.
Measured comparisons and their limitations are recorded in
[PR #122](https://github.com/arafathusayn/herdsman/pull/122); a speed ranking is
only claimed when supported by the complete run and its noise floor.

## Recorded five-iteration comparison

Measured on **2026-11-14 (Asia/Dhaka)** from source
`2c968c99dd801d30f51cbe4f0caa87df6b0c6cdc`, with Reactre pinned to
`f0cb6cb9f7d9c9a96c25fb25ece2d54adb702c27`. This is the final shared-renderer candidate.
Chrome 157.0.8103.78, puppeteer-core 25.16.0, and unchanged 4× CPU
throttling where marked. All **15 implementations plus the identical
vanillajs-lite-control**, **12 workloads**, and **5 iterations (960 samples)**
completed. All 16 correctness gates and 192 retained-node checks passed;
there were no invalid entries, timing errors or discarded samples. The load
safeguard remained enabled. The control is excluded from ranks.

| Rank | Implementation                  | Geometric mean of relative median times ↓ | JavaScript gzip bytes |
| ---: | ------------------------------- | ----------------------------------------: | --------------------: |
|    1 | Reactre core + built-in signals |                                  1.061728 |                10,552 |
|    2 | vanillajs                       |                                  1.077724 |                 4,607 |
|    3 | Reactre core + state            |                                  1.084408 |                11,155 |
|    4 | vanillajs-lite                  |                                  1.100668 |                 4,831 |
|    5 | sonnet                          |                                  1.123512 |                 4,659 |
|    6 | preact                          |                                  1.133704 |                 7,494 |
|    7 | doohtml-dp                      |                                  1.136785 |                 5,812 |
|    8 | thane                           |                                  1.172550 |                 8,063 |
|    9 | re                              |                                  1.177906 |                 7,260 |
|   10 | solid                           |                                  1.195415 |                10,037 |
|   11 | vue                             |                                  1.212230 |                25,743 |
|   12 | vue-vapor                       |                                  1.221141 |                32,021 |
|   13 | preact-signals                  |                                  1.273999 |                11,792 |
|   14 | solid-2                         |                                  1.324648 |                27,527 |
|   15 | react                           |                                  1.476775 |                69,710 |

Core + signals ranks **1/15** and core + state **3/15**. Their composite times
are **9.86%** and **7.94%** lower than `re` in this run.
**The target of both Reactre entries in the top two was not met.**

The identical-build A/A gap is **3.995% (4.00% rounded)**. State needs only a
0.62% reduction to match second place, well below this control gap. The top
four scores span just 3.67%; the run does not establish a repeatable ordering
among them, or prove that signals is the fastest. Do not label state second
or treat a small ranking change as a demonstrated gain.

[Full workload table, retained nodes, sizes and raw provenance](TOP2-RESULTS.md).
[Three paired CPU-profile comparisons, including regressions](TOP2-PROFILING.md).
The [first candidate's complete run](TOP2-FIRST-RESULTS.md), which ranked state
third and signals fifth, is retained separately. No timing samples were removed
and the final code was measured once; there was no rerun to obtain a better rank.

Raw output remains under ignored `results/2026-11-13T23-46-45-189Z.json` and `.md`.
Reproduce with `bun build.ts`, then `bun run.ts --iterations 5` at the source
revision above. Competitors, workloads, gates, throttling and load safeguards
were unchanged. A full 16-iteration timing-position rotation would be a stronger
follow-up to this noisy five-iteration ranking.

The optimizations retain native keyed rows, compare typed fields, reuse derived
lane arrays and event keys, remove per-log-line observers, and remove two
redundant snapshot memos per entry. They preserve every actor transition and
view disposal. Gzip sizes increase by 83 bytes for signals and 82 for state
relative to the vendor-only revision. Paired diagnostics show consistent gains
in Team mounting and event/burst propagation, with mixed results elsewhere.
They measure synchronous script work, not the benchmark's commit-time metric.
Independently normalized scores from separate runs are not a causal before/after
comparison, and microbenchmark improvements do not guarantee a framework rank.

## Previous five-iteration comparison (vendor update)

Measured on **2026-11-14 (Asia/Dhaka)** from source
`1da043c334b32a928399f0733585ddd5249db475`, with Reactre pinned to
`f0cb6cb9f7d9c9a96c25fb25ece2d54adb702c27` (upstream PRs #2–#6).
Chrome 157.0.8103.78, puppeteer-core 25.16.0, and the unchanged
4× CPU throttling where marked. All **15 implementations plus the identical
vanillajs-lite-control**, **12 workloads**, and **5 iterations (960 samples)**
completed. All 16 correctness gates and 192 retained-node checks passed;
there were no invalid entries, timing errors or discarded samples.
The control is excluded from ranks.

| Rank | Implementation                  | Geometric mean of relative median times ↓ | JavaScript gzip bytes |
| ---: | ------------------------------- | ----------------------------------------: | --------------------: |
|    1 | vanillajs-lite                  |                                  1.039537 |                 4,831 |
|    2 | vanillajs                       |                                  1.047349 |                 4,607 |
|    3 | Reactre core + state            |                                  1.073993 |                11,073 |
|    4 | Reactre core + built-in signals |                                  1.076882 |                10,469 |
|    5 | doohtml-dp                      |                                  1.101990 |                 5,812 |
|    6 | re                              |                                  1.124763 |                 7,260 |
|    7 | preact                          |                                  1.135094 |                 7,494 |
|    8 | sonnet                          |                                  1.142788 |                 4,659 |
|    9 | solid                           |                                  1.159203 |                10,037 |
|   10 | vue-vapor                       |                                  1.174451 |                32,021 |
|   11 | thane                           |                                  1.185160 |                 8,063 |
|   12 | vue                             |                                  1.200701 |                25,743 |
|   13 | preact-signals                  |                                  1.242008 |                11,792 |
|   14 | solid-2                         |                                  1.301736 |                27,527 |
|   15 | react                           |                                  1.445297 |                69,710 |

Core + state ranks **3/15** and core + signals **4/15**. Their composite times
are **4.51%** and **4.26%** lower than `re` in this run.
**The signals top-two target was not met; the state top-five target was met.**

The identical-build A/A control gap is **0.833%**. State is 2.54% behind
second place and signals 2.82%. The 0.27% gap between the Reactre entries
is below the control gap and does not establish a repeatable ordering.
Five samples per workload on a shared workstation remain limited evidence;
a complete 16-iteration timing-position rotation is a stronger follow-up.

[Full workload results, retained nodes, sizes and provenance](REACTRE6-RESULTS.md).
Raw output remains under ignored `results/2026-11-13T20-19-15-609Z.json` and `.md`.
Reproduce with `bun build.ts`, then `bun run.ts --iterations 5` at the source
revision above. Competitors, workloads, gates, throttling and load safeguards
were unchanged; the runner briefly waited for system load to fall.

This update adopts upstream's last-reader allocation reduction and selector
comparator fix. Herdsman's actor bridges are unchanged. Signals and state gzip
sizes fall by 25 and 21 bytes respectively. This run is not a paired before/after
experiment: do not subtract independently normalized scores or attribute ranking
changes to the vendor update. Upstream microbenchmarks are not Herdsman latency
or ranking guarantees.

## Previous five-iteration comparison

Measured on **2026-11-14 (Asia/Dhaka)** from source
`c4eb85b300a220cc5cf99da7206f93052bd8a61c`, with Reactre pinned to
`63542a92765fbd401e37ce3b99fe47f429e2cd0f` (upstream PRs #2–#5).
Chrome 157.0.8103.78, puppeteer-core 25.16.0, and the unchanged
4× CPU throttling where marked. All **15 implementations plus the identical
vanillajs-lite-control**, **12 workloads**, and **5 iterations (960 samples)**
completed. All 16 correctness gates and 192 retained-node measurements passed;
there were no invalid entries or timing errors. The control is excluded from ranks.

| Rank | Implementation                  | Geometric mean of relative median times ↓ | JavaScript gzip bytes |
| ---: | ------------------------------- | ----------------------------------------: | --------------------: |
|    1 | vanillajs-lite                  |                                  1.041455 |                 4,831 |
|    2 | vanillajs                       |                                  1.068348 |                 4,607 |
|    3 | Reactre core + built-in signals |                                  1.073360 |                10,494 |
|    4 | Reactre core + state            |                                  1.075941 |                11,094 |
|    5 | sonnet                          |                                  1.115435 |                 4,659 |
|    6 | doohtml-dp                      |                                  1.126570 |                 5,812 |
|    7 | preact                          |                                  1.134750 |                 7,494 |
|    8 | re                              |                                  1.136765 |                 7,260 |
|    9 | vue-vapor                       |                                  1.158973 |                32,021 |
|   10 | vue                             |                                  1.164023 |                25,743 |
|   11 | solid                           |                                  1.169482 |                10,037 |
|   12 | thane                           |                                  1.172542 |                 8,063 |
|   13 | preact-signals                  |                                  1.212191 |                11,792 |
|   14 | solid-2                         |                                  1.304255 |                27,527 |
|   15 | react                           |                                  1.892181 |                69,710 |

Core + signals ranks **3/15** and core + state **4/15**. Their composite times
are **5.58%** and **5.35%** lower than `re` in this run.
**The signals top-two target was not met; the state top-five target was met.**

The identical-build A/A control gap is **1.357%**. Signals is
0.47% behind second place; state is 0.71% behind it. Both gaps, and the
0.24% gap between the Reactre entries, are inside that control gap. These
close ranks do not establish a repeatable speed advantage. Five samples per
workload on a shared workstation remain limited evidence; the complete
16-iteration timing-position rotation is a stronger follow-up.

[Full workload results, retained nodes, sizes and provenance](UPSTREAM-RESULTS.md).
Raw output remains under the ignored `results/2026-11-13T19-33-30-130Z.json`
and `.md`, following this suite's convention. Reproduce with `bun build.ts`
then `bun run.ts --iterations 5` at the source revision above. Workloads,
competitor pins, gates, throttling and load safeguards were unchanged.

The production app now uses immutable JSON payloads, but the benchmark already
used directly typed payloads without string conversion. The compiler adapter
now uses the public analysis host API; it already restored whole-program facts
before this update. Both Reactre gzip sizes are unchanged. Do not subtract
normalized scores across the historical and current runs or attribute their
difference to the upstream changes. Upstream synthetic results are not Herdsman
latency or ranking guarantees.

## Historical three-iteration comparison

Measured on 2026-11-13 from benchmark source `463942782f4f50bcf0ab8fae00014d9a5904cfde`,
with Chrome 157.0.8103.78, puppeteer-core 25.16.0 and the unchanged
4× CPU throttling where marked. The complete run includes all **15 implementations
plus the identical `vanillajs-lite-control`**, 12 workloads and **3 iterations
(576 samples)**. All 16 correctness gates and 192 retained-node measurements
completed; there were no invalid entries or timing errors. The control is excluded
from ranks.

| Rank | Implementation                  | Geometric mean of relative median times ↓ | JavaScript gzip bytes |
| ---: | ------------------------------- | ----------------------------------------: | --------------------: |
|    1 | vanillajs-lite                  |                                  1.055304 |                 4,831 |
|    2 | vanillajs                       |                                  1.062771 |                 4,607 |
|    3 | Reactre core + built-in signals |                                  1.069389 |                10,494 |
|    4 | Reactre core + state            |                                  1.112858 |                11,094 |
|    5 | sonnet                          |                                  1.122745 |                 4,659 |
|    6 | preact                          |                                  1.128419 |                 7,494 |
|    7 | doohtml-dp                      |                                  1.136027 |                 5,812 |
|    8 | re                              |                                  1.148274 |                 7,260 |
|    9 | vue-vapor                       |                                  1.155847 |                32,021 |
|   10 | solid                           |                                  1.206954 |                10,037 |
|   11 | vue                             |                                  1.207533 |                25,743 |
|   12 | thane                           |                                  1.213469 |                 8,063 |
|   13 | preact-signals                  |                                  1.274285 |                11,792 |
|   14 | solid-2                         |                                  1.310989 |                27,527 |
|   15 | react                           |                                  1.926441 |                69,710 |

Core + built-in signals ranks **3 of 15**; core + state ranks **4 of 15**.
Reactre core + built-in signals measured **6.87% lower composite time than `re`**. Reactre core + state measured **3.08% lower composite time than `re`**.
**The signals top-two target was not met; the state top-five target was met in this run.**
Signals is 0.62% behind second-place `vanillajs`, inside the control gap below.

The identical-build A/A control gap is **2.795%**. Differences inside that gap
do not establish a speed advantage. Three samples per workload on a shared
workstation are a small, noisy comparison, not a robust or universal ordering.
This run includes a wait for workstation load to fall below the runner's existing
limit; that safeguard and all workloads remain unchanged.
Two React selection samples were about 151–153 ms and affect its score; no
samples were discarded. Inspect the full workload table before generalizing.

The [mounting and cleanup profile](PROFILING.md) explains the restored compiler
analysis, scalar text holes, tab disposal and deferred projection indexes. It
also records both intermediate three-iteration runs, including the run with a
4.603% control gap. Scores are normalized within each run and must not be
subtracted across runs to claim a speedup. The earlier historical 19-entry run
ranked signals/state seventh/tenth before the requested removals; this table is
a fresh measurement of the remaining field.

The [full results in PR #122](https://github.com/arafathusayn/herdsman/pull/122)
include each workload, retained-node count and bundle size. Raw local output is
`results/2026-11-13T16-37-12-579Z.json` and
`results/2026-11-13T16-37-12-579Z.md`, ignored as usual. Reproduce this
run with `bun build.ts` then `bun run.ts --iterations 3` at source `46394278`.
For a complete rotation through every entry's timing position, use
`bun run.ts --iterations 16`. A full build clears stale generated entries.
That historical run used source pin `8bae29b9fce00ab1e192f906e15191c6057550d7`,
before the upstream adoption recorded above. The state-machine safety argument
below is independent of these timings.

### Safety as well as speed

For Herdsman's state-management needs, we consider **Reactre core + state
the safest implementation choice in this suite**. This is an architectural
judgment based on its explicit contracts and enforced checks, not a safety
score produced by these DOM timings:

- **State machines own the data.** Every snapshot enters a declared event
  and transition; components derive their view from the actor. The benchmark
  uses a small `empty` → `showing` machine. The production application uses
  [cohesive workflow statecharts](../../../docs/adr/0036-actor-owned-web-state.md)
  for requests, retries, cancellation, pairing and route setup.
- **Strong types check the machine graph and contracts.** State-specific
  data narrows with the current state. Invalid targets, wrong event payloads,
  missing or extra state data and unreachable states fail type checking.
- **The state compiler checks common mistakes types cannot see.** It rejects
  impure transitions, casts or suppressions that conceal broken contracts,
  and unsafe task patterns such as fetching without the task's abort signal
  or reading a response body without checking its status. These are build
  checks, not extra work added to each timed DOM update.
- **Lifecycle follows ownership.** The `useActor` bridge stops its actor
  when its view is disposed. Production workflows use state-scoped tasks
  and cancellation to prevent late results from replacing current data.

The pinned upstream [state documentation and negative-test evidence](../../../web/vendor/reactre/packages/state/README.md)
describe the checks and their limits. This benchmark's simple synchronous
machine does not exercise all those capabilities, and its production build
does not include development-only warnings. Type and compiler checks do not
prove business logic correct or validate arbitrary external data by themselves.

Our performance target is to beat `re` across the complete workload, while
retaining these guarantees. `vanillajs-lite` may remain faster; speed alone
does not provide these state-machine contracts. Neither a target nor a
safety preference substitutes for a measured result, and this suite does
not establish that other libraries cannot be combined with similar checks.

## Operations

`board` builds a new world and renders the Team tab (creation). The others
change the world and render a fresh snapshot: `poll` (no change), `status`
(one card moves between the Working and Idle lanes), `select` (one card gains
`selected`, the last one loses it), `team`, `activity`, `checklist`, `log`
(tab switches), `event` (one new event at the top, the oldest drops),
`burst` (20 new events), `toggle` (checklist line 41 flips done),
`logline` (one new log line at the top).

## Browser selection

`run.ts` launches the Chrome named by `CHROME`, else the first one found in
the usual macOS and Linux install places or on `PATH` (`chrome.ts`).
