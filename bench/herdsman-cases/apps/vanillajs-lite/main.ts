// In the style of js-framework-benchmark's "vanillajs-lite" keyed entry:
// minimal bookkeeping. No row objects: each row element carries its key and
// the item it shows as expandos, and the DOM itself is the list. New rows are
// made by writing the values into a template row (its text node references
// cached on it with ??=) and deep-cloning it, so a clone's inner nodes are
// never touched from JavaScript; uniform rows are cloned in batches. Work is
// derived from the snapshot per keyed item, never from which button was hit.

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
  statusLabel,
} from "../../shared/state.ts";

// A row element: its key ($k), the item it shows ($v), cached nodes ($a..$d).
type R = HTMLElement & { $k: string; $v: any; $a?: any; $b?: any; $c?: any; $d?: any };

const { cloneNode } = Node.prototype;
const clone = (n: Node): R => cloneNode.call(n, true) as R;
const T = (html: string): R => {
  const t = document.createElement("template");
  t.innerHTML = html;
  return t.content.firstChild as R;
};
const txt = (n: Node): Text => n.firstChild as Text;

const PILL = `<span class="pill"><span class="dot" aria-hidden="true"></span> </span>`;
const HEAD = `<button class="card"><div class="card-top"><span class="card-title"> </span>${PILL}</div>`;
const TAIL = `<div class="card-sub"> </div><div class="card-foot"><code> </code><span class="muted"> </span><span class="muted clip"> </span></div></button>`;
const CARD = [T(HEAD + TAIL), T(HEAD + `<div class="card-task"> </div>` + TAIL)] as const;
const LANE = T(`<section class="lane"><h2><span class="lane-dot" aria-hidden="true"></span> <span class="count"> </span></h2></section>`);
const NONE = T(`<p class="lane-empty">None</p>`);
const EV_WEB = T(`<li class="web"><time> </time><p class="event"><span class="chip accent">web</span><span> </span></p></li>`);
const EV_ST = [T(`<li><time> </time><p class="event">${PILL}</p></li>`), T(`<li><time> </time><p class="event"><strong> </strong>${PILL}</p></li>`)];
const EV_OT = [T(`<li><time> </time><p class="event"><span class="chip"> </span></p></li>`), T(`<li><time> </time><p class="event"><span class="chip"> </span><strong> </strong></p></li>`)];
const CHECK = [T(`<li><span class="box"> </span><span> </span></li>`), T(`<li><span class="box"> </span><code> </code><span> </span></li>`)];
const LINE = T(`<li><time> </time><p> </p></li>`);

// ---------------------------------------------------------------------------
// Keyed sync over live children: the DOM is the old list.
// ---------------------------------------------------------------------------

type Make<T> = (item: T, k: string, parent: Node, ref: Node | null) => void;
/** Brings a row up to date; returns the row now in its place (a replacement when its shape changed). */
type Upd<T> = (row: R, item: T) => R;

/** Make the rows of `parent` after `stop` (or all of them) match `items`. */
function sync<T>(parent: Node, stop: Node | null, items: readonly T[], keyOf: (t: T) => string, make: Make<T>, upd: Upd<T>): void {
  const n = items.length;
  let c = (stop ? stop.nextSibling : parent.firstChild) as R | null,
    i = 0;
  while (c && i < n && c.$k === keyOf(items[i]!)) (c = upd(c, items[i]!).nextSibling as R | null), i++;
  if (i === n) {
    if (c && !c.previousSibling) parent.textContent = "";
    else while (c) {
      const x: R | null = c.nextSibling as R | null;
      c.remove();
      c = x;
    }
    return;
  }
  if (!c) return batch(parent, items, i, n, keyOf, make);
  const before = c.previousSibling;
  let e = parent.lastChild as R,
    j = n - 1;
  while (e !== before && j >= i && e.$k === keyOf(items[j]!)) (e = upd(e, items[j]!).previousSibling as R), j--;
  // After the window; recomputed because an update may have replaced c.
  let ref: Node | null = e === before ? (before ? before.nextSibling : parent.firstChild) : e.nextSibling;
  if (e === before) return batch(parent, items, i, j + 1, keyOf, make, ref);
  if (j < i) {
    for (let x: ChildNode = c; ; ) {
      const nx = x.nextSibling!;
      x.remove();
      if (x === e) return;
      x = nx;
    }
  }
  const old = new Map<string, R>();
  for (let x = c; ; x = x.nextSibling as R) {
    old.set(x.$k, x);
    if (x === e) break;
  }
  const keys: string[] = new Array(j + 1);
  const rows: (R | undefined)[] = new Array(j + 1);
  for (let x = i; x <= j; x++) {
    const k = (keys[x] = keyOf(items[x]!)),
      r = old.get(k);
    if (r) old.delete(k), (rows[x] = r);
  }
  for (const r of old.values()) r.remove();
  for (let x = j; x >= i; x--) {
    let r = rows[x];
    if (!r) {
      make(items[x]!, keys[x]!, parent, ref);
      ref = ref ? ref.previousSibling : parent.lastChild;
      continue;
    }
    r = upd(r, items[x]!);
    if (r.nextSibling !== ref) parent.insertBefore(r, ref);
    ref = r;
  }
}

function batch<T>(parent: Node, items: readonly T[], from: number, to: number, keyOf: (t: T) => string, make: Make<T>, ref: Node | null = null): void {
  for (let x = from; x < to; x++) make(items[x]!, keyOf(items[x]!), parent, ref);
}

// ---------------------------------------------------------------------------
// Rows: fill the template, clone it.
// ---------------------------------------------------------------------------

const put = (parent: Node, proto: R, k: string, v: unknown, ref: Node | null): R => {
  const r = clone(proto);
  r.$k = k;
  r.$v = v;
  parent.insertBefore(r, ref);
  return r;
};

const cardClass = (m: Member, sel: boolean) => `card r-${m.role} s-${m.status}${sel ? " selected" : ""}`;

let SEL: string | null = null;

function makeCard(m: Member): R {
  const t = CARD[m.task === null ? 0 : 1];
  const top = t.firstChild!,
    pill = top.lastChild as HTMLElement;
  let next = top.nextSibling!;
  if (m.task !== null) (t.$d ??= txt(next)).data = m.task, (next = next.nextSibling!);
  t.className = cardClass(m, SEL === m.pane);
  t.title = `Open ${m.title} (${m.pane})`;
  t.setAttribute("data-pane", m.pane);
  (t.$a ??= txt(top.firstChild!)).data = m.title;
  pill.className = `pill s-${m.status}`;
  (t.$b ??= pill.lastChild as Text).data = statusLabel[m.status];
  txt(next).data = m.sub;
  const foot = next.nextSibling!,
    term = foot.lastChild as HTMLElement;
  txt(foot.firstChild!).data = m.pane;
  txt(foot.firstChild!.nextSibling!).data = m.tab;
  term.title = m.term;
  txt(term).data = m.term;
  const r = clone(t);
  r.$k = m.pane;
  r.$v = m;
  r.$c = SEL === m.pane;
  return r;
}

function updCard(r: R, m: Member): R {
  const o: Member = r.$v,
    sel = SEL === m.pane;
  if ((m.task === null) !== (o.task === null)) {
    const n = makeCard(m);
    r.replaceWith(n);
    return n;
  }
  r.$v = m;
  if (m.role !== o.role || m.status !== o.status || sel !== r.$c) (r.className = cardClass(m, sel)), (r.$c = sel);
  const top = r.firstChild!;
  if (m.title !== o.title || m.pane !== o.pane) {
    r.title = `Open ${m.title} (${m.pane})`;
    r.setAttribute("data-pane", m.pane);
    txt(top.firstChild!).data = m.title;
  }
  if (m.status !== o.status) {
    const pill = top.lastChild as HTMLElement;
    pill.className = `pill s-${m.status}`;
    (pill.lastChild as Text).data = statusLabel[m.status];
  }
  let next = top.nextSibling!;
  if (m.task !== null) (m.task !== o.task && (txt(next).data = m.task)), (next = next.nextSibling!);
  if (m.sub !== o.sub) txt(next).data = m.sub;
  const foot = next.nextSibling!;
  if (m.pane !== o.pane) txt(foot.firstChild!).data = m.pane;
  if (m.tab !== o.tab) txt(foot.firstChild!.nextSibling!).data = m.tab;
  if (m.term !== o.term) ((foot.lastChild as HTMLElement).title = m.term), (txt(foot.lastChild!).data = m.term);
  return r;
}

function makeEvent(e: Event, k: string, parent: Node, ref: Node | null): void {
  let t: R;
  const who = e.who !== null ? 1 : 0;
  if (e.source === "web") (t = EV_WEB), ((t.$b ??= txt(t.lastChild!.lastChild!)).data = e.text ?? "");
  else if (e.status) {
    t = EV_ST[who]!;
    const pill = (t.$c ??= t.lastChild!.lastChild) as HTMLElement;
    pill.className = `pill s-${e.status}`;
    (pill.lastChild as Text).data = statusLabel[e.status];
    if (who) (t.$d ??= txt(t.lastChild!.firstChild!)).data = e.who!;
  } else {
    t = EV_OT[who]!;
    (t.$c ??= txt(t.lastChild!.firstChild!)).data = e.label;
    if (who) (t.$d ??= txt(t.lastChild!.lastChild!)).data = e.who!;
  }
  t.className = e.source;
  const time = (t.$a ??= t.firstChild) as HTMLElement;
  time.setAttribute("datetime", e.at);
  txt(time).data = e.clock;
  put(parent, t, k, e, ref);
}

function updEvent(r: R, e: Event): R {
  const o: Event = r.$v;
  r.$v = e;
  // The key holds at, kind, pane, status and text; check what else is shown.
  if (e.clock === o.clock && e.source === o.source && e.label === o.label && e.who === o.who && e.text === o.text) return r;
  const f = document.createDocumentFragment();
  makeEvent(e, r.$k, f, null);
  const n = f.firstChild as R;
  r.replaceWith(n);
  return n;
}

const stateOf = (c: Check) => (c.done ? 0 : c.skipped ? 1 : 2);
const CK_CLASS = ["done", "skipped", ""],
  CK_LABEL = ["Done", "Not applicable", "Open"],
  CK_MARK = ["✓", "–", ""];

function makeCheck(c: Check, k: string, parent: Node, ref: Node | null): void {
  const t = CHECK[c.id ? 1 : 0]!,
    st = stateOf(c),
    box = (t.$a ??= t.firstChild) as HTMLElement;
  t.className = CK_CLASS[st]!;
  box.setAttribute("aria-label", CK_LABEL[st]!);
  txt(box).data = CK_MARK[st]!;
  if (c.id) (t.$b ??= txt(box.nextSibling!)).data = c.id;
  (t.$c ??= txt(t.lastChild!)).data = c.text;
  put(parent, t, k, c, ref);
}

function updCheck(r: R, c: Check): R {
  const o: Check = r.$v;
  r.$v = c;
  if (!c.id !== !o.id) {
    const f = document.createDocumentFragment();
    makeCheck(c, r.$k, f, null);
    const n = f.firstChild as R;
    r.replaceWith(n);
    return n;
  }
  const st = stateOf(c);
  if (st !== stateOf(o)) {
    const box = r.firstChild as HTMLElement;
    r.className = CK_CLASS[st]!;
    box.setAttribute("aria-label", CK_LABEL[st]!);
    txt(box).data = CK_MARK[st]!;
  }
  if (c.id !== o.id) txt(r.firstChild!.nextSibling!).data = c.id;
  if (c.text !== o.text) txt(r.lastChild!).data = c.text;
  return r;
}

// Log lines are uniform: fill a batch of template rows, clone the batch at once.
const LINE_BATCH = 50;
let lineTmpl: DocumentFragment | null = null;
const lineRows: R[] = [];

function fillLine(t: R, l: Line): void {
  t.className = l.text.startsWith("web:") ? "web" : "";
  (t.$a ??= txt(t.firstChild!)).data = l.time;
  (t.$b ??= txt(t.lastChild!)).data = l.text;
}

function makeLine(l: Line, k: string, parent: Node, ref: Node | null): void {
  fillLine(LINE, l);
  put(parent, LINE, k, l, ref);
}

function lineBatch(parent: Node, items: readonly Line[], from: number, to: number, ref: Node | null): void {
  if (!lineTmpl) {
    lineTmpl = document.createDocumentFragment();
    for (let x = 0; x < LINE_BATCH; x++) lineRows.push(lineTmpl.appendChild(clone(LINE)));
  }
  while (to - from >= LINE_BATCH) {
    for (let x = 0; x < LINE_BATCH; x++) fillLine(lineRows[x]!, items[from + x]!);
    const f = clone(lineTmpl);
    for (let r = f.firstChild as R, x = from; r; r = r.nextSibling as R, x++) (r.$k = lineKey(items[x]!)), (r.$v = items[x]);
    parent.insertBefore(f, ref);
    from += LINE_BATCH;
  }
  for (; from < to; from++) makeLine(items[from]!, lineKey(items[from]!), parent, ref);
}

function updLine(r: R, l: Line): R {
  const o: Line = r.$v;
  r.$v = l;
  if (l.time !== o.time || l.text !== o.text) {
    r.className = l.text.startsWith("web:") ? "web" : "";
    txt(r.firstChild!).data = l.time;
    txt(r.lastChild!).data = l.text;
  }
  return r;
}

// ---------------------------------------------------------------------------
// Team: lanes keyed by status, cards keyed by pane across the whole board.
// ---------------------------------------------------------------------------

let CARDS = new Map<string, R>();

function fillLane(sec: R, l: Lane): void {
  const h2 = sec.firstChild!;
  if (l.members.length === 0) {
    if (h2.nextSibling === null) sec.appendChild(clone(NONE));
    return;
  }
  const none = h2.nextSibling as R | null;
  if (none && none.$k === undefined) none.remove();
  let ref: Node | null = null;
  for (let i = l.members.length - 1; i >= 0; i--) {
    const m = l.members[i]!;
    let r = CARDS.get(m.pane);
    r = r ? updCard(r, m) : makeCard(m);
    if (r.nextSibling !== ref || r.parentNode !== sec) sec.insertBefore(r, ref);
    ref = r;
  }
}

function makeLane(l: Lane, k: string, parent: Node, ref: Node | null): void {
  LANE.className = `lane s-${l.status}`;
  LANE.setAttribute("aria-label", l.title);
  const h2 = LANE.firstChild!;
  (LANE.$a ??= h2.firstChild!.nextSibling as Text).data = l.title;
  (LANE.$b ??= txt(h2.lastChild!)).data = String(l.members.length);
  fillLane(put(parent, LANE, k, l, ref), l);
}

function updLane(sec: R, l: Lane): R {
  const o: Lane = sec.$v;
  sec.$v = l;
  const h2 = sec.firstChild!;
  if (l.title !== o.title) sec.setAttribute("aria-label", l.title), ((h2.firstChild!.nextSibling as Text).data = l.title);
  if (l.members.length !== o.members.length) txt(h2.lastChild!).data = String(l.members.length);
  fillLane(sec, l);
  return sec;
}

function team(kanban: HTMLElement, s: State): void {
  SEL = s.selected;
  // Index the live cards; cards that left or changed lanes are taken out
  // first, so each lane's placement only sees rows that stay in it.
  const live = new Map<string, R>();
  for (let sec = kanban.firstChild as R | null; sec; sec = sec.nextSibling as R | null)
    for (let c = sec.firstChild!.nextSibling as R | null; c && c.$k !== undefined; c = c.nextSibling as R | null) live.set(c.$k, c);
  CARDS = live;
  let found = 0;
  for (const m of s.members) {
    const c = live.get(m.pane);
    if (!c) continue;
    found++;
    if ((c.$v as Member).status !== m.status) c.remove();
  }
  if (found !== live.size) {
    const seen = new Set(s.members.map((m) => m.pane));
    for (const [k, c] of live) if (!seen.has(k)) c.remove(), live.delete(k);
  }
  sync(kanban, null, lanes(s.members), (l) => l.status, makeLane, updLane);
}

// ---------------------------------------------------------------------------
// The app
// ---------------------------------------------------------------------------

const lineMake: Make<Line> = makeLine;
function syncLog(ol: Node, s: State): void {
  if (!ol.firstChild) return lineBatch(ol, s.log, 0, s.log.length, null);
  sync(ol, null, s.log, lineKey, lineMake, updLine);
}

start((root, onSelect) => {
  let tab = "",
    view: HTMLElement;
  root.onclick = (e) => {
    const c = (e.target as Element).closest(".card");
    c && onSelect(c.getAttribute("data-pane")!);
  };
  const el = (tag: string, cls: string) => {
    const n = document.createElement(tag);
    n.className = cls;
    return n;
  };
  return {
    render(s) {
      const fresh = s.tab !== tab;
      if (fresh) {
        tab = s.tab;
        view = s.tab === "team" ? el("div", "team") : s.tab === "activity" ? el("ol", "timeline activity") : s.tab === "checklist" ? el("ul", "checklist") : el("ol", "timeline");
        if (s.tab === "team") view.appendChild(el("div", "kanban"));
      }
      if (s.tab === "team") team(view.firstChild as HTMLElement, s);
      else if (s.tab === "activity") sync(view, null, s.events, eventKey, makeEvent, updEvent);
      else if (s.tab === "checklist") sync(view, null, s.checklist, checkKey, makeCheck, updCheck);
      else syncLog(view, s);
      // A tab switch replaces the view; it was filled off-document.
      if (fresh) (root.textContent = ""), root.appendChild(view);
    },
  };
});
