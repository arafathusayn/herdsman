// The app's frame: the topbar, the sidebar with the routes and workspaces,
// and what the main column shows before there is a route to show. main.ts
// owns the state and places these; every function here is data to a view.
import { icon, type IconNode, icons, logoMark, type LogoNode } from "../dom.ts";
import { attr, child, cls, html, list, type ListNode, on, text, type ViewNodeOf } from "../herdsman-blocks-view.ts";
import { checklistCounts, routeFor, routeLabel, statusLabel, workspaceGroups } from "../model.ts";
import { isMac, paletteKeys, sidebarKeys } from "../palette.ts";
import type { App } from "../store.ts";
import type { Route, Workspace } from "../types.ts";
import { type Theme, themeSwitch, type ThemeSwitchNode } from "../ui.ts";

/** r=* is the whole-session view; an empty r picks the first live route. */
export const SESSION = "*";

export function currentRoute(app: App): Route | null {
  return routeFor(app.server?.routes ?? [], app.view.route);
}

export function workspaceName(w: Workspace): string {
  return w.label || `Workspace ${w.number}`;
}

function sessionCrumb(app: App): string {
  const w = app.server?.snapshot.workspaces?.find((x) => x.workspace_id === app.view.ws);
  return w ? workspaceName(w) : "All agents";
}

/** The checklist lines settled, "3/5", or "" for a route without a checklist. */
export function progress(r: Route): string {
  const c = checklistCounts(r.checklist ?? []);
  if (!c.total) return "";
  return `${c.done + c.skipped}/${c.total}`;
}

// ---------------------------------------------------------------------------
// Topbar
// ---------------------------------------------------------------------------

const HostSwitch = html`<button
  class="host-switch"
  type="button"
  data-tip=${attr()}
  aria-label=${attr()}
  onclick=${on<"click">()}
>
  ${child<IconNode>()}<span class="label">${text()}</span>
</button>`;
const Unreachable = html`<span class="conn" title=${attr()}>herdr unreachable</span>`;
// prettier-ignore
const Reconnecting = html`<span class="conn" title="Reconnecting"><span class="dot" aria-hidden="true"></span>Reconnecting…</span>`;

const Topbar = html`<header class="topbar">
  <button
    class="icon-btn nav-toggle"
    title=${attr()}
    aria-label="Toggle the sidebar"
    aria-keyshortcuts=${attr()}
    aria-expanded=${attr<"true" | "false">()}
    onclick=${on<"click">()}
  >
    ${child<IconNode>()}
  </button>
  <a class="brand" href="/" title="All routes" onclick=${on<"click">()}>${child<LogoNode>()}<span>herdsman</span></a>
  <span class="crumb">${text()}</span>
  <span class="grow"></span>
  <button class="palette-trigger" title=${attr()} onclick=${on<"click">()}>
    ${child<IconNode>()}<span class="label">Search or run a command</span><kbd>${text()}</kbd>
  </button>
  ${child<ViewNodeOf<typeof HostSwitch> | null>()}${child<ViewNodeOf<typeof Unreachable> | null>()}${child<ViewNodeOf<typeof Reconnecting> | null>()}
</header>`;

/** What `topbar` returns. */
export type TopbarNode = ViewNodeOf<typeof Topbar>;

export interface TopbarActions {
  toggleNav(): void;
  home(): void;
  palette(): void;
  hosts(): void;
}

/**
 * The topbar: the sidebar toggle, the mark, where you are, the palette and
 * the host. navShown says whether the sidebar (or the drawer) shows.
 */
export function topbar(
  app: App,
  host: { name: string; url: string } | null,
  navShown: boolean,
  act: TopbarActions,
): TopbarNode {
  const s = app.server;
  const r = currentRoute(app);
  return Topbar([
    `Toggle the sidebar (${sidebarKeys})`,
    isMac ? "Meta+B" : "Control+B Control+Shift+B",
    navShown ? "true" : "false",
    () => act.toggleNav(),
    icon(navShown ? icons.sidebarOpen : icons.sidebar),
    (e) => {
      e.preventDefault();
      act.home();
    },
    logoMark(),
    r ? routeLabel(r, s?.routes ?? []) : app.view.route === SESSION ? sessionCrumb(app) : "",
    `Command palette (${paletteKeys})`,
    () => act.palette(),
    icon(icons.search),
    paletteKeys,
    host
      ? HostSwitch([
          `Host: ${host.url}. Switch or add hosts`,
          `Host ${host.name}: switch or add hosts`,
          () => act.hosts(),
          icon(icons.server),
          host.name,
        ])
      : null,
    s?.error ? Unreachable([s.error]) : null,
    // Only trouble shows: a live connection needs no badge, and before the
    // first one the main view says it is connecting.
    app.connected || !app.server ? null : Reconnecting([]),
  ]);
}

// ---------------------------------------------------------------------------
// Sidebar
// ---------------------------------------------------------------------------

// A route or a workspace: a dot, the name and a count or progress.
const NavLink = html`<a
  href=${attr()}
  class=${cls()}
  title=${attr()}
  aria-current=${attr<"page" | null>()}
  onclick=${on<"click">()}
>
  <span class=${cls()} aria-hidden="true"></span><span class="nav-text">${text()}</span
  ><span class="nav-meta">${text()}</span>
</a>`;
type NavLinkNode = ViewNodeOf<typeof NavLink>;
type RouteList = ListNode<NavLinkNode, string>;

const NavGroup = html`<div class="nav-group">${child<NavLinkNode>()}${child<RouteList>()}</div>`;
const AllAgents = html`<a
  href=${attr()}
  class=${cls()}
  title="Every agent and pane in this herdr session"
  onclick=${on<"click">()}
>
  ${child<IconNode>()}<span class="nav-text">All agents</span><span class="nav-meta">${text()}</span>
</a>`;
const NavHeadRow = html`<div class="nav-head nav-head-row">
  <h3>Workspaces</h3>
  <button
    class="nav-new"
    title="New route: start an orchestrator or an adjutant in a new herdr tab"
    onclick=${on<"click">()}
  >
    ${child<IconNode>()}<span>New route</span>
  </button>
</div>`;
const NavEmpty = html`<p class="nav-empty">No workspace runs an agent.</p>`;
const NavHead = html`<h3 class="nav-head">${text()}</h3>`;
type NavHeadNode = ViewNodeOf<typeof NavHead>;

// prettier-ignore
const NavFoot = html`<div class="nav-foot">
  ${child<ThemeSwitchNode>()}
  <button class="icon-btn" title="Appearance: terminal colors and code highlighting" aria-label="Appearance" onclick=${on<"click">()}>${child<IconNode>()}</button>
</div>`;

const Sidebar = html`<nav class="sidebar" aria-label="Routes">
  <div class="nav-scroll">
    ${child<ViewNodeOf<typeof AllAgents> | null>()}${child<ViewNodeOf<typeof NavHeadRow> | null>()}${child<ListNode<ViewNodeOf<typeof NavGroup>, string> | ViewNodeOf<typeof NavEmpty> | null>()}${child<NavHeadNode | null>()}${child<RouteList | null>()}${child<NavHeadNode | null>()}${child<RouteList | null>()}
  </div>
  ${child<ViewNodeOf<typeof NavFoot>>()}
</nav>`;

/** What `sidebar` returns. */
export type SidebarNode = ViewNodeOf<typeof Sidebar>;

export interface SidebarActions {
  /** Open a route, or the session view (narrowed to a workspace, or not). */
  go(route: string, ws?: string): void;
  newRoute(): void;
  theme(t: Theme): void;
  appearance(): void;
}

/**
 * The sidebar: every agent, the workspaces with their live routes, other
 * live routes and recent ones, and the appearance controls at the foot. Until
 * the app has a server (or after a fatal error) only the foot shows.
 */
export function sidebar(app: App, act: SidebarActions): SidebarNode {
  const foot = NavFoot([themeSwitch(app.theme, (t) => act.theme(t)), () => act.appearance(), icon(icons.swatch)]);
  const s = app.fatal ? null : app.server;
  if (!s) return Sidebar([null, null, null, null, null, null, null, foot]);
  const routes = s.routes ?? [];
  const rest = routes.filter((r) => !r.live);
  const { groups, unplaced } = workspaceGroups(s);
  const onSession = app.view.route === SESSION;
  const go =
    (route: string, ws?: string) =>
    (e: Event): void => {
      e.preventDefault();
      act.go(route, ws);
    };
  const item = (r: Route, nested = false): NavLinkNode =>
    NavLink([
      `?r=${encodeURIComponent(r.id)}`,
      ["nav-item", nested ? "nested" : "", app.view.route === r.id ? "active" : ""].filter(Boolean).join(" "),
      [r.title || r.name, r.live ? "running" : "not running", progress(r) && `${progress(r)} checklist lines done`]
        .filter(Boolean)
        .join(" · "),
      app.view.route === r.id ? "page" : null,
      go(r.id),
      r.live ? "live-dot on" : "live-dot",
      routeLabel(r, routes),
      progress(r),
    ]);
  const routeList = (rs: Route[], nested = false): RouteList =>
    list(
      rs,
      (r) => r.id,
      (r) => item(r, nested),
    );
  return Sidebar([
    AllAgents([
      `?r=${SESSION}`,
      onSession && !app.view.ws ? "nav-item active" : "nav-item",
      go(SESSION),
      icon(icons.users),
      String(s.snapshot.agents?.length ?? 0),
    ]),
    NavHeadRow([() => act.newRoute(), icon(icons.plus)]),
    groups.length
      ? list(
          groups,
          (g) => g.workspace.workspace_id,
          ({ workspace: w, agents: n, routes: rs }) => {
            const name = workspaceName(w);
            const active = onSession && app.view.ws === w.workspace_id;
            return NavGroup([
              NavLink([
                `?r=${SESSION}&ws=${encodeURIComponent(w.workspace_id)}`,
                active ? "nav-item active" : "nav-item",
                [
                  `${name}: ${n} ${n === 1 ? "agent" : "agents"}`,
                  rs.length ? `${rs.length} live ${rs.length === 1 ? "route" : "routes"}` : "",
                  statusLabel[w.agent_status],
                ]
                  .filter(Boolean)
                  .join(" · "),
                active ? "page" : null,
                go(SESSION, w.workspace_id),
                `ws-dot s-${w.agent_status}`,
                name,
                String(n),
              ]),
              routeList(rs, true),
            ]);
          },
        )
      : NavEmpty([]),
    unplaced.length ? NavHead(["Other live routes"]) : null,
    unplaced.length ? routeList(unplaced) : null,
    rest.length ? NavHead(["Recent routes"]) : null,
    rest.length ? routeList(rest) : null,
    foot,
  ]);
}

// ---------------------------------------------------------------------------
// The main column before a route shows
// ---------------------------------------------------------------------------

// prettier-ignore
const Fatal = html`<div class="empty fatal">
  <h2>Not connected</h2>
  <p>${text()}</p>
  <button class="btn" type="button" onclick=${on<"click">()}>${child<IconNode>()}Hosts</button>
</div>`;
const Connecting = html`<div class="empty">
  <div class="spinner" aria-hidden="true"></div>
  <p>${text()}</p>
</div>`;
const NotFound = html`<div class="empty">
  <h2>Route not found</h2>
  <p>${text()}</p>
</div>`;

/** The connection failed for good: the reason and the way to the hosts. */
export function fatalView(reason: string, hosts: () => void): ViewNodeOf<typeof Fatal> {
  return Fatal([reason, () => hosts(), icon(icons.server)]);
}

/** Waiting for the host's first state. */
export function connectingView(hostName: string): ViewNodeOf<typeof Connecting> {
  return Connecting([`Connecting to ${hostName}…`]);
}

export function notFoundView(route: string): ViewNodeOf<typeof NotFound> {
  return NotFound([`No route named ${route} under the panes' folders.`]);
}

// The frame's fixed parts: the main column (a render container), the scrim
// behind the drawer on phones, and the two columns' holders.
export const MainColumn = html`<main class="main" tabindex="-1"></main>`;
export const Scrim = html`<div class="scrim" aria-hidden="true" onclick=${on<"click">()}></div>`;
export const PanelHost = html`<div class="panel-host">${child<HTMLElement | null>()}</div>`;
export const Body = html`<div class="body">
  ${child<HTMLElement>()}${child<HTMLElement>()}${child<HTMLElement>()}${child<HTMLElement>()}${child<HTMLElement>()}
</div>`;
