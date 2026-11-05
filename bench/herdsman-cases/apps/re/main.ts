// re, the library at the top of the js-framework-benchmark keyed table, as an
// expert would write herdsman's route tabs with it: html`` templates compiled
// once and cloned, fine-grained signals bound straight to text nodes and
// attributes, and each() for every keyed list.
//
// re-core/ is the library source vendored unchanged from the official
// js-framework-benchmark entry (frameworks/keyed/re, @re/core 0.1.0, MIT,
// "@re contributors"), krausest/js-framework-benchmark commit
// ca8715d9ae84916fd24de3a79457d8c328f9b766.
//
// Each snapshot is reconciled per key into long-lived row models whose fields
// are signals: a signal write that does not change the value does nothing, so
// only the text nodes and attributes whose values changed are written.

import { append, batch, each, html, type ReFragment, type Signal, signal } from "./re-core/src/index.ts";
import { start } from "../../shared/driver.ts";
import {
  type Check,
  checkKey,
  type Event,
  eventKey,
  lanes,
  type Line,
  lineKey,
  type Member,
  type State,
  type Status,
  statusLabel,
  type Tab,
} from "../../shared/state.ts";

interface View {
  frag: ReFragment;
  update(s: State): void;
}

/** Set a signal; re skips the write when the value is the same. */
const put = <T>(sig: Signal<T>, v: T) => void (sig.value = v);

function pill(status: Status | Signal<Status>): ReFragment {
  const label = typeof status === "string" ? statusLabel[status] : () => statusLabel[status.value];
  return html`<span class="pill s-${status}"><span class="dot" aria-hidden="true"></span>${label}</span>`;
}

// ---- Team ------------------------------------------------------------------

interface CardModel {
  pane: string;
  /** The card's shape: a card whose task appears or disappears gets a new model, so a new row. */
  hasTask: boolean;
  title: Signal<string>;
  role: Signal<string>;
  status: Signal<Status>;
  sel: Signal<string>;
  task: Signal<string>;
  sub: Signal<string>;
  tab: Signal<string>;
  term: Signal<string>;
}

interface LaneModel {
  status: Status;
  title: string;
  count: Signal<number>;
  /** The lane's cards, or the one EMPTY item that renders "None". */
  items: Signal<readonly LaneItem[]>;
}

const EMPTY = { empty: true } as const;
type LaneItem = CardModel | typeof EMPTY;

function cardModel(m: Member, selected: boolean): CardModel {
  return {
    pane: m.pane,
    hasTask: m.task !== null,
    title: signal(m.title),
    role: signal<string>(m.role),
    status: signal(m.status),
    sel: signal(selected ? " selected" : ""),
    task: signal(m.task ?? ""),
    sub: signal(m.sub),
    tab: signal(m.tab),
    term: signal(m.term),
  };
}

function updateCard(c: CardModel, m: Member, selected: boolean): void {
  put(c.title, m.title);
  put(c.role, m.role);
  put(c.status, m.status);
  put(c.sel, selected ? " selected" : "");
  put(c.task, m.task ?? "");
  put(c.sub, m.sub);
  put(c.tab, m.tab);
  put(c.term, m.term);
}

function teamView(onSelect: (pane: string) => void): View {
  const cards = new Map<string, CardModel>();
  const laneModels = new Map<Status, LaneModel>();
  const laneList = signal<readonly LaneModel[]>([]);

  const card = (c: CardModel) =>
    html`<button class="card r-${c.role} s-${c.status}${c.sel}" title="Open ${c.title} (${c.pane})" data-pane=${c.pane}><div class="card-top"><span class="card-title">${c.title}</span>${pill(c.status)}</div>${c.hasTask ? html`<div class="card-task">${c.task}</div>` : null}<div class="card-sub">${c.sub}</div><div class="card-foot"><code>${c.pane}</code><span class="muted">${c.tab}</span><span class="muted clip" title=${c.term}>${c.term}</span></div></button>`;

  const laneItem = (x: LaneItem) => (x === EMPTY ? html`<p class="lane-empty">None</p>` : card(x as CardModel));

  const lane = (l: LaneModel) =>
    html`<section class="lane s-${l.status}" aria-label=${l.title}><h2><span class="lane-dot" aria-hidden="true"></span>${l.title}<span class="count">${l.count}</span></h2>${each(l.items, (x) => x, laneItem)}</section>`;

  const frag = html`<div class="team"><div class="kanban">${each(laneList, (l) => l.status, lane)}</div></div>`;
  // One delegated listener for every card, as the official re entry handles
  // row clicks: re leaves an on* binding's marker in the attribute, which
  // would show in the markup.
  frag.root!.addEventListener("click", (e) => {
    const card = (e.target as Element).closest<HTMLElement>(".card");
    if (card) onSelect(card.dataset["pane"]!);
  });

  return {
    frag,
    update(s) {
      batch(() => {
        const seen = new Set<string>();
        for (const m of s.members) {
          seen.add(m.pane);
          const selected = s.selected === m.pane;
          const c = cards.get(m.pane);
          if (c === undefined || c.hasTask !== (m.task !== null)) cards.set(m.pane, cardModel(m, selected));
          else updateCard(c, m, selected);
        }
        for (const pane of cards.keys()) if (!seen.has(pane)) cards.delete(pane);

        const next: LaneModel[] = [];
        for (const l of lanes(s.members)) {
          let lm = laneModels.get(l.status);
          if (lm === undefined) {
            lm = { status: l.status, title: l.title, count: signal(0), items: signal<readonly LaneItem[]>([]) };
            laneModels.set(l.status, lm);
          }
          put(lm.count, l.members.length);
          put(lm.items, l.members.length ? l.members.map((m) => cards.get(m.pane)!) : [EMPTY]);
          next.push(lm);
        }
        for (const st of laneModels.keys()) if (!next.some((l) => l.status === st)) laneModels.delete(st);
        put(laneList, next);
      });
    },
  };
}

// ---- Activity --------------------------------------------------------------

// An event's key holds its time, kind, pane, status and text, so a row never
// changes once rendered: rows are plain templates with no signals.
function eventItem(e: Event): ReFragment {
  const who = e.who !== null ? html`<strong>${e.who}</strong>` : null;
  const body =
    e.source === "web"
      ? html`<span class="chip accent">web</span><span>${e.text ?? ""}</span>`
      : e.status
        ? html`${who}${pill(e.status)}`
        : html`<span class="chip">${e.label}</span>${who}`;
  return html`<li class=${e.source}><time datetime=${e.at}>${e.clock}</time><p class="event">${body}</p></li>`;
}

function activityView(): View {
  const events = signal<readonly Event[]>([]);
  return {
    frag: html`<ol class="timeline activity">${each(events, eventKey, eventItem)}</ol>`,
    update: (s) => put(events, s.events),
  };
}

// ---- Checklist -------------------------------------------------------------

interface CheckModel {
  key: string;
  /** Part of the key when present, so fixed for the row. */
  id: string;
  cls: Signal<string>;
  label: Signal<string>;
  box: Signal<string>;
  text: Signal<string>;
}

const checkClass = (c: Check) => (c.done ? "done" : c.skipped ? "skipped" : "");
const checkLabel = (c: Check) => (c.done ? "Done" : c.skipped ? "Not applicable" : "Open");
const checkBox = (c: Check) => (c.done ? "✓" : c.skipped ? "–" : "");

function checkItem(c: CheckModel): ReFragment {
  return html`<li class=${c.cls}><span class="box" aria-label=${c.label}>${c.box}</span>${c.id ? html`<code>${c.id}</code>` : null}<span>${c.text}</span></li>`;
}

function checklistView(): View {
  const models = new Map<string, CheckModel>();
  const list = signal<readonly CheckModel[]>([]);
  return {
    frag: html`<ul class="checklist">${each(list, (c) => c.key, checkItem)}</ul>`,
    update(s) {
      batch(() => {
        const next: CheckModel[] = [];
        for (const c of s.checklist) {
          const key = checkKey(c);
          let m = models.get(key);
          if (m === undefined) {
            m = { key, id: c.id, cls: signal(checkClass(c)), label: signal(checkLabel(c)), box: signal(checkBox(c)), text: signal(c.text) };
            models.set(key, m);
          } else {
            put(m.cls, checkClass(c));
            put(m.label, checkLabel(c));
            put(m.box, checkBox(c));
            put(m.text, c.text);
          }
          next.push(m);
        }
        if (models.size !== next.length) {
          const keep = new Set(next);
          for (const [k, m] of models) if (!keep.has(m)) models.delete(k);
        }
        put(list, next);
      });
    },
  };
}

// ---- Log -------------------------------------------------------------------

// A line's key is its time and text, all it shows: rows never change.
function lineItem(l: Line): ReFragment {
  return html`<li class=${l.text.startsWith("web:") ? "web" : ""}><time>${l.time}</time><p>${l.text}</p></li>`;
}

function logView(): View {
  const log = signal<readonly Line[]>([]);
  return {
    frag: html`<ol class="timeline">${each(log, lineKey, lineItem)}</ol>`,
    update: (s) => put(log, s.log),
  };
}

// ---- Route body ------------------------------------------------------------

start((root, onSelect) => {
  let tab: Tab | null = null;
  let view: View | null = null;
  return {
    render(s) {
      if (s.tab === tab) return view!.update(s);
      // A tab switch replaces the view: dispose the old one's bindings and lists.
      view?.frag.dispose();
      root.textContent = "";
      tab = s.tab;
      view =
        s.tab === "team" ? teamView(onSelect) : s.tab === "activity" ? activityView() : s.tab === "checklist" ? checklistView() : logView();
      view.update(s);
      append(root, view.frag);
    },
  };
});
