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
- The DOM under `#main` must match `apps/herdsman-patch` exactly in the canonical form
  `window.__bench.canon()` gives after every operation. The canonical form
  ignores `data-key`, empty `class` attributes, comments, blank text, the
  order of attributes and classes, and how text is split into text nodes.
  Everything else counts: tags, nesting, attributes, text.
- A tab switch replaces the tab's view; the lanes, cards and items of one
  tab never morph into another tab's elements.

The reference markup is `apps/herdsman-patch/main.ts`. In short:

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

## Reactre core + state

`apps/reactre-state` is one application combining the current vendored
`@reactre/core` and `@reactre/state`, with snapshots owned by a compiled actor
and core's native keyed rendering. See its [source and reproduction notes](apps/reactre-state/README.md).
It uses the same driver, data, canonical DOM gate, workloads and A/A control.
Measured comparisons and their limitations are recorded in
[PR #122](https://github.com/arafathusayn/herdsman/pull/122); a speed ranking is
only claimed when supported by the complete run and its noise floor.

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

## SVG and typed-hole check in Chrome

`bun check-svg.ts` (or `bun run check-svg`) bundles `check-svg.page.ts` and
runs it in headless Chrome: `svg` templates and views get the SVG namespace,
SVG views render and reorder inside an HTML view's `<svg>`, `class` on an SVG
element is an attribute (a class marker there fails when the template
compiles), and typed `on()` and `prop()` holes work on real elements. The
unit tests' fake DOM only imitates namespaces; this runs the browser's own
parser. It exits non-zero when a check fails.

Both `run.ts` and `check-svg.ts` launch the Chrome named by `CHROME`, else
the first one found in the usual macOS and Linux install places or on
`PATH` (`chrome.ts`).
