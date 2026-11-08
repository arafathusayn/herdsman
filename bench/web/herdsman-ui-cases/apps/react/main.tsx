// React 19 with the React Compiler, as an expert would write herdsman's screens
// for speed: plain function components, keyed lists, one createRoot, and no
// hand-written memo. The compiler (run by ./build.ts) memoizes each
// component's JSX on its props. Snapshots are fresh objects with equal values,
// so rows take their data as primitive props (spread from the item): the
// compiler's per-prop checks then hit and a row returns its cached element,
// which React skips without reconciling it.
//
// Every click renders the new snapshot with root.render inside flushSync, so
// the whole commit lands within the click (the toolbar's native listeners are
// outside React's event system; the card's onClick nests a render inside a
// React event).

import { flushSync } from "react-dom";
import { createRoot } from "react-dom/client";
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

function Pill({ status }: { status: Status }) {
  return (
    <span className={`pill s-${status}`}>
      <span className="dot" aria-hidden="true" />
      {statusLabel[status]}
    </span>
  );
}

function Card({ pane, title, role, status, task, sub, tab, term, selected, onSelect }: Member & { selected: boolean; onSelect: OnSelect }) {
  return (
    <button
      className={`card r-${role} s-${status}${selected ? " selected" : ""}`}
      title={`Open ${title} (${pane})`}
      data-pane={pane}
      onClick={() => onSelect(pane)}
    >
      <div className="card-top">
        <span className="card-title">{title}</span>
        <Pill status={status} />
      </div>
      {task !== null && <div className="card-task">{task}</div>}
      <div className="card-sub">{sub}</div>
      <div className="card-foot">
        <code>{pane}</code>
        <span className="muted">{tab}</span>
        <span className="muted clip" title={term}>
          {term}
        </span>
      </div>
    </button>
  );
}

function LaneView({ lane, selected, onSelect }: { lane: Lane; selected: string | null; onSelect: OnSelect }) {
  return (
    <section className={`lane s-${lane.status}`} aria-label={lane.title}>
      <h2>
        <span className="lane-dot" aria-hidden="true" />
        {lane.title}
        <span className="count">{lane.members.length}</span>
      </h2>
      {lane.members.length ? (
        lane.members.map((m) => <Card key={m.pane} {...m} selected={selected === m.pane} onSelect={onSelect} />)
      ) : (
        <p className="lane-empty">None</p>
      )}
    </section>
  );
}

function Team({ s, onSelect }: { s: State; onSelect: OnSelect }) {
  return (
    <div className="team">
      <div className="kanban">
        {lanes(s.members).map((lane) => (
          <LaneView key={lane.status} lane={lane} selected={s.selected} onSelect={onSelect} />
        ))}
      </div>
    </div>
  );
}

function EventItem({ at, clock, source, label, who, status, text }: Event) {
  const strong = who !== null ? <strong>{who}</strong> : null;
  return (
    <li className={source}>
      <time dateTime={at}>{clock}</time>
      <p className="event">
        {source === "web" ? (
          <>
            <span className="chip accent">web</span>
            <span>{text ?? ""}</span>
          </>
        ) : status ? (
          <>
            {strong}
            <Pill status={status} />
          </>
        ) : (
          <>
            <span className="chip">{label}</span>
            {strong}
          </>
        )}
      </p>
    </li>
  );
}

function Activity({ s }: { s: State }) {
  return (
    <ol className="timeline activity">
      {s.events.map((e) => (
        <EventItem key={eventKey(e)} {...e} />
      ))}
    </ol>
  );
}

function CheckItem({ id, text, done, skipped }: Check) {
  return (
    <li className={done ? "done" : skipped ? "skipped" : ""}>
      <span className="box" aria-label={done ? "Done" : skipped ? "Not applicable" : "Open"}>
        {done ? "✓" : skipped ? "–" : null}
      </span>
      {id ? <code>{id}</code> : null}
      <span>{text}</span>
    </li>
  );
}

function Checklist({ s }: { s: State }) {
  return (
    <ul className="checklist">
      {s.checklist.map((c) => (
        <CheckItem key={checkKey(c)} {...c} />
      ))}
    </ul>
  );
}

function LineItem({ time, text }: Line) {
  return (
    <li className={text.startsWith("web:") ? "web" : ""}>
      <time>{time}</time>
      <p>{text}</p>
    </li>
  );
}

function Log({ s }: { s: State }) {
  return (
    <ol className="timeline">
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

start((el, onSelect) => {
  const root = createRoot(el);
  return {
    render(s) {
      flushSync(() => root.render(<Main s={s} onSelect={onSelect} />));
    },
  };
});
