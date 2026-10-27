// herdsman-blocks's lower layers used directly, as the library's author
// would for speed: html`` templates with typed holes (each row is a clone of a
// prototype already holding its values), Block.set for per-hole updates (a
// no-op when the value is unchanged), and List for keyed reconciliation. No
// fresh tree is built per render and nothing is morphed.

import { type Block, html, List, type ListOptions, type Template, attr, cls, on, text } from "../../../../web/src/herdsman-blocks.ts";
import { start } from "../../shared/driver.ts";
import {
  type Check,
  checkKey,
  type Event,
  eventKey,
  type Lane,
  lanes,
  type Line,
  lineKey,
  type Member,
  type State,
  type Status,
  statusLabel,
  type Tab,
} from "../../shared/state.ts";

type Click = ((this: HTMLElement) => void) | null;

// ---------------------------------------------------------------------------
// Templates. A row whose shape varies (an optional element) has one template
// per shape; holes are positional, so each shape lists its holes in order.
// ---------------------------------------------------------------------------

const CardTask = html`<button class=${cls()} title=${attr()} data-pane=${attr()} onclick=${on<"click">()}><div class="card-top"><span class="card-title">${text()}</span><span class=${cls()}><span class="dot" aria-hidden="true"></span>${text()}</span></div><div class="card-task">${text()}</div><div class="card-sub">${text()}</div><div class="card-foot"><code>${text()}</code><span class="muted">${text()}</span><span class="muted clip" title=${attr()}>${text()}</span></div></button>`;
const CardPlain = html`<button class=${cls()} title=${attr()} data-pane=${attr()} onclick=${on<"click">()}><div class="card-top"><span class="card-title">${text()}</span><span class=${cls()}><span class="dot" aria-hidden="true"></span>${text()}</span></div><div class="card-sub">${text()}</div><div class="card-foot"><code>${text()}</code><span class="muted">${text()}</span><span class="muted clip" title=${attr()}>${text()}</span></div></button>`;
const LaneT = html`<section class=${cls()} aria-label=${attr()}><h2><span class="lane-dot" aria-hidden="true"></span>${text()}<span class="count">${text<number>()}</span></h2></section>`;
const NoneT = html`<p class="lane-empty">None</p>`;
const Board = html`<div class="team"><div class="kanban"></div></div>`;

const EvWeb = html`<li class=${cls()}><time datetime=${attr()}>${text()}</time><p class="event"><span class="chip accent">web</span><span>${text()}</span></p></li>`;
const EvStatus = html`<li class=${cls()}><time datetime=${attr()}>${text()}</time><p class="event"><strong>${text()}</strong><span class=${cls()}><span class="dot" aria-hidden="true"></span>${text()}</span></p></li>`;
const EvStatusAnon = html`<li class=${cls()}><time datetime=${attr()}>${text()}</time><p class="event"><span class=${cls()}><span class="dot" aria-hidden="true"></span>${text()}</span></p></li>`;
const EvOther = html`<li class=${cls()}><time datetime=${attr()}>${text()}</time><p class="event"><span class="chip">${text()}</span><strong>${text()}</strong></p></li>`;
const EvOtherAnon = html`<li class=${cls()}><time datetime=${attr()}>${text()}</time><p class="event"><span class="chip">${text()}</span></p></li>`;
const Activity = html`<ol class="timeline activity"></ol>`;

const CheckCode = html`<li class=${cls()}><span class="box" aria-label=${attr()}>${text()}</span><code>${text()}</code><span>${text()}</span></li>`;
const CheckPlain = html`<li class=${cls()}><span class="box" aria-label=${attr()}>${text()}</span><span>${text()}</span></li>`;
const Checklist = html`<ul class="checklist"></ul>`;

const LineT = html`<li class=${cls()}><time>${text()}</time><p>${text()}</p></li>`;
const Log = html`<ol class="timeline"></ol>`;

// Blocks of different templates share no type (Block<V> is invariant in V),
// so a row that switches between shape templates holds them widened.
type AnyBlock = Block<readonly unknown[]>;
type AnyTemplate = Template<readonly unknown[]>;
const widen = <V extends readonly unknown[]>(b: Block<V>): AnyBlock => b as unknown as AnyBlock;
const widenT = <V extends readonly unknown[]>(t: Template<V>): AnyTemplate => t as unknown as AnyTemplate;

/** A row entry whose template can change with the item's shape. */
interface Row<B> {
  root: Element;
  b: B;
}

// ---------------------------------------------------------------------------
// Team
// ---------------------------------------------------------------------------

type CardB = AnyBlock;
const cardClass = (m: Member, sel: boolean) => `card r-${m.role} s-${m.status}${sel ? " selected" : ""}`;

function mountCard(m: Member, sel: boolean, click: Click): CardB {
  const head = [cardClass(m, sel), `Open ${m.title} (${m.pane})`, m.pane, click, m.title, `pill s-${m.status}`, statusLabel[m.status]] as const;
  const tail = [m.sub, m.pane, m.tab, m.term, m.term] as const;
  return m.task === null ? widen(CardPlain.mount([...head, ...tail])) : widen(CardTask.mount([...head, m.task, ...tail]));
}

function updateCard(row: Row<CardB>, m: Member, sel: boolean, click: Click): void {
  if (row.b.t !== (m.task === null ? widenT(CardPlain) : widenT(CardTask))) {
    const b = mountCard(m, sel, click);
    row.root.replaceWith(b.root);
    row.root = b.root;
    row.b = b;
    return;
  }
  const b = row.b;
  b.set(0, cardClass(m, sel));
  b.set(1, `Open ${m.title} (${m.pane})`);
  b.set(2, m.pane);
  b.set(4, m.title);
  b.set(5, `pill s-${m.status}`);
  b.set(6, statusLabel[m.status]);
  let i = 7;
  if (m.task !== null) b.set(i++, m.task);
  b.set(i++, m.sub);
  b.set(i++, m.pane);
  b.set(i++, m.tab);
  b.set(i++, m.term);
  b.set(i, m.term);
}

interface LaneRow {
  root: Element;
  b: Block<readonly [string, string, string, number]>;
  cards: List<Member, Row<CardB>, string>;
  none: Element | null;
}

function teamView(onSelect: (pane: string) => void) {
  const view = Board.mountRoot([]),
    kanban = view.firstChild as Element;
  let selected: string | null = null;
  const click: Click = function () {
    onSelect(this.getAttribute("data-pane")!);
  };

  const fillLane = (lane: LaneRow, l: Lane) => {
    if (l.members.length === 0) {
      lane.cards.set(l.members);
      if (!lane.none) lane.root.appendChild((lane.none = NoneT.mountRoot([])));
      return;
    }
    if (lane.none) lane.none.remove(), (lane.none = null);
    lane.cards.set(l.members);
  };

  const lanesList = new List<Lane, LaneRow, Status>(kanban, {
    owns: true,
    key: (l) => l.status,
    create: (l) => {
      const b = LaneT.mount([`lane s-${l.status}`, l.title, l.title, l.members.length]);
      const lane: LaneRow = {
        root: b.root,
        b,
        none: null,
        cards: new List<Member, Row<CardB>, string>(b.root, {
          key: (m) => m.pane,
          create: (m) => {
            const cb = mountCard(m, selected === m.pane, click);
            return { root: cb.root, b: cb };
          },
          update: (row, m) => updateCard(row, m, selected === m.pane, click),
        }),
      };
      fillLane(lane, l);
      return lane;
    },
    update: (lane, l) => {
      lane.b.set(1, l.title);
      lane.b.set(2, l.title);
      lane.b.set(3, l.members.length);
      fillLane(lane, l);
    },
  });

  return {
    view,
    render(s: State) {
      selected = s.selected;
      lanesList.set(lanes(s.members));
    },
  };
}

// ---------------------------------------------------------------------------
// Activity
// ---------------------------------------------------------------------------

type EvB = AnyBlock;

function eventTemplate(e: Event): AnyTemplate {
  if (e.source === "web") return widenT(EvWeb);
  if (e.status) return e.who !== null ? widenT(EvStatus) : widenT(EvStatusAnon);
  return e.who !== null ? widenT(EvOther) : widenT(EvOtherAnon);
}

function eventValues(e: Event): readonly unknown[] {
  if (e.source === "web") return [e.source, e.at, e.clock, e.text ?? ""];
  if (e.status) {
    const pill = [`pill s-${e.status}`, statusLabel[e.status]];
    return e.who !== null ? [e.source, e.at, e.clock, e.who, ...pill] : [e.source, e.at, e.clock, ...pill];
  }
  return e.who !== null ? [e.source, e.at, e.clock, e.label, e.who] : [e.source, e.at, e.clock, e.label];
}

function createEvent(e: Event): Row<EvB> {
  const b = eventTemplate(e).mount(eventValues(e));
  return { root: b.root, b };
}

function updateEvent(row: Row<EvB>, e: Event): void {
  const t = eventTemplate(e);
  if (row.b.t === t) {
    // Hole by hole, so a poll allocates nothing per row.
    const b = row.b;
    b.set(0, e.source);
    b.set(1, e.at);
    b.set(2, e.clock);
    if (e.source === "web") b.set(3, e.text ?? "");
    else if (e.status) {
      let i = 3;
      if (e.who !== null) b.set(i++, e.who);
      b.set(i++, `pill s-${e.status}`);
      b.set(i, statusLabel[e.status]);
    } else {
      b.set(3, e.label);
      if (e.who !== null) b.set(4, e.who);
    }
    return;
  }
  const b = t.mount(eventValues(e));
  row.root.replaceWith(b.root);
  row.root = b.root;
  row.b = b;
}

// ---------------------------------------------------------------------------
// Checklist and log
// ---------------------------------------------------------------------------

type CheckB = AnyBlock;
const checkClass = (c: Check) => (c.done ? "done" : c.skipped ? "skipped" : "");
const checkLabel = (c: Check) => (c.done ? "Done" : c.skipped ? "Not applicable" : "Open");
const checkMark = (c: Check) => (c.done ? "✓" : c.skipped ? "–" : "");

function createCheck(c: Check): Row<CheckB> {
  const b = c.id
    ? widen(CheckCode.mount([checkClass(c), checkLabel(c), checkMark(c), c.id, c.text]))
    : widen(CheckPlain.mount([checkClass(c), checkLabel(c), checkMark(c), c.text]));
  return { root: b.root, b };
}

function updateCheck(row: Row<CheckB>, c: Check): void {
  if (row.b.t !== (c.id ? widenT(CheckCode) : widenT(CheckPlain))) {
    const fresh = createCheck(c);
    row.root.replaceWith(fresh.root);
    row.root = fresh.root;
    row.b = fresh.b;
    return;
  }
  const b = row.b;
  b.set(0, checkClass(c));
  b.set(1, checkLabel(c));
  b.set(2, checkMark(c));
  if (c.id) b.set(3, c.id), b.set(4, c.text);
  else b.set(3, c.text);
}

type LineB = Block<readonly [string, string, string]>;
const lineClass = (l: Line) => (l.text.startsWith("web:") ? "web" : "");

// ---------------------------------------------------------------------------
// The app
// ---------------------------------------------------------------------------

function listView<T, E extends { readonly root: Node }>(
  view: Element,
  pick: (s: State) => readonly T[],
  key: (item: T) => string,
  create: (item: T) => E,
  update: (entry: E, item: T) => void,
) {
  // RootOption<E> stays unresolved for a generic E, so the options are asserted.
  const list = new List<T, E, string>(view, { owns: true, key, create, update } as ListOptions<T, E, string>);
  return { view, render: (s: State) => list.set(pick(s)) };
}

start((root, onSelect) => {
  let tab: Tab | null = null;
  let current: { view: Element; render(s: State): void } | null = null;
  return {
    render(s) {
      if (s.tab !== tab) {
        // A tab switch replaces the view: fill it off-document, then show it.
        tab = s.tab;
        current =
          tab === "team"
            ? teamView(onSelect)
            : tab === "activity"
              ? listView(Activity.mountRoot([]), (s) => s.events, eventKey, createEvent, updateEvent)
              : tab === "checklist"
                ? listView(Checklist.mountRoot([]), (s) => s.checklist, checkKey, createCheck, updateCheck)
                : listView<Line, LineB>(
                    Log.mountRoot([]),
                    (s) => s.log,
                    lineKey,
                    (l) => LineT.mount([lineClass(l), l.time, l.text]),
                    (b, l) => {
                      b.set(0, lineClass(l));
                      b.set(1, l.time);
                      b.set(2, l.text);
                    },
                  );
        current.render(s);
        root.textContent = "";
        root.appendChild(current.view);
        return;
      }
      current!.render(s);
    },
  };
});
