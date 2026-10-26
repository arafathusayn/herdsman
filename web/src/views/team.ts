// The Team tab: the route's agents as a kanban board, in columns by role or
// by status, and the agent card the session view shares.
import {
  attr,
  child,
  cls,
  html,
  list,
  type ListNode,
  on,
  render,
  text,
  type ViewNodeOf,
} from "../herdsman-blocks-view.ts";
import { type BoardBy, type Lane, lanes, type Member, roleName } from "../model.ts";
import { type App, store } from "../store.ts";
import { pill, type PillNode } from "../ui.ts";

const byKey = "herdsman.team-board";

function loadBy(): BoardBy {
  try {
    return localStorage.getItem(byKey) === "status" ? "status" : "role";
  } catch {
    return "role";
  }
}

let boardBy = loadBy();

const CardTask = html`<div class="card-task">${text()}</div>`;
const CardSub = html`<div class="card-sub">${text()}</div>`;
const CardTab = html`<span class="muted">${text()}</span>`;
const CardTitle = html`<span class="muted clip" title=${attr()}>${text()}</span>`;
const Card = html`<button class=${cls()} title=${attr()} data-pane=${attr()} onclick=${on<"click">()}>
  <div class="card-top"><span class="card-title">${text()}</span>${child<PillNode>()}</div>
  ${child<ViewNodeOf<typeof CardTask> | null>()}${child<ViewNodeOf<typeof CardSub> | null>()}
  <div class="card-foot">
    <code>${text()}</code>${child<ViewNodeOf<typeof CardTab> | null>()}${child<ViewNodeOf<typeof CardTitle> | null>()}
  </div>
</button>`;

/** What `card` returns. */
export type CardNode = ViewNodeOf<typeof Card>;

/** An agent's card; with role, it also says what the agent does in the route. */
export function card(m: Member, app: App, role = false): CardNode {
  const sub = [role ? roleName[m.role] : "", m.agent?.display_agent ?? m.agent?.agent, m.task?.branch]
    .filter(Boolean)
    .join(" · ");
  const title = m.agent?.terminal_title_stripped ?? m.pane?.terminal_title_stripped ?? "";
  return Card([
    `card r-${m.role} s-${m.status}${app.view.pane === m.pane_id ? " selected" : ""}`,
    `Open ${m.title} (${m.pane_id}): its output, keys and actions`,
    m.pane_id,
    () => store.go({ pane: m.pane_id }),
    m.title,
    pill(m.status),
    m.task?.task && m.task.task !== m.title ? CardTask([m.task.task]) : null,
    sub ? CardSub([sub]) : null,
    m.pane_id,
    m.tab ? CardTab([m.tab.label]) : null,
    title ? CardTitle([title, title]) : null,
  ]);
}

const boards: readonly { by: BoardBy; label: string; tip: string }[] = [
  { by: "role", label: "Role", tip: "A column for each role in the route" },
  { by: "status", label: "Status", tip: "A column for each status; cards move as agents work" },
];

const BoardOption = html`<button
  type="button"
  class=${cls()}
  aria-pressed=${attr<"true" | "false">()}
  title=${attr()}
  onclick=${on<"click">()}
>
  ${text()}
</button>`;
const LaneDot = html`<span class="lane-dot" aria-hidden="true"></span>`;
const LaneEmpty = html`<p class="lane-empty">None</p>`;
const LaneView = html`<section class=${cls()} aria-label=${attr()}>
  <h2>${child<ViewNodeOf<typeof LaneDot> | null>()}${text()}<span class="count">${text()}</span></h2>
  ${child<ListNode<CardNode, string> | ViewNodeOf<typeof LaneEmpty>>()}
</section>`;
const Kanban = html`<div class="kanban">${child<ListNode<ViewNodeOf<typeof LaneView>, string>>()}</div>`;
const NoPanes = html`<p class="muted">This route's task table names no panes.</p>`;
const Team = html`<div class="team">
  <div class="team-bar">
    <div class="view-switch board-switch" role="group" aria-label="Columns">
      ${child<ListNode<ViewNodeOf<typeof BoardOption>, BoardBy>>()}
    </div>
  </div>
  ${child<ViewNodeOf<typeof Kanban> | ViewNodeOf<typeof NoPanes>>()}
</div>`;

/** What `teamBoard` returns. */
export type TeamNode = ViewNodeOf<typeof Team>;

function pickBoard(by: BoardBy): void {
  boardBy = by;
  try {
    localStorage.setItem(byKey, by);
  } catch {
    // The choice lasts for this page only.
  }
  store.emit();
}

function lane(l: Lane, app: App): ViewNodeOf<typeof LaneView> {
  return LaneView([
    l.status ? `lane s-${l.status}` : "lane",
    l.title,
    l.status ? LaneDot([]) : null,
    l.title,
    String(l.members.length),
    l.members.length
      ? list(
          l.members,
          (m) => m.pane_id,
          (m) => card(m, app, boardBy === "status"),
        )
      : LaneEmpty([]),
  ]);
}

/** The Team tab: the board switch and the lanes, each card keyed by its pane. */
export function teamBoard(members: Member[], app: App): TeamNode {
  return Team([
    list(
      boards,
      (b) => b.by,
      (b) =>
        BoardOption([
          b.by === boardBy ? "active" : "",
          b.by === boardBy ? "true" : "false",
          b.tip,
          () => pickBoard(b.by),
          b.label,
        ]),
    ),
    members.length
      ? Kanban([
          list(
            lanes(members, boardBy),
            (l) => l.id,
            (l) => lane(l, app),
          ),
        ])
      : NoPanes([]),
  ]);
}

// TEMPORARY, until main.ts places teamBoard() in a child hole: the board as
// one element that this module keeps and patches, so main.ts can still key
// it and patch() it into the route body like a persistent component.
const teamBox = document.createElement("div");
const isHTML = (n: Node | null): n is HTMLElement => n !== null && n.nodeType === 1;

let teamRoot: HTMLElement | null = null;

export function teamView(members: Member[], app: App): HTMLElement {
  render(teamBox, teamBoard(members, app));
  // render() keeps its instance on teamBox while the root lives in the route body.
  if (teamRoot === null) {
    const root = teamBox.firstChild;
    if (!isHTML(root)) throw new Error("teamView: the board rendered no element");
    teamRoot = root;
  }
  return teamRoot;
}
