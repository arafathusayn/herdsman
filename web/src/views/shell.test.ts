import { beforeEach, expect, test } from "bun:test";

import { each, h, icon, icons, logoMark, unkeyed } from "../dom.ts";
import { render, type ViewNode } from "../herdsman-blocks-view.ts";
import { type FakeElement, type FakeNode, installFakeDocument, ops, resetOps } from "../herdsman-blocks.testdom.ts";
import { ago, checklistCounts, Index, type Member, routeLabel, statusLabel, workspaceGroups } from "../model.ts";
import { isMac, paletteKeys, sidebarKeys } from "../palette.ts";
import type { App } from "../store.ts";
import { dispatch, dump } from "../testdom.ts";
import type { Agent, Entry, Pane, Route, State, Workspace } from "../types.ts";
import { pill, themeSwitch } from "../ui.ts";

// The store reads the URL and listens for history changes when it loads.
const g = globalThis as unknown as Record<string, unknown>;
g["location"] ??= { search: "", pathname: "/" };
g["window"] ??= { addEventListener: () => {} };
const { activityView, checklistView, logView, routeShell, sessionView, tabRow } = await import("./route.ts");
const { connectingView, fatalView, notFoundView, progress, SESSION, sidebar, topbar } = await import("./shell.ts");

beforeEach(installFakeDocument);

const fake = (n: unknown): FakeElement => n as FakeElement;
const elements = (n: FakeNode): FakeElement[] => n.childNodes.filter((c): c is FakeElement => c.nodeType === 1);
const rendered = (v: ViewNode, box = document.createElement("div")): FakeElement => {
  render(box, v);
  return fake(box.firstChild);
};
const noop = (): void => {};

// ---------------------------------------------------------------------------
// Fixtures: two workspaces, one with two agents and a live route, one idle.
// ---------------------------------------------------------------------------

const ws = (workspace_id: string, number: number, label: string): Workspace => ({
  workspace_id,
  number,
  label,
  focused: false,
  active_tab_id: "",
  agent_status: "working",
});
const pane = (pane_id: string, workspace_id: string, tab_id: string, cwd: string | null): Pane => ({
  pane_id,
  workspace_id,
  tab_id,
  focused: false,
  label: null,
  cwd,
  foreground_cwd: null,
  terminal_title_stripped: null,
  agent: null,
  agent_status: "idle",
  revision: 0,
});
const agent = (pane_id: string, name: string): Agent => ({
  pane_id,
  tab_id: "t1",
  workspace_id: "w1",
  name,
  agent: "claude",
  display_agent: "Claude",
  agent_status: "working",
  focused: false,
  cwd: null,
  terminal_title_stripped: null,
  interactive_ready: true,
  launch_pending: false,
  state_change_seq: 0,
});
const route = (id: string, live: boolean, extra: Partial<Route> = {}): Route => ({
  id,
  dir: `/code/proj/.herdsman/${id}`,
  name: id,
  title: "",
  orchestrator_pane: live ? "p1" : "",
  tasks: null,
  checklist: null,
  run_state: null,
  files: null,
  modified: "2026-10-27T00:00:00Z",
  live,
  ...extra,
});
const routes = [
  route("auth", true, {
    title: "Auth work",
    checklist: [
      { done: true, id: "A1", text: "login" },
      { done: false, skipped: true, id: "", text: "sso" },
      { done: false, id: "A3", text: "logout" },
    ],
    run_state: [
      { time: "10:00", text: "started" },
      { time: "10:05", text: "web: prompted" },
    ],
  }),
  route("ghost", true, { orchestrator_pane: "nowhere" }),
  route("old", false),
];
const state: State = {
  herdr: { version: "0.9", protocol: 1 },
  snapshot: {
    version: "0.9",
    protocol: 1,
    focused_workspace_id: null,
    focused_tab_id: null,
    focused_pane_id: null,
    workspaces: [ws("w1", 1, "main"), ws("w2", 2, "")],
    tabs: [
      {
        tab_id: "t1",
        workspace_id: "w1",
        number: 1,
        label: "work",
        focused: false,
        pane_count: 2,
        agent_status: "working",
      },
      {
        tab_id: "t2",
        workspace_id: "w2",
        number: 1,
        label: "shell",
        focused: false,
        pane_count: 1,
        agent_status: "idle",
      },
    ],
    panes: [pane("p1", "w1", "t1", null), pane("p2", "w1", "t1", null), pane("p3", "w2", "t2", "/tmp")],
    layouts: null,
    agents: [agent("p1", "orch"), agent("p2", "im-1")],
  },
  routes,
};
const app = (view: Partial<App["view"]> = {}, extra: Partial<App> = {}): App => ({
  server: state,
  entries: [],
  connected: true,
  view: { route: "auth", tab: "team", file: "", source: "", diffFile: "", artifact: "", pane: "", ws: "", ...view },
  theme: "system",
  fatal: "",
  channel: null,
  ...extra,
});

// ---------------------------------------------------------------------------
// The old h() builders (main.ts before the migration), with properties
// written as the attributes the browser reflects them to (tabIndex, type).
// ---------------------------------------------------------------------------

function oldTopbar(a: App, host: { name: string; url: string } | null, shown: boolean): HTMLElement {
  const s = a.server;
  const r = (s?.routes ?? []).find((x) => x.id === a.view.route) ?? null;
  return h(
    "header",
    { class: "topbar" },
    h(
      "button",
      {
        class: "icon-btn nav-toggle",
        title: `Toggle the sidebar (${sidebarKeys})`,
        "aria-label": "Toggle the sidebar",
        "aria-keyshortcuts": isMac ? "Meta+B" : "Control+B Control+Shift+B",
        "aria-expanded": String(shown),
      },
      icon(shown ? icons.sidebarOpen : icons.sidebar),
    ),
    h("a", { class: "brand", href: "/", title: "All routes" }, logoMark(), h("span", null, "herdsman")),
    h("span", { class: "crumb" }, r ? routeLabel(r, s?.routes ?? []) : a.view.route === SESSION ? "All agents" : ""),
    h("span", { class: "grow" }),
    h(
      "button",
      { class: "palette-trigger", title: `Command palette (${paletteKeys})` },
      icon(icons.search),
      h("span", { class: "label" }, "Search or run a command"),
      h("kbd", null, paletteKeys),
    ),
    host
      ? h(
          "button",
          {
            class: "host-switch",
            type: "button",
            "data-tip": `Host: ${host.url}. Switch or add hosts`,
            "aria-label": `Host ${host.name}: switch or add hosts`,
          },
          icon(icons.server),
          h("span", { class: "label" }, host.name),
        )
      : null,
    s?.error ? h("span", { class: "conn", title: s.error }, "herdr unreachable") : null,
    a.connected || !a.server
      ? null
      : h(
          "span",
          { class: "conn", title: "Reconnecting" },
          h("span", { class: "dot", "aria-hidden": "true" }),
          "Reconnecting…",
        ),
  );
}

function oldNavFoot(a: App): HTMLElement {
  return h(
    "div",
    { class: "nav-foot" },
    themeSwitch(a.theme, noop),
    h(
      "button",
      { class: "icon-btn", title: "Appearance: terminal colors and code highlighting", "aria-label": "Appearance" },
      icon(icons.swatch),
    ),
  );
}

function oldSidebar(a: App): HTMLElement {
  if (!a.server || a.fatal)
    return h("nav", { class: "sidebar", "aria-label": "Routes" }, h("div", { class: "nav-scroll" }), oldNavFoot(a));
  const all = a.server.routes ?? [];
  const rest = all.filter((r) => !r.live);
  const { groups, unplaced } = workspaceGroups(a.server);
  const item = (r: Route, nested = false) =>
    h(
      "a",
      {
        href: `?r=${encodeURIComponent(r.id)}`,
        class: ["nav-item", nested ? "nested" : "", a.view.route === r.id ? "active" : ""].filter(Boolean).join(" "),
        title: [
          r.title || r.name,
          r.live ? "running" : "not running",
          progress(r) && `${progress(r)} checklist lines done`,
        ]
          .filter(Boolean)
          .join(" · "),
        "aria-current": a.view.route === r.id ? "page" : undefined,
      },
      h("span", { class: r.live ? "live-dot on" : "live-dot", "aria-hidden": "true" }),
      h("span", { class: "nav-text" }, routeLabel(r, all)),
      h("span", { class: "nav-meta" }, progress(r)),
    );
  const onSession = a.view.route === SESSION;
  return h(
    "nav",
    { class: "sidebar", "aria-label": "Routes" },
    h(
      "div",
      { class: "nav-scroll" },
      h(
        "a",
        {
          href: `?r=${SESSION}`,
          class: onSession && !a.view.ws ? "nav-item active" : "nav-item",
          title: "Every agent and pane in this herdr session",
        },
        icon(icons.users),
        h("span", { class: "nav-text" }, "All agents"),
        h("span", { class: "nav-meta" }, String(a.server.snapshot.agents?.length ?? 0)),
      ),
      h(
        "div",
        { class: "nav-head nav-head-row" },
        h("h3", null, "Workspaces"),
        h(
          "button",
          { class: "nav-new", title: "New route: start an orchestrator or an adjutant in a new herdr tab" },
          icon(icons.plus),
          h("span", null, "New route"),
        ),
      ),
      groups.length
        ? each(
            groups,
            (gr) => gr.workspace.workspace_id,
            ({ workspace: w, agents: n, routes: rs }) => {
              const name = w.label || `Workspace ${w.number}`;
              const active = onSession && a.view.ws === w.workspace_id;
              return h(
                "div",
                { class: "nav-group" },
                h(
                  "a",
                  {
                    href: `?r=${SESSION}&ws=${encodeURIComponent(w.workspace_id)}`,
                    class: active ? "nav-item active" : "nav-item",
                    title: [
                      `${name}: ${n} ${n === 1 ? "agent" : "agents"}`,
                      rs.length ? `${rs.length} live ${rs.length === 1 ? "route" : "routes"}` : "",
                      statusLabel[w.agent_status],
                    ]
                      .filter(Boolean)
                      .join(" · "),
                    "aria-current": active ? "page" : undefined,
                  },
                  h("span", { class: `ws-dot s-${w.agent_status}`, "aria-hidden": "true" }),
                  h("span", { class: "nav-text" }, name),
                  h("span", { class: "nav-meta" }, String(n)),
                ),
                each(
                  rs,
                  (r) => r.id,
                  (r) => item(r, true),
                ),
              );
            },
          )
        : h("p", { class: "nav-empty" }, "No workspace runs an agent."),
      unplaced.length
        ? [
            h("h3", { class: "nav-head" }, "Other live routes"),
            each(
              unplaced,
              (r) => r.id,
              (r) => item(r),
            ),
          ]
        : null,
      rest.length
        ? [
            h("h3", { class: "nav-head" }, "Recent routes"),
            each(
              rest,
              (r) => r.id,
              (r) => item(r),
            ),
          ]
        : null,
    ),
    oldNavFoot(a),
  );
}

// ---------------------------------------------------------------------------
// Frame
// ---------------------------------------------------------------------------

const topActs = (log: string[]) => ({
  toggleNav: () => log.push("toggle"),
  home: () => log.push("home"),
  palette: () => log.push("palette"),
  hosts: () => log.push("hosts"),
});

test("topbar: the markup of the h() version in each state, patched in place, and its buttons call back", () => {
  const host = { name: "mac", url: "http://mac:8080" };
  const log: string[] = [];
  const box = document.createElement("div");
  const bar = rendered(topbar(app(), host, true, topActs(log)), box);
  expect(dump(bar)).toBe(dump(fake(oldTopbar(app(), host, true))));
  const [toggle, brand] = elements(bar);

  const down = app({ route: SESSION }, { connected: false, server: { ...state, error: "no herdr" } });
  rendered(topbar(down, null, false, topActs(log)), box);
  expect(dump(bar)).toBe(dump(fake(oldTopbar(down, null, false))));
  expect(elements(bar)[0]).toBe(toggle!);

  rendered(topbar(app({}, { server: null }), null, false, topActs(log)), box);
  expect(dump(bar)).toBe(dump(fake(oldTopbar(app({}, { server: null }), null, false))));

  dispatch(toggle!, "click");
  const e = dispatch(brand!, "click");
  expect(e.defaultPrevented).toBe(true);
  expect(log).toEqual(["toggle", "home"]);
});

const navActs = (log: string[]) => ({
  go: (r: string, w?: string) => log.push(w === undefined ? `go ${r}` : `go ${r} ${w}`),
  newRoute: () => log.push("new"),
  theme: (t: string) => log.push(`theme ${t}`),
  appearance: () => log.push("appearance"),
});

test("sidebar: the markup of the h() version, links keep their nodes, and they call back", () => {
  const log: string[] = [];
  const box = document.createElement("div");
  for (const a of [app({}, { server: null }), app({}, { fatal: "gone" })]) {
    expect(dump(rendered(sidebar(a, navActs(log)), document.createElement("div")))).toBe(dump(fake(oldSidebar(a))));
  }
  const nav = rendered(sidebar(app(), navActs(log)), box);
  expect(dump(nav)).toBe(dump(fake(oldSidebar(app()))));
  const scroll = elements(nav)[0]!;
  const links = elements(scroll);
  const onSession = app({ route: SESSION, ws: "w1" });
  rendered(sidebar(onSession, navActs(log)), box);
  expect(dump(nav)).toBe(dump(fake(oldSidebar(onSession))));
  expect(elements(nav)[0]).toBe(scroll);
  expect(elements(scroll)).toEqual(links);

  resetOps();
  rendered(sidebar(onSession, navActs(log)), box);
  expect(Object.values(ops).reduce((x, y) => x + y, 0)).toBe(0); // an unchanged render writes nothing

  const group = elements(scroll)[2]!;
  const [wsLink, nested] = elements(group);
  const e = dispatch(nested!, "click");
  expect(e.defaultPrevented).toBe(true);
  dispatch(wsLink!, "click");
  dispatch(elements(scroll)[0]!, "click");
  dispatch(elements(elements(scroll)[1]!)[1]!, "click");
  expect(log).toEqual(["go auth", `go ${SESSION} w1`, `go ${SESSION}`, "new"]);
});

test("the main column's empty states are the h() versions", () => {
  const log: string[] = [];
  const fatal = rendered(fatalView("Session expired.", () => log.push("hosts")));
  expect(dump(fatal)).toBe(
    dump(
      fake(
        h(
          "div",
          { class: "empty fatal" },
          h("h2", null, "Not connected"),
          h("p", null, "Session expired."),
          h("button", { class: "btn", type: "button" }, icon(icons.server), "Hosts"),
        ),
      ),
    ),
  );
  dispatch(elements(fatal)[2]!, "click");
  expect(log).toEqual(["hosts"]);
  expect(dump(rendered(connectingView("mac")))).toBe(
    dump(
      fake(
        h(
          "div",
          { class: "empty" },
          h("div", { class: "spinner", "aria-hidden": "true" }),
          h("p", null, "Connecting to mac…"),
        ),
      ),
    ),
  );
  expect(dump(rendered(notFoundView("x")))).toBe(
    dump(
      fake(
        h(
          "div",
          { class: "empty" },
          h("h2", null, "Route not found"),
          h("p", null, "No route named x under the panes' folders."),
        ),
      ),
    ),
  );
});

// ---------------------------------------------------------------------------
// Route
// ---------------------------------------------------------------------------

function oldHead(r: Route): HTMLElement[] {
  const c = checklistCounts(r.checklist ?? []);
  const done = c.done + c.skipped;
  const parts = r.dir.split("/").filter(Boolean);
  const at = parts.lastIndexOf(".herdsman");
  const project = at > 0 ? parts[at - 1] : parts.at(-2);
  return [
    h(
      "div",
      null,
      h("h1", null, r.title || r.name),
      h(
        "p",
        { class: "sub" },
        h("span", { class: r.live ? "badge live" : "badge" }, r.live ? "Live" : "Not running"),
        h(
          "span",
          { class: "location", title: r.dir },
          icon(icons.folder),
          h("span", { class: "location-name" }, project ?? r.dir),
          h("span", { class: "location-tail" }, at > 0 ? "/.herdsman" : ""),
          h(
            "button",
            { class: "chip-btn", title: "Copy the route folder path", "aria-label": "Copy the route folder path" },
            icon(icons.copy),
          ),
        ),
        h(
          "span",
          { class: "meta-item", title: new Date(r.modified).toLocaleString() },
          icon(icons.clock),
          ago(r.modified),
        ),
      ),
    ),
    ...(c.total
      ? [
          h(
            "div",
            {
              class: "progress",
              title: [
                `${c.done} of ${c.total} checklist lines done`,
                c.skipped ? `${c.skipped} not applicable` : "",
                c.open ? `${c.open} open` : "",
              ]
                .filter(Boolean)
                .join(", "),
            },
            h("div", { class: "bar" }, h("span", { style: `width:${Math.round((done / c.total) * 100)}%` })),
            h("small", null, `${done}/${c.total}`),
          ),
        ]
      : []),
  ];
}

const tabList: [string, string, string[], string][] = [
  ["team", "Team", icons.users, "The agents of this route and what each is doing"],
  ["checklist", "Checklist", icons.check, "The route's checklist lines"],
  ["log", "Run state", icons.list, "The orchestrator's run log, newest first"],
  ["docs", "Docs", icons.file, "Contracts, reports and notes in the route folder"],
  ["changes", "Changes", icons.diff, "The code each task changed against its base branch"],
  ["artifacts", "Artifacts", icons.artifact, "HTML pages in the route's workspace folders: reports, reviews, plans"],
  ["messages", "Messages", icons.message, "Herdsman messages between the adjutant and the orchestrator"],
  ["activity", "Activity", icons.activity, "Agent and web events since this page opened"],
];

function oldTabs(a: App, withMessages: boolean, pending: number): HTMLElement {
  return h(
    "div",
    { class: "tabs", role: "tablist" },
    each(
      tabList.filter(([id]) => id !== "messages" || withMessages || a.view.tab === "messages"),
      ([id]) => id,
      ([id, label, glyph, tip]) =>
        h(
          "button",
          {
            role: "tab",
            class: a.view.tab === id ? "tab active" : "tab",
            title: tip,
            "aria-selected": String(a.view.tab === id),
          },
          icon(glyph),
          label,
          id === "messages" && pending
            ? h("span", { class: "tab-count", "aria-label": `${pending} unread` }, String(pending))
            : null,
        ),
    ),
  );
}

test("route shell: head, tabs and body as the h() version, and the body changes in place", () => {
  const r = routes[0]!;
  const picked: string[] = [];
  const tabsBox = document.createElement("div");
  const tabs = rendered(
    tabRow(r, app(), 0, (t) => picked.push(t)),
    tabsBox,
  );
  expect(dump(tabs)).toBe(dump(fake(oldTabs(app(), false, 0))));
  const withMsgs = app({ tab: "messages" });
  rendered(
    tabRow(r, withMsgs, 3, (t) => picked.push(t)),
    tabsBox,
  );
  expect(dump(tabs)).toBe(dump(fake(oldTabs(withMsgs, false, 3))));
  dispatch(elements(tabs)[1]!, "click");
  expect(picked).toEqual(["checklist"]);

  const tabsEl = tabs as unknown as HTMLDivElement;
  const box = document.createElement("div");
  const shell = rendered(routeShell(r, tabsEl, checklistView(r)), box);
  const old = (body: HTMLElement): HTMLElement =>
    h(
      "div",
      { class: "route-shell" },
      h("div", { class: "route-head" }, ...oldHead(r)),
      oldTabs(withMsgs, false, 3),
      h("div", { class: "tab-body" }, body),
    );
  const oldChecklist = h(
    "ul",
    { class: "checklist" },
    unkeyed([
      h(
        "li",
        { class: "done" },
        h("span", { class: "box", "aria-label": "Done" }, icon(icons.check)),
        h("code", null, "A1"),
        h("span", null, "login"),
      ),
      h(
        "li",
        { class: "skipped", title: "Stopped applying; the reason follows the line" },
        h("span", { class: "box", "aria-label": "Not applicable" }, "–"),
        h("span", null, "sso"),
      ),
      // An open line: the old h() wrote class="", which a class hole leaves out.
      h(
        "li",
        null,
        h("span", { class: "box", "aria-label": "Open" }),
        h("code", null, "A3"),
        h("span", null, "logout"),
      ),
    ]),
  );
  expect(dump(shell)).toBe(dump(fake(old(oldChecklist))));
  const head = elements(shell)[0]!;

  rendered(routeShell(r, tabsEl, logView(r)), box);
  expect(elements(shell)[0]).toBe(head); // patched: the head and the tab row stay
  expect(elements(shell)[1]).toBe(fake(tabsEl));
  expect(dump(shell)).toBe(
    dump(
      fake(
        old(
          h(
            "ol",
            { class: "timeline" },
            // A line not from the web: class="" before, no class now.
            h("li", { class: "web" }, h("time", null, "10:05"), h("p", null, "web: prompted")),
            h("li", null, h("time", null, "10:00"), h("p", null, "started")),
          ),
        ),
      ),
    ),
  );

  const empty = route("bare", false);
  expect(dump(rendered(checklistView(empty)))).toBe(
    dump(fake(h("p", { class: "muted pad" }, "This route has no checklist."))),
  );
  expect(dump(rendered(logView(empty)))).toBe(dump(fake(h("p", { class: "muted pad" }, "No run-state lines yet."))));
});

test("activity: the h() version's events, newest first", () => {
  const idx = new Index(state.snapshot);
  const members: Member[] = [idx.member("p1", "orchestrator", null)];
  const entries: Entry[] = [
    { at: "2026-10-27T10:00:00Z", source: "herdr", kind: "pane.agent_status_changed", pane_id: "p1", status: "idle" },
    { at: "2026-10-27T10:01:00Z", source: "herdr", kind: "pane_closed", pane_id: "p1" },
    { at: "2026-10-27T10:02:00Z", source: "web", kind: "prompt", pane_id: "p1", text: "go" },
    { at: "2026-10-27T10:03:00Z", source: "herdr", kind: "tab_focused", pane_id: "p9" },
  ];
  const clock = new Intl.DateTimeFormat(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hourCycle: "h23",
  });
  const time = (at: string) =>
    h("time", { datetime: at, title: new Date(at).toLocaleString() }, clock.format(new Date(at)));
  const old = h(
    "ol",
    { class: "timeline activity" },
    h(
      "li",
      { class: "web" },
      time(entries[2]!.at),
      h("p", { class: "event" }, h("span", { class: "chip accent" }, "web"), h("span", null, "go")),
    ),
    h(
      "li",
      { class: "herdr" },
      time(entries[1]!.at),
      h("p", { class: "event" }, h("span", { class: "chip" }, "pane deleted"), h("strong", null, "orch")),
    ),
    h(
      "li",
      { class: "herdr" },
      time(entries[0]!.at),
      h("p", { class: "event" }, h("strong", null, "orch"), pill("idle")),
    ),
  );
  expect(dump(rendered(activityView(app({}, { entries }), members)))).toBe(dump(fake(old)));
  expect(dump(rendered(activityView(app(), members)))).toBe(
    dump(fake(h("p", { class: "muted pad" }, "Nothing has happened since this page opened."))),
  );
});

test("session view: groups by tab, idle workspaces, rows open their pane", () => {
  const idx = new Index(state.snapshot);
  const opened: string[] = [];
  const a = app({ route: SESSION });
  const box = document.createElement("div");
  const view = rendered(
    sessionView(a, state.snapshot, idx, (p) => opened.push(p)),
    box,
  );
  const [head, board] = elements(view);
  expect(dump(head!)).toBe(
    dump(
      fake(
        h(
          "div",
          { class: "route-head" },
          h("div", null, h("h1", null, "All agents"), h("p", { class: "sub" }, "2 agents in 2 tabs · herdr 0.9")),
        ),
      ),
    ),
  );
  const [group, idle] = elements(board!);
  const [h2, cards] = elements(group!);
  expect(dump(h2!)).toBe(dump(fake(h("h2", null, "main › work", h("span", { class: "count" }, "2")))));
  expect(fake(cards).className).toBe("cards");
  expect(elements(cards!).map((c) => c.getAttribute("data-pane"))).toEqual(["p1", "p2"]);
  expect(dump(idle!)).toBe(
    dump(
      fake(
        h(
          "details",
          { class: "group shells" },
          h("summary", null, "Workspaces without an agent (1)"),
          h(
            "div",
            { class: "shell-list" },
            h(
              "details",
              { class: "workspace-row" },
              h(
                "summary",
                { title: "w2: 1 tabs, 1 panes, no agent" },
                h("code", null, "w2"),
                h("span", null, ""),
                h("span", { class: "muted" }, "1 tab · 1 pane"),
              ),
              h(
                "button",
                { class: "shell-row", title: "Open pane p3 in /tmp" },
                h("code", null, "p3"),
                h("span", null, "shell"),
                h("span", { class: "muted clip" }, "/tmp"),
              ),
            ),
          ),
        ),
      ),
    ),
  );
  idle!.setAttribute("open", ""); // the person opened it
  rendered(
    sessionView(a, state.snapshot, idx, (p) => opened.push(p)),
    box,
  );
  expect(elements(elements(view)[1]!)[1]).toBe(idle!); // kept, open
  expect(idle!.hasAttribute("open")).toBe(true);
  dispatch(elements(elements(elements(idle!)[1]!)[0]!)[1]!, "click");
  expect(opened).toEqual(["p3"]);
});
