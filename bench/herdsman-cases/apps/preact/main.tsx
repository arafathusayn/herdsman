// Preact 11, as an expert would write herdsman's screens for speed: plain
// function components, keyed lists, and one top-level render() per snapshot.
// Preact's render() diffs synchronously, so each click's whole commit lands
// within the click (no debounced setState queue is involved).
//
// Snapshots are fresh objects with equal values, so rows take their data as
// primitive props (spread from the item) and bail out per row with a
// shouldComponentUpdate that compares those props, the way preact/compat's
// memo does, without compat's extra wrapper component and option hooks.

import { type Component, render } from "preact";
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

type OnSelect = (pane: string) => void;

/** shouldComponentUpdate for a row: true when any prop differs. Rows always pass the same prop names. */
function changed(this: Component<Record<string, unknown>>, next: Record<string, unknown>): boolean {
  const prev = this.props;
  for (const k in next) if (next[k] !== prev[k]) return true;
  return false;
}

function Pill({ status }: { status: Status }) {
  return (
    <span class={`pill s-${status}`}>
      <span class="dot" aria-hidden="true" />
      {statusLabel[status]}
    </span>
  );
}

type CardProps = Member & { selected: boolean; onSelect: OnSelect };

function Card(this: Component<CardProps>, { pane, title, role, status, task, sub, tab, term, selected, onSelect }: CardProps) {
  this.shouldComponentUpdate = changed;
  return (
    <button
      class={`card r-${role} s-${status}${selected ? " selected" : ""}`}
      title={`Open ${title} (${pane})`}
      data-pane={pane}
      onClick={() => onSelect(pane)}
    >
      <div class="card-top">
        <span class="card-title">{title}</span>
        <Pill status={status} />
      </div>
      {task !== null && <div class="card-task">{task}</div>}
      <div class="card-sub">{sub}</div>
      <div class="card-foot">
        <code>{pane}</code>
        <span class="muted">{tab}</span>
        <span class="muted clip" title={term}>
          {term}
        </span>
      </div>
    </button>
  );
}

function LaneView({ lane, selected, onSelect }: { lane: Lane; selected: string | null; onSelect: OnSelect }) {
  return (
    <section class={`lane s-${lane.status}`} aria-label={lane.title}>
      <h2>
        <span class="lane-dot" aria-hidden="true" />
        {lane.title}
        <span class="count">{lane.members.length}</span>
      </h2>
      {lane.members.length ? (
        lane.members.map((m) => <Card key={m.pane} {...m} selected={selected === m.pane} onSelect={onSelect} />)
      ) : (
        <p class="lane-empty">None</p>
      )}
    </section>
  );
}

function Team({ s, onSelect }: { s: State; onSelect: OnSelect }) {
  return (
    <div class="team">
      <div class="kanban">
        {lanes(s.members).map((lane) => (
          <LaneView key={lane.status} lane={lane} selected={s.selected} onSelect={onSelect} />
        ))}
      </div>
    </div>
  );
}

function EventItem(this: Component<Event>, { at, clock, source, label, who, status, text }: Event) {
  this.shouldComponentUpdate = changed;
  const strong = who !== null ? <strong>{who}</strong> : null;
  return (
    <li class={source}>
      <time datetime={at}>{clock}</time>
      <p class="event">
        {source === "web" ? (
          <>
            <span class="chip accent">web</span>
            <span>{text ?? ""}</span>
          </>
        ) : status ? (
          <>
            {strong}
            <Pill status={status} />
          </>
        ) : (
          <>
            <span class="chip">{label}</span>
            {strong}
          </>
        )}
      </p>
    </li>
  );
}

function Activity({ s }: { s: State }) {
  return (
    <ol class="timeline activity">
      {s.events.map((e) => (
        <EventItem key={eventKey(e)} {...e} />
      ))}
    </ol>
  );
}

function CheckItem(this: Component<Check>, { id, text, done, skipped }: Check) {
  this.shouldComponentUpdate = changed;
  return (
    <li class={done ? "done" : skipped ? "skipped" : ""}>
      <span class="box" aria-label={done ? "Done" : skipped ? "Not applicable" : "Open"}>
        {done ? "✓" : skipped ? "–" : null}
      </span>
      {id ? <code>{id}</code> : null}
      <span>{text}</span>
    </li>
  );
}

function Checklist({ s }: { s: State }) {
  return (
    <ul class="checklist">
      {s.checklist.map((c) => (
        <CheckItem key={checkKey(c)} {...c} />
      ))}
    </ul>
  );
}

function LineItem(this: Component<Line>, { time, text }: Line) {
  this.shouldComponentUpdate = changed;
  return (
    <li class={text.startsWith("web:") ? "web" : ""}>
      <time>{time}</time>
      <p>{text}</p>
    </li>
  );
}

function Log({ s }: { s: State }) {
  return (
    <ol class="timeline">
      {s.log.map((l) => (
        <LineItem key={lineKey(l)} {...l} />
      ))}
    </ol>
  );
}

function Main({ s, onSelect }: { s: State; onSelect: OnSelect }) {
  // Keyed by tab, as herdsman's renderMain, so a tab switch replaces the view.
  return s.tab === "team" ? (
    <Team key="team" s={s} onSelect={onSelect} />
  ) : s.tab === "activity" ? (
    <Activity key="activity" s={s} />
  ) : s.tab === "checklist" ? (
    <Checklist key="checklist" s={s} />
  ) : (
    <Log key="log" s={s} />
  );
}

start((root, onSelect) => ({
  render(s) {
    render(<Main s={s} onSelect={onSelect} />, root);
  },
}));
