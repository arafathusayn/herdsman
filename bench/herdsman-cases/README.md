# herdsman-cases benchmark

Measures DOM libraries on the screens herdsman web renders, not on the
js-framework-benchmark table of 1,000 rows. Each contender is one app in
`apps/<name>/` that renders the same four route tabs from the same data:

| Tab | Content | Size |
| --- | --- | --- |
| Team | status lanes of agent cards | 32 cards |
| Activity | event feed, newest first | 200 events |
| Checklist | route checklist lines | 80 lines |
| Log | run-state lines, newest first | 300 lines |

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
<div class="team"><div class="kanban">
  <section class="lane s-{status}" aria-label="{Lane title}">
    <h2><span class="lane-dot" aria-hidden="true"></span>{Lane title}<span class="count">{n}</span></h2>
    <button class="card r-{role} s-{status}[ selected]" title="Open {title} ({pane})" data-pane="{pane}">
      <div class="card-top"><span class="card-title">{title}</span>{pill}</div>
      <div class="card-task">{task}</div>            <!-- only when task !== null -->
      <div class="card-sub">{sub}</div>
      <div class="card-foot"><code>{pane}</code><span class="muted">{tab}</span><span class="muted clip" title="{term}">{term}</span></div>
    </button>
    <!-- or, for a lane with no members: <p class="lane-empty">None</p> -->
  </section>
</div></div>

<!-- pill -->
<span class="pill s-{status}"><span class="dot" aria-hidden="true"></span>{statusLabel}</span>

<ol class="timeline activity">
  <li class="{source}"><time datetime="{at}">{clock}</time><p class="event">{body}</p></li>
</ol>
<!-- body: source "web": <span class="chip accent">web</span><span>{text}</span>
           status set:   <strong>{who}</strong>{pill}
           otherwise:    <span class="chip">{label}</span><strong>{who}</strong>  (strong only when who !== null) -->

<ul class="checklist">
  <li class="done|skipped|">  <!-- done wins over skipped -->
    <span class="box" aria-label="Done|Not applicable|Open">✓|–|(empty)</span>
    <code>{id}</code>  <!-- only when id is not empty -->
    <span>{text}</span>
  </li>
</ul>

<ol class="timeline">
  <li class="web|"><time>{time}</time><p>{text}</p></li>  <!-- "web" when text starts with "web:" -->
</ol>
```

Lanes come from `lanes(members)` in `shared/state.ts`.

## Operations

`board` builds a new world and renders the Team tab (creation). The others
change the world and render a fresh snapshot: `poll` (no change), `status`
(one card moves between the Working and Idle lanes), `select` (one card gains
`selected`, the last one loses it), `team`, `activity`, `checklist`, `log`
(tab switches), `event` (one new event at the top, the oldest drops),
`burst` (20 new events), `toggle` (checklist line 41 flips done),
`logline` (one new log line at the top).
