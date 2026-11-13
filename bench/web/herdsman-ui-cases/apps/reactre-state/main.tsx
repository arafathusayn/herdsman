import { attach, For, memo, selector, Show } from "@reactre/core";
import { useActor } from "@reactre/state/signals";

import { start } from "../../shared/driver.ts";
import {
  checkKey,
  eventKey,
  lanes,
  lineKey,
  statusLabel,
  type Event,
  type Member,
  type State,
} from "../../shared/state.ts";
import { rows } from "./rows.ts";
import { viewMachine } from "./view.machine.ts";

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
  );
  const shown = memo(() => lanes([...members()]).map((lane) => lane.status));
  return (
    <div class="team">
      <div class="kanban">
        <For each={shown()} key={(status) => status}>
          {(status) => (
            <section
              class={`lane s-${status}`}
              aria-label={statusLabel[status]}
            >
              <h2>
                <span class="lane-dot" aria-hidden="true" />
                {statusLabel[status]}
                <span class="count">
                  {members().filter((m) => m.status === status).length}
                </span>
              </h2>
              <For
                each={members().filter((m) => m.status === status)}
                key={(m) => m.pane}
                fallback={<p class="lane-empty">None</p>}
              >
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
                        {statusLabel[m.status]}
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

function eventBody(e: Event) {
  const who = e.who === null ? null : <strong>{e.who}</strong>;
  const body =
    e.source === "web" ? (
      <>
        <span class="chip accent">web</span>
        <span>{e.text ?? ""}</span>
      </>
    ) : e.status ? (
      <>
        {who}
        <span class={`pill s-${e.status}`}>
          <span class="dot" aria-hidden="true" />
          {statusLabel[e.status]}
        </span>
      </>
    ) : (
      <>
        <span class="chip">{e.label}</span>
        {who}
      </>
    );
  return body;
}

function Activity(props: { state: ViewState }) {
  const items = rows(() => props.state.events, eventKey);
  return (
    <ol class="timeline activity">
      <For each={items()} key={eventKey}>
        {(e) => (
          <li class={e.source}>
            <time datetime={e.at}>{e.clock}</time>
            <p class="event">{eventBody(e)}</p>
          </li>
        )}
      </For>
    </ol>
  );
}

function Checklist(props: { state: ViewState }) {
  const items = rows(() => props.state.checklist, checkKey);
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
  return (
    <ol class="timeline">
      <For each={props.state.log} key={lineKey}>
        {(l) => (
          <li class={l.text.startsWith("web:") ? "web" : ""}>
            <time>{l.time}</time>
            <p>{l.text}</p>
          </li>
        )}
      </For>
    </ol>
  );
}

function Tabs(props: { state: ViewState; onSelect(pane: string): void }) {
  return (
    <>
      <Show when={props.state.tab === "team"}>
        <Team
          members={props.state.members}
          selected={props.state.selected}
          onSelect={props.onSelect}
        />
      </Show>
      <Show when={props.state.tab === "activity"}>
        <Activity state={props.state} />
      </Show>
      <Show when={props.state.tab === "checklist"}>
        <Checklist state={props.state} />
      </Show>
      <Show when={props.state.tab === "log"}>
        <Log state={props.state} />
      </Show>
    </>
  );
}

start((root, onSelect) => {
  let render!: (state: State) => void;
  const dispose = attach(() => {
    const [snapshot, actor] = useActor(viewMachine);
    render = (next) => {
      actor.send({ type: "render", next });
    };
    const state = memo(() => {
      const s = snapshot();
      return s.state === "showing" ? s.data.value : null;
    });
    return (
      <Show when={state()}>
        {(s) => <Tabs state={s()} onSelect={onSelect} />}
      </Show>
    );
  }, root);
  window.addEventListener("pagehide", dispose, { once: true });
  return { render: (state) => render(state) };
});
