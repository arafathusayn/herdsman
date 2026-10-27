import { activityPinger } from "./activity.ts";
import { AuthError, openStream, pairStatus, readChannel, sendActivity, startSession, useHost } from "./api.ts";
import { applyCodeTheme, applyTermScheme, loadCodeTheme, loadTermScheme } from "./appearance.ts";
import { unread } from "./channel.ts";
import { buildCommands } from "./commands.ts";
import { h, icon, icons, logoMark, mount } from "./dom.ts";
import {
  blockedReason,
  type Host,
  loadHosts,
  localNetworkHint,
  normalizeUrl,
  parseLink,
  saveHosts,
  upsert,
} from "./hosts.ts";
import {
  ago,
  canonicalRoute,
  checklistCounts,
  Index,
  type Member,
  paneSize,
  routeFor,
  routeLabel,
  statusLabel,
  team,
  type WorkspaceGroup,
  workspaceGroups,
} from "./model.ts";
import { isMac, openPalette, paletteKeys, paletteOpen, sidebarKeys } from "./palette.ts";
import { loadWidths, navResizer } from "./resize.ts";
import { type App, type RouteTab, store } from "./store.ts";
import { installTooltips } from "./tooltip.ts";
import type { Route, Workspace } from "./types.ts";
import { applyTheme, pill, themeSwitch, toast } from "./ui.ts";
import { AgentPanel } from "./views/agent.ts";
import { openAppearance } from "./views/appearance.ts";
import { ArtifactsView } from "./views/artifacts.ts";
import { DiffView } from "./views/diff.ts";
import { FilesView } from "./views/files.ts";
import { confirmHost, keepPendingLink, openHosts, showConnect, takePendingLink } from "./views/hosts.ts";
import { logView } from "./views/log-view.ts";
import { MessagesView } from "./views/messages.ts";
import { openNewRoute } from "./views/new-route.ts";
import { showPairing } from "./views/pair.ts";
import { card, teamView } from "./views/team.ts";

/** r=* is the whole-session view; an empty r picks the first live route. */
const SESSION = "*";

const root = document.getElementById("app") as HTMLElement;
const topbar = h("header", { class: "topbar" });
const sidebar = h("nav", { class: "sidebar", "aria-label": "Routes" });
const main = h("main", { class: "main", tabIndex: -1 });
const panelHost = h("div", { class: "panel-host" });
const navHandle = navResizer(sidebar);
// Behind the drawer on phones: a tap outside closes it.
const scrim = h("div", {
  class: "scrim",
  "aria-hidden": "true",
  onclick: () => {
    navOpen = false;
    store.emit();
  },
});
mount(root, topbar, h("div", { class: "body" }, sidebar, navHandle, scrim, main, panelHost));

let panel: AgentPanel | null = null;
let navOpen = false; // the drawer on narrow screens
let navCollapsed = readCollapsed(); // the sidebar on wide screens

const narrow = matchMedia("(max-width: 860px)");

function readCollapsed(): boolean {
  try {
    return localStorage.getItem("herdsman.nav") === "collapsed";
  } catch {
    return false;
  }
}

function toggleNav(): void {
  if (narrow.matches) navOpen = !navOpen;
  else {
    navCollapsed = !navCollapsed;
    try {
      localStorage.setItem("herdsman.nav", navCollapsed ? "collapsed" : "open");
    } catch {
      // Not remembered, still applied.
    }
  }
  store.emit();
}
let files: FilesView | null = null;
let changes: DiffView | null = null; // polls git only while its tab shows
let artifacts: ArtifactsView | null = null; // lists pages only while its tab shows
const messages = new MessagesView();
let host: Host | null = null;
let shownTab = "";

// The route view keeps these elements across renders, so a focused input in
// a tab body (the file search) survives live updates.
const routeHead = h("div", { class: "route-head" });
const routeTabs = h("div", { class: "tabs", role: "tablist" });
const routeBody = h("div", { class: "tab-body" });
const routeShell = h("div", { class: "route-shell" }, routeHead, routeTabs, routeBody);

applyTheme(store.app.theme);
applyTermScheme(loadTermScheme(), false);
applyCodeTheme(loadCodeTheme(), false);
loadWidths();
installTooltips();
store.on(render);
// The user's own clicks and keys (route tabs, Changes, a terminal) say the
// user is here, at most every 10 s (A33); timers never do.
const ping = activityPinger(sendActivity, 10_000);
document.addEventListener("pointerdown", ping, { capture: true, passive: true });
document.addEventListener("keydown", ping, { capture: true, passive: true });

boot().catch((e: unknown) => store.set({ fatal: e instanceof Error ? e.message : String(e) }));

/** Whether the page's own server is a herdsman host: true when `herdsman web` served the app. */
async function servesApi(origin: string): Promise<boolean> {
  try {
    const res = await fetch(`${origin}/api/pair`, { cache: "no-store", signal: AbortSignal.timeout(2500) });
    const body = (await res.json()) as { status?: unknown };
    return res.ok && typeof body.status === "string";
  } catch {
    return false;
  }
}

async function boot(): Promise<void> {
  const hs = loadHosts();
  // A pairing link: #t=TOKEN from `herdsman web`, or #host=URL&t=TOKEN for a
  // hosted app. It leaves the address bar and history at once.
  const link = /(?:^#|&)t=/.test(location.hash) ? parseLink(location.href, location.origin) : null;
  if (location.hash) history.replaceState(null, "", location.pathname + location.search);
  let token = "";
  // The page's own server and the hosts already here are trusted; a link that
  // names any other host asks first, since any page can open such a link.
  const ownUrl = normalizeUrl(location.origin + location.pathname);
  if (
    link &&
    link.url !== ownUrl &&
    !hs.hosts.some((x) => x.url === link.url) &&
    !(await confirmHost(root, link.url))
  ) {
    showConnect(
      root,
      "Host not added",
      "The link's host was not added. Pick a host you know, or paste a link you trust.",
    );
    return;
  }
  if (link) {
    upsert(hs, link.url);
    token = link.token;
    saveHosts(hs);
  } else if (!hs.hosts.length && (await servesApi(location.origin))) {
    const self = normalizeUrl(location.origin);
    if (self) {
      upsert(hs, self);
      saveHosts(hs);
    }
  }
  const current = hs.hosts.find((x) => x.id === hs.current);
  if (!current) {
    showConnect(
      root,
      "Connect to a host",
      "Paste the pairing link that `herdsman web` printed on the machine that runs your agents.",
    );
    return;
  }
  host = current;
  useHost(current);
  store.emit(); // shows "Connecting to …" while the host answers
  token ||= takePendingLink(current.url);
  let st: Awaited<ReturnType<typeof pairStatus>>;
  try {
    st = token ? await startSession(token) : await pairStatus();
  } catch {
    // The link's token waits for the host, so trying again (after the
    // browser's local network prompt, or once the server runs) still pairs.
    keepPendingLink(current.url, token);
    const blocked = blockedReason(location.protocol, current.url);
    const hint = localNetworkHint(location.hostname, current.url);
    showConnect(
      root,
      `Cannot reach ${current.name}`,
      blocked ||
        `Is herdsman web running at ${current.url}? Start it there, or pick another host.${hint ? ` ${hint}` : ""}`,
      blocked ? undefined : () => location.reload(),
    );
    return;
  }
  if (st.status === "paired") connect();
  else if (st.status === "pairing")
    showPairing(root, current.name, (device) => {
      rememberDevice(device);
      connectAfterPairing();
    });
  else {
    if (current.device) rememberDevice("");
    showConnect(
      root,
      token ? "That link did not work" : `Pair with ${current.name}`,
      st.error ??
        "This host does not know this browser yet, or its server restarted. Paste the pairing link that herdsman web printed there.",
    );
  }
}

/** Keeps the current host's device secret, or forgets it with "". */
function rememberDevice(device: string): void {
  if (!host) return;
  const hs = loadHosts();
  const x = hs.hosts.find((y) => y.id === host?.id);
  if (!x) return;
  if (device) x.device = device;
  else delete x.device;
  host = x;
  saveHosts(hs);
  useHost(x);
}

// Pairing replaces the app's frame until the browser is paired.
function connectAfterPairing(): void {
  mount(root, topbar, h("div", { class: "body" }, sidebar, navHandle, scrim, main, panelHost));
  connect();
}

function connect(): void {
  openStream({
    state: (s) => {
      store.set({ server: s });
      pickDefaultRoute();
    },
    entries: (e) => store.set({ entries: e }),
    entry: (e) => store.addEntries([e]),
    connection: (up) => store.set({ connected: up }),
    unauthorized: () => {
      rememberDevice("");
      showConnect(
        root,
        "Pairing ended",
        `${host?.name ?? "The host"} no longer knows this browser: its server restarted. Paste the new pairing link that herdsman web printed.`,
      );
    },
  });
  void pollChannel();
}

/** Reads the message channel: often while the Messages tab shows, now and then otherwise. */
async function pollChannel(): Promise<void> {
  try {
    store.set({ channel: await readChannel() });
  } catch {
    // Kept as it was; the next read tries again.
  }
  const fast = store.app.view.tab === "messages" && !!store.app.channel?.configured;
  setTimeout(() => void pollChannel(), fast ? 2500 : store.app.channel?.configured ? 10000 : 30000);
}

function pickDefaultRoute(): void {
  const { view, server } = store.app;
  if (view.route) {
    // A link by bare name moves to the id, so the sidebar marks it.
    const patch = canonicalRoute(server?.routes ?? [], view);
    if (patch) store.go(patch, false);
    return;
  }
  const live = server?.routes?.find((r) => r.live);
  store.go({ route: live ? live.id : SESSION }, false);
}

function currentRoute(app: App): Route | null {
  return routeFor(app.server?.routes ?? [], app.view.route);
}

function render(app: App): void {
  document.body.classList.toggle("nav-open", navOpen);
  document.body.classList.toggle("nav-collapsed", navCollapsed);
  document.body.classList.toggle("has-panel", !!app.view.pane);
  renderTopbar(app);
  if (app.fatal) {
    panel?.destroy();
    panel = null;
    panelHost.replaceChildren();
    mount(sidebar, h("div", { class: "nav-scroll" }), navFoot(app));
    mount(
      main,
      h(
        "div",
        { class: "empty fatal" },
        h("h2", null, "Not connected"),
        h("p", null, app.fatal),
        h("button", { class: "btn", type: "button", onclick: openHosts }, icon(icons.server), "Hosts"),
      ),
    );
    return;
  }
  if (!app.server) {
    mount(sidebar, h("div", { class: "nav-scroll" }), navFoot(app));
    mount(
      main,
      h(
        "div",
        { class: "empty" },
        h("div", { class: "spinner", "aria-hidden": "true" }),
        h("p", null, `Connecting to ${host?.name ?? "herdr"}…`),
      ),
    );
    return;
  }
  const idx = new Index(app.server.snapshot);
  renderSidebar(app);
  renderMain(app, idx);
  renderPanel(app, idx);
}

function renderTopbar(app: App): void {
  const s = app.server;
  const r = currentRoute(app);
  mount(
    topbar,
    h(
      "button",
      {
        class: "icon-btn nav-toggle",
        title: `Toggle the sidebar (${sidebarKeys})`,
        "aria-label": "Toggle the sidebar",
        "aria-keyshortcuts": isMac ? "Meta+B" : "Control+B Control+Shift+B",
        "aria-expanded": String(narrow.matches ? navOpen : !navCollapsed),
        onclick: toggleNav,
      },
      icon((narrow.matches ? navOpen : !navCollapsed) ? icons.sidebarOpen : icons.sidebar),
    ),
    h(
      "a",
      {
        class: "brand",
        href: "/",
        title: "All routes",
        onclick: (e: Event) => (e.preventDefault(), store.go({ route: "", pane: "", tab: "team" })),
      },
      logoMark(),
      h("span", null, "herdsman"),
    ),
    h(
      "span",
      { class: "crumb" },
      r ? routeLabel(r, app.server?.routes ?? []) : app.view.route === SESSION ? sessionCrumb(app) : "",
    ),
    h("span", { class: "grow" }),
    h(
      "button",
      {
        class: "palette-trigger",
        title: `Command palette (${paletteKeys})`,
        onclick: () => showPalette(),
      },
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
            onclick: openHosts,
          },
          icon(icons.server),
          h("span", { class: "label" }, host.name),
        )
      : null,
    s?.error ? h("span", { class: "conn", title: s.error }, "herdr unreachable") : null,
    // Only trouble shows: a live connection needs no badge, and before the
    // first one the main view says it is connecting.
    app.connected || !app.server
      ? null
      : h(
          "span",
          { class: "conn", title: "Reconnecting" },
          h("span", { class: "dot", "aria-hidden": "true" }),
          "Reconnecting…",
        ),
  );
}

function renderSidebar(app: App): void {
  const routes = app.server?.routes ?? [];
  const rest = routes.filter((r) => !r.live);
  const { groups, unplaced } = app.server ? workspaceGroups(app.server) : { groups: [], unplaced: [] };
  const item = (r: Route, nested = false) =>
    h(
      "a",
      {
        href: `?r=${encodeURIComponent(r.id)}`,
        class: ["nav-item", nested ? "nested" : "", app.view.route === r.id ? "active" : ""].filter(Boolean).join(" "),
        title: [
          r.title || r.name,
          r.live ? "running" : "not running",
          progress(r) && `${progress(r)} checklist lines done`,
        ]
          .filter(Boolean)
          .join(" · "),
        "aria-current": app.view.route === r.id ? "page" : undefined,
        onclick: (e: Event) => {
          e.preventDefault();
          navOpen = false;
          store.go({ route: r.id, tab: "team", file: "", pane: "" });
        },
      },
      h("span", {
        class: r.live ? "live-dot on" : "live-dot",
        "aria-hidden": "true",
      }),
      h("span", { class: "nav-text" }, routeLabel(r, routes)),
      h("span", { class: "nav-meta" }, progress(r)),
    );
  const agents = app.server?.snapshot.agents ?? [];
  const onSession = app.view.route === SESSION;
  const workspace = ({ workspace: w, agents: n, routes: rs }: WorkspaceGroup) => {
    const name = workspaceName(w);
    const active = onSession && app.view.ws === w.workspace_id;
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
          onclick: (e: Event) => {
            e.preventDefault();
            navOpen = false;
            store.go({
              route: SESSION,
              ws: w.workspace_id,
              tab: "team",
              file: "",
              pane: "",
            });
          },
        },
        h("span", {
          class: `ws-dot s-${w.agent_status}`,
          "aria-hidden": "true",
        }),
        h("span", { class: "nav-text" }, name),
        h("span", { class: "nav-meta" }, String(n)),
      ),
      rs.map((r) => item(r, true)),
    );
  };
  mount(
    sidebar,
    h(
      "div",
      { class: "nav-scroll" },
      h(
        "a",
        {
          href: `?r=${SESSION}`,
          class: onSession && !app.view.ws ? "nav-item active" : "nav-item",
          title: "Every agent and pane in this herdr session",
          onclick: (e: Event) => {
            e.preventDefault();
            navOpen = false;
            store.go({ route: SESSION, tab: "team", file: "", pane: "" });
          },
        },
        icon(icons.users),
        h("span", { class: "nav-text" }, "All agents"),
        h("span", { class: "nav-meta" }, String(agents.length)),
      ),
      h(
        "div",
        { class: "nav-head nav-head-row" },
        h("h3", null, "Workspaces"),
        h(
          "button",
          {
            class: "nav-new",
            title: "New route: start an orchestrator or an adjutant in a new herdr tab",
            onclick: () => {
              navOpen = false;
              openNewRoute();
            },
          },
          icon(icons.plus),
          h("span", null, "New route"),
        ),
      ),
      groups.length ? groups.map(workspace) : h("p", { class: "nav-empty" }, "No workspace runs an agent."),
      unplaced.length ? [h("h3", { class: "nav-head" }, "Other live routes"), unplaced.map((r) => item(r))] : null,
      rest.length ? [h("h3", { class: "nav-head" }, "Recent routes"), rest.map((r) => item(r))] : null,
    ),
    navFoot(app),
  );
}

function workspaceName(w: Workspace): string {
  return w.label || `Workspace ${w.number}`;
}

function sessionCrumb(app: App): string {
  const w = app.server?.snapshot.workspaces?.find((x) => x.workspace_id === app.view.ws);
  return w ? workspaceName(w) : "All agents";
}

/** The sidebar footer: the theme switch and the rest of Appearance, out of the way of the work. */
function navFoot(app: App): HTMLElement {
  return h(
    "div",
    { class: "nav-foot" },
    themeSwitch(app.theme, (t) => {
      applyTheme(t);
      store.set({ theme: t });
    }),
    h(
      "button",
      {
        class: "icon-btn",
        title: "Appearance: terminal colors and code highlighting",
        "aria-label": "Appearance",
        onclick: () => openAppearance(),
      },
      icon(icons.swatch),
    ),
  );
}

/**
 * The route folder as a chip: the project it belongs to, the full path on
 * hover, and a button that copies the path.
 */
function locationChip(dir: string): HTMLElement {
  const parts = dir.split("/").filter(Boolean);
  const at = parts.lastIndexOf(".herdsman");
  const project = at > 0 ? parts[at - 1] : parts.at(-2);
  const copy = h(
    "button",
    {
      class: "chip-btn",
      title: "Copy the route folder path",
      "aria-label": "Copy the route folder path",
      onclick: async () => {
        try {
          await navigator.clipboard.writeText(dir);
          toast("Copied the route folder path.");
        } catch {
          toast("The browser blocked the clipboard.", "error");
        }
      },
    },
    icon(icons.copy),
  );
  return h(
    "span",
    { class: "location", title: dir },
    icon(icons.folder),
    h("span", { class: "location-name" }, project ?? dir),
    h("span", { class: "location-tail" }, at > 0 ? "/.herdsman" : ""),
    copy,
  );
}

function progress(r: Route): string {
  const c = checklistCounts(r.checklist ?? []);
  if (!c.total) return "";
  return `${c.done + c.skipped}/${c.total}`;
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

function renderMain(app: App, idx: Index): void {
  if (app.view.tab !== "changes" || app.view.route !== changes?.routeId) changes?.stop();
  if (app.view.tab !== "artifacts" || app.view.route !== artifacts?.routeId) artifacts?.stop();
  if (app.view.route === SESSION || !app.view.route) {
    mount(main, sessionView(app, idx));
    return;
  }
  const r = currentRoute(app);
  if (!r) {
    mount(
      main,
      h(
        "div",
        { class: "empty" },
        h("h2", null, "Route not found"),
        h("p", null, `No route named ${app.view.route} under the panes' folders.`),
      ),
    );
    return;
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
  const tabs: [RouteTab, string, string[]][] = [
    ["team", "Team", icons.users],
    ["checklist", "Checklist", icons.check],
    ["log", "Run state", icons.list],
    ["docs", "Docs", icons.file],
    ["changes", "Changes", icons.diff],
    ["artifacts", "Artifacts", icons.artifact],
    ["messages", "Messages", icons.message],
    ["activity", "Activity", icons.activity],
  ];
  // Messages shows for a route with an adjutant, or when a channel is set up.
  const withMessages = !!r.adjutant_pane || !!app.channel?.configured;
  const shown = tabs.filter(([id]) => id !== "messages" || withMessages || app.view.tab === "messages");
  const pending = unread(app.channel);
  // A `[-]` line is settled with its reason, as the route's close check counts it.
  const c = checklistCounts(r.checklist ?? []);
  const done = c.done + c.skipped;
  if (main.firstChild !== routeShell) mount(main, routeShell);
  mount(
    routeHead,
    h(
      "div",
      null,
      h("h1", null, r.title || r.name),
      h(
        "p",
        { class: "sub" },
        h("span", { class: r.live ? "badge live" : "badge" }, r.live ? "Live" : "Not running"),
        locationChip(r.dir),
        h(
          "span",
          { class: "meta-item", title: new Date(r.modified).toLocaleString() },
          icon(icons.clock),
          ago(r.modified),
        ),
      ),
    ),
    c.total
      ? h(
          "div",
          {
            class: "progress",
            title: progressTip(c),
          },
          h(
            "div",
            { class: "bar" },
            h("span", {
              style: `width:${Math.round((done / c.total) * 100)}%`,
            }),
          ),
          h("small", null, `${done}/${c.total}`),
        )
      : null,
  );
  mount(
    routeTabs,
    shown.map(([id, label, glyph]) =>
      h(
        "button",
        {
          role: "tab",
          class: app.view.tab === id ? "tab active" : "tab",
          title: tabTips[id],
          "aria-selected": String(app.view.tab === id),
          onclick: () => store.go({ tab: id }),
        },
        icon(glyph),
        label,
        id === "messages" && pending
          ? h("span", { class: "tab-count", "aria-label": `${pending} unread` }, String(pending))
          : null,
      ),
    ),
  );
  // On a phone the tab row scrolls; keep the open tab in view, Docs included.
  if (shownTab !== app.view.tab) {
    shownTab = app.view.tab;
    const active = routeTabs.querySelector<HTMLElement>(".tab.active");
    if (active) {
      const left = active.getBoundingClientRect().left - routeTabs.getBoundingClientRect().left + routeTabs.scrollLeft;
      routeTabs.scrollLeft = left - (routeTabs.clientWidth - active.offsetWidth) / 2;
    }
  }
  if (app.view.tab === "docs") {
    if (files?.routeId !== r.id) files = new FilesView(r.id);
    if (routeBody.firstChild !== files.el) mount(routeBody, files.el);
    files.update(r, app.view.file);
    return;
  }
  if (app.view.tab === "artifacts") {
    if (artifacts?.routeId !== r.id) {
      artifacts?.stop();
      artifacts = new ArtifactsView(r.id);
    }
    if (routeBody.firstChild !== artifacts.el) mount(routeBody, artifacts.el);
    artifacts.update(app.view.artifact);
    artifacts.start();
    return;
  }
  if (app.view.tab === "messages") {
    if (routeBody.firstChild !== messages.el) mount(routeBody, messages.el);
    messages.render(app.channel, { adjutant: r.adjutant_pane ?? "", orchestrator: r.orchestrator_pane });
    return;
  }
  if (app.view.tab === "changes") {
    if (changes?.routeId !== r.id) {
      changes?.stop();
      changes = new DiffView(r.id);
    }
    if (routeBody.firstChild !== changes.el) mount(routeBody, changes.el);
    changes.update(app.view.source, app.view.diffFile);
    changes.start();
    return;
  }
  mount(
    routeBody,
    app.view.tab === "checklist"
      ? checklistView(r)
      : app.view.tab === "log"
        ? logView(r)
        : app.view.tab === "activity"
          ? activityView(app, team(r, idx))
          : teamView(team(r, idx), app),
  );
}

function sessionView(app: App, idx: Index): HTMLElement {
  const snap = app.server!.snapshot;
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
  return h(
    "div",
    null,
    h(
      "div",
      { class: "route-head" },
      h(
        "div",
        null,
        h("h1", null, only ? workspaceName(only) : "All agents"),
        h(
          "p",
          { class: "sub" },
          `${agents.length} agents in ${tabCount} tabs · ${only ? only.workspace_id : `herdr ${snap.version}`}`,
        ),
      ),
    ),
    h(
      "div",
      { class: "board" },
      [...byTab].map(([tabId, ms]) => {
        const t = idx.tabs.get(tabId);
        return h(
          "section",
          { class: "group" },
          h(
            "h2",
            null,
            only ? t?.label || tabId : `${wsLabel.get(t?.workspace_id ?? "") ?? ""} › ${t?.label || tabId}`,
            h("span", { class: "count" }, String(ms.length)),
          ),
          h(
            "div",
            { class: "cards" },
            ms.map((m) => card(m, app)),
          ),
        );
      }),
      idle.length
        ? h(
            "details",
            { class: "group shells" },
            h("summary", null, `Workspaces without an agent (${idle.length})`),
            h(
              "div",
              { class: "shell-list" },
              idle.map(({ w, panes }) => {
                const tabs = new Set(panes.map((p) => p.tab_id)).size;
                return h(
                  "details",
                  { class: "workspace-row" },
                  h(
                    "summary",
                    {
                      title: `${w.label || w.workspace_id}: ${tabs} tabs, ${panes.length} panes, no agent`,
                    },
                    h("code", null, w.workspace_id),
                    h("span", null, w.label || ""),
                    h(
                      "span",
                      { class: "muted" },
                      `${tabs} ${tabs === 1 ? "tab" : "tabs"} · ${panes.length} ${panes.length === 1 ? "pane" : "panes"}`,
                    ),
                  ),
                  panes.map((p) =>
                    h(
                      "button",
                      {
                        class: "shell-row",
                        title: `Open pane ${p.pane_id}${p.cwd ? ` in ${p.cwd}` : ""}`,
                        onclick: () => store.go({ pane: p.pane_id }),
                      },
                      h("code", null, p.pane_id),
                      h("span", null, p.label || idx.tabs.get(p.tab_id)?.label || ""),
                      h("span", { class: "muted clip" }, p.cwd ?? ""),
                    ),
                  ),
                );
              }),
            ),
          )
        : null,
    ),
  );
}

function checklistView(r: Route): HTMLElement {
  const items = r.checklist ?? [];
  if (!items.length) return h("p", { class: "muted pad" }, "This route has no checklist.");
  return h(
    "ul",
    { class: "checklist" },
    items.map((i) => {
      const state = i.done ? "Done" : i.skipped ? "Not applicable" : "Open";
      return h(
        "li",
        {
          class: i.done ? "done" : i.skipped ? "skipped" : "",
          title: i.skipped ? "Stopped applying; the reason follows the line" : null,
        },
        h("span", { class: "box", "aria-label": state }, i.done ? icon(icons.check) : i.skipped ? "–" : null),
        i.id ? h("code", null, i.id) : null,
        h("span", null, i.text),
      );
    }),
  );
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

function activityView(app: App, members: Member[]): HTMLElement {
  const byPane = new Map(members.map((m) => [m.pane_id, m]));
  const items = app.entries
    .filter((e) => !e.pane_id || byPane.has(e.pane_id))
    .slice(-200)
    .reverse();
  if (!items.length) return h("p", { class: "muted pad" }, "Nothing has happened since this page opened.");
  return h(
    "ol",
    { class: "timeline activity" },
    items.map((e) => {
      const who = e.pane_id ? h("strong", null, byPane.get(e.pane_id)?.title ?? e.pane_id) : null;
      const body =
        e.source === "web"
          ? [h("span", { class: "chip accent" }, "web"), h("span", null, e.text ?? "")]
          : e.status
            ? [who, pill(e.status)]
            : [h("span", { class: "chip" }, eventName(e.kind)), who];
      return h(
        "li",
        { class: e.source },
        h("time", { datetime: e.at, title: new Date(e.at).toLocaleString() }, clock.format(new Date(e.at))),
        h("p", { class: "event" }, body),
      );
    }),
  );
}

function renderPanel(app: App, idx: Index): void {
  const pane = app.view.pane;
  if (!pane) {
    panel?.destroy();
    panel = null;
    panelHost.replaceChildren();
    return;
  }
  if (panel?.pane !== pane) {
    panel?.destroy();
    panel = new AgentPanel(pane, () => [
      ...new Set((app.server?.snapshot.agents ?? []).map((a) => a.agent).filter((k): k is string => !!k)),
    ]);
    panelHost.replaceChildren(panel.el);
  }
  const r = currentRoute(app);
  const m = r ? team(r, idx).find((x) => x.pane_id === pane) : undefined;
  panel.update(m ?? idx.member(pane, "agent", null), paneSize(idx.snap, pane), !!r?.adjutant_pane);
}

// Keyboard: j/k move between cards, / focuses the composer, Esc closes.
function showPalette(initial = ""): void {
  const app = store.app;
  if (!app.server) return;
  const idx = new Index(app.server.snapshot);
  openPalette(
    () =>
      buildCommands({
        app: store.app,
        idx,
        route: currentRoute(store.app),
        session: SESSION,
        toggleNav,
        focusComposer: () => panel?.focusComposer(),
        focusFileSearch: () => files?.focusSearch(),
        focusChangeSearch: () => changes?.focusSearch(),
        focusArtifactSearch: () => artifacts?.focusSearch(),
      }),
    initial,
  );
}

// ⌘K / Ctrl+K (and ⌘⇧P, as in VS Code) open the palette from anywhere.
window.addEventListener("keydown", (e) => {
  const mod = isMac ? e.metaKey : e.ctrlKey;
  if (mod && !e.altKey && (e.key.toLowerCase() === "k" || (e.shiftKey && e.key.toLowerCase() === "p"))) {
    e.preventDefault();
    if (!paletteOpen()) showPalette(e.shiftKey ? ">" : "");
  }
  // ⌘B / Ctrl+B shows or hides the sidebar, as in VS Code. Typing in the
  // terminal view keeps Ctrl+B for the pane (that field stops the event), so
  // off a Mac Ctrl+Shift+B does it too, from anywhere.
  const sidebarKey = mod && !e.altKey && e.code === "KeyB" && (!e.shiftKey || !isMac);
  if (sidebarKey && !paletteOpen()) {
    e.preventDefault();
    toggleNav();
  }
});

window.addEventListener("keydown", (e) => {
  const t = e.target as HTMLElement | null;
  if (t && (t.closest("input, textarea, select, dialog") || t.isContentEditable)) {
    if (e.key === "Escape" && t.tagName === "TEXTAREA") t.blur();
    return;
  }
  if (e.metaKey || e.ctrlKey || e.altKey) return;
  if (e.key === "Escape" && navOpen) {
    navOpen = false;
    store.emit();
  } else if (e.key === "Escape" && store.app.view.pane) store.go({ pane: "" });
  else if (e.key === "/" && panel) {
    e.preventDefault();
    panel.focusComposer();
  } else if (e.key === "/" && files && store.app.view.tab === "docs") {
    e.preventDefault();
    files.focusSearch();
  } else if (e.key === "/" && changes && store.app.view.tab === "changes") {
    e.preventDefault();
    changes.focusSearch();
  } else if (e.key === "/" && artifacts && store.app.view.tab === "artifacts") {
    e.preventDefault();
    artifacts.focusSearch();
  } else if (e.key === "j" || e.key === "k") {
    const cards = [...main.querySelectorAll<HTMLElement>(".card")];
    if (!cards.length) return;
    const at = cards.findIndex((c) => c === document.activeElement || c.dataset["pane"] === store.app.view.pane);
    const next = cards[Math.max(0, Math.min(cards.length - 1, at + (e.key === "j" ? 1 : -1)))];
    next?.focus();
    next?.click();
  }
});

// A new pairing link from a restarted server pasted into this tab: start over with it.
window.addEventListener("hashchange", () => {
  if (/(?:^#|&)t=/.test(location.hash)) location.reload();
});

window.addEventListener("unhandledrejection", (e) => {
  if (e.reason instanceof AuthError) store.set({ fatal: e.reason.message });
});
