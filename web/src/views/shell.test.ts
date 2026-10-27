import { beforeEach, expect, test } from "bun:test";

import { render, type ViewNode } from "../herdsman-blocks-view.ts";
import { type FakeElement, type FakeNode, installFakeDocument, ops, resetOps } from "../herdsman-blocks.testdom.ts";
import { icon, icons } from "../icons.ts";
import { ago, Index, type Member } from "../model.ts";
import { isMac, paletteKeys, sidebarKeys } from "../palette.ts";
import type { App } from "../store.ts";
import { dispatch, dump, markup } from "../testdom.ts";
import type { Agent, Entry, Pane, Route, State, Workspace } from "../types.ts";

// The store reads the URL and listens for history changes when it loads.
const g = globalThis as unknown as Record<string, unknown>;
g["location"] ??= { search: "", pathname: "/" };
g["window"] ??= { addEventListener: () => {} };
const { activityView, checklistView, routeShell, sessionView, tabRow } = await import("./route.ts");
const { logView: routeLog } = await import("./log-view.ts");
const logView = (r: Route) => routeLog(r, () => {});
const { connectingView, fatalView, notFoundView, SESSION, sidebar, topbar } = await import("./shell.ts");

beforeEach(installFakeDocument);

const fake = (n: unknown): FakeElement => n as FakeElement;
const elements = (n: FakeNode): FakeElement[] => n.childNodes.filter((c): c is FakeElement => c.nodeType === 1);
const iconMarkup = (paths: readonly string[], label?: string): string => {
  const box = document.createElement("div");
  render(box, icon(paths, label));
  return dump(box.firstChild as unknown as FakeNode);
};
const rendered = (v: ViewNode, box = document.createElement("div")): FakeElement => {
  render(box, v);
  return fake(box.firstChild);
};

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

// The sidebar's shortcut as the topbar's toggle names it; the keys differ on a Mac.
const toggleKeys = isMac ? "Meta+B" : "Control+B Control+Shift+B";
// The brand's logo, and the sidebar's foot: the theme switch (system) and
// the appearance button.
const logo = markup`
  <svg:svg aria-hidden="true" class="logo" viewBox="0 0 32 32">
    <svg:rect fill="#1d4d38" height="32" rx="8" width="32"></svg:rect>
    <svg:g fill="none" stroke-linecap="round" stroke-linejoin="round" stroke-width="2.6" stroke="#e9f6ef">
      <svg:path d="M10 6.5V26"></svg:path>
      <svg:path d="M10 17.5a5.5 5.5 0 0 1 11 0v5.5a3 3 0 0 0 3 3"></svg:path>
    </svg:g>
    <svg:circle cx="15.5" cy="24" fill="#5cc093" r="2"></svg:circle>
  </svg:svg>
`;
const navFoot = (): string => markup`
  <div class="nav-foot">
    <div aria-label="Theme" class="theme-switch" role="radiogroup">
      <button aria-checked="true" class="active" role="radio" title="System theme" type="button">
        ${iconMarkup(icons.monitor)}
        <span>System</span>
      </button>
      <button aria-checked="false" role="radio" title="Light theme" type="button">
        ${iconMarkup(icons.sun)}
        <span>Light</span>
      </button>
      <button aria-checked="false" role="radio" title="Dark theme" type="button">
        ${iconMarkup(icons.moon)}
        <span>Dark</span>
      </button>
    </div>
    <button aria-label="Appearance" class="icon-btn" title="Appearance: terminal colors and code highlighting">
      ${iconMarkup(icons.swatch)}
    </button>
  </div>
`;

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
  expect(dump(bar)).toBe(markup`
    <header class="topbar">
      <button aria-expanded="true" aria-keyshortcuts="${toggleKeys}" aria-label="Toggle the sidebar" class="icon-btn nav-toggle" title="Toggle the sidebar (${sidebarKeys})">
        ${iconMarkup(icons.sidebarOpen)}
      </button>
      <a class="brand" href="/" title="All routes">
        ${logo}
        <span>herdsman</span>
      </a>
      <span class="crumb">auth</span>
      <span class="grow"></span>
      <button class="palette-trigger" title="Command palette (${paletteKeys})">
        ${iconMarkup(icons.search)}
        <span class="label">Search or run a command</span>
        <kbd>${paletteKeys}</kbd>
      </button>
      <button aria-label="Host mac: switch or add hosts" class="host-switch" data-tip="Host: http://mac:8080. Switch or add hosts" type="button">
        ${iconMarkup(icons.server)}
        <span class="label">mac</span>
      </button>
    </header>
  `);
  const [toggle, brand] = elements(bar);

  const down = app({ route: SESSION }, { connected: false, server: { ...state, error: "no herdr" } });
  rendered(topbar(down, null, false, topActs(log)), box);
  expect(dump(bar)).toBe(markup`
    <header class="topbar">
      <button aria-expanded="false" aria-keyshortcuts="${toggleKeys}" aria-label="Toggle the sidebar" class="icon-btn nav-toggle" title="Toggle the sidebar (${sidebarKeys})">
        ${iconMarkup(icons.sidebar)}
      </button>
      <a class="brand" href="/" title="All routes">
        ${logo}
        <span>herdsman</span>
      </a>
      <span class="crumb">All agents</span>
      <span class="grow"></span>
      <button class="palette-trigger" title="Command palette (${paletteKeys})">
        ${iconMarkup(icons.search)}
        <span class="label">Search or run a command</span>
        <kbd>${paletteKeys}</kbd>
      </button>
      <span class="conn" title="no herdr">herdr unreachable</span>
      <span class="conn" title="Reconnecting">
        <span aria-hidden="true" class="dot"></span>
        Reconnecting…
      </span>
    </header>
  `);
  expect(elements(bar)[0]).toBe(toggle!);

  rendered(topbar(app({}, { server: null }), null, false, topActs(log)), box);
  expect(dump(bar)).toBe(markup`
    <header class="topbar">
      <button aria-expanded="false" aria-keyshortcuts="${toggleKeys}" aria-label="Toggle the sidebar" class="icon-btn nav-toggle" title="Toggle the sidebar (${sidebarKeys})">
        ${iconMarkup(icons.sidebar)}
      </button>
      <a class="brand" href="/" title="All routes">
        ${logo}
        <span>herdsman</span>
      </a>
      <span class="crumb"></span>
      <span class="grow"></span>
      <button class="palette-trigger" title="Command palette (${paletteKeys})">
        ${iconMarkup(icons.search)}
        <span class="label">Search or run a command</span>
        <kbd>${paletteKeys}</kbd>
      </button>
    </header>
  `);

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
    expect(dump(rendered(sidebar(a, navActs(log)), document.createElement("div")))).toBe(markup`
      <nav aria-label="Routes" class="sidebar">
        <div class="nav-scroll"></div>
        ${navFoot()}
      </nav>
    `);
  }
  const nav = rendered(sidebar(app(), navActs(log)), box);
  expect(dump(nav)).toBe(markup`
    <nav aria-label="Routes" class="sidebar">
      <div class="nav-scroll">
        <a class="nav-item" href="?r=*" title="Every agent and pane in this herdr session">
          ${iconMarkup(icons.users)}
          <span class="nav-text">All agents</span>
          <span class="nav-meta">2</span>
        </a>
        <div class="nav-head nav-head-row">
          <h3>Workspaces</h3>
          <button class="nav-new" title="New route: start an orchestrator or an adjutant in a new herdr tab">
            ${iconMarkup(icons.plus)}
            <span>New route</span>
          </button>
        </div>
        <div class="nav-group">
          <a class="nav-item" href="?r=*&ws=w1" title="main: 2 agents · 1 live route · Working">
            <span aria-hidden="true" class="ws-dot s-working"></span>
            <span class="nav-text">main</span>
            <span class="nav-meta">2</span>
          </a>
          <a aria-current="page" class="nav-item nested active" href="?r=auth" title="Auth work · running · 2/3 checklist lines done">
            <span aria-hidden="true" class="live-dot on"></span>
            <span class="nav-text">auth</span>
            <span class="nav-meta">2/3</span>
          </a>
        </div>
        <h3 class="nav-head">Other live routes</h3>
        <a class="nav-item" href="?r=ghost" title="ghost · running">
          <span aria-hidden="true" class="live-dot on"></span>
          <span class="nav-text">ghost</span>
          <span class="nav-meta"></span>
        </a>
        <h3 class="nav-head">Recent routes</h3>
        <a class="nav-item" href="?r=old" title="old · not running">
          <span aria-hidden="true" class="live-dot"></span>
          <span class="nav-text">old</span>
          <span class="nav-meta"></span>
        </a>
      </div>
      ${navFoot()}
    </nav>
  `);
  const scroll = elements(nav)[0]!;
  const links = elements(scroll);
  const onSession = app({ route: SESSION, ws: "w1" });
  rendered(sidebar(onSession, navActs(log)), box);
  expect(dump(nav)).toBe(markup`
    <nav aria-label="Routes" class="sidebar">
      <div class="nav-scroll">
        <a class="nav-item" href="?r=*" title="Every agent and pane in this herdr session">
          ${iconMarkup(icons.users)}
          <span class="nav-text">All agents</span>
          <span class="nav-meta">2</span>
        </a>
        <div class="nav-head nav-head-row">
          <h3>Workspaces</h3>
          <button class="nav-new" title="New route: start an orchestrator or an adjutant in a new herdr tab">
            ${iconMarkup(icons.plus)}
            <span>New route</span>
          </button>
        </div>
        <div class="nav-group">
          <a aria-current="page" class="nav-item active" href="?r=*&ws=w1" title="main: 2 agents · 1 live route · Working">
            <span aria-hidden="true" class="ws-dot s-working"></span>
            <span class="nav-text">main</span>
            <span class="nav-meta">2</span>
          </a>
          <a class="nav-item nested" href="?r=auth" title="Auth work · running · 2/3 checklist lines done">
            <span aria-hidden="true" class="live-dot on"></span>
            <span class="nav-text">auth</span>
            <span class="nav-meta">2/3</span>
          </a>
        </div>
        <h3 class="nav-head">Other live routes</h3>
        <a class="nav-item" href="?r=ghost" title="ghost · running">
          <span aria-hidden="true" class="live-dot on"></span>
          <span class="nav-text">ghost</span>
          <span class="nav-meta"></span>
        </a>
        <h3 class="nav-head">Recent routes</h3>
        <a class="nav-item" href="?r=old" title="old · not running">
          <span aria-hidden="true" class="live-dot"></span>
          <span class="nav-text">old</span>
          <span class="nav-meta"></span>
        </a>
      </div>
      ${navFoot()}
    </nav>
  `);
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
    markup`
      <div class="empty fatal">
        <h2>Not connected</h2>
        <p>Session expired.</p>
        <button class="btn" type="button">${iconMarkup(icons.server)}Hosts</button>
      </div>
    `,
  );
  dispatch(elements(fatal)[2]!, "click");
  expect(log).toEqual(["hosts"]);
  expect(dump(rendered(connectingView("mac")))).toBe(
    '<div class="empty"><div aria-hidden="true" class="spinner"></div><p>Connecting to mac…</p></div>',
  );
  expect(dump(rendered(notFoundView("x")))).toBe(
    '<div class="empty"><h2>Route not found</h2><p>No route named x under the panes\' folders.</p></div>',
  );
});

// ---------------------------------------------------------------------------
// Route
// ---------------------------------------------------------------------------

test("route shell: head, tabs and body as the h() version, and the body changes in place", () => {
  const r = routes[0]!;
  const picked: string[] = [];
  const tabsBox = document.createElement("div");
  const tabs = rendered(
    tabRow(r, app(), 0, (t) => picked.push(t)),
    tabsBox,
  );
  expect(dump(tabs)).toBe(markup`
    <div class="tabs" role="tablist">
      <button aria-selected="true" class="tab active" role="tab" title="The agents of this route and what each is doing">
        ${iconMarkup(icons.users)}
        Team
      </button>
      <button aria-selected="false" class="tab" role="tab" title="The route's checklist lines">
        ${iconMarkup(icons.check)}
        Checklist
      </button>
      <button aria-selected="false" class="tab" role="tab" title="The orchestrator's run log, newest first">
        ${iconMarkup(icons.list)}
        Run state
      </button>
      <button aria-selected="false" class="tab" role="tab" title="Contracts, reports and notes in the route folder">
        ${iconMarkup(icons.file)}
        Docs
      </button>
      <button aria-selected="false" class="tab" role="tab" title="The code each task changed against its base branch">
        ${iconMarkup(icons.diff)}
        Changes
      </button>
      <button aria-selected="false" class="tab" role="tab" title="HTML pages in the route's workspace folders: reports, reviews, plans">
        ${iconMarkup(icons.artifact)}
        Artifacts
      </button>
      <button aria-selected="false" class="tab" role="tab" title="Agent and web events since this page opened">
        ${iconMarkup(icons.activity)}
        Activity
      </button>
    </div>
  `);
  const withMsgs = app({ tab: "messages" });
  rendered(
    tabRow(r, withMsgs, 3, (t) => picked.push(t)),
    tabsBox,
  );
  // The tab row with Messages picked and 3 unread, in the shells below too.
  const messagesTabs = markup`
    <div class="tabs" role="tablist">
      <button aria-selected="false" class="tab" role="tab" title="The agents of this route and what each is doing">
        ${iconMarkup(icons.users)}
        Team
      </button>
      <button aria-selected="false" class="tab" role="tab" title="The route's checklist lines">
        ${iconMarkup(icons.check)}
        Checklist
      </button>
      <button aria-selected="false" class="tab" role="tab" title="The orchestrator's run log, newest first">
        ${iconMarkup(icons.list)}
        Run state
      </button>
      <button aria-selected="false" class="tab" role="tab" title="Contracts, reports and notes in the route folder">
        ${iconMarkup(icons.file)}
        Docs
      </button>
      <button aria-selected="false" class="tab" role="tab" title="The code each task changed against its base branch">
        ${iconMarkup(icons.diff)}
        Changes
      </button>
      <button aria-selected="false" class="tab" role="tab" title="HTML pages in the route's workspace folders: reports, reviews, plans">
        ${iconMarkup(icons.artifact)}
        Artifacts
      </button>
      <button aria-selected="true" class="tab active" role="tab" title="Herdsman messages between the adjutant and the orchestrator">
        ${iconMarkup(icons.message)}
        Messages
        <span aria-label="3 unread" class="tab-count">3</span>
      </button>
      <button aria-selected="false" class="tab" role="tab" title="Agent and web events since this page opened">
        ${iconMarkup(icons.activity)}
        Activity
      </button>
    </div>
  `;
  expect(dump(tabs)).toBe(messagesTabs);
  dispatch(elements(tabs)[1]!, "click");
  expect(picked).toEqual(["checklist"]);

  const tabsEl = tabs as unknown as HTMLDivElement;
  const box = document.createElement("div");
  // The route's head: title, live badge, folder, age and checklist progress.
  const routeHead = markup`
    <div class="route-head">
      <div>
        <h1>Auth work</h1>
        <p class="sub">
          <span class="badge live">Live</span>
          <span class="location" title="/code/proj/.herdsman/auth">
            ${iconMarkup(icons.folder)}
            <span class="location-name">proj</span>
            <span class="location-tail">/.herdsman</span>
            <button aria-label="Copy the route folder path" class="chip-btn" title="Copy the route folder path">
              ${iconMarkup(icons.copy)}
            </button>
          </span>
          <span class="meta-item" title="${new Date(r.modified).toLocaleString()}">
            ${iconMarkup(icons.clock)}
            ${ago(r.modified)}
          </span>
        </p>
      </div>
      <div class="progress" title="1 of 3 checklist lines done, 1 not applicable, 1 open">
        <div class="bar"><span style="width:67%"></span></div>
        <small>2/3</small>
      </div>
    </div>
  `;
  const shell = rendered(routeShell(r, tabsEl, checklistView(r)), box);
  expect(dump(shell)).toBe(markup`
    <div class="route-shell">
      ${routeHead}
      ${messagesTabs}
      <div class="tab-body">
        <ul class="checklist">
          <li class="done">
            <span aria-label="Done" class="box">${iconMarkup(icons.check)}</span>
            <code>A1</code>
            <span>login</span>
          </li>
          <li class="skipped" title="Stopped applying; the reason follows the line">
            <span aria-label="Not applicable" class="box">–</span>
            <span>sso</span>
          </li>
          <li><span aria-label="Open" class="box"></span><code>A3</code><span>logout</span></li>
        </ul>
      </div>
    </div>
  `);
  const head = elements(shell)[0]!;

  rendered(routeShell(r, tabsEl, logView(r)), box);
  expect(elements(shell)[0]).toBe(head); // patched: the head and the tab row stay
  expect(elements(shell)[1]).toBe(fake(tabsEl));
  expect(dump(shell)).toBe(
    markup`
      <div class="route-shell">
        ${routeHead}
        ${messagesTabs}
        <div class="tab-body">
          <ol class="timeline">
            <li class="web"><time>10:05</time><p>web: prompted</p></li>
            <li><time>10:00</time><p>started</p></li>
          </ol>
        </div>
      </div>
    `,
  );

  const empty = route("bare", false);
  expect(dump(rendered(checklistView(empty)))).toBe('<p class="muted pad">This route has no checklist.</p>');
  expect(dump(rendered(logView(empty)))).toBe('<p class="muted pad">No run-state lines yet.</p>');
});

test("repeated checklist and run-state lines move as rows of their own", () => {
  // The server accepts repeats: unnamed checklist lines with the same text,
  // and log lines with the same minute and text. Each repeat is its own row,
  // so reordering a section with repeats leaves no stale or missing rows.
  const line = (text: string) => ({ id: "", text, done: false });
  const log = (text: string) => ({ time: "10:00", text });
  const orders = [
    ["a", "x", "x", "y", "b"],
    ["a", "y", "x", "x", "b"],
    ["a", "x", "y", "x", "x", "b"],
    ["x", "a", "x", "b", "x"],
    ["b", "x", "x", "a"],
    // A line whose own text looks like a repeat's key.
    ["x", "x#2", "x", "a"],
    ["x#2", "x", "a", "x"],
  ];
  for (const view of ["checklist", "log"] as const) {
    const box = document.createElement("div");
    for (const texts of orders) {
      const r =
        view === "checklist"
          ? route("rep", true, { checklist: texts.map(line) })
          : route("rep", true, { run_state: texts.map(log).reverse() });
      const v = view === "checklist" ? checklistView(r) : logView(r);
      render(box, v);
      const fresh = view === "checklist" ? checklistView(r) : logView(r);
      expect(dump(fake(box))).toBe(dump(fake(rendered(fresh).parentNode)));
    }
  }
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
  // An entry's time: the clock, and the date and time in its title, in the
  // test's time zone.
  const time = (at: string): string =>
    `<time datetime="${at}" title="${new Date(at).toLocaleString()}">${clock.format(new Date(at))}</time>`;
  expect(dump(rendered(activityView(app({}, { entries }), members)))).toBe(markup`
    <ol class="timeline activity">
      <li class="web">
        ${time(entries[2]!.at)}
        <p class="event"><span class="chip accent">web</span><span>go</span></p>
      </li>
      <li class="herdr">
        ${time(entries[1]!.at)}
        <p class="event"><span class="chip">pane deleted</span><strong>orch</strong></p>
      </li>
      <li class="herdr">
        ${time(entries[0]!.at)}
        <p class="event">
          <strong>orch</strong>
          <span class="pill s-idle"><span aria-hidden="true" class="dot"></span>Idle</span>
        </p>
      </li>
    </ol>
  `);
  expect(dump(rendered(activityView(app(), members)))).toBe(
    '<p class="muted pad">Nothing has happened since this page opened.</p>',
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
    markup`
      <div class="route-head">
        <div><h1>All agents</h1><p class="sub">2 agents in 2 tabs · herdr 0.9</p></div>
      </div>
    `,
  );
  const [group, idle] = elements(board!);
  const [h2, cards] = elements(group!);
  expect(dump(h2!)).toBe('<h2>main › work<span class="count">2</span></h2>');
  expect(fake(cards).className).toBe("cards");
  expect(elements(cards!).map((c) => c.getAttribute("data-pane"))).toEqual(["p1", "p2"]);
  expect(dump(idle!)).toBe(
    markup`
      <details class="group shells">
        <summary>Workspaces without an agent (1)</summary>
        <div class="shell-list">
          <details class="workspace-row">
            <summary title="w2: 1 tabs, 1 panes, no agent">
              <code>w2</code>
              <span></span>
              <span class="muted">1 tab · 1 pane</span>
            </summary>
            <button class="shell-row" title="Open pane p3 in /tmp">
              <code>p3</code>
              <span>shell</span>
              <span class="muted clip">/tmp</span>
            </button>
          </details>
        </div>
      </details>
    `,
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
