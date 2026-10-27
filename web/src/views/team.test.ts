import { beforeEach, expect, test } from "bun:test";

import { render, type ViewNode } from "../herdsman-blocks-view.ts";
import { type FakeElement, type FakeNode, installFakeDocument } from "../herdsman-blocks.testdom.ts";
import type { BoardBy, Member, Role } from "../model.ts";
import type { App } from "../store.ts";
import { dispatch, markup } from "../testdom.ts";

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

// Elements and text with attributes sorted; a slot's empty anchor text is
// not content.
const dump = (n: FakeNode): string => {
  if (n.nodeType === 3) return n.textContent;
  if (n.nodeType !== 1) return "";
  const e = fake(n);
  const attrs = e.attributes
    .map((a) => `${a.name}="${a.value}"`)
    .sort()
    .join(" ");
  return `<${e.tagName.toLowerCase()} ${attrs}>${e.childNodes.map(dump).join("")}</${e.tagName.toLowerCase()}>`;
};

// A bare member's card: idle, its pane as its title, a sub line only when
// the board is by status (the role).
const bareCard = (pane: string, role: Role, sub = ""): string => markup`
  <button class="card r-${role} s-idle" data-pane="${pane}" title="Open ${pane} (${pane}): its output, keys and actions">
    <div class="card-top">
      <span class="card-title" title="${pane}">${pane}</span>
      <span class="pill s-idle"><span aria-hidden="true" class="dot"></span>Idle</span>
    </div>
    ${sub ? `<div class="card-sub" title="${sub}">${sub}</div>` : ""}
    <div class="card-foot"><code >${pane}</code></div>
  </button>
`;
// The full member's card, selected or not, with its sub line, and its task
// line unless the task is the title.
const fullCard = (cls: string, sub: string, task = true): string => markup`
  <button class="card r-implementer s-working${cls}" data-pane="p7" title="Open Fix the parser (p7): its output, keys and actions">
    <div class="card-top">
      <span class="card-title" title="Fix the parser">Fix the parser</span>
      <span class="pill s-working"><span aria-hidden="true" class="dot"></span>Working</span>
    </div>
    ${task ? `<div class="card-task">Parse <b> & quotes</div>` : ""}
    <div class="card-sub" title="${sub}">${sub}</div>
    <div class="card-foot">
      <code >p7</code>
      <span class="card-tab muted clip" title="impl">impl</span>
      <span class="muted clip" title="editing parser.ts">editing parser.ts</span>
    </div>
  </button>
`;
const teamBar = (by: BoardBy): string => markup`
  <div class="team-bar">
    <div aria-label="Columns" class="view-switch board-switch" role="group">
      <button aria-pressed="${String(by === "role")}"${by === "role" ? ' class="active"' : ""} title="A column for each role in the route" type="button">Role</button>
      <button aria-pressed="${String(by === "status")}"${by === "status" ? ' class="active"' : ""} title="A column for each status; cards move as agents work" type="button">Status</button>
    </div>
  </div>
`;

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
  for (const [m, a, role, want] of [
    [member("p1", "reviewer"), app, false, bareCard("p1", "reviewer")],
    [full, app, false, fullCard("", "Claude · fix-parser")],
    [full, selected, true, fullCard(" selected", "Implementer · Claude · fix-parser")],
    [
      { ...full, task: { ...full.task!, task: full.title } },
      app,
      true,
      fullCard("", "Implementer · Claude · fix-parser", false),
    ],
  ] as const) {
    const box = fake(document.createElement("div"));
    expect(dump(rendered(box, card(m, a, role)))).toBe(want);
  }
});

test("a card patched to another member keeps its node and opens the latest pane", () => {
  const box = fake(document.createElement("div"));
  const el = rendered(box, card(member("p1", "reviewer"), app));
  expect(rendered(box, card(full, app))).toBe(el);
  expect(dump(el)).toBe(fullCard("", "Claude · fix-parser"));
  dispatch(el, "click");
  expect(store.app.view.pane).toBe("p7");
});

test("the board renders what the h() version did, and its switch picks the columns", () => {
  const ms = [member("p0", "orchestrator"), full, member("p2", "reviewer")];
  const byRole = markup`
    <div class="team">
      ${teamBar("role")}
      <div class="kanban">
        <section aria-label="Orchestrator" class="lane">
          <h2 >Orchestrator<span class="count">1</span></h2>
          ${bareCard("p0", "orchestrator")}
        </section>
        <section aria-label="Implementers" class="lane">
          <h2 >Implementers<span class="count">1</span></h2>
          ${fullCard("", "Claude · fix-parser")}
        </section>
        <section aria-label="Reviewers" class="lane">
          <h2 >Reviewers<span class="count">1</span></h2>
          ${bareCard("p2", "reviewer")}
        </section>
      </div>
    </div>
  `;
  const box = fake(document.createElement("div"));
  const team = rendered(box, teamBoard(ms, app));
  // The test's localStorage is empty or absent, so the board starts by role.
  expect(dump(team)).toBe(byRole);
  const [, byStatus] = elements(elements(elements(team)[0]!)[0]!);
  dispatch(byStatus!, "click");
  expect(rendered(box, teamBoard(ms, app))).toBe(team);
  expect(dump(team)).toBe(markup`
    <div class="team">
      ${teamBar("status")}
      <div class="kanban">
        <section aria-label="Working" class="lane s-working">
          <h2 ><span aria-hidden="true" class="lane-dot"></span>Working<span class="count">1</span></h2>
          ${fullCard("", "Implementer · Claude · fix-parser")}
        </section>
        <section aria-label="Needs input" class="lane s-blocked">
          <h2 ><span aria-hidden="true" class="lane-dot"></span>Needs input<span class="count">0</span></h2>
          <p class="lane-empty">None</p>
        </section>
        <section aria-label="Idle" class="lane s-idle">
          <h2 ><span aria-hidden="true" class="lane-dot"></span>Idle<span class="count">2</span></h2>
          ${bareCard("p0", "orchestrator", "Orchestrator")}
          ${bareCard("p2", "reviewer", "Reviewer")}
        </section>
        <section aria-label="Done" class="lane s-done">
          <h2 ><span aria-hidden="true" class="lane-dot"></span>Done<span class="count">0</span></h2>
          <p class="lane-empty">None</p>
        </section>
      </div>
    </div>
  `);
  dispatch(elements(elements(elements(team)[0]!)[0]!)[0]!, "click");
  rendered(box, teamBoard(ms, app));
  expect(dump(team)).toBe(byRole);
  expect(dump(rendered(box, teamBoard([], app)))).toBe(markup`
    <div class="team">
      ${teamBar("role")}
      <p class="muted">This route's task table names no panes.</p>
    </div>
  `);
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
