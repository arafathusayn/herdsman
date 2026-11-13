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
upstream revision `8bae29b9fce00ab1e192f906e15191c6057550d7`. See its
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

## Recorded three-iteration comparison

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
The upstream source pin remains `8bae29b9fce00ab1e192f906e15191c6057550d7`,
verified against Reactre main on 2026-11-13. The state-machine safety argument
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
