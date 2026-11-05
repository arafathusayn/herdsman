// Solid 2.0 (release candidate) as an expert would write herdsman web's
// screens: the fresh snapshot goes into one signal, and each keyed list is a
// derived store, createProjection(() => snap().list, [], { key }), so every
// snapshot reconciles into it by key and surviving rows keep their store
// proxies (and so their DOM). Selection is a projection too (2.0's
// createSelector). <For> keyed by item identity renders the lists;
// Show/Switch the branches. Compiled by babel-preset-solid 2.0 (see build.ts).

import { createMemo, createProjection, createSignal, flush, For, Match, Show, Switch } from "solid-js";
import { render } from "@solidjs/web";
import { start } from "../../shared/driver.ts";
import { checkKey, eventKey, lanes, lineKey, statusLabel } from "../../shared/state.ts";

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
      class={`card r-${m.role} s-${m.status}${props.selected ? " selected" : ""}`}
      title={`Open ${m.title} (${m.pane})`}
      data-pane={m.pane}
      onClick={() => props.onSelect(m.pane)}
    >
      <div class="card-top">
        <span class="card-title">{m.title}</span>
        <Pill status={m.status} />
      </div>
      <Show when={m.task !== null}>
        <div class="card-task">{m.task}</div>
      </Show>
      <div class="card-sub">{m.sub}</div>
      <div class="card-foot">
        <code>{m.pane}</code>
        <span class="muted">{m.tab}</span>
        <span class="muted clip" title={m.term}>
          {m.term}
        </span>
      </div>
    </button>
  );
};

const Team = (props) => {
  const { snap, onSelect } = props;
  const members = createProjection(() => snap().members, [], { key: "pane" });
  // createSelector in 2.0: a projection that flips only the two panes involved.
  let last = null;
  const selected = createProjection(
    (draft) => {
      const pane = snap().selected;
      if (pane === last) return;
      if (last !== null) delete draft[last];
      if (pane !== null) draft[pane] = true;
      last = pane;
    },
    {},
  );
  // The lanes shown, by status; strings, so <For> keeps each section.
  const shown = createMemo(() => lanes(members).map((l) => l.status));
  return (
    <div class="team">
      <div class="kanban">
        <For each={shown()}>
          {(status) => {
            const inLane = createMemo(() => members.filter((m) => m.status === status));
            return (
              <section class={`lane s-${status}`} aria-label={statusLabel[status]}>
                <h2>
                  <span class="lane-dot" aria-hidden="true" />
                  {statusLabel[status]}
                  <span class="count">{inLane().length}</span>
                </h2>
                <For each={inLane()} fallback={<p class="lane-empty">None</p>}>
                  {(m) => <Card m={m} selected={selected[m.pane]} onSelect={onSelect} />}
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
    <li class={e.source}>
      <time datetime={e.at}>{e.clock}</time>
      <p class="event">
        <Switch>
          <Match when={e.source === "web"}>
            <span class="chip accent">web</span>
            <span>{e.text ?? ""}</span>
          </Match>
          <Match when={e.status}>
            <Show when={e.who !== null}>
              <strong>{e.who}</strong>
            </Show>
            <Pill status={e.status} />
          </Match>
          <Match when={true}>
            <span class="chip">{e.label}</span>
            <Show when={e.who !== null}>
              <strong>{e.who}</strong>
            </Show>
          </Match>
        </Switch>
      </p>
    </li>
  );
};

const Activity = (props) => {
  const events = createProjection(() => props.snap().events, [], { key: eventKey });
  return (
    <ol class="timeline activity">
      <For each={events}>{(e) => <EventItem e={e} />}</For>
    </ol>
  );
};

const CheckItem = (props) => {
  const c = props.c;
  return (
    <li class={c.done ? "done" : c.skipped ? "skipped" : ""}>
      <span class="box" aria-label={c.done ? "Done" : c.skipped ? "Not applicable" : "Open"}>
        {c.done ? "✓" : c.skipped ? "–" : ""}
      </span>
      <Show when={c.id}>
        <code>{c.id}</code>
      </Show>
      <span>{c.text}</span>
    </li>
  );
};

const Checklist = (props) => {
  const checklist = createProjection(() => props.snap().checklist, [], { key: checkKey });
  return (
    <ul class="checklist">
      <For each={checklist}>{(c) => <CheckItem c={c} />}</For>
    </ul>
  );
};

const LineItem = (props) => {
  const l = props.l;
  return (
    <li class={l.text.startsWith("web:") ? "web" : ""}>
      <time>{l.time}</time>
      <p>{l.text}</p>
    </li>
  );
};

const Log = (props) => {
  const log = createProjection(() => props.snap().log, [], { key: lineKey });
  return (
    <ol class="timeline">
      <For each={log}>{(l) => <LineItem l={l} />}</For>
    </ol>
  );
};

start((root, onSelect) => {
  const [snap, setSnap] = createSignal(null);
  render(() => {
    const tab = createMemo(() => snap()?.tab);
    return (
      <Switch>
        <Match when={tab() === "team"}>
          <Team snap={snap} onSelect={onSelect} />
        </Match>
        <Match when={tab() === "activity"}>
          <Activity snap={snap} />
        </Match>
        <Match when={tab() === "checklist"}>
          <Checklist snap={snap} />
        </Match>
        <Match when={tab() === "log"}>
          <Log snap={snap} />
        </Match>
      </Switch>
    );
  }, root);
  return {
    render(s) {
      setSnap(s);
      // 2.0 batches writes to the next microtask; apply this render now.
      flush();
    },
  };
});
