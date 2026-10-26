// herdsman-blocks-view, the declarative layer over herdsman-blocks: one view
// function per screen builds descriptions from the snapshot, and render()
// writes only the holes whose values changed. Optional parts are child holes,
// keyed lists are list(), handlers are fresh closures (stored indirectly, so a
// new one costs no DOM write). No hand-written per-hole updates.

import { attr, child, cls, html, list, on, render, text, type ViewNode } from "../../../../web/src/herdsman-blocks-view.ts";
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
} from "../../shared/state.ts";

const Pill = html`<span class=${cls()}><span class="dot" aria-hidden="true"></span>${text()}</span>`;
const Chip = html`<span class=${cls()}>${text()}</span>`;
const Strong = html`<strong>${text()}</strong>`;
const Code = html`<code>${text()}</code>`;

const pill = (s: Status): ViewNode => Pill([`pill s-${s}`, statusLabel[s]]);
const strong = (who: string | null): ViewNode | null => (who !== null ? Strong([who]) : null);

// ---------------------------------------------------------------------------
// Team
// ---------------------------------------------------------------------------

const Team = html`<div class="team"><div class="kanban">${child()}</div></div>`;
const LaneV = html`<section class=${cls()} aria-label=${attr()}><h2><span class="lane-dot" aria-hidden="true"></span>${text()}<span class="count">${text<number>()}</span></h2>${child()}</section>`;
const None = html`<p class="lane-empty">None</p>`;
const Card = html`<button class=${cls()} title=${attr()} data-pane=${attr()} onclick=${on<"click">()}><div class="card-top"><span class="card-title">${text()}</span>${child()}</div>${child()}<div class="card-sub">${text()}</div><div class="card-foot"><code>${text()}</code><span class="muted">${text()}</span><span class="muted clip" title=${attr()}>${text()}</span></div></button>`;
const Task = html`<div class="card-task">${text()}</div>`;

function card(m: Member, selected: boolean, onSelect: (pane: string) => void): ViewNode {
  return Card([
    `card r-${m.role} s-${m.status}${selected ? " selected" : ""}`,
    `Open ${m.title} (${m.pane})`,
    m.pane,
    () => onSelect(m.pane),
    m.title,
    pill(m.status),
    m.task !== null ? Task([m.task]) : null,
    m.sub,
    m.pane,
    m.tab,
    m.term,
    m.term,
  ]);
}

function teamView(s: State, onSelect: (pane: string) => void): ViewNode {
  const lane = (l: Lane): ViewNode =>
    LaneV([
      `lane s-${l.status}`,
      l.title,
      l.title,
      l.members.length,
      l.members.length
        ? list(
            l.members,
            (m) => m.pane,
            (m) => card(m, s.selected === m.pane, onSelect),
          )
        : None([]),
    ]);
  return Team([list(lanes(s.members), (l) => l.status, lane)]);
}

// ---------------------------------------------------------------------------
// Activity, checklist, log
// ---------------------------------------------------------------------------

const Activity = html`<ol class="timeline activity">${child()}</ol>`;
const EventV = html`<li class=${cls()}><time datetime=${attr()}>${text()}</time><p class="event">${child()}${child()}</p></li>`;
const Span = html`<span>${text()}</span>`;

function eventItem(e: Event): ViewNode {
  const head = [e.source, e.at, e.clock] as const;
  if (e.source === "web") return EventV([...head, Chip(["chip accent", "web"]), Span([e.text ?? ""])]);
  if (e.status) return EventV([...head, strong(e.who), pill(e.status)]);
  return EventV([...head, Chip(["chip", e.label]), strong(e.who)]);
}

const activityView = (s: State): ViewNode => Activity([list(s.events, eventKey, eventItem)]);

const Checklist = html`<ul class="checklist">${child()}</ul>`;
const CheckV = html`<li class=${cls()}><span class="box" aria-label=${attr()}>${text()}</span>${child()}<span>${text()}</span></li>`;

const checkItem = (c: Check): ViewNode =>
  CheckV([
    c.done ? "done" : c.skipped ? "skipped" : "",
    c.done ? "Done" : c.skipped ? "Not applicable" : "Open",
    c.done ? "✓" : c.skipped ? "–" : "",
    c.id ? Code([c.id]) : null,
    c.text,
  ]);

const checklistView = (s: State): ViewNode => Checklist([list(s.checklist, checkKey, checkItem)]);

const Log = html`<ol class="timeline">${child()}</ol>`;
const LineV = html`<li class=${cls()}><time>${text()}</time><p>${text()}</p></li>`;

const lineItem = (l: Line): ViewNode => LineV([l.text.startsWith("web:") ? "web" : "", l.time, l.text]);
const logView = (s: State): ViewNode => Log([list(s.log, lineKey, lineItem)]);

// Each tab is its own view, so a tab switch replaces the tab's view.
start((root, onSelect) => ({
  render(s) {
    render(
      root,
      s.tab === "team"
        ? teamView(s, onSelect)
        : s.tab === "activity"
          ? activityView(s)
          : s.tab === "checklist"
            ? checklistView(s)
            : logView(s),
    );
  },
}));
