import { expect, test } from "bun:test";

import { afterSend, checklistCounts, Index, lanes, type Member, roleOf, team, workspaceGroups } from "./model.ts";
import type { Agent, Pane, Route, State, Workspace } from "./types.ts";

const ws = (id: string, number: number, label = ""): Workspace => ({
  workspace_id: id,
  number,
  label,
  focused: false,
  active_tab_id: `${id}:t1`,
  agent_status: "idle",
});

const pane = (id: string, workspace: string): Pane => ({
  pane_id: id,
  workspace_id: workspace,
  tab_id: `${workspace}:t1`,
  focused: false,
  label: null,
  cwd: null,
  foreground_cwd: null,
  terminal_title_stripped: null,
  agent: null,
  agent_status: "idle",
  revision: 0,
});

const agent = (id: string, workspace: string): Agent => ({
  pane_id: id,
  tab_id: `${workspace}:t1`,
  workspace_id: workspace,
  name: null,
  agent: "claude",
  display_agent: null,
  agent_status: "working",
  focused: false,
  cwd: null,
  terminal_title_stripped: null,
  interactive_ready: true,
  launch_pending: false,
  state_change_seq: 0,
});

const route = (name: string, orchestrator: string, panes: string[], live = true): Route => ({
  dir: `/r/${name}`,
  name,
  title: name,
  orchestrator_pane: orchestrator,
  tasks: panes.map((p) => ({
    task: p,
    branch: "",
    worktree: "",
    agent: "",
    pane: p,
    contract: "",
  })),
  checklist: null,
  run_state: null,
  files: null,
  modified: "",
  live,
});

test("routes sit under the workspace of their orchestrator, in herdr's order", () => {
  const state: State = {
    herdr: { version: "0.9.4", protocol: 22 },
    snapshot: {
      version: "0.9.4",
      protocol: 22,
      focused_workspace_id: null,
      focused_tab_id: null,
      focused_pane_id: null,
      workspaces: [ws("wB", 2, "beta"), ws("wA", 1, "alpha"), ws("wC", 3, "shells")],
      tabs: null,
      panes: [pane("wA:p1", "wA"), pane("wB:p1", "wB"), pane("wB:p2", "wB"), pane("wC:p1", "wC")],
      layouts: null,
      agents: [agent("wA:p1", "wA"), agent("wB:p1", "wB"), agent("wB:p2", "wB")],
    },
    routes: [
      // The orchestrator is in wB, though a task pane is in wA.
      route("one", "wB:p1", ["wA:p1"]),
      // No orchestrator pane left: the first task pane herdr has decides.
      route("two", "gone:p9", ["gone:p8", "wA:p1"]),
      route("lost", "gone:p1", []),
      route("old", "wB:p2", [], false),
    ],
  };
  const { groups, unplaced } = workspaceGroups(state);
  expect(groups.map((g) => [g.workspace.workspace_id, g.agents, g.routes.map((r) => r.name)])).toEqual([
    ["wA", 1, ["two"]],
    ["wB", 2, ["one"]],
  ]);
  expect(unplaced.map((r) => r.name)).toEqual(["lost"]);
});

const member = (pane_id: string, role: Member["role"], status: Member["status"]): Member => ({
  pane_id,
  role,
  status,
  title: pane_id,
  task: null,
  agent: null,
  pane: null,
  tab: null,
});

test("the Team board's lanes follow role or status", () => {
  const route = [
    member("p1", "orchestrator", "idle"),
    member("p2", "implementer", "working"),
    member("p3", "reviewer", "done"),
    member("p4", "reviewer", "working"),
    member("p5", "implementer", "unknown"),
  ];
  const shape = (by: "role" | "status") => lanes(route, by).map((l) => [l.title, l.members.map((m) => m.pane_id)]);
  expect(shape("role")).toEqual([
    ["Orchestrator", ["p1"]],
    ["Implementers", ["p2", "p5"]],
    ["Reviewers", ["p3", "p4"]],
  ]);
  // The steady lanes stay even when empty; the rest show only when used.
  expect(shape("status")).toEqual([
    ["Working", ["p2", "p4"]],
    ["Needs input", []],
    ["Idle", ["p1"]],
    ["Done", ["p3"]],
    ["Unknown", ["p5"]],
  ]);
});

test("an adjutant leads the team, by its route line or its task row", () => {
  const r = { ...route("adj", "w1:p1", ["w1:p2", "w1:p3"]), adjutant_pane: "w1:p6" };
  const snap = {
    panes: [pane("w1:p1", "w1"), pane("w1:p6", "w1")],
    agents: [],
    tabs: [],
  } as unknown as State["snapshot"];
  const roles = team(r, new Index(snap)).map((m) => [m.pane_id, m.role]);
  expect(roles).toEqual([
    ["w1:p6", "adjutant"],
    ["w1:p1", "orchestrator"],
    ["w1:p2", "implementer"],
    ["w1:p3", "implementer"],
  ]);
  const row = { task: "front", branch: "", worktree: "", agent: "adjutant-claude", pane: "w1:p9", contract: "" };
  expect(roleOf(row, route("x", "w1:p1", []))).toBe("adjutant");
  expect(roleOf({ ...row, task: "adjutant (Claude)", agent: "" }, route("x", "w1:p1", []))).toBe("adjutant");
  // Implementers working on the adjutant feature stay implementers.
  for (const [task, agent] of [
    ["adjutant-design", "im-adjutant"],
    ["adjutant-skill", "im-adjutant-skill"],
  ])
    expect(roleOf({ ...row, task: task ?? "", agent: agent ?? "" }, route("x", "w1:p1", []))).toBe("implementer");
  // A route's adjutant pane places it in a workspace when nothing else does.
  const placed = workspaceGroups({
    herdr: { version: "", protocol: 0 },
    snapshot: { panes: [pane("w2:p6", "w2")], agents: [agent("w2:p6", "w2")], workspaces: [ws("w2", 1)], tabs: [] },
    routes: [{ ...route("only-adjutant", "", []), adjutant_pane: "w2:p6" }],
  } as unknown as State);
  expect(placed.groups.map((g) => g.routes.map((x) => x.name))).toEqual([["only-adjutant"]]);
});

// A `[-]` line stopped applying: it is neither done nor open.
test("checklist counts keep not-applicable lines apart", () => {
  const item = (done: boolean, skipped = false) => ({ done, skipped, id: "", text: "x" });
  expect(checklistCounts([item(true), item(false, true), item(false), item(false)])).toEqual({
    done: 1,
    skipped: 1,
    open: 2,
    total: 4,
  });
});

test("a send clears only the text it sent", () => {
  expect(afterSend("fix the test", "fix the test")).toBe("");
  expect(afterSend("fix the test", "fix the test, then push")).toBe("fix the test, then push");
  expect(afterSend("fix the test", "")).toBe("");
});
