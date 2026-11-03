import { For, memo, Show } from "@reactre/core";
import { map } from "@reactre/core/features/lists";
import type { Child } from "@reactre/core/jsx-runtime";

import { Icon } from "../components/icons.tsx";
import { Pill } from "../components/status.tsx";
import { icons } from "../icons.ts";
import { ago, checklistCounts, type Index, type Member } from "../model.ts";
import type { App, RouteTab } from "../store.ts";
import type { CheckItem, Entry, Pane, Route, Snapshot, Workspace } from "../types.ts";
import { toast } from "../ui.ts";
import { workspaceName } from "./shell.tsx";
import { MemberCard } from "./team.tsx";

function LocationChip(props: { dir: string }) {
  const label = memo(() => {
    const parts = props.dir.split("/").filter(Boolean);
    const at = parts.lastIndexOf(".herdsman");
    return { project: (at > 0 ? parts[at - 1] : parts.at(-2)) ?? props.dir, tail: at > 0 ? "/.herdsman" : "" };
  });
  return (
    <span class="location" title={props.dir}>
      <Icon paths={icons.folder} />
      <span class="location-name">{label().project}</span>
      <span class="location-tail">{label().tail}</span>
      <button
        class="chip-btn"
        title="Copy the route folder path"
        aria-label="Copy the route folder path"
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(props.dir);
            toast("Copied the route folder path.");
          } catch {
            toast("The browser blocked the clipboard.", "error");
          }
        }}
      >
        <Icon paths={icons.copy} />
      </button>
    </span>
  );
}

function progressTip(counts: ReturnType<typeof checklistCounts>): string {
  return [
    `${counts.done} of ${counts.total} checklist lines done`,
    counts.skipped ? `${counts.skipped} not applicable` : "",
    counts.open ? `${counts.open} open` : "",
  ]
    .filter(Boolean)
    .join(", ");
}

export function RouteShell(props: { route: Route; tabs: Child; children?: Child }) {
  const counts = memo(() => checklistCounts(props.route.checklist ?? []));
  const done = memo(() => counts().done + counts().skipped);
  return (
    <div class="route-shell">
      <div class="route-head">
        <div>
          <h1>{props.route.title || props.route.name}</h1>
          <p class="sub">
            <span class={props.route.live ? "badge live" : "badge"}>{props.route.live ? "Live" : "Not running"}</span>
            <LocationChip dir={props.route.dir} />
            <span class="meta-item" title={new Date(props.route.modified).toLocaleString()}>
              <Icon paths={icons.clock} />
              {ago(props.route.modified)}
            </span>
          </p>
        </div>
        <Show when={counts().total > 0}>
          <div class="progress" title={progressTip(counts())}>
            <div class="bar">
              <span style={`width:${Math.round((done() / counts().total) * 100)}%`} />
            </div>
            <small>{`${done()}/${counts().total}`}</small>
          </div>
        </Show>
      </div>
      <>{props.tabs}</>
      <div class="tab-body">{props.children}</div>
    </div>
  );
}

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

export function TabRow(props: {
  route: Route;
  app: App;
  unread: number;
  pick: (tab: RouteTab) => void;
  ref?: (element: HTMLDivElement) => void;
}) {
  const shown = memo(() =>
    tabs.filter(
      (tab) =>
        tab.id !== "messages" ||
        props.route.adjutant_pane ||
        props.app.channel?.configured ||
        props.app.view.tab === "messages",
    ),
  );
  return (
    <div class="tabs" role="tablist" ref={props.ref}>
      <For each={shown()} key={(tab) => tab.id}>
        {(tab) => (
          <button
            role="tab"
            class={props.app.view.tab === tab.id ? "tab active" : "tab"}
            title={tabTips[tab.id]}
            aria-selected={props.app.view.tab === tab.id ? "true" : "false"}
            onClick={() => props.pick(tab.id)}
          >
            <Icon paths={tab.glyph} />
            {tab.label}
            <Show when={tab.id === "messages" && props.unread > 0}>
              <span class="tab-count" aria-label={`${props.unread} unread`}>
                {String(props.unread)}
              </span>
            </Show>
          </button>
        )}
      </For>
    </div>
  );
}

/** Repeated server rows get distinct keys without letting their text impersonate another key. */
export function keyedOnce<T>(items: readonly T[], parts: (item: T) => readonly string[]): [string, T][] {
  const seen = new Map<string, number>();
  return items.map((item) => {
    const identity = JSON.stringify(parts(item));
    const occurrence = (seen.get(identity) ?? 0) + 1;
    seen.set(identity, occurrence);
    return [`${occurrence}:${identity}`, item];
  });
}

function CheckLine(props: { item: CheckItem }) {
  return (
    <li
      class={props.item.done ? "done" : props.item.skipped ? "skipped" : ""}
      title={props.item.skipped ? "Stopped applying; the reason follows the line" : null}
    >
      <span class="box" aria-label={props.item.done ? "Done" : props.item.skipped ? "Not applicable" : "Open"}>
        <Show when={props.item.done} fallback={props.item.skipped ? "–" : null}>
          <Icon paths={icons.check} />
        </Show>
      </span>
      <Show when={props.item.id}>
        <code>{props.item.id}</code>
      </Show>
      <span>{props.item.text}</span>
    </li>
  );
}
export function ChecklistView(props: { route: Route }) {
  const entries = memo(() =>
    keyedOnce(props.route.checklist ?? [], (item) => (item.id ? ["id", item.id] : ["text", item.text])),
  );
  const rows = map(entries, (entry) => entry, { keyed: ([key]) => key });
  return (
    <Show when={entries().length > 0} fallback={<p class="muted pad">This route has no checklist.</p>}>
      <ul class="checklist">
        <For each={rows()} key={(entry) => entry()[0]}>
          {(entry) => <CheckLine item={entry()[1]} />}
        </For>
      </ul>
    </Show>
  );
}

const clock = new Intl.DateTimeFormat(undefined, {
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hourCycle: "h23",
});
const eventName = (kind: string): string =>
  kind === "pane_closed" ? "pane deleted" : kind.replace(/^pane\./, "").replace(/[._]/g, " ");
function EventItem(props: { entry: Entry; who: string }) {
  return (
    <li class={props.entry.source}>
      <time datetime={props.entry.at} title={new Date(props.entry.at).toLocaleString()}>
        {clock.format(new Date(props.entry.at))}
      </time>
      <p class="event">
        <Show
          when={props.entry.source === "web"}
          fallback={
            <Show
              when={props.entry.status}
              fallback={
                <>
                  <span class="chip">{eventName(props.entry.kind)}</span>
                  <Show when={props.who}>
                    <strong>{props.who}</strong>
                  </Show>
                </>
              }
            >
              <Show when={props.who}>
                <strong>{props.who}</strong>
              </Show>
              <Pill status={props.entry.status!} />
            </Show>
          }
        >
          <span class="chip accent">web</span>
          <span>{props.entry.text ?? ""}</span>
        </Show>
      </p>
    </li>
  );
}
export function ActivityView(props: { app: App; members: Member[] }) {
  const byPane = memo(() => new Map(props.members.map((member) => [member.pane_id, member])));
  const entries = memo(() =>
    props.app.entries
      .filter((entry) => !entry.pane_id || byPane().has(entry.pane_id))
      .slice(-200)
      .reverse(),
  );
  const rows = map(entries, (entry) => entry, {
    keyed: (entry) => `${entry.at}|${entry.kind}|${entry.pane_id ?? ""}|${entry.status ?? ""}|${entry.text ?? ""}`,
  });
  return (
    <Show when={entries().length > 0} fallback={<p class="muted pad">Nothing has happened since this page opened.</p>}>
      <ol class="timeline activity">
        <For
          each={rows()}
          key={(entry) => JSON.stringify([entry().at, entry().kind, entry().pane_id, entry().status, entry().text])}
        >
          {(entry) => (
            <EventItem
              entry={entry()}
              who={entry().pane_id ? (byPane().get(entry().pane_id!)?.title ?? entry().pane_id!) : ""}
            />
          )}
        </For>
      </ol>
    </Show>
  );
}

interface AgentGroup {
  id: string;
  title: string;
  members: Member[];
}
function AgentGroupView(props: { group: AgentGroup; app: App }) {
  const cards = map(
    () => props.group.members,
    (member) => member,
    { keyed: (member) => member.pane_id },
  );
  return (
    <section class="group">
      <h2>
        {props.group.title}
        <span class="count">{String(props.group.members.length)}</span>
      </h2>
      <div class="cards">
        <For each={cards()} key={(member) => member().pane_id}>
          {(member) => <MemberCard member={member()} app={props.app} />}
        </For>
      </div>
    </section>
  );
}
function IdleWorkspace(props: { workspace: Workspace; panes: Pane[]; idx: Index; open: (pane: string) => void }) {
  const tabs = memo(() => new Set(props.panes.map((pane) => pane.tab_id)).size);
  const rows = map(
    () => props.panes,
    (pane) => pane,
    { keyed: (pane) => pane.pane_id },
  );
  return (
    <details class="workspace-row">
      <summary
        title={`${props.workspace.label || props.workspace.workspace_id}: ${tabs()} tabs, ${props.panes.length} panes, no agent`}
      >
        <code>{props.workspace.workspace_id}</code>
        <span>{props.workspace.label || ""}</span>
        <span class="muted">{`${tabs()} ${tabs() === 1 ? "tab" : "tabs"} · ${props.panes.length} ${props.panes.length === 1 ? "pane" : "panes"}`}</span>
      </summary>
      <For each={rows()} key={(pane) => pane().pane_id}>
        {(pane) => (
          <button
            class="shell-row"
            title={`Open pane ${pane().pane_id}${pane().cwd ? ` in ${pane().cwd}` : ""}`}
            onClick={() => props.open(pane().pane_id)}
          >
            <code>{pane().pane_id}</code>
            <span>{pane().label || props.idx.tabs.get(pane().tab_id)?.label || ""}</span>
            <span class="muted clip">{pane().cwd ?? ""}</span>
          </button>
        )}
      </For>
    </details>
  );
}

export function SessionView(props: { app: App; snap: Snapshot; idx: Index; open: (pane: string) => void }) {
  const data = memo(() => {
    const snap = props.snap;
    const only = (snap.workspaces ?? []).find((workspace) => workspace.workspace_id === props.app.view.ws);
    const inView = (id: string) => !only || id === only.workspace_id;
    const agents = (snap.agents ?? []).filter((agent) => inView(agent.workspace_id));
    const labels = new Map(
      (snap.workspaces ?? []).map((workspace) => [workspace.workspace_id, workspaceName(workspace)]),
    );
    const byTab = new Map<string, Member[]>();
    for (const agent of agents) {
      byTab.set(agent.tab_id, [...(byTab.get(agent.tab_id) ?? []), props.idx.member(agent.pane_id, "agent", null)]);
    }
    const withAgent = new Set((snap.agents ?? []).map((agent) => agent.workspace_id));
    const idle = (snap.workspaces ?? [])
      .filter((workspace) => inView(workspace.workspace_id) && !withAgent.has(workspace.workspace_id))
      .map((workspace) => ({
        workspace,
        panes: (snap.panes ?? []).filter((pane) => pane.workspace_id === workspace.workspace_id),
      }));
    const tabCount = (snap.tabs ?? []).filter((tab) => inView(tab.workspace_id)).length;
    const groups: AgentGroup[] = [...byTab].map(([id, members]) => {
      const tab = props.idx.tabs.get(id);
      return {
        id,
        members,
        title: only ? tab?.label || id : `${labels.get(tab?.workspace_id ?? "") ?? ""} › ${tab?.label || id}`,
      };
    });
    return {
      title: only ? workspaceName(only) : "All agents",
      sub: `${agents.length} agents in ${tabCount} tabs · ${only ? only.workspace_id : `herdr ${snap.version}`}`,
      groups,
      idle,
    };
  });
  const groups = map(
    () => data().groups,
    (group) => group,
    { keyed: (group) => group.id },
  );
  const idle = map(
    () => data().idle,
    (group) => group,
    { keyed: (group) => group.workspace.workspace_id },
  );
  return (
    <div>
      <div class="route-head">
        <div>
          <h1>{data().title}</h1>
          <p class="sub">{data().sub}</p>
        </div>
      </div>
      <div class="board">
        <For each={groups()} key={(group) => group().id}>
          {(group) => <AgentGroupView group={group()} app={props.app} />}
        </For>
        <Show when={data().idle.length > 0}>
          <details class="group shells">
            <summary>{`Workspaces without an agent (${data().idle.length})`}</summary>
            <div class="shell-list">
              <For each={idle()} key={(group) => group().workspace.workspace_id}>
                {(group) => (
                  <IdleWorkspace
                    workspace={group().workspace}
                    panes={group().panes}
                    idx={props.idx}
                    open={props.open}
                  />
                )}
              </For>
            </div>
          </details>
        </Show>
      </div>
    </div>
  );
}
