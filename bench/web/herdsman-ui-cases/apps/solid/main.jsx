// Solid 1.9 as an expert would write herdsman web's screens for speed: each
// keyed list keeps one row object per key, with a signal per field, so a row
// keeps its identity (and so its DOM under <For>) while its key lives, and a
// changed field notifies only its own bindings. Each fresh snapshot is applied
// row by row: rows matched by key, fields set (equal values do not notify),
// new rows created, gone rows dropped. Show/Switch render the branches.
//
// This measured faster than the store idiom, createStore +
// setState(reconcile(snapshot, { key })): equal on create, poll and select,
// about twice as fast on event and logline, where reconcile walks every
// shifted index of the store array.
//
// Compiled by babel-preset-solid (see build.ts), as the official
// js-framework-benchmark entry is.

import { batch, createMemo, createSelector, createSignal, For, Match, Show, Switch } from "solid-js";
import { render } from "solid-js/web";
import { start } from "../../shared/driver.ts";
import { checkKey, eventKey, lanes, lineKey, statusLabel } from "../../shared/state.ts";

/** A row: an accessor per field of o, and set(next) to write every field. */
function createRow(o) {
  const row = {};
  const setters = {};
  for (const k in o) [row[k], setters[k]] = createSignal(o[k]);
  row.set = (next) => {
    for (const k in next) setters[k](next[k]);
  };
  return row;
}

/** A keyed list of rows: list() for <For>, update(items) to apply a snapshot's items. */
function createRows(keyOf) {
  let byKey = new Map();
  const [list, setList] = createSignal([]);
  return {
    list,
    update(items) {
      const next = new Map();
      const rows = new Array(items.length);
      for (let i = 0; i < items.length; i++) {
        const o = items[i];
        const k = keyOf(o);
        let row = byKey.get(k);
        if (row) row.set(o);
        else row = createRow(o);
        next.set(k, row);
        rows[i] = row;
      }
      byKey = next;
      setList(rows);
    },
  };
}

const Pill = (props) => (
  <span class={`pill s-${props.status}`}>
    <span class="dot" aria-hidden="true" />
    {statusLabel[props.status]}
  </span>
);

const Card = (props) => {
  const m = props.m;
  return (
    <button
      class={`card r-${m.role()} s-${m.status()}${props.selected ? " selected" : ""}`}
      title={`Open ${m.title()} (${m.pane()})`}
      data-pane={m.pane()}
      onClick={() => props.onSelect(m.pane())}
    >
      <div class="card-top">
        <span class="card-title">{m.title()}</span>
        <Pill status={m.status()} />
      </div>
      <Show when={m.task() !== null}>
        <div class="card-task">{m.task()}</div>
      </Show>
      <div class="card-sub">{m.sub()}</div>
      <div class="card-foot">
        <code>{m.pane()}</code>
        <span class="muted">{m.tab()}</span>
        <span class="muted clip" title={m.term()}>
          {m.term()}
        </span>
      </div>
    </button>
  );
};

const Team = (props) => {
  const { members, selected, onSelect } = props;
  const isSelected = createSelector(selected);
  // The lanes shown, by status (lanes() reads plain statuses); strings, so
  // <For> keeps each section.
  const shown = createMemo(() => lanes(members().map((m) => ({ status: m.status() }))).map((l) => l.status));
  return (
    <div class="team">
      <div class="kanban">
        <For each={shown()}>
          {(status) => {
            const inLane = createMemo(() => members().filter((m) => m.status() === status));
            return (
              <section class={`lane s-${status}`} aria-label={statusLabel[status]}>
                <h2>
                  <span class="lane-dot" aria-hidden="true" />
                  {statusLabel[status]}
                  <span class="count">{inLane().length}</span>
                </h2>
                <For each={inLane()} fallback={<p class="lane-empty">None</p>}>
                  {(m) => <Card m={m} selected={isSelected(m.pane())} onSelect={onSelect} />}
                </For>
              </section>
            );
          }}
        </For>
      </div>
    </div>
  );
};

const EventItem = (props) => {
  const e = props.e;
  return (
    <li class={e.source()}>
      <time datetime={e.at()}>{e.clock()}</time>
      <p class="event">
        <Switch>
          <Match when={e.source() === "web"}>
            <span class="chip accent">web</span>
            <span>{e.text() ?? ""}</span>
          </Match>
          <Match when={e.status()}>
            <Show when={e.who() !== null}>
              <strong>{e.who()}</strong>
            </Show>
            <Pill status={e.status()} />
          </Match>
          <Match when={true}>
            <span class="chip">{e.label()}</span>
            <Show when={e.who() !== null}>
              <strong>{e.who()}</strong>
            </Show>
          </Match>
        </Switch>
      </p>
    </li>
  );
};

const CheckItem = (props) => {
  const c = props.c;
  return (
    <li class={c.done() ? "done" : c.skipped() ? "skipped" : ""}>
      <span class="box" aria-label={c.done() ? "Done" : c.skipped() ? "Not applicable" : "Open"}>
        {c.done() ? "✓" : c.skipped() ? "–" : ""}
      </span>
      <Show when={c.id()}>
        <code>{c.id()}</code>
      </Show>
      <span>{c.text()}</span>
    </li>
  );
};

const LineItem = (props) => {
  const l = props.l;
  return (
    <li class={l.text().startsWith("web:") ? "web" : ""}>
      <time>{l.time()}</time>
      <p>{l.text()}</p>
    </li>
  );
};

start((root, onSelect) => {
  const [tab, setTab] = createSignal(null);
  const [selected, setSelected] = createSignal(null);
  const members = createRows((m) => m.pane);
  const events = createRows(eventKey);
  const checklist = createRows(checkKey);
  const log = createRows(lineKey);
  render(
    () => (
      <Switch>
        <Match when={tab() === "team"}>
          <Team members={members.list} selected={selected} onSelect={onSelect} />
        </Match>
        <Match when={tab() === "activity"}>
          <ol class="timeline activity">
            <For each={events.list()}>{(e) => <EventItem e={e} />}</For>
          </ol>
        </Match>
        <Match when={tab() === "checklist"}>
          <ul class="checklist">
            <For each={checklist.list()}>{(c) => <CheckItem c={c} />}</For>
          </ul>
        </Match>
        <Match when={tab() === "log"}>
          <ol class="timeline">
            <For each={log.list()}>{(l) => <LineItem l={l} />}</For>
          </ol>
        </Match>
      </Switch>
    ),
    root,
  );
  // Each render applies the shown tab's list only; the other lists wait until
  // their tab is shown, and are applied in that same batch.
  const shownList = { team: members, activity: events, checklist, log };
  const items = { team: "members", activity: "events", checklist: "checklist", log: "log" };
  return {
    render(s) {
      batch(() => {
        shownList[s.tab].update(s[items[s.tab]]);
        setTab(s.tab);
        setSelected(s.selected);
      });
    },
  };
});
