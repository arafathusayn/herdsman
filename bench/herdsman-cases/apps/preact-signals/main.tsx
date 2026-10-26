// Preact 11 with @preact/signals, as the official js-framework-benchmark
// preact-signals entry is written: every row is a store of per-field signals,
// rows bind those signals straight to DOM props (textContent, class, title,
// aria-label) so a field change writes one DOM property without re-rendering,
// and computeds derive the class strings (a card's "selected" reads one shared
// signal, as the official Row's class reads `selected`).
//
// Each snapshot is mapped into those stores by key: a row's signals are written
// only when a field differs, and a list signal is replaced only when its row
// sequence differs. Only the lists the current tab shows are mapped; the others
// have no subscribers and are mapped when their tab opens. Lists render cached
// per-row vnodes (signals/utils' For, without its per-item index signal, which
// would re-render every row whose index shifts on a prepend), so Preact skips
// unchanged rows by identity.
//
// Preact debounces component re-renders (a microtask) and @preact/signals
// defers prop writes (two microtasks). Both go through options hooks, which
// this app captures and flushes at the end of render(), so each click's whole
// update lands within the click, as React's flushSync does for apps/react.

import { batch, computed, type ReadonlySignal, type Signal, signal, useComputed } from "@preact/signals";
import { type ComponentChildren, options, render, type VNode } from "preact";
import { start } from "../../shared/driver.ts";
import {
  type Check,
  checkKey,
  type Event,
  eventKey,
  type Line,
  lineKey,
  type Member,
  type Role,
  type State,
  type Status,
  statusLabel,
  statusOrder,
  type Tab,
} from "../../shared/state.ts";

// --- synchronous flush of Preact's render queue and the signals DOM queue

let rerender: (() => void) | null = null;
const domWork: (() => void)[] = [];

function flush(): void {
  for (;;) {
    if (rerender) {
      const run = rerender;
      rerender = null;
      run();
    } else if (domWork.length) domWork.shift()!();
    else return;
  }
}

options.debounceRendering = (cb) => {
  rerender = cb;
  queueMicrotask(flush);
};
(options as { requestAnimationFrame?: (cb: () => void) => void }).requestAnimationFrame = (cb) => {
  domWork.push(cb);
  queueMicrotask(flush);
};

// --- stores

interface CardRow {
  pane: string;
  title: Signal<string>;
  role: Signal<Role>;
  status: Signal<Status>;
  task: Signal<string | null>;
  sub: Signal<string>;
  tab: Signal<string>;
  term: Signal<string>;
  v?: VNode;
}

interface LaneRow {
  status: Status;
  title: string;
  members: Signal<CardRow[]>;
  count: ReadonlySignal<number>;
  v?: VNode;
}

/** Fields outside the key (at, kind, pane, status, text) may change; the rest is fixed by the key. */
interface EventRow {
  key: string;
  at: string;
  status: Status | null;
  text: string | null;
  clock: Signal<string>;
  source: Signal<Event["source"]>;
  label: Signal<string>;
  who: Signal<string | null>;
  v?: VNode;
}

/** id is part of the key (and text is too when id is empty), so only text, done and skipped change. */
interface CheckRow {
  key: string;
  id: string;
  text: Signal<string>;
  done: Signal<boolean>;
  skipped: Signal<boolean>;
  v?: VNode;
}

/** A log line is all key. */
interface LineRow {
  key: string;
  time: string;
  text: string;
  v?: VNode;
}

const tab = signal<Tab>("team");
const selected = signal<string | null>(null);

let cards = new Map<string, CardRow>();
const steady = new Set<Status>(["working", "blocked", "idle", "done"]);
const laneRows = statusOrder.map((status): LaneRow => {
  const members = signal<CardRow[]>([]);
  return { status, title: statusLabel[status], members, count: computed(() => members.value.length) };
});
const shownLanes = signal<LaneRow[]>([]);

let eventRows = new Map<string, EventRow>();
const events = signal<EventRow[]>([]);
let checkRows = new Map<string, CheckRow>();
const checks = signal<CheckRow[]>([]);
let lineRows = new Map<string, LineRow>();
const lines = signal<LineRow[]>([]);

/** Replaces a list signal's value only when its row sequence differs. */
function setList<T>(list: Signal<T[]>, next: T[]): void {
  const prev = list.peek();
  if (prev.length === next.length) {
    let i = 0;
    while (i < next.length && prev[i] === next[i]) i++;
    if (i === next.length) return;
  }
  list.value = next;
}

// Signals skip writes of an equal value, so a field's signal changes only when the field does.

function syncMembers(members: Member[]): void {
  const next = new Map<string, CardRow>();
  for (const m of members) {
    let r = cards.get(m.pane);
    if (r) {
      r.title.value = m.title;
      r.role.value = m.role;
      r.status.value = m.status;
      r.task.value = m.task;
      r.sub.value = m.sub;
      r.tab.value = m.tab;
      r.term.value = m.term;
    } else {
      r = {
        pane: m.pane,
        title: signal(m.title),
        role: signal(m.role),
        status: signal(m.status),
        task: signal(m.task),
        sub: signal(m.sub),
        tab: signal(m.tab),
        term: signal(m.term),
      };
    }
    next.set(m.pane, r);
  }
  cards = next;
  const shown: LaneRow[] = [];
  for (const lane of laneRows) {
    const rows: CardRow[] = [];
    for (const m of members) if (m.status === lane.status) rows.push(next.get(m.pane)!);
    setList(lane.members, rows);
    if (rows.length || steady.has(lane.status)) shown.push(lane);
  }
  setList(shownLanes, shown);
}

function syncEvents(list: Event[]): void {
  const next = new Map<string, EventRow>();
  const rows = list.map((e) => {
    const key = eventKey(e);
    let r = eventRows.get(key);
    if (r) {
      r.clock.value = e.clock;
      r.source.value = e.source;
      r.label.value = e.label;
      r.who.value = e.who;
    } else {
      r = {
        key,
        at: e.at,
        status: e.status,
        text: e.text,
        clock: signal(e.clock),
        source: signal(e.source),
        label: signal(e.label),
        who: signal(e.who),
      };
    }
    next.set(key, r);
    return r;
  });
  eventRows = next;
  setList(events, rows);
}

function syncChecks(list: Check[]): void {
  const next = new Map<string, CheckRow>();
  const rows = list.map((c) => {
    const key = checkKey(c);
    let r = checkRows.get(key);
    if (r) {
      r.text.value = c.text;
      r.done.value = c.done;
      r.skipped.value = c.skipped;
    } else {
      r = { key, id: c.id, text: signal(c.text), done: signal(c.done), skipped: signal(c.skipped) };
    }
    next.set(key, r);
    return r;
  });
  checkRows = next;
  setList(checks, rows);
}

function syncLines(list: Line[]): void {
  const next = new Map<string, LineRow>();
  const rows = list.map((l) => {
    const key = lineKey(l);
    const r = lineRows.get(key) ?? { key, time: l.time, text: l.text };
    next.set(key, r);
    return r;
  });
  lineRows = next;
  setList(lines, rows);
}

// --- views

let onSelect: (pane: string) => void = () => {};

/** One cached vnode per row object, so Preact skips an unchanged row by identity. */
function each<T extends { v?: VNode }>(list: T[], view: (row: T) => VNode): ComponentChildren {
  return list.map((r) => (r.v ??= view(r)));
}

function Pill({ status }: { status: Status }) {
  return (
    <span class={`pill s-${status}`}>
      <span class="dot" aria-hidden="true" />
      {statusLabel[status]}
    </span>
  );
}

function LivePill({ status }: { status: Signal<Status> }) {
  const cls = useComputed(() => `pill s-${status.value}`);
  const label = useComputed(() => statusLabel[status.value]);
  return (
    <span class={cls}>
      <span class="dot" aria-hidden="true" />
      {label}
    </span>
  );
}

function Card({ m }: { m: CardRow }) {
  const cls = useComputed(() => `card r-${m.role.value} s-${m.status.value}${selected.value === m.pane ? " selected" : ""}`);
  const title = useComputed(() => `Open ${m.title.value} (${m.pane})`);
  const hasTask = useComputed(() => m.task.value !== null);
  return (
    <button class={cls} title={title} data-pane={m.pane} onClick={() => onSelect(m.pane)}>
      <div class="card-top">
        <span class="card-title" textContent={m.title} />
        <LivePill status={m.status} />
      </div>
      {hasTask.value && <div class="card-task" textContent={m.task} />}
      <div class="card-sub" textContent={m.sub} />
      <div class="card-foot">
        <code>{m.pane}</code>
        <span class="muted" textContent={m.tab} />
        <span class="muted clip" title={m.term} textContent={m.term} />
      </div>
    </button>
  );
}

function LaneCards({ lane }: { lane: LaneRow }) {
  const rows = lane.members.value;
  return rows.length ? each(rows, (m) => <Card key={m.pane} m={m} />) : <p class="lane-empty">None</p>;
}

function LaneView({ lane }: { lane: LaneRow }) {
  return (
    <section class={`lane s-${lane.status}`} aria-label={lane.title}>
      <h2>
        <span class="lane-dot" aria-hidden="true" />
        {lane.title}
        <span class="count" textContent={lane.count} />
      </h2>
      <LaneCards lane={lane} />
    </section>
  );
}

function Team() {
  return (
    <div class="team">
      <div class="kanban">{each(shownLanes.value, (lane) => <LaneView key={lane.status} lane={lane} />)}</div>
    </div>
  );
}

function EventItem({ e }: { e: EventRow }) {
  const web = useComputed(() => e.source.value === "web");
  const hasWho = useComputed(() => e.who.value !== null);
  const strong = hasWho.value ? <strong textContent={e.who} /> : null;
  return (
    <li class={e.source}>
      <time datetime={e.at} textContent={e.clock} />
      <p class="event">
        {web.value ? (
          <>
            <span class="chip accent">web</span>
            <span>{e.text ?? ""}</span>
          </>
        ) : e.status ? (
          <>
            {strong}
            <Pill status={e.status} />
          </>
        ) : (
          <>
            <span class="chip" textContent={e.label} />
            {strong}
          </>
        )}
      </p>
    </li>
  );
}

function Activity() {
  return <ol class="timeline activity">{each(events.value, (e) => <EventItem key={e.key} e={e} />)}</ol>;
}

function CheckItem({ c }: { c: CheckRow }) {
  const cls = useComputed(() => (c.done.value ? "done" : c.skipped.value ? "skipped" : ""));
  const state = useComputed(() => (c.done.value ? "Done" : c.skipped.value ? "Not applicable" : "Open"));
  const mark = useComputed(() => (c.done.value ? "✓" : c.skipped.value ? "–" : ""));
  return (
    <li class={cls}>
      <span class="box" aria-label={state} textContent={mark} />
      {c.id ? <code>{c.id}</code> : null}
      <span textContent={c.text} />
    </li>
  );
}

function Checklist() {
  return <ul class="checklist">{each(checks.value, (c) => <CheckItem key={c.key} c={c} />)}</ul>;
}

function LineItem({ l }: { l: LineRow }) {
  return (
    <li class={l.text.startsWith("web:") ? "web" : ""}>
      <time>{l.time}</time>
      <p>{l.text}</p>
    </li>
  );
}

function Log() {
  return <ol class="timeline">{each(lines.value, (l) => <LineItem key={l.key} l={l} />)}</ol>;
}

function Main() {
  // Keyed by tab, as herdsman's renderMain, so a tab switch replaces the view.
  const t = tab.value;
  return t === "team" ? (
    <Team key="team" />
  ) : t === "activity" ? (
    <Activity key="activity" />
  ) : t === "checklist" ? (
    <Checklist key="checklist" />
  ) : (
    <Log key="log" />
  );
}

start((root, select) => {
  onSelect = select;
  let mounted = false;
  return {
    render(s: State) {
      batch(() => {
        // Only the shown tab's lists are mapped; a hidden list has no subscribers.
        if (s.tab === "team") syncMembers(s.members);
        else if (s.tab === "activity") syncEvents(s.events);
        else if (s.tab === "checklist") syncChecks(s.checklist);
        else syncLines(s.log);
        selected.value = s.selected;
        tab.value = s.tab;
      });
      if (!mounted) {
        mounted = true;
        render(<Main />, root);
      }
      flush();
    },
  };
});
