// App state, the URL it is kept in, and change notification.
import type { Channel, Entry, State } from "./types.ts";
import { loadTheme, type Theme } from "./ui.ts";

export type RouteTab = "team" | "checklist" | "log" | "docs" | "changes" | "artifacts" | "messages" | "activity";

export interface View {
  /** Route name, or "" for the whole session. */
  route: string;
  tab: RouteTab;
  /** The open file of the Docs tab. */
  file: string;
  /** The Changes tab's open source (a task, or the project checkout) and file. */
  source: string;
  diffFile: string;
  /** The Artifacts tab's open page: its full path. */
  artifact: string;
  /** Open agent pane, or "". */
  pane: string;
  /** The workspace the session view shows, or "" for every workspace. */
  ws: string;
}

export interface App {
  server: State | null;
  entries: Entry[];
  connected: boolean;
  view: View;
  theme: Theme;
  fatal: string;
  /** The adjutant's message channel, or null until it is read. */
  channel: Channel | null;
}

const maxEntries = 400;

const tabs: RouteTab[] = ["team", "checklist", "log", "docs", "changes", "artifacts", "messages", "activity"];

function readView(): View {
  const q = new URLSearchParams(location.search);
  const tab = q.get("tab") as RouteTab | null;
  return {
    route: q.get("r") ?? "",
    tab: tab && tabs.includes(tab) ? tab : "team",
    file: q.get("f") ?? "",
    source: q.get("src") ?? "",
    diffFile: q.get("df") ?? "",
    artifact: q.get("a") ?? "",
    pane: q.get("p") ?? "",
    ws: q.get("ws") ?? "",
  };
}

function writeView(v: View, push: boolean): void {
  const q = new URLSearchParams();
  if (v.route) q.set("r", v.route);
  if (v.tab !== "team") q.set("tab", v.tab);
  if (v.file) q.set("f", v.file);
  if (v.source) q.set("src", v.source);
  if (v.diffFile) q.set("df", v.diffFile);
  if (v.artifact) q.set("a", v.artifact);
  if (v.pane) q.set("p", v.pane);
  if (v.ws) q.set("ws", v.ws);
  const url = `${location.pathname}${q.size ? `?${q}` : ""}`;
  if (push) history.pushState(null, "", url);
  else history.replaceState(null, "", url);
}

type Listener = (app: App) => void;

class Store {
  readonly app: App = {
    server: null,
    entries: [],
    connected: false,
    view: readView(),
    theme: loadTheme(),
    fatal: "",
    channel: null,
  };
  private listeners: Listener[] = [];
  private queued = false;

  constructor() {
    window.addEventListener("popstate", () => {
      this.app.view = readView();
      this.emit();
    });
  }

  on(fn: Listener): void {
    this.listeners.push(fn);
  }

  /** Batches changes into one render per frame. */
  emit(): void {
    if (this.queued) return;
    this.queued = true;
    requestAnimationFrame(() => {
      this.queued = false;
      for (const fn of this.listeners) fn(this.app);
    });
  }

  set(patch: Partial<App>): void {
    Object.assign(this.app, patch);
    this.emit();
  }

  go(patch: Partial<View>, push = true): void {
    // A workspace filter belongs to the view it was set with.
    const ws = patch.route === undefined ? this.app.view.ws : "";
    this.app.view = { ...this.app.view, ws, ...patch };
    writeView(this.app.view, push);
    this.emit();
  }

  addEntries(e: Entry[]): void {
    this.app.entries = [...this.app.entries, ...e].slice(-maxEntries);
    this.emit();
  }
}

export const store = new Store();
