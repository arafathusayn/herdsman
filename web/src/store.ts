// Browser navigation and persistence around the reactive application state.
import { createAppState, type RouteTab, type View } from "./app-state.ts";
import { loadTheme } from "./ui.ts";

export type { App, RouteTab, View } from "./app-state.ts";

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

export const store = createAppState(
  {
    server: null,
    entries: [],
    connected: false,
    view: readView(),
    theme: loadTheme(),
    fatal: "",
    channel: null,
  },
  writeView,
);

window.addEventListener("popstate", () => store.set({ view: readView() }));
