import { AuthError, openStream, pairStatus, readChannel, startSession, useHost } from "./api.ts";
import { applyCodeTheme, applyTermScheme, loadCodeTheme, loadTermScheme } from "./appearance.ts";
import { unread } from "./channel.ts";
import { buildCommands } from "./commands.ts";
import { render } from "./herdsman-blocks-view.ts";
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
import { canonicalRoute, Index, paneSize, team } from "./model.ts";
import { isMac, openPalette, paletteOpen } from "./palette.ts";
import { loadWidths, navResizer } from "./resize.ts";
import { type App, store } from "./store.ts";
import { installTooltips } from "./tooltip.ts";
import type { Snapshot } from "./types.ts";
import { applyTheme } from "./ui.ts";
import { AgentPanel } from "./views/agent.ts";
import { openAppearance } from "./views/appearance.ts";
import { ArtifactsView } from "./views/artifacts.ts";
import { DiffView } from "./views/diff.ts";
import { element, ElementView } from "./views/element-view.ts";
import { FilesView } from "./views/files.ts";
import { confirmHost, keepPendingLink, openHosts, showConnect, takePendingLink } from "./views/hosts.ts";
import { MessagesView } from "./views/messages.ts";
import { openNewRoute } from "./views/new-route.ts";
import { showPairing } from "./views/pair.ts";
import {
  activityView,
  checklistView,
  logView,
  routeShell,
  sessionView,
  type TabBody,
  tabRow,
  type TabsNode,
} from "./views/route.ts";
import {
  Body,
  connectingView,
  currentRoute,
  fatalView,
  MainColumn,
  notFoundView,
  PanelHost,
  Scrim,
  SESSION,
  sidebar,
  type SidebarNode,
  topbar,
  type TopbarNode,
} from "./views/shell.ts";
import { teamBoard } from "./views/team.ts";

const appRoot = document.getElementById("app");
if (!appRoot) throw new Error("index.html has no #app");
const root: HTMLElement = appRoot;

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

const topbarOf = (app: App): TopbarNode =>
  topbar(app, host, narrow.matches ? navOpen : !navCollapsed, {
    toggleNav,
    home: () => store.go({ route: "", pane: "", tab: "team" }),
    palette: () => showPalette(),
    hosts: openHosts,
  });

// A sidebar link or button closes the drawer first.
const sidebarOf = (app: App): SidebarNode =>
  sidebar(app, {
    go: (route, ws) => {
      navOpen = false;
      store.go(
        ws === undefined ? { route, tab: "team", file: "", pane: "" } : { route, ws, tab: "team", file: "", pane: "" },
      );
    },
    newRoute: () => {
      navOpen = false;
      openNewRoute();
    },
    theme: (t) => {
      applyTheme(t);
      store.set({ theme: t });
    },
    appearance: () => openAppearance(),
  });

// The frame. The topbar, the sidebar, the panel's column and the route's tab
// row are views whose roots are placed once and patched in place; the main
// column is a render container for whatever shows there.
const topbarView = new ElementView("header", topbarOf(store.app));
const nav = new ElementView("nav", sidebarOf(store.app));
const main = element("main", MainColumn([]));
const panelHost = new ElementView("div", PanelHost([null]));
// Behind the drawer on phones: a tap outside closes it.
const scrim = element(
  "div",
  Scrim([
    () => {
      navOpen = false;
      store.emit();
    },
  ]),
);
const frameBody = new ElementView("div", Body([nav.el, navResizer(nav.el), scrim, main, panelHost.el]));
root.replaceChildren(topbarView.el, frameBody.el);
// The route's tab row is kept, so the open tab can be scrolled into view.
let routeTabs: ElementView<"div", TabsNode> | null = null;

applyTheme(store.app.theme);
applyTermScheme(loadTermScheme(), false);
applyCodeTheme(loadCodeTheme(), false);
loadWidths();
installTooltips();
store.on(renderApp);

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
  if (link && link.url !== ownUrl && !hs.hosts.some((x) => x.url === link.url)) {
    if (!(await confirmHost(root, link.url))) {
      showConnect(
        root,
        "Host not added",
        "The link's host was not added. Pick a host you know, or paste a link you trust.",
      );
      return;
    }
    // The question took the frame's place; a host that answers "paired"
    // connects into the frame, so it comes back first.
    root.replaceChildren(topbarView.el, frameBody.el);
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
  root.replaceChildren(topbarView.el, frameBody.el);
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

function renderApp(app: App): void {
  document.body.classList.toggle("nav-open", navOpen);
  document.body.classList.toggle("nav-collapsed", navCollapsed);
  document.body.classList.toggle("has-panel", !!app.view.pane);
  topbarView.update(topbarOf(app));
  nav.update(sidebarOf(app));
  if (app.fatal) {
    panel?.destroy();
    panel = null;
    panelHost.update(PanelHost([null]));
    render(main, fatalView(app.fatal, openHosts));
    return;
  }
  if (!app.server) {
    render(main, connectingView(host?.name ?? "herdr"));
    return;
  }
  const idx = new Index(app.server.snapshot);
  renderMain(app, app.server.snapshot, idx);
  renderPanel(app, idx);
}

function renderMain(app: App, snap: Snapshot, idx: Index): void {
  if (app.view.tab !== "changes" || app.view.route !== changes?.routeId) changes?.stop();
  if (app.view.tab !== "artifacts" || app.view.route !== artifacts?.routeId) artifacts?.stop();
  if (app.view.route === SESSION || !app.view.route) {
    render(
      main,
      sessionView(app, snap, idx, (pane) => store.go({ pane })),
    );
    return;
  }
  const r = currentRoute(app);
  if (!r) {
    render(main, notFoundView(app.view.route));
    return;
  }
  const tabs = tabRow(r, app, unread(app.channel), (tab) => store.go({ tab }));
  if (routeTabs) routeTabs.update(tabs);
  else routeTabs = new ElementView("div", tabs);
  // A component's element is placed first and updated after, as before.
  let body: TabBody;
  let after = (): void => {};
  const tab = app.view.tab;
  if (tab === "docs") {
    const f = files?.routeId === r.id ? files : (files = new FilesView(r.id));
    body = f.el;
    after = () => f.update(r, app.view.file);
  } else if (tab === "artifacts") {
    if (artifacts?.routeId !== r.id) {
      artifacts?.stop();
      artifacts = new ArtifactsView(r.id);
    }
    const a = artifacts;
    body = a.el;
    after = () => {
      a.update(app.view.artifact);
      a.start();
    };
  } else if (tab === "messages") {
    body = messages.el;
    after = () => messages.render(app.channel, { adjutant: r.adjutant_pane ?? "", orchestrator: r.orchestrator_pane });
  } else if (tab === "changes") {
    if (changes?.routeId !== r.id) {
      changes?.stop();
      changes = new DiffView(r.id);
    }
    const c = changes;
    body = c.el;
    after = () => {
      c.update(app.view.source, app.view.diffFile);
      c.start();
    };
  } else
    body =
      tab === "checklist"
        ? checklistView(r)
        : tab === "log"
          ? logView(r)
          : tab === "activity"
            ? activityView(app, team(r, idx))
            : teamBoard(team(r, idx), app);
  render(main, routeShell(r, routeTabs.el, body));
  // On a phone the tab row scrolls; keep the open tab in view, Docs included.
  if (shownTab !== tab) {
    shownTab = tab;
    const row = routeTabs.el;
    const active = row.querySelector<HTMLElement>(".tab.active");
    if (active) {
      const left = active.getBoundingClientRect().left - row.getBoundingClientRect().left + row.scrollLeft;
      row.scrollLeft = left - (row.clientWidth - active.offsetWidth) / 2;
    }
  }
  after();
}

function renderPanel(app: App, idx: Index): void {
  const pane = app.view.pane;
  if (!pane) {
    panel?.destroy();
    panel = null;
    panelHost.update(PanelHost([null]));
    return;
  }
  if (panel?.pane !== pane) {
    panel?.destroy();
    panel = new AgentPanel(pane, () => [
      ...new Set((app.server?.snapshot.agents ?? []).map((a) => a.agent).filter((k): k is string => !!k)),
    ]);
    panelHost.update(PanelHost([panel.el]));
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
  const t = e.target instanceof HTMLElement ? e.target : null;
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
