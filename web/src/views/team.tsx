import { For, memo, Show, signal } from "@reactre/core";
import { map } from "@reactre/core/features/lists";

import { Pill } from "../components/status.tsx";
import { type BoardBy, type Lane, lanes as groupLanes, type Member, roleName } from "../model.ts";
import { type App, store } from "../store.ts";

const byKey = "herdsman.team-board";
function loadBy(): BoardBy {
  try {
    return localStorage.getItem(byKey) === "status" ? "status" : "role";
  } catch {
    return "role";
  }
}
const [boardBy, setBoardBy] = signal(loadBy());

export function MemberCard(props: { member: Member; app: App; role?: boolean }) {
  const sub = memo(() =>
    [
      props.role ? roleName[props.member.role] : "",
      props.member.agent?.display_agent ?? props.member.agent?.agent,
      props.member.task?.branch,
    ]
      .filter(Boolean)
      .join(" · "),
  );
  const title = memo(
    () => props.member.agent?.terminal_title_stripped ?? props.member.pane?.terminal_title_stripped ?? "",
  );
  return (
    <button
      class={`card r-${props.member.role} s-${props.member.status}${props.app.view.pane === props.member.pane_id ? " selected" : ""}`}
      title={`Open ${props.member.title} (${props.member.pane_id}): its output, keys and actions`}
      data-pane={props.member.pane_id}
      onClick={() => store.go({ pane: props.member.pane_id })}
    >
      <div class="card-top">
        <span class="card-title" title={props.member.title}>
          {props.member.title}
        </span>
        <Pill status={props.member.status} />
      </div>
      <Show when={props.member.task?.task && props.member.task.task !== props.member.title}>
        <div class="card-task">{props.member.task?.task}</div>
      </Show>
      <Show when={sub()}>
        <div class="card-sub" title={sub()}>
          {sub()}
        </div>
      </Show>
      <div class="card-foot">
        <code>{props.member.pane_id}</code>
        <Show when={props.member.tab}>
          <span class="card-tab muted clip" title={props.member.tab?.label}>
            {props.member.tab?.label}
          </span>
        </Show>
        <Show when={title()}>
          <span class="muted clip" title={title()}>
            {title()}
          </span>
        </Show>
      </div>
    </button>
  );
}

const boards: readonly { by: BoardBy; label: string; tip: string }[] = [
  { by: "role", label: "Role", tip: "A column for each role in the route" },
  { by: "status", label: "Status", tip: "A column for each status; cards move as agents work" },
];

function TeamLane(props: { lane: Lane; app: App; by: BoardBy }) {
  const cards = map(
    () => props.lane.members,
    (member) => member,
    { keyed: (member) => member.pane_id },
  );
  return (
    <section class={props.lane.status ? `lane s-${props.lane.status}` : "lane"} aria-label={props.lane.title}>
      <h2>
        <Show when={props.lane.status}>
          <span class="lane-dot" aria-hidden="true" />
        </Show>
        {props.lane.title}
        <span class="count">{String(props.lane.members.length)}</span>
      </h2>
      <For each={cards()} key={(member) => member().pane_id} fallback={<p class="lane-empty">None</p>}>
        {(member) => <MemberCard member={member()} app={props.app} role={props.by === "status"} />}
      </For>
    </section>
  );
}

export function TeamBoard(props: { members: Member[]; app: App }) {
  const lanes = memo(() => groupLanes(props.members, boardBy()));
  const columns = map(lanes, (lane) => lane, {
    keyed: (lane) => lane.id,
  });
  const pick = (by: BoardBy): void => {
    setBoardBy(by);
    try {
      localStorage.setItem(byKey, by);
    } catch {
      /* Retained by the signal for this page. */
    }
  };
  return (
    <div class="team">
      <div class="team-bar">
        <div class="view-switch board-switch" role="group" aria-label="Columns">
          <For each={boards} key={(board) => board.by}>
            {(board) => (
              <button
                type="button"
                class={board.by === boardBy() ? "active" : ""}
                aria-pressed={board.by === boardBy() ? "true" : "false"}
                title={board.tip}
                onClick={() => pick(board.by)}
              >
                {board.label}
              </button>
            )}
          </For>
        </div>
      </div>
      <Show when={props.members.length > 0} fallback={<p class="muted">This route's task table names no panes.</p>}>
        <div class="kanban">
          <For each={columns()} key={(lane) => lane().id}>
            {(lane) => <TeamLane lane={lane()} app={props.app} by={boardBy()} />}
          </For>
        </div>
      </Show>
    </div>
  );
}
