import { attr, child, cls, html, list, type ListNode, on, text, type ViewNodeOf } from "../herdsman-blocks-view.ts";
// The main column: a route with its head, its tabs and the open tab's body,
// the tab bodies main.ts does not keep as components (checklist, run state,
// activity), and the session view of every agent. main.ts decides what shows
// and owns the components; every function here is data to a view.
import { icon, type IconNode, icons } from "../icons.ts";
import { ago, checklistCounts, type Index, type Member } from "../model.ts";
import type { App, RouteTab } from "../store.ts";
import type { Route, Snapshot } from "../types.ts";
import { pill, type PillNode, toast } from "../ui.ts";
import { workspaceName } from "./shell.ts";
import { card, type CardNode, type TeamNode } from "./team.ts";

// ---------------------------------------------------------------------------
// The route shell
// ---------------------------------------------------------------------------

// The route folder as a chip: the project it belongs to, the full path on
// hover, and a button that copies the path.
const Location = html`<span class="location" title=${attr()}>
  ${child<IconNode>()}<span class="location-name">${text()}</span><span class="location-tail">${text()}</span
  ><button
    class="chip-btn"
    title="Copy the route folder path"
    aria-label="Copy the route folder path"
    onclick=${on<"click">()}
  >
    ${child<IconNode>()}
  </button>
</span>`;

function locationChip(dir: string): ViewNodeOf<typeof Location> {
  const parts = dir.split("/").filter(Boolean);
  const at = parts.lastIndexOf(".herdsman");
  const project = at > 0 ? parts[at - 1] : parts.at(-2);
  return Location([
    dir,
    icon(icons.folder),
    project ?? dir,
    at > 0 ? "/.herdsman" : "",
    async () => {
      try {
        await navigator.clipboard.writeText(dir);
        toast("Copied the route folder path.");
      } catch {
        toast("The browser blocked the clipboard.", "error");
      }
    },
    icon(icons.copy),
  ]);
}

/** The progress bar's tip: what is done, what stopped applying, what is open. */
function progressTip(c: ReturnType<typeof checklistCounts>): string {
  return [
    `${c.done} of ${c.total} checklist lines done`,
    c.skipped ? `${c.skipped} not applicable` : "",
    c.open ? `${c.open} open` : "",
  ]
    .filter(Boolean)
    .join(", ");
}

const Progress = html`<div class="progress" title=${attr()}>
  <div class="bar"><span style=${attr()}></span></div>
  <small>${text()}</small>
</div>`;

// prettier-ignore
const RouteShell = html`<div class="route-shell">
  <div class="route-head">
    <div>
      <h1>${text()}</h1>
      <p class="sub"><span class=${cls()}>${text()}</span>${child<ViewNodeOf<typeof Location>>()}<span class="meta-item" title=${attr()}>${child<IconNode>()}${text()}</span></p>
    </div>
    ${child<ViewNodeOf<typeof Progress> | null>()}
  </div>
  ${child<HTMLDivElement>()}
  <div class="tab-body">${child<TabBody>()}</div>
</div>`;

/** What `routeShell` returns. */
export type RouteShellNode = ViewNodeOf<typeof RouteShell>;

/**
 * The open tab's body: an element a component keeps (Docs, Changes,
 * Artifacts, Messages), the Team board, or one of the views below.
 */
export type TabBody = Element | TeamNode | ChecklistNode | LogNode | ActivityNode | MutedNode;

/**
 * A route: its title, state, folder, age and checklist progress, the tab row
 * (an element main.ts keeps, so it can scroll the open tab into view) and
 * the open tab's body.
 */
export function routeShell(r: Route, tabs: HTMLDivElement, body: TabBody): RouteShellNode {
  // A `[-]` line is settled with its reason, as the route's close check counts it.
  const c = checklistCounts(r.checklist ?? []);
  const done = c.done + c.skipped;
  return RouteShell([
    r.title || r.name,
    r.live ? "badge live" : "badge",
    r.live ? "Live" : "Not running",
    locationChip(r.dir),
    new Date(r.modified).toLocaleString(),
    icon(icons.clock),
    ago(r.modified),
    c.total ? Progress([progressTip(c), `width:${Math.round((done / c.total) * 100)}%`, `${done}/${c.total}`]) : null,
    tabs,
    body,
  ]);
}

// ---------------------------------------------------------------------------
// The tab row
// ---------------------------------------------------------------------------

const tabTips: Record<RouteTab, string> = {
  team: "The agents of this route and what each is doing",
  checklist: "The route's checklist lines",
  log: "The orchestrator's run log, newest first",
  docs: "Contracts, reports and notes in the route folder",
  changes: "The code each task changed against its base branch",
  artifacts: "HTML pages in the route's workspace folders: reports, reviews, plans",
  messages: "Herdsman messages between the adjutant and the orchestrator",
  activity: "Agent and web events since this page opened",
};
const tabs: readonly { id: RouteTab; label: string; glyph: readonly string[] }[] = [
  { id: "team", label: "Team", glyph: icons.users },
  { id: "checklist", label: "Checklist", glyph: icons.check },
  { id: "log", label: "Run state", glyph: icons.list },
  { id: "docs", label: "Docs", glyph: icons.file },
  { id: "changes", label: "Changes", glyph: icons.diff },
  { id: "artifacts", label: "Artifacts", glyph: icons.artifact },
  { id: "messages", label: "Messages", glyph: icons.message },
  { id: "activity", label: "Activity", glyph: icons.activity },
];

const TabCount = html`<span class="tab-count" aria-label=${attr()}>${text()}</span>`;
const Tab = html`<button
  role="tab"
  class=${cls()}
  title=${attr()}
  aria-selected=${attr<"true" | "false">()}
  onclick=${on<"click">()}
>
  ${child<IconNode>()}${text()}${child<ViewNodeOf<typeof TabCount> | null>()}
</button>`;
const Tabs = html`<div class="tabs" role="tablist">${child<ListNode<ViewNodeOf<typeof Tab>, RouteTab>>()}</div>`;

/** What `tabRow` returns. */
export type TabsNode = ViewNodeOf<typeof Tabs>;

/**
 * The route's tabs, keyed by tab. Messages shows for a route with an
 * adjutant, when a channel is set up, or while it is open; it counts the
 * unread messages.
 */
export function tabRow(r: Route, app: App, unread: number, pick: (tab: RouteTab) => void): TabsNode {
  const withMessages = !!r.adjutant_pane || !!app.channel?.configured;
  const shown = tabs.filter(({ id }) => id !== "messages" || withMessages || app.view.tab === "messages");
  return Tabs([
    list(
      shown,
      (t) => t.id,
      (t) =>
        Tab([
          app.view.tab === t.id ? "tab active" : "tab",
          tabTips[t.id],
          app.view.tab === t.id ? "true" : "false",
          () => pick(t.id),
          icon(t.glyph),
          t.label,
          t.id === "messages" && unread ? TabCount([`${unread} unread`, String(unread)]) : null,
        ]),
    ),
  ]);
}

// ---------------------------------------------------------------------------
// Tab bodies
// ---------------------------------------------------------------------------

const Muted = html`<p class="muted pad">${text()}</p>`;
type MutedNode = ViewNodeOf<typeof Muted>;

const CheckId = html`<code>${text()}</code>`;
const CheckLine = html`<li class=${cls()} title=${attr<string | null>()}>
  <span class="box" aria-label=${attr()}>${child<IconNode | string | null>()}</span
  >${child<ViewNodeOf<typeof CheckId> | null>()}<span>${text()}</span>
</li>`;
const Checklist = html`<ul class="checklist">
  ${child<ListNode<ViewNodeOf<typeof CheckLine>, string>>()}
</ul>`;
type ChecklistNode = ViewNodeOf<typeof Checklist>;

export function checklistView(r: Route): ChecklistNode | MutedNode {
  const items = r.checklist ?? [];
  if (!items.length) return Muted(["This route has no checklist."]);
  return Checklist([
    list(
      items,
      (i) => i.id || `text:${i.text}`,
      (i) =>
        CheckLine([
          i.done ? "done" : i.skipped ? "skipped" : "",
          i.skipped ? "Stopped applying; the reason follows the line" : null,
          i.done ? "Done" : i.skipped ? "Not applicable" : "Open",
          i.done ? icon(icons.check) : i.skipped ? "–" : null,
          i.id ? CheckId([i.id]) : null,
          i.text,
        ]),
    ),
  ]);
}

const LogLine = html`<li class=${cls()}>
  <time>${text()}</time>
  <p>${text()}</p>
</li>`;
const Log = html`<ol class="timeline">
  ${child<ListNode<ViewNodeOf<typeof LogLine>, string>>()}
</ol>`;
type LogNode = ViewNodeOf<typeof Log>;

export function logView(r: Route): LogNode | MutedNode {
  const lines = [...(r.run_state ?? [])].reverse();
  if (!lines.length) return Muted(["No run-state lines yet."]);
  return Log([
    list(
      lines,
      (l) => `${l.time}|${l.text}`,
      (l) => LogLine([l.text.startsWith("web:") ? "web" : "", l.time, l.text]),
    ),
  ]);
}

const clock = new Intl.DateTimeFormat(undefined, {
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hourCycle: "h23",
});

/** "pane.agent_status_changed" -> "agent status changed", "tab_focused" -> "tab focused". */
function eventName(kind: string): string {
  if (kind === "pane_closed") return "pane deleted"; // the word the app uses for it
  return kind.replace(/^pane\./, "").replace(/[._]/g, " ");
}

const Chip = html`<span class=${cls()}>${text()}</span>`;
const Said = html`<span>${text()}</span>`;
const Who = html`<strong>${text()}</strong>`;
type ChipNode = ViewNodeOf<typeof Chip>;
type WhoNode = ViewNodeOf<typeof Who>;
// prettier-ignore
const EventItem = html`<li class=${cls()}>
  <time datetime=${attr()} title=${attr()}>${text()}</time>
  <p class="event">${child<ChipNode | WhoNode | null>()}${child<ViewNodeOf<typeof Said> | PillNode | WhoNode | null>()}</p>
</li>`;
const Activity = html`<ol class="timeline activity">
  ${child<ListNode<ViewNodeOf<typeof EventItem>, string>>()}
</ol>`;
type ActivityNode = ViewNodeOf<typeof Activity>;

/** Agent and web events of these members since the page opened, newest first. */
export function activityView(app: App, members: Member[]): ActivityNode | MutedNode {
  const byPane = new Map(members.map((m) => [m.pane_id, m]));
  const items = app.entries
    .filter((e) => !e.pane_id || byPane.has(e.pane_id))
    .slice(-200)
    .reverse();
  if (!items.length) return Muted(["Nothing has happened since this page opened."]);
  return Activity([
    list(
      items,
      (e) => `${e.at}|${e.kind}|${e.pane_id ?? ""}|${e.status ?? ""}|${e.text ?? ""}`,
      (e) => {
        const who = e.pane_id ? Who([byPane.get(e.pane_id)?.title ?? e.pane_id]) : null;
        const [first, second]: [ChipNode | WhoNode | null, ViewNodeOf<typeof Said> | PillNode | WhoNode | null] =
          e.source === "web"
            ? [Chip(["chip accent", "web"]), Said([e.text ?? ""])]
            : e.status
              ? [who, pill(e.status)]
              : [Chip(["chip", eventName(e.kind)]), who];
        return EventItem([
          e.source,
          e.at,
          new Date(e.at).toLocaleString(),
          clock.format(new Date(e.at)),
          first,
          second,
        ]);
      },
    ),
  ]);
}

// ---------------------------------------------------------------------------
// The session view: every agent, by herdr tab
// ---------------------------------------------------------------------------

// The cards of one herdr tab, keyed by pane, so a card keeps its node (and
// focus) when its agent's status changes or the cards reorder.
const Cards = html`<div class="cards">${child<ListNode<CardNode, string>>()}</div>`;

function cardGrid(ms: Member[], app: App): ViewNodeOf<typeof Cards> {
  return Cards([
    list(
      ms,
      (m) => m.pane_id,
      (m) => card(m, app),
    ),
  ]);
}

// prettier-ignore
const Group = html`<section class="group">
  <h2>${text()}<span class="count">${text()}</span></h2>
  ${child<ViewNodeOf<typeof Cards>>()}
</section>`;
const ShellRow = html`<button class="shell-row" title=${attr()} onclick=${on<"click">()}>
  <code>${text()}</code><span>${text()}</span><span class="muted clip">${text()}</span>
</button>`;
const IdleWorkspace = html`<details class="workspace-row">
  <summary title=${attr()}><code>${text()}</code><span>${text()}</span><span class="muted">${text()}</span></summary>
  ${child<ListNode<ViewNodeOf<typeof ShellRow>, string>>()}
</details>`;
const Idle = html`<details class="group shells">
  <summary>${text()}</summary>
  <div class="shell-list">${child<ListNode<ViewNodeOf<typeof IdleWorkspace>, string>>()}</div>
</details>`;
const Session = html`<div>
  <div class="route-head">
    <div>
      <h1>${text()}</h1>
      <p class="sub">${text()}</p>
    </div>
  </div>
  <div class="board">
    ${child<ListNode<ViewNodeOf<typeof Group>, string>>()}${child<ViewNodeOf<typeof Idle> | null>()}
  </div>
</div>`;

/** What `sessionView` returns. */
export type SessionNode = ViewNodeOf<typeof Session>;

/**
 * Every agent in the herdr session (or in the workspace the sidebar picked),
 * grouped by herdr tab, and the workspaces where no pane runs an agent.
 */
export function sessionView(app: App, snap: Snapshot, idx: Index, open: (pane: string) => void): SessionNode {
  // The sidebar can narrow this view to one workspace.
  const only = (snap.workspaces ?? []).find((w) => w.workspace_id === app.view.ws);
  const inView = (wsId: string) => !only || wsId === only.workspace_id;
  const agents = (snap.agents ?? []).filter((a) => inView(a.workspace_id));
  const wsLabel = new Map((snap.workspaces ?? []).map((w) => [w.workspace_id, workspaceName(w)]));
  const byTab = new Map<string, Member[]>();
  for (const a of agents) {
    const m = idx.member(a.pane_id, "agent", null);
    byTab.set(a.tab_id, [...(byTab.get(a.tab_id) ?? []), m]);
  }
  // Workspaces where no pane runs an agent, each with its panes.
  const withAgent = new Set((snap.agents ?? []).map((a) => a.workspace_id));
  const idle = (snap.workspaces ?? [])
    .filter((w) => inView(w.workspace_id) && !withAgent.has(w.workspace_id))
    .map((w) => ({
      w,
      panes: (snap.panes ?? []).filter((p) => p.workspace_id === w.workspace_id),
    }));
  const tabCount = (snap.tabs ?? []).filter((t) => inView(t.workspace_id)).length;
  return Session([
    only ? workspaceName(only) : "All agents",
    `${agents.length} agents in ${tabCount} tabs · ${only ? only.workspace_id : `herdr ${snap.version}`}`,
    list(
      [...byTab],
      ([tabId]) => tabId,
      ([tabId, ms]) => {
        const t = idx.tabs.get(tabId);
        return Group([
          only ? t?.label || tabId : `${wsLabel.get(t?.workspace_id ?? "") ?? ""} › ${t?.label || tabId}`,
          String(ms.length),
          cardGrid(ms, app),
        ]);
      },
    ),
    idle.length
      ? Idle([
          `Workspaces without an agent (${idle.length})`,
          list(
            idle,
            ({ w }) => w.workspace_id,
            ({ w, panes }) => {
              const tabs = new Set(panes.map((p) => p.tab_id)).size;
              return IdleWorkspace([
                `${w.label || w.workspace_id}: ${tabs} tabs, ${panes.length} panes, no agent`,
                w.workspace_id,
                w.label || "",
                `${tabs} ${tabs === 1 ? "tab" : "tabs"} · ${panes.length} ${panes.length === 1 ? "pane" : "panes"}`,
                list(
                  panes,
                  (p) => p.pane_id,
                  (p) =>
                    ShellRow([
                      `Open pane ${p.pane_id}${p.cwd ? ` in ${p.cwd}` : ""}`,
                      () => open(p.pane_id),
                      p.pane_id,
                      p.label || idx.tabs.get(p.tab_id)?.label || "",
                      p.cwd ?? "",
                    ]),
                ),
              ]);
            },
          ),
        ])
      : null,
  ]);
}
