import { For, memo, Show } from "@reactre/core";
import { map } from "@reactre/core/features/lists";

import { Icon, Logo } from "../components/icons.tsx";
import { ThemeSwitch } from "../components/theme-switch.tsx";
import { icons } from "../icons.ts";
import { checklistCounts, routeFor, routeLabel, statusLabel, workspaceGroups } from "../model.ts";
import { pages } from "../pages/index.tsx";
import { isMac, paletteKeys, sidebarKeys } from "../palette.tsx";
import type { App } from "../store.ts";
import type { Route, State, Workspace } from "../types.ts";
import type { Theme } from "../ui.ts";

export const SESSION = "*";
export function currentRoute(app: App): Route | null {
  return routeFor(app.server?.routes ?? [], app.view.route);
}
export function workspaceName(workspace: Workspace): string {
  return workspace.label || `Workspace ${workspace.number}`;
}
function sessionCrumb(app: App): string {
  const workspace = app.server?.snapshot.workspaces?.find((item) => item.workspace_id === app.view.ws);
  return workspace ? workspaceName(workspace) : "All agents";
}
export function progress(route: Route): string {
  const counts = checklistCounts(route.checklist ?? []);
  return counts.total ? `${counts.done + counts.skipped}/${counts.total}` : "";
}

export interface TopbarActions {
  toggleNav(): void;
  home(): void;
  palette(): void;
  hosts(): void;
}
export interface TopbarProps {
  app: App;
  host: { name: string; url: string } | null;
  navShown: boolean;
  actions: TopbarActions;
}

export function Topbar(props: TopbarProps) {
  const route = memo(() => currentRoute(props.app));
  const crumb = memo(() =>
    route()
      ? routeLabel(route()!, props.app.server?.routes ?? [])
      : props.app.view.route === SESSION
        ? sessionCrumb(props.app)
        : "",
  );
  return (
    <header class="topbar">
      <button
        class="icon-btn nav-toggle"
        title={`Toggle the sidebar (${sidebarKeys})`}
        aria-label="Toggle the sidebar"
        aria-keyshortcuts={isMac ? "Meta+B" : "Control+B Control+Shift+B"}
        aria-expanded={props.navShown ? "true" : "false"}
        onClick={() => props.actions.toggleNav()}
      >
        <Icon paths={props.navShown ? icons.sidebarOpen : icons.sidebar} />
      </button>
      <a
        class="brand"
        href="/"
        title="All routes"
        onClick={(event) => {
          event.preventDefault();
          props.actions.home();
        }}
      >
        <Logo />
        <span>herdsman</span>
      </a>
      <span class="crumb">{crumb()}</span>
      <span class="grow" />
      <button
        class="palette-trigger"
        aria-label="Command palette"
        title={`Command palette (${paletteKeys})`}
        onClick={() => props.actions.palette()}
      >
        <Icon paths={icons.search} />
        <span class="label">Search or run a command</span>
        <kbd>{paletteKeys}</kbd>
      </button>
      <Show when={props.host}>
        {(host) => (
          <button
            class="host-switch"
            type="button"
            data-tip={`Host: ${host().url}. Switch or add hosts`}
            aria-label={`Host ${host().name}: switch or add hosts`}
            onClick={() => props.actions.hosts()}
          >
            <Icon paths={icons.server} />
            <span class="label">{host().name}</span>
          </button>
        )}
      </Show>
      <Show when={props.app.server?.error}>
        <span class="conn" title={props.app.server?.error}>
          herdr unreachable
        </span>
      </Show>
      <Show when={!props.app.connected && !!props.app.server}>
        <span class="conn" title="Reconnecting">
          <span class="dot" aria-hidden="true" />
          Reconnecting…
        </span>
      </Show>
    </header>
  );
}

export interface SidebarActions {
  page(id: string): void;
  go(route: string, workspace?: string): void;
  newRoute(): void;
  theme(theme: Theme): void;
  appearance(): void;
}
export interface SidebarProps {
  app: App;
  actions: SidebarActions;
}

function RouteLink(props: {
  route: Route;
  routes: readonly Route[];
  selected: string;
  nested: boolean;
  actions: SidebarActions;
}) {
  const tip = memo(() =>
    [
      props.route.title || props.route.name,
      props.route.live ? "running" : "not running",
      progress(props.route) && `${progress(props.route)} checklist lines done`,
    ]
      .filter(Boolean)
      .join(" · "),
  );
  return (
    <a
      href={`?r=${encodeURIComponent(props.route.id)}`}
      class={["nav-item", props.nested ? "nested" : "", props.selected === props.route.id ? "active" : ""]
        .filter(Boolean)
        .join(" ")}
      title={tip()}
      aria-current={props.selected === props.route.id ? "page" : null}
      onClick={(event) => {
        event.preventDefault();
        props.actions.go(props.route.id);
      }}
    >
      <span class={props.route.live ? "live-dot on" : "live-dot"} aria-hidden="true" />
      <span class="nav-text">{routeLabel(props.route, props.routes)}</span>
      <span class="nav-meta">{progress(props.route)}</span>
    </a>
  );
}

function RouteLinks(props: {
  routes: readonly Route[];
  all: readonly Route[];
  selected: string;
  nested: boolean;
  actions: SidebarActions;
}) {
  const links = map(
    () => props.routes,
    (route) => route,
    { keyed: (route) => route.id },
  );
  return (
    <For each={links()} key={(route) => route().id}>
      {(route) => (
        <RouteLink
          route={route()}
          routes={props.all}
          selected={props.selected}
          nested={props.nested}
          actions={props.actions}
        />
      )}
    </For>
  );
}

type WorkspaceGroup = ReturnType<typeof workspaceGroups>["groups"][number];
function WorkspaceLinks(props: { group: WorkspaceGroup; app: App; actions: SidebarActions; routes: readonly Route[] }) {
  const active = memo(
    () => props.app.view.route === SESSION && props.app.view.ws === props.group.workspace.workspace_id,
  );
  const tip = memo(() => {
    const group = props.group;
    return [
      `${workspaceName(group.workspace)}: ${group.agents} ${group.agents === 1 ? "agent" : "agents"}`,
      group.routes.length ? `${group.routes.length} live ${group.routes.length === 1 ? "route" : "routes"}` : "",
      statusLabel[group.workspace.agent_status],
    ]
      .filter(Boolean)
      .join(" · ");
  });
  return (
    <div class="nav-group">
      <a
        href={`?r=${SESSION}&ws=${encodeURIComponent(props.group.workspace.workspace_id)}`}
        class={active() ? "nav-item active" : "nav-item"}
        title={tip()}
        aria-current={active() ? "page" : null}
        onClick={(event) => {
          event.preventDefault();
          props.actions.go(SESSION, props.group.workspace.workspace_id);
        }}
      >
        <span class={`ws-dot s-${props.group.workspace.agent_status}`} aria-hidden="true" />
        <span class="nav-text">{workspaceName(props.group.workspace)}</span>
        <span class="nav-meta">{String(props.group.agents)}</span>
      </a>
      <RouteLinks
        routes={props.group.routes}
        all={props.routes}
        selected={props.app.view.route}
        nested
        actions={props.actions}
      />
    </div>
  );
}

function SidebarRoutes(props: { app: App; server: State; actions: SidebarActions }) {
  const routes = memo(() => props.server.routes ?? []);
  const groups = memo(() => workspaceGroups(props.server));
  const recent = memo(() => routes().filter((route) => !route.live));
  const workspaces = map(
    () => groups().groups,
    (group) => group,
    { keyed: (group) => group.workspace.workspace_id },
  );
  return (
    <>
      <a
        href={`?r=${SESSION}`}
        class={props.app.view.route === SESSION && !props.app.view.ws ? "nav-item active" : "nav-item"}
        title="Every agent and pane in this herdr session"
        onClick={(event) => {
          event.preventDefault();
          props.actions.go(SESSION);
        }}
      >
        <Icon paths={icons.users} />
        <span class="nav-text">All agents</span>
        <span class="nav-meta">{String(props.server.snapshot.agents?.length ?? 0)}</span>
      </a>
      <div class="nav-head nav-head-row">
        <h3>Workspaces</h3>
        <button
          class="nav-new"
          title="New route: start an orchestrator or an adjutant in a new herdr tab"
          onClick={() => props.actions.newRoute()}
        >
          <Icon paths={icons.plus} />
          <span>New route</span>
        </button>
      </div>
      <Show when={groups().groups.length > 0} fallback={<p class="nav-empty">No workspace runs an agent.</p>}>
        <For each={workspaces()} key={(group) => group().workspace.workspace_id}>
          {(group) => <WorkspaceLinks group={group()} app={props.app} actions={props.actions} routes={routes()} />}
        </For>
      </Show>
      <Show when={groups().unplaced.length > 0}>
        <h3 class="nav-head">Other live routes</h3>
        <RouteLinks
          routes={groups().unplaced}
          all={routes()}
          selected={props.app.view.route}
          nested={false}
          actions={props.actions}
        />
      </Show>
      <Show when={recent().length > 0}>
        <h3 class="nav-head">Recent routes</h3>
        <RouteLinks
          routes={recent()}
          all={routes()}
          selected={props.app.view.route}
          nested={false}
          actions={props.actions}
        />
      </Show>
    </>
  );
}

export function Sidebar(props: SidebarProps) {
  const server = memo(() => (props.app.fatal ? null : props.app.server));
  return (
    <nav class="sidebar" aria-label="Routes">
      <div class="nav-scroll">
        <h3 class="nav-head">Workspace data</h3>
        <For each={pages.filter((page) => page.id !== "new-route")} key={(page) => page.id}>
          {(page) => (
            <a
              href={`?page=${page.id}`}
              class={props.app.view.page === page.id ? "nav-item nav-page active" : "nav-item nav-page"}
              aria-current={props.app.view.page === page.id ? "page" : null}
              onClick={(event) => {
                event.preventDefault();
                props.actions.page(page.id);
              }}
            >
              <Icon paths={page.icon} />
              <span class="nav-text">{page.label}</span>
            </a>
          )}
        </For>
        <Show when={server()}>
          {(current) => <SidebarRoutes app={props.app} server={current()} actions={props.actions} />}
        </Show>
      </div>
      <div class="nav-foot">
        <ThemeSwitch current={props.app.theme} choose={(theme) => props.actions.theme(theme)} />
        <button
          class="icon-btn"
          title="Appearance: terminal colors and code highlighting"
          aria-label="Appearance"
          onClick={() => props.actions.appearance()}
        >
          <Icon paths={icons.swatch} />
        </button>
      </div>
    </nav>
  );
}

export function FatalView(props: { reason: string; hosts: () => void }) {
  return (
    <div class="empty fatal">
      <h2>Not connected</h2>
      <p>{props.reason}</p>
      <button class="btn" type="button" onClick={() => props.hosts()}>
        <Icon paths={icons.server} />
        Hosts
      </button>
    </div>
  );
}
export function ConnectingView(props: { hostName: string }) {
  return (
    <div class="empty">
      <div class="spinner" aria-hidden="true" />
      <p>{`Connecting to ${props.hostName}…`}</p>
    </div>
  );
}
export function NotFoundView(props: { route: string }) {
  return (
    <div class="empty">
      <h2>Route not found</h2>
      <p>{`No route named ${props.route} under the panes' folders.`}</p>
    </div>
  );
}
