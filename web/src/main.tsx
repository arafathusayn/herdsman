import { batch, effect, Match, memo, onCleanup, Show, Switch, untrack } from "@reactre/core";
import { createActor } from "@reactre/state";

import { activityPinger } from "./activity.ts";
import { AuthError, openStream, pairStatus, readChannel, sendActivity, startSession, useHost } from "./api.ts";
import { applyCodeTheme, applyTermScheme, loadCodeTheme, loadTermScheme } from "./appearance.ts";
import { unread } from "./channel.ts";
import { buildCommands } from "./commands.ts";
import { mountRoot } from "./components/mount-root.ts";
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
import { pageById } from "./pages/index.tsx";
import { isMac, openPalette, paletteOpen } from "./palette.tsx";
import { loadWidths, navResizer } from "./resize.tsx";
import { bindHost } from "./sdk/index.ts";
import { lifetime } from "./state/lifetime.ts";
import { createPoll } from "./state/poll.ts";
import { registerRequest } from "./state/request-service.ts";
import { controllerScope } from "./state/scope.ts";
import { startupMachine } from "./state/startup.machine.ts";
import { valueState } from "./state/value.ts";
import { decodeValue, encodeValue } from "./state/wire.ts";
import { type App, store } from "./store.ts";
import { installTooltips } from "./tooltip.tsx";
import type { Route, State } from "./types.ts";
import { applyTheme } from "./ui.ts";
import { type AgentHandle, AgentPanel } from "./views/agent.tsx";
import { openAppearance } from "./views/appearance.tsx";
import { type ArtifactsState, ArtifactsView, createArtifactsState } from "./views/artifacts.tsx";
import { createDiffState, type DiffState, DiffView } from "./views/diff.tsx";
import { createFilesState, type FilesState, FilesView } from "./views/files.tsx";
import {
  confirmHost,
  disposeHostScreen,
  keepPendingLink,
  openHosts,
  showConnect,
  takePendingLink,
} from "./views/hosts.tsx";
import { LogView } from "./views/log-view.tsx";
import { createMessagesState, MessagesView } from "./views/messages.tsx";
import { showPairing } from "./views/pair.tsx";
import { ActivityView, ChecklistView, RouteShell, SessionView, TabRow } from "./views/route.tsx";
import {
  ConnectingView,
  currentRoute,
  FatalView,
  NotFoundView,
  SESSION,
  Sidebar,
  type SidebarActions,
  Topbar,
} from "./views/shell.tsx";
import { TeamBoard } from "./views/team.tsx";

const appRoot = document.getElementById("app");
if (!appRoot) throw new Error("index.html has no #app");
const root: HTMLElement = appRoot;

let panel: AgentHandle | null = null;
let disposePairing: (() => void) | null = null;
const [navOpen, setNavOpen] = valueState(false);
const [navCollapsed, setNavCollapsed] = valueState(readCollapsed());

const narrow = matchMedia("(max-width: 860px)");
const [isNarrow, setNarrow] = valueState(narrow.matches);
narrow.addEventListener("change", (event) =>
  batch(() => {
    setNarrow(event.matches);
    setNavOpen(false);
  }),
);

function readCollapsed(): boolean {
  try {
    return localStorage.getItem("herdsman.nav") === "collapsed";
  } catch {
    return false;
  }
}

function toggleNav(): void {
  if (isNarrow()) setNavOpen((open) => !open);
  else {
    setNavCollapsed((collapsed) => !collapsed);
    try {
      localStorage.setItem("herdsman.nav", navCollapsed() ? "collapsed" : "open");
    } catch {
      // Not remembered, still applied.
    }
  }
}
let files: (FilesState & { dispose(): void }) | null = null;
let changes: (DiffState & { dispose(): void }) | null = null; // polls git only while its tab shows
let artifacts: (ArtifactsState & { dispose(): void }) | null = null; // lists pages only while its tab shows
const messages = createMessagesState();
const [host, setHost] = valueState<Host | null>(null);

const sidebarActions: SidebarActions = {
  go: (route, ws) =>
    batch(() => {
      setNavOpen(false);
      store.go(
        ws === undefined ? { route, tab: "team", file: "", pane: "" } : { route, ws, tab: "team", file: "", pane: "" },
      );
    }),
  newRoute: () =>
    batch(() => {
      setNavOpen(false);
      store.go({ page: "new-route", pane: "" });
    }),
  page: (page) =>
    batch(() => {
      setNavOpen(false);
      store.go({ page, pane: "" });
    }),
  theme: (theme) => {
    applyTheme(theme);
    store.set({ theme });
  },
  appearance: openAppearance,
};

const topbarView = mountRoot("header", () => (
  <Topbar
    app={store.state()}
    host={host()}
    navShown={isNarrow() ? navOpen() : !navCollapsed()}
    actions={{
      toggleNav,
      home: () => store.go({ route: "", pane: "", tab: "team" }),
      palette: () => showPalette(),
      hosts: openHosts,
    }}
  />
));
const nav = mountRoot("nav", () => <Sidebar app={store.state()} actions={sidebarActions} />);
const main = mountRoot("main", () => (
  <main class="main" tabindex="-1">
    <MainContent />
  </main>
)).el;
const panelHost = mountRoot("div", () => (
  <div class="panel-host">
    <PanelContent />
  </div>
));
const frameBody = mountRoot("div", () => (
  <div class="body">
    <>
      {nav.el}
      {navResizer(nav.el)}
      <div class="scrim" aria-hidden="true" onClick={() => setNavOpen(false)} />
      {main}
      {panelHost.el}
    </>
  </div>
));
root.replaceChildren(topbarView.el, frameBody.el);
effect(() => {
  document.body.classList.toggle("nav-open", navOpen());
});
effect(() => {
  document.body.classList.toggle("nav-collapsed", navCollapsed());
});

applyTheme(store.app.theme);
applyTermScheme(loadTermScheme(), false);
applyCodeTheme(loadCodeTheme(), false);
loadWidths();
installTooltips();
effect(() => {
  document.body.classList.toggle("has-panel", !!store.state().view.pane);
});
// The user's own clicks and keys (route tabs, Changes, a terminal) say the
// user is here, at most every 10 s (A33); timers never do.
const ping = activityPinger(sendActivity, 10_000);
document.addEventListener("pointerdown", ping, {
  capture: true,
  passive: true,
});
document.addEventListener("keydown", ping, { capture: true, passive: true });

boot();

/** Whether the page's own server is a herdsman host: true when `herdsman web` served the app. */
async function servesApi(origin: string): Promise<boolean> {
  try {
    const res = await fetch(`${origin}/api/pair`, {
      cache: "no-store",
      signal: AbortSignal.timeout(2500),
    });
    const body = (await res.json()) as { status?: unknown };
    return res.ok && typeof body.status === "string";
  } catch {
    return false;
  }
}

/** Startup decisions live in the actor; callbacks below only perform each step's I/O. */
function boot(): void {
  interface Context {
    hosts: ReturnType<typeof loadHosts>;
    link: ReturnType<typeof parseLink>;
    token: string;
    current?: Host;
  }
  const service = registerRequest(async (json, signal) => {
    const command = JSON.parse(json) as { step: string; context: string };
    const reply = (status: string, context: Context) => JSON.stringify({ status, context: encodeValue(context) });
    if (command.step === "inspecting") {
      const hosts = loadHosts();
      const link = /(?:^#|&)t=/.test(location.hash) ? parseLink(location.href, location.origin) : null;
      if (location.hash) history.replaceState(null, "", location.pathname + location.search);
      const ownUrl = normalizeUrl(location.origin + location.pathname);
      const trust = link && link.url !== ownUrl && !hosts.hosts.some((host) => host.url === link.url);
      return reply(trust ? "trust" : "select", { hosts, link, token: "" });
    }
    const context = decodeValue<Context>(command.context);
    const { hosts, link } = context;
    if (command.step === "trusting") {
      const accepted = await confirmHost(root, link!.url);
      signal.throwIfAborted();
      if (!accepted) {
        showConnect(
          root,
          "Host not added",
          "The link's host was not added. Pick a host you know, or paste a link you trust.",
        );
        return reply("unavailable", context);
      }
      disposeHostScreen(root);
      root.replaceChildren(topbarView.el, frameBody.el);
      return reply("select", context);
    }
    if (command.step === "selecting") {
      if (link) {
        upsert(hosts, link.url);
        context.token = link.token;
        saveHosts(hosts);
      } else if (!hosts.hosts.length && (await servesApi(location.origin))) {
        signal.throwIfAborted();
        const self = normalizeUrl(location.origin);
        if (self) {
          upsert(hosts, self);
          saveHosts(hosts);
        }
      }
      signal.throwIfAborted();
      const current = hosts.hosts.find((item) => item.id === hosts.current);
      if (!current) {
        showConnect(
          root,
          "Connect to a host",
          "Paste the pairing link that `herdsman web` printed on the machine that runs your agents.",
        );
        return reply("unavailable", context);
      }
      bindHost(current);
      setHost(current);
      useHost(current);
      context.current = current;
      context.token ||= takePendingLink(current.url);
      return reply("check", context);
    }
    const current = context.current!;
    let result: Awaited<ReturnType<typeof pairStatus>>;
    try {
      result = context.token ? await startSession(context.token, signal) : await pairStatus(signal);
    } catch {
      signal.throwIfAborted();
      keepPendingLink(current.url, context.token);
      const blocked = blockedReason(location.protocol, current.url);
      const hint = localNetworkHint(location.hostname, current.url);
      showConnect(
        root,
        `Cannot reach ${current.name}`,
        blocked ||
          `Is herdsman web running at ${current.url}? Start it there, or pick another host.${hint ? ` ${hint}` : ""}`,
        blocked ? undefined : () => location.reload(),
      );
      return reply("unavailable", context);
    }
    signal.throwIfAborted();
    if (result.status === "none") {
      if (current.device) rememberDevice("");
      showConnect(
        root,
        context.token ? "That link did not work" : `Pair with ${current.name}`,
        result.error ??
          "This host does not know this browser yet, or its server restarted. Paste the pairing link that herdsman web printed there.",
      );
    }
    return reply(result.status, context);
  });
  const actor = createActor(startupMachine, { input: { service: service.id, context: "" } });
  const off = actor.subscribe((snapshot) => {
    if (snapshot.state === "connected") connect();
    else if (snapshot.state === "pairing") {
      disposeHostScreen(root);
      disposePairing = showPairing(root, host()?.name ?? "Host", (device) => {
        rememberDevice(device);
        connectAfterPairing();
      });
    } else if (snapshot.state === "failed") store.set({ fatal: snapshot.data.error });
    if (actor.status() === "done") {
      off();
      service.dispose();
    }
  });
  window.addEventListener("pagehide", (event) => {
    if (event.persisted) return;
    actor.send({ type: "cancel" });
    actor.stop();
    off();
    service.dispose();
    disposeHostScreen(root);
    disposePairing?.();
  });
}

/** Keeps the current host's device secret, or forgets it with "". */
function rememberDevice(device: string): void {
  const current = host();
  if (!current) return;
  const hs = loadHosts();
  const x = hs.hosts.find((y) => y.id === current.id);
  if (!x) return;
  if (device) x.device = device;
  else delete x.device;
  bindHost(x);
  setHost(x);
  saveHosts(hs);
  useHost(x);
}

// Pairing replaces the app's frame until the browser is paired.
function connectAfterPairing(): void {
  disposePairing?.();
  disposePairing = null;
  root.replaceChildren(topbarView.el, frameBody.el);
  connect();
}

let closeStream = (): void => {};
const channelPoll = createPoll(async (signal) => {
  const channel = await readChannel(signal);
  if (!signal.aborted) store.set({ channel });
  const fast = store.app.view.tab === "messages" && !!channel.configured;
  return fast ? 2500 : channel.configured ? 10000 : 30000;
}, 10000);
window.addEventListener("pagehide", (event) => {
  if (event.persisted) return;
  closeStream();
  channelPoll.dispose();
  messages.dispose();
  files?.dispose();
  changes?.stop();
  changes?.dispose();
  artifacts?.stop();
  artifacts?.dispose();
});

function connect(): void {
  closeStream();
  channelPoll.stop();
  closeStream = openStream({
    state: (s) =>
      batch(() => {
        store.set({ server: s });
        pickDefaultRoute();
      }),
    entries: (e) => store.set({ entries: e }),
    entry: (e) => store.addEntries([e]),
    connection: (up) => store.set({ connected: up }),
    unauthorized: () => {
      channelPoll.stop();
      rememberDevice("");
      showConnect(
        root,
        "Pairing ended",
        `${host()?.name ?? "The host"} no longer knows this browser: its server restarted. Paste the new pairing link that herdsman web printed.`,
      );
    },
  }).close;
  channelPoll.start();
}

function pickDefaultRoute(): void {
  const { view, server } = store.app;
  // Moving to a route would close the page this link opened.
  if (view.page) return;
  if (view.route) {
    // A link by bare name moves to the id, so the sidebar marks it.
    const patch = canonicalRoute(server?.routes ?? [], view);
    if (patch) store.go(patch, false);
    return;
  }
  const live = server?.routes?.find((r) => r.live);
  store.go({ route: live ? live.id : SESSION }, false);
}

function MainContent() {
  const app = store.state;
  return (
    <Show when={!app().fatal} fallback={<FatalView reason={app().fatal} hosts={openHosts} />}>
      <Show
        when={host() && pageById(app().view.page)}
        keyed
        fallback={
          <Show when={app().server} fallback={<ConnectingView hostName={host()?.name ?? "herdr"} />}>
            {(server) => <ConnectedContent app={app()} server={server()} />}
          </Show>
        }
      >
        {(page) => page.view()}
      </Show>
    </Show>
  );
}

function ConnectedContent(props: { app: App; server: State }) {
  const idx = memo(() => new Index(props.server.snapshot));
  const route = memo(() => currentRoute(props.app));
  return (
    <Show
      when={props.app.view.route !== SESSION && !!props.app.view.route}
      fallback={
        <SessionView app={props.app} snap={props.server.snapshot} idx={idx()} open={(pane) => store.go({ pane })} />
      }
    >
      <Show when={route()} fallback={<NotFoundView route={props.app.view.route} />}>
        {(current) => (
          <Show when={current().id} keyed>
            {() => <RouteScreen route={current()} app={props.app} idx={idx()} />}
          </Show>
        )}
      </Show>
    </Show>
  );
}

function RouteScreen(props: { route: Route; app: App; idx: Index }) {
  let row!: HTMLDivElement;
  const [shown, setShown] = valueState("");
  const life = lifetime();
  onCleanup(() => {
    life.close();
  });
  effect(() => {
    const tab = props.app.view.tab;
    queueMicrotask(() => {
      if (life.closed() || shown() === tab || props.app.view.tab !== tab) return;
      setShown(tab);
      const active = row?.querySelector<HTMLElement>(".tab.active");
      if (active) {
        const left = active.getBoundingClientRect().left - row.getBoundingClientRect().left + row.scrollLeft;
        row.scrollLeft = left - (row.clientWidth - active.offsetWidth) / 2;
      }
    });
  });
  return (
    <RouteShell
      route={props.route}
      tabs={
        <TabRow
          route={props.route}
          app={props.app}
          unread={unread(props.app.channel)}
          pick={(tab) => store.go({ tab })}
          ref={(element) => {
            row = element;
          }}
        />
      }
    >
      <Switch>
        <Match when={props.app.view.tab === "docs"}>
          <DocsTab route={props.route} selected={props.app.view.file} />
        </Match>
        <Match when={props.app.view.tab === "changes"}>
          <ChangesTab route={props.route} app={props.app} />
        </Match>
        <Match when={props.app.view.tab === "artifacts"}>
          <ArtifactsTab route={props.route} selected={props.app.view.artifact} />
        </Match>
        <Match when={props.app.view.tab === "messages"}>
          <MessagesTab route={props.route} channel={props.app.channel} />
        </Match>
        <Match when={props.app.view.tab === "checklist"}>
          <ChecklistView route={props.route} />
        </Match>
        <Match when={props.app.view.tab === "log"}>
          <LogView route={props.route} />
        </Match>
        <Match when={props.app.view.tab === "activity"}>
          <ActivityView app={props.app} members={team(props.route, props.idx)} />
        </Match>
        <Match when={props.app.view.tab === "team"}>
          <TeamBoard members={team(props.route, props.idx)} app={props.app} />
        </Match>
      </Switch>
    </RouteShell>
  );
}

function DocsTab(props: { route: Route; selected: string }) {
  if (files?.routeId !== props.route.id) {
    files?.dispose();
    files = controllerScope(() => createFilesState(props.route.id));
  }
  const view = files;
  effect(() => {
    const route = props.route,
      selected = props.selected;
    untrack(() => view.update(route, selected));
  });
  return <FilesView state={view} />;
}

function ChangesTab(props: { route: Route; app: App }) {
  if (changes?.routeId !== props.route.id) {
    changes?.stop();
    changes?.dispose();
    changes = controllerScope(() => createDiffState(props.route.id));
  }
  const view = changes;
  effect(() => {
    const selected = props.app.view;
    untrack(() => {
      view.update(selected.source, selected.diffFile);
      view.start();
    });
  });
  onCleanup(() => view.stop());
  return <DiffView state={view} />;
}

function ArtifactsTab(props: { route: Route; selected: string }) {
  if (artifacts?.routeId !== props.route.id) {
    artifacts?.stop();
    artifacts?.dispose();
    artifacts = controllerScope(() => createArtifactsState(props.route.id));
  }
  const view = artifacts;
  effect(() => {
    const selected = props.selected;
    untrack(() => {
      view.update(selected);
      view.start();
    });
  });
  onCleanup(() => view.stop());
  return <ArtifactsView state={view} />;
}

function MessagesTab(props: { route: Route; channel: App["channel"] }) {
  onCleanup(() => messages.stop());
  return (
    <MessagesView
      channel={props.channel}
      panes={{
        adjutant: props.route.adjutant_pane ?? "",
        orchestrator: props.route.orchestrator_pane,
      }}
      state={messages}
    />
  );
}

function PanelContent() {
  const pane = memo(() => {
    const app = store.state();
    return !app.fatal && app.server ? app.view.pane : "";
  });
  return (
    <Show when={pane()} keyed>
      {(pane) => <PanePanel pane={pane} />}
    </Show>
  );
}

function PanePanel(props: { pane: string }) {
  const data = memo(() => {
    const app = store.state();
    if (!app.server) return null;
    const idx = new Index(app.server.snapshot);
    const route = currentRoute(app);
    const member = route ? team(route, idx).find((member) => member.pane_id === props.pane) : undefined;
    return {
      member: member ?? idx.member(props.pane, "agent", null),
      size: paneSize(idx.snap, props.pane),
      hasAdjutant: !!route?.adjutant_pane,
    };
  });
  return (
    <Show when={data()}>
      {(data) => (
        <AgentPanel
          pane={props.pane}
          member={data().member}
          size={data().size}
          hasAdjutant={data().hasAdjutant}
          kinds={() => [
            ...new Set(
              (store.app.server?.snapshot.agents ?? [])
                .map((agent) => agent.agent)
                .filter((kind): kind is string => !!kind),
            ),
          ]}
          handle={(handle) => {
            panel = handle;
          }}
        />
      )}
    </Show>
  );
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
  if (e.key === "Escape" && navOpen()) {
    setNavOpen(false);
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
