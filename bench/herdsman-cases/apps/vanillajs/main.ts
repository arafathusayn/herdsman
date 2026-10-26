// Hand-written DOM code in the style of js-framework-benchmark's "vanillajs"
// keyed entry: one template per row shape parsed once, rows made by cloning
// it, direct references to each row's nodes kept in a row record, and a keyed
// reconcile written here. Every render derives its work from the snapshot
// alone: each keyed item is compared with what its row shows.

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

const tpl = (html: string): Element => {
  const t = document.createElement("template");
  t.innerHTML = html;
  return t.content.firstChild as Element;
};
const clone = (proto: Element): HTMLElement => proto.cloneNode(true) as HTMLElement;

const PILL = `<span class="pill"><span class="dot" aria-hidden="true"></span> </span>`;
const CARD_HEAD = `<button class="card"><div class="card-top"><span class="card-title"> </span>${PILL}</div>`;
const CARD_TAIL = `<div class="card-sub"> </div><div class="card-foot"><code> </code><span class="muted"> </span><span class="muted clip"> </span></div></button>`;
const cardTask = tpl(CARD_HEAD + `<div class="card-task"> </div>` + CARD_TAIL);
const cardPlain = tpl(CARD_HEAD + CARD_TAIL);
const laneT = tpl(`<section class="lane"><h2><span class="lane-dot" aria-hidden="true"></span> <span class="count"> </span></h2></section>`);
const emptyT = tpl(`<p class="lane-empty">None</p>`);
const evWeb = tpl(`<li><time> </time><p class="event"><span class="chip accent">web</span><span> </span></p></li>`);
const evStatus = tpl(`<li><time> </time><p class="event"><strong> </strong>${PILL}</p></li>`);
const evStatusAnon = tpl(`<li><time> </time><p class="event">${PILL}</p></li>`);
const evOther = tpl(`<li><time> </time><p class="event"><span class="chip"> </span><strong> </strong></p></li>`);
const evOtherAnon = tpl(`<li><time> </time><p class="event"><span class="chip"> </span></p></li>`);
const checkCode = tpl(`<li><span class="box"> </span><code> </code><span> </span></li>`);
const checkPlain = tpl(`<li><span class="box"> </span><span> </span></li>`);
const lineT = tpl(`<li><time> </time><p> </p></li>`);

// ---------------------------------------------------------------------------
// Keyed reconcile
// ---------------------------------------------------------------------------

interface Row {
  k: string;
  el: Element;
}

/**
 * Make `parent`'s rows (which end before `after`) match `items`. Trims the
 * common prefix and suffix, then matches the rest by key, removes the rows that
 * left, and places the window right to left, moving a row only when it is not
 * already followed by the row that must follow it.
 */
function reconcile<T, R extends Row>(
  parent: Node,
  rows: R[],
  items: readonly T[],
  keyOf: (item: T) => string,
  create: (item: T, k: string) => R,
  update: (row: R, item: T) => void,
  after: Node | null,
  owns: boolean,
): R[] {
  const n = items.length,
    m = rows.length,
    out: R[] = new Array(n),
    keys: string[] = new Array(n);
  for (let i = 0; i < n; i++) keys[i] = keyOf(items[i]!);
  let s = 0,
    oe = m - 1,
    ne = n - 1;
  while (s <= oe && s <= ne && rows[s]!.k === keys[s]) update((out[s] = rows[s]!), items[s]!), s++;
  while (s <= oe && s <= ne && rows[oe]!.k === keys[ne]) update((out[ne] = rows[oe]!), items[ne]!), oe--, ne--;
  if (s > ne) {
    if (owns && s === 0 && oe === m - 1) parent.textContent = "";
    else for (let i = s; i <= oe; i++) rows[i]!.el.remove();
    return out;
  }
  let ref = ne + 1 < n ? out[ne + 1]!.el : after;
  if (s > oe) {
    for (let i = s; i <= ne; i++) parent.insertBefore((out[i] = create(items[i]!, keys[i]!)).el, ref);
    return out;
  }
  const old = new Map<string, R>();
  for (let i = s; i <= oe; i++) old.set(rows[i]!.k, rows[i]!);
  let kept = 0;
  for (let i = s; i <= ne; i++) {
    const r = old.get(keys[i]!);
    if (r !== undefined) {
      old.delete(keys[i]!);
      update((out[i] = r), items[i]!);
      kept++;
    }
  }
  if (kept === 0 && owns && s === 0 && oe === m - 1 && ne === n - 1) parent.textContent = "";
  else for (const r of old.values()) r.el.remove();
  for (let i = ne; i >= s; i--) {
    let r = out[i];
    if (r === undefined) parent.insertBefore((r = out[i] = create(items[i]!, keys[i]!)).el, ref);
    else if (r.el.nextSibling !== ref) parent.insertBefore(r.el, ref);
    ref = r.el;
  }
  return out;
}

// ---------------------------------------------------------------------------
// Team
// ---------------------------------------------------------------------------

interface CardRow {
  el: HTMLElement;
  m: Member;
  sel: boolean;
  lane: Status;
  title: Text;
  pill: Element;
  pillText: Text;
  task: Text | null;
  sub: Text;
  pane: Text;
  tab: Text;
  termEl: HTMLElement;
  term: Text;
}

const cardClass = (m: Member, sel: boolean) => `card r-${m.role} s-${m.status}${sel ? " selected" : ""}`;

function createCard(m: Member, sel: boolean): CardRow {
  const el = clone(m.task === null ? cardPlain : cardTask);
  const top = el.firstChild!,
    pill = top.lastChild as Element;
  let next = top.nextSibling!;
  let task: Text | null = null;
  if (m.task !== null) {
    task = next.firstChild as Text;
    task.data = m.task;
    next = next.nextSibling!;
  }
  const sub = next.firstChild as Text,
    foot = next.nextSibling!,
    code = foot.firstChild!,
    tab = code.nextSibling!,
    termEl = tab.nextSibling as HTMLElement;
  const r: CardRow = {
    el,
    m,
    sel,
    lane: m.status,
    title: top.firstChild!.firstChild as Text,
    pill,
    pillText: pill.lastChild as Text,
    task,
    sub,
    pane: code.firstChild as Text,
    tab: tab.firstChild as Text,
    termEl,
    term: termEl.firstChild as Text,
  };
  el.className = cardClass(m, sel);
  el.title = `Open ${m.title} (${m.pane})`;
  el.setAttribute("data-pane", m.pane);
  r.title.data = m.title;
  pill.className = `pill s-${m.status}`;
  r.pillText.data = statusLabel[m.status];
  sub.data = m.sub;
  r.pane.data = m.pane;
  r.tab.data = m.tab;
  termEl.title = m.term;
  r.term.data = m.term;
  return r;
}

/** Returns false when the row's shape no longer fits (task appeared or went). */
function updateCard(r: CardRow, m: Member, sel: boolean): boolean {
  const o = r.m;
  if ((m.task === null) !== (o.task === null)) return false;
  if (m.role !== o.role || m.status !== o.status || sel !== r.sel) r.el.className = cardClass(m, sel);
  if (m.title !== o.title || m.pane !== o.pane) {
    r.el.title = `Open ${m.title} (${m.pane})`;
    if (m.title !== o.title) r.title.data = m.title;
    if (m.pane !== o.pane) r.el.setAttribute("data-pane", m.pane), (r.pane.data = m.pane);
  }
  if (m.status !== o.status) {
    r.pill.className = `pill s-${m.status}`;
    r.pillText.data = statusLabel[m.status];
  }
  if (m.task !== o.task) r.task!.data = m.task!;
  if (m.sub !== o.sub) r.sub.data = m.sub;
  if (m.tab !== o.tab) r.tab.data = m.tab;
  if (m.term !== o.term) (r.termEl.title = m.term), (r.term.data = m.term);
  r.m = m;
  r.sel = sel;
  return true;
}

interface LaneRow extends Row {
  k: Status;
  el: HTMLElement;
  title: Text;
  count: Text;
  titleS: string;
  n: number;
  empty: Element | null;
}

function teamView(root: HTMLElement) {
  const team = document.createElement("div"),
    kanban = document.createElement("div");
  team.className = "team";
  kanban.className = "kanban";
  team.appendChild(kanban);
  root.appendChild(team);
  const cards = new Map<string, CardRow>();
  let laneRows: LaneRow[] = [];
  let selected: string | null = null;

  // The lane's cards (or its "None" line) after the heading, right to left.
  const place = (lane: LaneRow, l: Lane) => {
    const sec = lane.el;
    if (l.members.length === 0) {
      if (!lane.empty) sec.appendChild((lane.empty = clone(emptyT)));
      return;
    }
    if (lane.empty) lane.empty.remove(), (lane.empty = null);
    let ref: Node | null = null;
    for (let i = l.members.length - 1; i >= 0; i--) {
      const m = l.members[i]!,
        sel = selected === m.pane;
      let r = cards.get(m.pane);
      if (r !== undefined && !updateCard(r, m, sel)) {
        r.el.remove();
        r = undefined;
      }
      if (r === undefined) cards.set(m.pane, (r = createCard(m, sel)));
      r.lane = m.status;
      if (r.el.nextSibling !== ref || r.el.parentNode !== sec) sec.insertBefore(r.el, ref);
      ref = r.el;
    }
  };

  const createLane = (l: Lane): LaneRow => {
    const el = clone(laneT),
      h2 = el.firstChild!;
    const lane: LaneRow = {
      k: l.status,
      el,
      title: h2.firstChild!.nextSibling as Text,
      count: h2.lastChild!.firstChild as Text,
      titleS: l.title,
      n: l.members.length,
      empty: null,
    };
    el.className = `lane s-${l.status}`;
    el.setAttribute("aria-label", l.title);
    lane.title.data = l.title;
    lane.count.data = String(l.members.length);
    place(lane, l);
    return lane;
  };

  const updateLane = (lane: LaneRow, l: Lane) => {
    if (l.title !== lane.titleS) {
      lane.el.setAttribute("aria-label", l.title);
      lane.title.data = lane.titleS = l.title;
    }
    if (l.members.length !== lane.n) lane.count.data = String((lane.n = l.members.length));
    place(lane, l);
  };

  return (s: State) => {
    selected = s.selected;
    // Cards that left the board or changed lanes leave their lane first, so
    // each lane's placement only sees rows that stay in it.
    let found = 0;
    for (const m of s.members) {
      const r = cards.get(m.pane);
      if (r === undefined) continue;
      found++;
      if (r.lane !== m.status) r.el.remove();
    }
    if (found !== cards.size) {
      const seen = new Set(s.members.map((m) => m.pane));
      for (const [pane, r] of cards) if (!seen.has(pane)) r.el.remove(), cards.delete(pane);
    }
    laneRows = reconcile(kanban, laneRows, lanes(s.members), (l) => l.status, createLane, updateLane, null, false);
  };
}

// ---------------------------------------------------------------------------
// Activity
// ---------------------------------------------------------------------------

interface EventRow extends Row {
  e: Event;
}

function createEvent(e: Event, k: string): EventRow {
  let el: HTMLElement;
  const anon = e.who === null;
  if (e.source === "web") {
    el = clone(evWeb);
    (el.lastChild!.lastChild!.firstChild as Text).data = e.text ?? "";
  } else if (e.status) {
    el = clone(anon ? evStatusAnon : evStatus);
    const p = el.lastChild!,
      pill = p.lastChild as Element;
    if (!anon) (p.firstChild!.firstChild as Text).data = e.who!;
    pill.className = `pill s-${e.status}`;
    (pill.lastChild as Text).data = statusLabel[e.status];
  } else {
    el = clone(anon ? evOtherAnon : evOther);
    const chip = el.lastChild!.firstChild!;
    (chip.firstChild as Text).data = e.label;
    if (!anon) (chip.nextSibling!.firstChild as Text).data = e.who!;
  }
  el.className = e.source;
  const time = el.firstChild as HTMLElement;
  time.setAttribute("datetime", e.at);
  (time.firstChild as Text).data = e.clock;
  return { k, el, e };
}

function updateEvent(r: EventRow, e: Event): void {
  const o = r.e;
  r.e = e;
  // The key holds at, kind, pane, status and text; check what else is shown.
  if (e.clock === o.clock && e.source === o.source && e.label === o.label && e.who === o.who && e.text === o.text)
    return;
  const fresh = createEvent(e, r.k);
  r.el.replaceWith(fresh.el);
  r.el = fresh.el;
}

// ---------------------------------------------------------------------------
// Checklist
// ---------------------------------------------------------------------------

interface CheckRow extends Row {
  c: Check;
  box: HTMLElement;
  code: Text | null;
  text: Text;
}

const checkState = (c: Check) => (c.done ? 0 : c.skipped ? 1 : 2);
const checkClass = ["done", "skipped", ""];
const checkLabel = ["Done", "Not applicable", "Open"];
const checkMark = ["✓", "–", ""];

function createCheck(c: Check, k: string): CheckRow {
  const el = clone(c.id ? checkCode : checkPlain),
    box = el.firstChild as HTMLElement,
    st = checkState(c);
  let next = box.nextSibling!,
    code: Text | null = null;
  if (c.id) {
    code = next.firstChild as Text;
    code.data = c.id;
    next = next.nextSibling!;
  }
  const text = next.firstChild as Text;
  text.data = c.text;
  el.className = checkClass[st]!;
  box.setAttribute("aria-label", checkLabel[st]!);
  (box.firstChild as Text).data = checkMark[st]!;
  return { k, el, c, box, code, text };
}

function updateCheck(r: CheckRow, c: Check): void {
  const o = r.c;
  r.c = c;
  if (!c.id !== !o.id) {
    const fresh = createCheck(c, r.k);
    r.el.replaceWith(fresh.el);
    Object.assign(r, fresh);
    return;
  }
  const st = checkState(c);
  if (st !== checkState(o)) {
    r.el.className = checkClass[st]!;
    r.box.setAttribute("aria-label", checkLabel[st]!);
    (r.box.firstChild as Text).data = checkMark[st]!;
  }
  if (c.id !== o.id) r.code!.data = c.id;
  if (c.text !== o.text) r.text.data = c.text;
}

// ---------------------------------------------------------------------------
// Log
// ---------------------------------------------------------------------------

interface LineRow extends Row {
  l: Line;
}

function createLine(l: Line, k: string): LineRow {
  const el = clone(lineT),
    time = el.firstChild!;
  if (l.text.startsWith("web:")) el.className = "web";
  (time.firstChild as Text).data = l.time;
  (time.nextSibling!.firstChild as Text).data = l.text;
  return { k, el, l };
}

function updateLine(r: LineRow, l: Line): void {
  const o = r.l;
  r.l = l;
  if (l.time === o.time && l.text === o.text) return;
  const time = r.el.firstChild!;
  r.el.className = l.text.startsWith("web:") ? "web" : "";
  (time.firstChild as Text).data = l.time;
  (time.nextSibling!.firstChild as Text).data = l.text;
}

// ---------------------------------------------------------------------------
// Lists and the app
// ---------------------------------------------------------------------------

function listView<T, R extends Row>(
  root: HTMLElement,
  tag: string,
  cls: string,
  pick: (s: State) => readonly T[],
  keyOf: (item: T) => string,
  create: (item: T, k: string) => R,
  update: (row: R, item: T) => void,
) {
  const list = document.createElement(tag);
  list.className = cls;
  root.appendChild(list);
  let rows: R[] = [];
  return (s: State) => {
    rows = reconcile(list, rows, pick(s), keyOf, create, update, null, true);
  };
}

start((root, onSelect) => {
  let tab: Tab | null = null;
  let view: (s: State) => void = () => {};
  root.addEventListener("click", (e) => {
    const card = (e.target as Element).closest(".card");
    if (card) onSelect(card.getAttribute("data-pane")!);
  });
  return {
    render(s) {
      if (s.tab !== tab) {
        // A tab switch replaces the view: fill it off-document, then show it.
        tab = s.tab;
        const host = document.createElement("div");
        view =
          tab === "team"
            ? teamView(host)
            : tab === "activity"
              ? listView(host, "ol", "timeline activity", (s) => s.events, eventKey, createEvent, updateEvent)
              : tab === "checklist"
                ? listView(host, "ul", "checklist", (s) => s.checklist, checkKey, createCheck, updateCheck)
                : listView(host, "ol", "timeline", (s) => s.log, lineKey, createLine, updateLine);
        view(s);
        root.textContent = "";
        root.appendChild(host.firstChild!);
        return;
      }
      view(s);
    },
  };
});
