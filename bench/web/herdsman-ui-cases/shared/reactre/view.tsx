import { For, Match, memo, selector, Show, Switch } from "@reactre/core";

import {
  checkKey,
  eventKey,
  lanes,
  lineKey,
  statusLabel,
  statusOrder,
  type Event,
  type Member,
  type State,
} from "../state.ts";
import { cachedKey, rows, stableList } from "./rows.ts";

function cardTask(task: string | null) {
  return task === null ? null : <div class="card-task">{task}</div>;
}

function Team(props: {
  members: readonly Member[];
  selected: string | null;
  onSelect(pane: string): void;
}) {
  const selected = selector(() => props.selected);
  const members = rows(
    () => props.members,
    (member) => member.pane,
    (a, b) =>
      a.pane === b.pane &&
      a.title === b.title &&
      a.role === b.role &&
      a.status === b.status &&
      a.task === b.task &&
      a.sub === b.sub &&
      a.tab === b.tab &&
      a.term === b.term,
  );
  const groups = memo(() => lanes([...members()]));
  // Columns are stable read-only projections. A card move only wakes the
  // affected lists and counts; unchanged columns do not touch the DOM.
  const columns = statusOrder.map((status) => {
    const items = stableList(
      () => groups().find((lane) => lane.status === status)?.members ?? [],
    );
    return {
      status,
      label: statusLabel[status],
      members: items,
      count: memo(() => items().length),
    };
  });
  const shown = stableList(() => {
    const active = groups();
    return columns.filter((column) =>
      active.some((lane) => lane.status === column.status),
    );
  });
  return (
    <div class="team">
      <div class="kanban">
        <For each={shown()} key={(column) => column.status}>
          {(column) => (
            <section
              class={`lane s-${column.status}`}
              aria-label={column.label}
            >
              <h2>
                <span class="lane-dot" aria-hidden="true" />
                {column.label}
                <span class="count">{column.count()}</span>
              </h2>
              <For each={column.members()} key={(m) => m.pane}>
                {(m) => (
                  <button
                    class={`card r-${m.role} s-${m.status}${selected(m.pane) ? " selected" : ""}`}
                    title={`Open ${m.title} (${m.pane})`}
                    data-pane={m.pane}
                    onClick={() => props.onSelect(m.pane)}
                  >
                    <div class="card-top">
                      <span class="card-title">{m.title}</span>
                      <span class={`pill s-${m.status}`}>
                        <span class="dot" aria-hidden="true" />
                        {column.label}
                      </span>
                    </div>
                    {cardTask(m.task)}
                    <div class="card-sub">{m.sub}</div>
                    <div class="card-foot">
                      <code>{m.pane}</code>
                      <span class="muted">{m.tab}</span>
                      <span class="muted clip" title={m.term}>
                        {m.term}
                      </span>
                    </div>
                  </button>
                )}
              </For>
              {column.count() === 0 ? <p class="lane-empty">None</p> : null}
            </section>
          )}
        </For>
      </div>
    </div>
  );
}

type ViewState = {
  [K in keyof State]: State[K] extends (infer V)[] ? readonly V[] : State[K];
};

// Each immutable event body is one flat template. Its shape is chosen once
// for the row, so removal disposes one body rather than nested fragment slots.
function eventBody(e: Event, statusText: string) {
  if (e.source === "web") {
    return (
      <p class="event">
        <span class="chip accent">web</span>
        <span>{e.text ?? ""}</span>
      </p>
    );
  }
  if (e.status) {
    if (e.who !== null) {
      return (
        <p class="event">
          <strong>{e.who}</strong>
          <span class={`pill s-${e.status}`}>
            <span class="dot" aria-hidden="true" />
            {statusText}
          </span>
        </p>
      );
    }
    return (
      <p class="event">
        <span class={`pill s-${e.status}`}>
          <span class="dot" aria-hidden="true" />
          {statusText}
        </span>
      </p>
    );
  }
  if (e.who !== null) {
    return (
      <p class="event">
        <span class="chip">{e.label}</span>
        <strong>{e.who}</strong>
      </p>
    );
  }
  return (
    <p class="event">
      <span class="chip">{e.label}</span>
    </p>
  );
}

function Activity(props: { state: ViewState }) {
  const key = cachedKey(eventKey);
  const items = rows(
    () => props.state.events,
    eventKey,
    (a, b) =>
      a.at === b.at &&
      a.clock === b.clock &&
      a.source === b.source &&
      a.kind === b.kind &&
      a.label === b.label &&
      a.pane === b.pane &&
      a.who === b.who &&
      a.status === b.status &&
      a.text === b.text,
  );
  return (
    <ol class="timeline activity">
      <For each={items()} key={key}>
        {(e) => (
          <li class={e.source}>
            <time datetime={e.at}>{e.clock}</time>
            {eventBody(e, e.status === null ? "" : statusLabel[e.status])}
          </li>
        )}
      </For>
    </ol>
  );
}

function Checklist(props: { state: ViewState }) {
  const items = rows(
    () => props.state.checklist,
    checkKey,
    (a, b) =>
      a.id === b.id &&
      a.text === b.text &&
      a.done === b.done &&
      a.skipped === b.skipped,
  );
  return (
    <ul class="checklist">
      <For each={items()} key={checkKey}>
        {(c) => (
          <li class={c.done ? "done" : c.skipped ? "skipped" : ""}>
            <span
              class="box"
              aria-label={
                c.done ? "Done" : c.skipped ? "Not applicable" : "Open"
              }
            >
              {c.done ? "✓" : c.skipped ? "–" : ""}
            </span>
            <Show when={c.id}>
              <code>{c.id}</code>
            </Show>
            <span>{c.text}</span>
          </li>
        )}
      </For>
    </ul>
  );
}

function Log(props: { state: ViewState }) {
  // Plain prefix reads let the compiler fill a static class hole. A method call
  // here is conservatively reactive and allocates an observer for every line.
  return (
    <ol class="timeline">
      <For each={props.state.log} key={lineKey}>
        {(l) => (
          <li
            class={
              l.text[0] === "w" &&
              l.text[1] === "e" &&
              l.text[2] === "b" &&
              l.text[3] === ":"
                ? "web"
                : ""
            }
          >
            <time>{l.time}</time>
            <p>{l.text}</p>
          </li>
        )}
      </For>
    </ol>
  );
}

export function Tabs(props: {
  state: ViewState;
  onSelect(pane: string): void;
}) {
  return (
    <Switch>
      <Match when={props.state.tab === "team"}>
        <Team
          members={props.state.members}
          selected={props.state.selected}
          onSelect={props.onSelect}
        />
      </Match>
      <Match when={props.state.tab === "activity"}>
        <Activity state={props.state} />
      </Match>
      <Match when={props.state.tab === "checklist"}>
        <Checklist state={props.state} />
      </Match>
      <Match when={props.state.tab === "log"}>
        <Log state={props.state} />
      </Match>
    </Switch>
  );
}
