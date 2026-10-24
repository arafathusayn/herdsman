// Derives what the views show from the raw server state.
import type { Agent, CheckItem, Pane, Route, Snapshot, State, Status, Tab, Task, Workspace } from "./types.ts";

export type Role = "adjutant" | "orchestrator" | "implementer" | "integrator" | "reviewer" | "checkpointer" | "agent";

export const roleOrder: Role[] = [
  "adjutant",
  "orchestrator",
  "implementer",
  "integrator",
  "reviewer",
  "checkpointer",
  "agent",
];

export const roleTitle: Record<Role, string> = {
  adjutant: "Adjutant",
  orchestrator: "Orchestrator",
  implementer: "Implementers",
  integrator: "Integrator",
  reviewer: "Reviewers",
  checkpointer: "Checkpointer",
  agent: "Other agents",
};

export interface Member {
  pane_id: string;
  role: Role;
  task: Task | null;
  agent: Agent | null;
  pane: Pane | null;
  tab: Tab | null;
  /** The name the route gave this agent, or herdr's name, or the pane label. */
  title: string;
  status: Status | "gone";
}

export class Index {
  readonly panes = new Map<string, Pane>();
  readonly agents = new Map<string, Agent>();
  readonly tabs = new Map<string, Tab>();

  constructor(readonly snap: Snapshot) {
    for (const p of snap.panes ?? []) this.panes.set(p.pane_id, p);
    for (const a of snap.agents ?? []) this.agents.set(a.pane_id, a);
    for (const t of snap.tabs ?? []) this.tabs.set(t.tab_id, t);
  }

  member(pane_id: string, role: Role, task: Task | null): Member {
    const pane = this.panes.get(pane_id) ?? null;
    const agent = this.agents.get(pane_id) ?? null;
    const tab = pane ? (this.tabs.get(pane.tab_id) ?? null) : null;
    const title = task?.agent || agent?.name || pane?.label || task?.task || pane_id;
    const status: Member["status"] = pane ? (agent?.agent_status ?? pane.agent_status) : "gone";
    return { pane_id, role, task, agent, pane, tab, title, status };
  }
}

export function roleOf(t: Task, route: Route): Role {
  if (t.pane && t.pane === route.orchestrator_pane) return "orchestrator";
  if (t.pane && t.pane === route.adjutant_pane) return "adjutant";
  // Only a row that is the adjutant: "im-adjutant" or "adjutant-skill" is an
  // implementer working on the adjutant feature.
  if (/^adjutant(\s|\(|$)/i.test(t.task) || /^(adj|adjutant)(-|$)/i.test(t.agent)) return "adjutant";
  const s = `${t.task} ${t.agent}`.toLowerCase();
  if (s.includes("checkpoint")) return "checkpointer";
  if (s.includes("orchestrat")) return "orchestrator";
  if (/\breview|^rv-|\brv-/.test(s)) return "reviewer";
  if (/integrat|^in-/.test(s)) return "integrator";
  return "implementer";
}

/** The team of a route: its adjutant and orchestrator first, then every task row. */
export function team(route: Route, idx: Index): Member[] {
  const out: Member[] = [];
  const seen = new Set<string>();
  if (route.adjutant_pane) {
    out.push(idx.member(route.adjutant_pane, "adjutant", null));
    seen.add(route.adjutant_pane);
  }
  if (route.orchestrator_pane && !seen.has(route.orchestrator_pane)) {
    out.push(idx.member(route.orchestrator_pane, "orchestrator", null));
    seen.add(route.orchestrator_pane);
  }
  for (const t of route.tasks ?? []) {
    if (!t.pane || seen.has(t.pane)) continue;
    seen.add(t.pane);
    out.push(idx.member(t.pane, roleOf(t, route), t));
  }
  return out;
}

/** A pane's terminal size in cells: its box in herdr's layout, less the border herdr draws around it. */
export function paneSize(snap: Snapshot, paneId: string): { cols: number; rows: number } | null {
  for (const l of snap.layouts ?? []) {
    const p = l.panes?.find((x) => x.pane_id === paneId);
    if (p) return { cols: Math.max(1, p.rect.width - 2), rows: Math.max(1, p.rect.height - 2) };
  }
  return null;
}

export interface WorkspaceGroup {
  workspace: Workspace;
  /** Agents running in the workspace. */
  agents: number;
  /** Live routes whose orchestrator works in the workspace. */
  routes: Route[];
}

/**
 * Groups the live routes by herdr workspace, in herdr's order. A route sits
 * in the workspace of its orchestrator pane, else of its first task pane
 * herdr still has. Workspaces with neither an agent nor a route are left
 * out; live routes with no pane in any workspace come back as unplaced.
 */
export function workspaceGroups(state: State): {
  groups: WorkspaceGroup[];
  unplaced: Route[];
} {
  const snap = state.snapshot;
  const paneWs = new Map((snap.panes ?? []).map((p) => [p.pane_id, p.workspace_id]));
  const agents = new Map<string, number>();
  for (const a of snap.agents ?? []) agents.set(a.workspace_id, (agents.get(a.workspace_id) ?? 0) + 1);
  const routes = new Map<string, Route[]>();
  const unplaced: Route[] = [];
  for (const r of state.routes ?? []) {
    if (!r.live) continue;
    const panes = [r.orchestrator_pane, r.adjutant_pane ?? "", ...(r.tasks ?? []).map((t) => t.pane)];
    const ws = panes.map((p) => paneWs.get(p)).find((w) => w !== undefined);
    if (ws === undefined) unplaced.push(r);
    else routes.set(ws, [...(routes.get(ws) ?? []), r]);
  }
  const groups = [...(snap.workspaces ?? [])]
    .sort((a, b) => a.number - b.number)
    .filter((w) => agents.has(w.workspace_id) || routes.has(w.workspace_id))
    .map((w) => ({
      workspace: w,
      agents: agents.get(w.workspace_id) ?? 0,
      routes: routes.get(w.workspace_id) ?? [],
    }));
  return { groups, unplaced };
}

export function group(members: Member[]): [Role, Member[]][] {
  const by = new Map<Role, Member[]>();
  for (const m of members) by.set(m.role, [...(by.get(m.role) ?? []), m]);
  return roleOrder.filter((r) => by.has(r)).map((r) => [r, by.get(r) ?? []]);
}

export const statusLabel: Record<Member["status"], string> = {
  working: "Working",
  idle: "Idle",
  blocked: "Needs input",
  done: "Done",
  unknown: "Unknown",
  gone: "Pane gone",
};

/** One member's role, as a card names it. */
export const roleName: Record<Role, string> = {
  adjutant: "Adjutant",
  orchestrator: "Orchestrator",
  implementer: "Implementer",
  integrator: "Integrator",
  reviewer: "Reviewer",
  checkpointer: "Checkpointer",
  agent: "Other agent",
};

export type BoardBy = "role" | "status";

/** One column of the Team board. */
export interface Lane {
  id: string;
  title: string;
  /** Set on status lanes, for the column's color. */
  status?: Member["status"];
  members: Member[];
}

const statusOrder: Member["status"][] = ["working", "blocked", "idle", "done", "unknown", "gone"];

/** Status lanes shown even when empty, so the board keeps its shape. */
const steadyLanes = new Set<string>(["working", "blocked", "idle", "done"]);

/**
 * The Team board's columns: by role, the route's groups in role order; by
 * status, the route's agents in status order.
 */
export function lanes(members: Member[], by: BoardBy): Lane[] {
  if (by === "role") return group(members).map(([role, ms]) => ({ id: role, title: roleTitle[role], members: ms }));
  return statusOrder
    .map((s) => ({ id: s, title: statusLabel[s], status: s, members: members.filter((m) => m.status === s) }))
    .filter((l) => l.members.length > 0 || steadyLanes.has(l.id));
}

/** Splits a command line into words, honouring single and double quotes. */
export function splitArgs(line: string): string[] {
  const out: string[] = [];
  let cur = "";
  let quote: string | null = null;
  let has = false;
  for (const ch of line) {
    if (quote) {
      if (ch === quote) quote = null;
      else cur += ch;
    } else if (ch === '"' || ch === "'") {
      quote = ch;
      has = true;
    } else if (/\s/.test(ch)) {
      if (has || cur) out.push(cur);
      cur = "";
      has = false;
    } else cur += ch;
  }
  if (has || cur) out.push(cur);
  return out;
}

export function ago(iso: string, now = Date.now()): string {
  const s = Math.max(0, Math.round((now - Date.parse(iso)) / 1000));
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.round(s / 60)}m ago`;
  if (s < 86400) return `${Math.round(s / 3600)}h ago`;
  return `${Math.round(s / 86400)}d ago`;
}

/** How far a checklist is: done, not applicable (`[-]`, settled with a reason) and open lines. */
export function checklistCounts(items: CheckItem[]): { done: number; skipped: number; open: number; total: number } {
  const done = items.filter((i) => i.done).length;
  const skipped = items.filter((i) => !i.done && i.skipped).length;
  return { done, skipped, open: items.length - done - skipped, total: items.length };
}

/**
 * What the message box holds after a send went through: empty when it still
 * holds the sent text, else the edits typed while the send was out.
 */
export function afterSend(sent: string, current: string): string {
  return current === sent ? "" : current;
}

/** A route's name, with its project folder when another route has the same name. */
export function routeLabel(r: Route, all: Route[]): string {
  if (!all.some((x) => x.id !== r.id && x.name === r.name)) return r.name;
  const project = r.dir.split("/").at(-3) ?? ""; // <project>/.herdsman/<route>
  return project ? `${r.name} · ${project}` : r.name;
}

/**
 * The route a link names: by its id, or, for a link from before ids had a
 * hash, by its folder name while only one route has that name.
 */
export function routeFor(routes: Route[], key: string): Route | null {
  const byId = routes.find((r) => r.id === key);
  if (byId) return byId;
  const named = routes.filter((r) => r.name === key);
  return named.length === 1 ? (named[0] ?? null) : null;
}
