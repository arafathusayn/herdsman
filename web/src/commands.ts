import { run } from "./actions.ts";
// Everything the command palette can find and run, built from the current
// state each time it opens. Commands for the open agent come first.
import { arrowKeys, keySet, renamePane, sendOtherKeys, startAgent } from "./agent-actions.ts";
import {
  applyCodeTheme,
  applyTermScheme,
  appThemes,
  codeThemes,
  matchApp,
  noHighlight,
  termSchemes,
} from "./appearance.ts";
import { icons } from "./dom.ts";
import { loadHosts } from "./hosts.ts";
import { type Index, type Member, statusLabel, team } from "./model.ts";
import { type Command, sidebarKeys } from "./palette.ts";
import { type App, type RouteTab, store } from "./store.ts";
import type { Route } from "./types.ts";
import { applyTheme, type Theme, toast } from "./ui.ts";
import { openAppearance } from "./views/appearance.ts";
import { openHosts, switchTo } from "./views/hosts.ts";

export interface Context {
  app: App;
  idx: Index;
  route: Route | null;
  /** The value of r= for the whole-session view. */
  session: string;
  toggleNav(): void;
  focusComposer(): void;
  focusFileSearch(): void;
  focusChangeSearch(): void;
  focusArtifactSearch(): void;
}

const tabs: [RouteTab, string][] = [
  ["team", "Team"],
  ["checklist", "Checklist"],
  ["log", "Run state"],
  ["docs", "Docs"],
  ["changes", "Changes"],
  ["artifacts", "Artifacts"],
  ["messages", "Messages"],
  ["activity", "Activity"],
];

async function copy(text: string, what: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text);
    toast(`Copied the ${what}.`);
  } catch {
    toast("The browser blocked the clipboard.", "error");
  }
}

function kinds(app: App): string[] {
  return [...new Set((app.server?.snapshot.agents ?? []).map((a) => a.agent).filter((k): k is string => !!k))];
}

/** Every agent, with the route's names for the ones a route claims. */
function members(ctx: Context): Member[] {
  const out = new Map<string, Member>();
  if (ctx.route) for (const m of team(ctx.route, ctx.idx)) out.set(m.pane_id, m);
  for (const a of ctx.app.server?.snapshot.agents ?? [])
    if (!out.has(a.pane_id)) out.set(a.pane_id, ctx.idx.member(a.pane_id, "agent", null));
  return [...out.values()];
}

function prompt(m: Member): Command {
  return {
    id: `prompt:${m.pane_id}`,
    kind: "command",
    title: `Agent: Prompt ${m.title}…`,
    detail: m.pane_id,
    icon: icons.send,
    ask: {
      prompt: `Message ${m.title} (${m.pane_id})`,
      placeholder: "Type the prompt and press Enter",
      submit: (text) => run({ type: "prompt", pane: m.pane_id, text }),
    },
  };
}

/** The full action set for the agent whose panel is open. */
function paneCommands(ctx: Context, m: Member): Command[] {
  const p = m.pane_id;
  const t = m.title;
  const cmd = (id: string, title: string, icon: string[], runFn: () => unknown, keys?: string): Command => ({
    id: `${id}:${p}`,
    kind: "command",
    title,
    detail: p,
    icon,
    run: runFn,
    ...(keys ? { keys } : {}),
  });
  return [
    { ...prompt(m), title: `Agent: Prompt ${t}…` },
    cmd("composer", `Agent: Focus the message box of ${t}`, icons.send, () => ctx.focusComposer(), "/"),
    ...keySet.map((k) =>
      cmd(`key-${k.label}`, `Agent: Send ${k.label} to ${t}`, icons.keyboard, () =>
        run({ type: "keys", pane: p, keys: k.keys }),
      ),
    ),
    ...arrowKeys.map((k) =>
      cmd(`key-${k.name}`, `Agent: Send ${k.name} arrow to ${t}`, icons.keyboard, () =>
        run({ type: "keys", pane: p, keys: k.keys }),
      ),
    ),
    cmd("keys", `Agent: Send other keys to ${t}…`, icons.keyboard, () => sendOtherKeys(p)),
    cmd("start", `Pane: Start an agent in ${p}…`, icons.play, () => startAgent(p, kinds(ctx.app))),
    cmd("rename", `Pane: Rename ${t}…`, icons.tag, () => renamePane(m)),
    cmd("split-right", `Pane: Split ${t} right`, icons.split, () =>
      run({ type: "split", pane: p, direction: "right" }),
    ),
    cmd("split-down", `Pane: Split ${t} down`, icons.splitDown, () =>
      run({ type: "split", pane: p, direction: "down" }),
    ),
    cmd("focus", `Pane: Focus ${t} in herdr`, icons.focus, () => run({ type: "focus", pane: p })),
    cmd("copy-pane", `Pane: Copy the pane id ${p}`, icons.copy, () => copy(p, "pane id")),
    cmd("close", `Pane: Delete ${t}…`, icons.trash, () => run({ type: "close", pane: p })),
    cmd("close-panel", "View: Close the agent panel", icons.close, () => store.go({ pane: "" }), "Esc"),
  ];
}

export function buildCommands(ctx: Context): Command[] {
  const { app, route } = ctx;
  const out: Command[] = [];
  const all = members(ctx);

  const open = all.find((m) => m.pane_id === app.view.pane);
  if (open) out.push(...paneCommands(ctx, open));

  for (const m of all) {
    const status = statusLabel[m.status];
    const kind = m.agent?.display_agent ?? m.agent?.agent ?? "";
    out.push({
      id: `open:${m.pane_id}`,
      kind: "agent",
      title: m.title,
      detail: [m.role === "agent" ? "" : m.role, kind, status, m.pane_id].filter(Boolean).join(" · "),
      icon: icons.users,
      run: () => store.go({ pane: m.pane_id }),
    });
  }
  for (const m of all) if (m.pane_id !== app.view.pane) out.push(prompt(m));

  for (const r of app.server?.routes ?? []) {
    out.push({
      id: `route:${r.name}`,
      kind: "route",
      title: `Go to Route: ${r.name}`,
      detail: r.live ? "live" : "not running",
      icon: icons.list,
      run: () => store.go({ route: r.name, tab: "team", file: "", pane: "" }),
    });
  }
  for (const w of app.server?.snapshot.workspaces ?? []) {
    const name = w.label || `Workspace ${w.number}`;
    out.push({
      id: `workspace:${w.workspace_id}`,
      kind: "command",
      title: `Go to Workspace: ${name}`,
      detail: `${w.workspace_id} · ${statusLabel[w.agent_status]}`,
      icon: icons.users,
      run: () =>
        store.go({
          route: ctx.session,
          ws: w.workspace_id,
          tab: "team",
          file: "",
          pane: "",
        }),
    });
  }
  out.push({
    id: "go-session",
    kind: "command",
    title: "Go to: All agents",
    icon: icons.users,
    run: () => store.go({ route: ctx.session, tab: "team", file: "", pane: "" }),
  });

  if (route) {
    for (const [id, label] of tabs) {
      out.push({
        id: `tab:${id}`,
        kind: "command",
        title: `View: ${label}`,
        detail: route.name,
        icon: icons.list,
        run: () => store.go({ tab: id }),
      });
    }
    out.push({
      id: "file-search",
      kind: "command",
      title: "Docs: Search the route's docs",
      icon: icons.search,
      keys: "/",
      run: () => {
        store.go({ tab: "docs" });
        requestAnimationFrame(() => requestAnimationFrame(() => ctx.focusFileSearch()));
      },
    });
    out.push({
      id: "change-search",
      kind: "command",
      title: "Changes: Search the changed files",
      icon: icons.diff,
      run: () => {
        store.go({ tab: "changes" });
        requestAnimationFrame(() => requestAnimationFrame(() => ctx.focusChangeSearch()));
      },
    });
    out.push({
      id: "artifact-search",
      kind: "command",
      title: "Artifacts: Search the session's HTML pages",
      icon: icons.artifact,
      run: () => {
        store.go({ tab: "artifacts" });
        requestAnimationFrame(() => requestAnimationFrame(() => ctx.focusArtifactSearch()));
      },
    });
    out.push({
      id: "copy-route",
      kind: "command",
      title: "Route: Copy the route folder path",
      detail: route.dir,
      icon: icons.copy,
      run: () => copy(route.dir, "route folder path"),
    });
    for (const f of route.files ?? []) {
      out.push({
        id: `file:${f.path}`,
        kind: "file",
        title: f.path,
        detail: "doc",
        icon: icons.file,
        run: () => store.go({ tab: "docs", file: f.path }),
      });
    }
  }

  out.push({
    id: "hosts",
    kind: "command",
    title: "Hosts: Switch or add hosts…",
    detail: "the herdsman servers this browser talks to",
    icon: icons.server,
    run: () => openHosts(),
  });
  const hs = loadHosts();
  for (const x of hs.hosts) {
    if (x.id === hs.current) continue;
    out.push({
      id: `host:${x.id}`,
      kind: "command",
      title: `Host: Connect to ${x.name}`,
      detail: x.url,
      icon: icons.server,
      run: () => switchTo(loadHosts(), x),
    });
  }
  out.push({
    id: "appearance",
    kind: "command",
    title: "Preferences: Appearance…",
    detail: "theme, terminal colors, code highlighting",
    icon: icons.swatch,
    run: () => openAppearance(),
  });
  for (const s of [{ id: matchApp, label: "Match app" }, ...termSchemes])
    out.push({
      id: `term:${s.id}`,
      kind: "command",
      title: `Terminal colors: ${s.label}`,
      icon: icons.swatch,
      run: () => applyTermScheme(s.id),
    });
  for (const t of [{ id: matchApp, label: "Match app" }, ...codeThemes, { id: noHighlight, label: "No highlighting" }])
    out.push({
      id: `code:${t.id}`,
      kind: "command",
      title: `Code highlighting: ${t.label}`,
      icon: icons.swatch,
      run: () => {
        applyCodeTheme(t.id);
        store.emit();
      },
    });
  for (const [t, label] of [
    ["system", "System"],
    ["light", "Light"],
    ["dark", "Dark"],
    ...appThemes.map((x) => [x.id, x.label]),
  ] as [Theme, string][]) {
    out.push({
      id: `theme:${t}`,
      kind: "command",
      title: `Theme: ${label}`,
      detail: app.theme === t ? "current" : "",
      icon: t === "dark" ? icons.moon : t === "light" ? icons.sun : t === "system" ? icons.monitor : icons.swatch,
      run: () => {
        applyTheme(t);
        store.set({ theme: t });
      },
    });
  }
  out.push({
    id: "toggle-nav",
    kind: "command",
    title: "View: Toggle the sidebar",
    icon: icons.sidebar,
    keys: sidebarKeys,
    run: () => ctx.toggleNav(),
  });
  return out;
}
