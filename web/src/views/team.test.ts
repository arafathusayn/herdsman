import { beforeEach, expect, test } from "bun:test";

import { each, h, unkeyed } from "../dom.ts";
import { render, type ViewNode } from "../herdsman-blocks-view.ts";
import { type FakeElement, type FakeNode, installFakeDocument } from "../herdsman-blocks.testdom.ts";
import { type BoardBy, lanes, type Member, type Role, roleName } from "../model.ts";
import type { App } from "../store.ts";
import { dispatch } from "../testdom.ts";
import { pill } from "../ui.ts";

// The store reads the URL and listens for history changes when it loads,
// writes the URL on go() and batches renders in animation frames.
const g = globalThis as unknown as Record<string, unknown>;
g["location"] ??= { search: "", pathname: "/" };
g["window"] ??= { addEventListener: () => {} };
g["history"] ??= { pushState: () => {}, replaceState: () => {} };
g["requestAnimationFrame"] ??= () => 0;
const { card, teamBoard } = await import("./team.ts");
const { store } = await import("../store.ts");

beforeEach(installFakeDocument);

const fake = (n: unknown): FakeElement => n as FakeElement;
const app = { view: { pane: "" } } as App;
const member = (pane_id: string, role: Role): Member => ({
  pane_id,
  role,
  task: null,
  agent: null,
  pane: null,
  tab: null,
  title: pane_id,
  status: "idle",
});
const elements = (n: FakeNode): FakeElement[] => n.childNodes.filter((c): c is FakeElement => c.nodeType === 1);
const rendered = (box: FakeElement, v: ViewNode): FakeElement => {
  render(box as unknown as Node, v);
  return fake(box.firstChild);
};
const lanesOf = (team: FakeElement): FakeElement[] => {
  const kanban = elements(team).find((n) => n.className === "kanban")!;
  return elements(kanban);
};
// A lane holds its heading, then its cards.
const cardsOf = (lane: FakeElement) => elements(lane).slice(1);

// Elements and text with attributes sorted and each()'s data-key left out; a
// slot's empty anchor text is not content.
const dump = (n: FakeNode): string => {
  if (n.nodeType === 3) return n.textContent;
  if (n.nodeType !== 1) return "";
  const e = fake(n);
  const attrs = e.attributes
    .filter((a) => a.name !== "data-key")
    .map((a) => `${a.name}="${a.value}"`)
    .sort()
    .join(" ");
  return `<${e.tagName.toLowerCase()} ${attrs}>${e.childNodes.map(dump).join("")}</${e.tagName.toLowerCase()}>`;
};

// The h() versions the views replace, for the comparison.
function oldCard(m: Member, app: App, role = false): HTMLElement {
  const sub = [role ? roleName[m.role] : "", m.agent?.display_agent ?? m.agent?.agent, m.task?.branch]
    .filter(Boolean)
    .join(" · ");
  const title = m.agent?.terminal_title_stripped ?? m.pane?.terminal_title_stripped ?? "";
  return h(
    "button",
    {
      class: `card r-${m.role} s-${m.status}${app.view.pane === m.pane_id ? " selected" : ""}`,
      title: `Open ${m.title} (${m.pane_id}): its output, keys and actions`,
      dataset: { pane: m.pane_id },
    },
    h("div", { class: "card-top" }, h("span", { class: "card-title" }, m.title), pill(m.status)),
    m.task?.task && m.task.task !== m.title ? h("div", { class: "card-task" }, m.task.task) : null,
    sub ? h("div", { class: "card-sub" }, sub) : null,
    h(
      "div",
      { class: "card-foot" },
      h("code", null, m.pane_id),
      m.tab ? h("span", { class: "muted" }, m.tab.label) : null,
      title ? h("span", { class: "muted clip", title }, title) : null,
    ),
  );
}

function oldBoard(members: Member[], app: App, boardBy: BoardBy): HTMLElement {
  const switcher = h(
    "div",
    { class: "view-switch board-switch", role: "group", "aria-label": "Columns" },
    unkeyed(
      (
        [
          ["role", "Role", "A column for each role in the route"],
          ["status", "Status", "A column for each status; cards move as agents work"],
        ] as const
      ).map(([by, label, tip]) =>
        // The browser keeps class="" for an inactive button; a class hole leaves none.
        h(
          "button",
          {
            type: "button",
            class: by === boardBy ? "active" : undefined,
            "aria-pressed": String(by === boardBy),
            title: tip,
          },
          label,
        ),
      ),
    ),
  );
  return h(
    "div",
    { class: "team" },
    h("div", { class: "team-bar" }, switcher),
    members.length
      ? h(
          "div",
          { class: "kanban" },
          each(
            lanes(members, boardBy),
            (lane) => lane.id,
            (lane) =>
              h(
                "section",
                { class: lane.status ? `lane s-${lane.status}` : "lane", "aria-label": lane.title },
                h(
                  "h2",
                  null,
                  lane.status ? h("span", { class: "lane-dot", "aria-hidden": "true" }) : null,
                  lane.title,
                  h("span", { class: "count" }, String(lane.members.length)),
                ),
                lane.members.length
                  ? each(
                      lane.members,
                      (m) => m.pane_id,
                      (m) => oldCard(m, app, boardBy === "status"),
                    )
                  : h("p", { class: "lane-empty" }, "None"),
              ),
          ),
        )
      : h("p", { class: "muted" }, "This route's task table names no panes."),
  );
}

const full: Member = {
  ...member("p7", "implementer"),
  title: "Fix the parser",
  status: "working",
  task: { task: "Parse <b> & quotes", branch: "fix-parser", worktree: "", agent: "", pane: "p7", contract: "" },
  agent: {
    pane_id: "p7",
    tab_id: "t1",
    workspace_id: "w1",
    name: null,
    agent: "claude",
    display_agent: "Claude",
    agent_status: "working",
    focused: false,
    cwd: null,
    terminal_title_stripped: "editing parser.ts",
    interactive_ready: true,
    launch_pending: false,
    state_change_seq: 1,
  },
  tab: {
    tab_id: "t1",
    workspace_id: "w1",
    number: 1,
    label: "impl",
    focused: false,
    pane_count: 1,
    agent_status: "working",
  },
};

test("cards render what the h() version did", () => {
  const selected = { view: { pane: "p7" } } as App;
  for (const [m, a, role] of [
    [member("p1", "reviewer"), app, false],
    [full, app, false],
    [full, selected, true],
    [{ ...full, task: { ...full.task!, task: full.title } }, app, true],
  ] as const) {
    const box = fake(document.createElement("div"));
    expect(dump(rendered(box, card(m, a, role)))).toBe(dump(fake(oldCard(m, a, role))));
  }
});

test("a card patched to another member keeps its node and opens the latest pane", () => {
  const box = fake(document.createElement("div"));
  const el = rendered(box, card(member("p1", "reviewer"), app));
  expect(rendered(box, card(full, app))).toBe(el);
  expect(dump(el)).toBe(dump(fake(oldCard(full, app))));
  dispatch(el, "click");
  expect(store.app.view.pane).toBe("p7");
});

test("the board renders what the h() version did, and its switch picks the columns", () => {
  const ms = [member("p0", "orchestrator"), full, member("p2", "reviewer")];
  const box = fake(document.createElement("div"));
  const team = rendered(box, teamBoard(ms, app));
  // The test's localStorage is empty or absent, so the board starts by role.
  expect(dump(team)).toBe(dump(fake(oldBoard(ms, app, "role"))));
  const [, byStatus] = elements(elements(elements(team)[0]!)[0]!);
  dispatch(byStatus!, "click");
  expect(rendered(box, teamBoard(ms, app))).toBe(team);
  expect(dump(team)).toBe(dump(fake(oldBoard(ms, app, "status"))));
  dispatch(elements(elements(elements(team)[0]!)[0]!)[0]!, "click");
  rendered(box, teamBoard(ms, app));
  expect(dump(team)).toBe(dump(fake(oldBoard(ms, app, "role"))));
  expect(dump(rendered(box, teamBoard([], app)))).toBe(dump(fake(oldBoard([], app, "role"))));
});

test("lanes keep their nodes when a lane appears ahead of them and their cards change", () => {
  const box = fake(document.createElement("div"));
  const team = rendered(box, teamBoard([member("p1", "implementer"), member("p2", "reviewer")], app));
  const [impl, rev] = lanesOf(team);
  const cards = cardsOf(impl!);

  const next = [member("p0", "orchestrator"), member("p1", "implementer"), member("p3", "implementer")];
  expect(rendered(box, teamBoard([...next, member("p2", "reviewer")], app))).toBe(team);
  const lanes = lanesOf(team);
  expect(lanes.map((l) => l.getAttribute("aria-label"))).toEqual(["Orchestrator", "Implementers", "Reviewers"]);
  expect(lanes[1]).toBe(impl!);
  expect(lanes[2]).toBe(rev!);
  expect(cardsOf(lanes[1]!)[0]).toBe(cards[0]!);
  expect(cardsOf(lanes[1]!).map((c) => c.getAttribute("data-pane"))).toEqual(["p1", "p3"]);
});
