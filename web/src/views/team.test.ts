import { beforeEach, expect, test } from "bun:test";

import { patch } from "../dom.ts";
import { type FakeElement, installFakeDocument } from "../herdsman-blocks.testdom.ts";
import type { Member, Role } from "../model.ts";
import type { App } from "../store.ts";

// The store reads the URL and listens for history changes when it loads.
const g = globalThis as unknown as Record<string, unknown>;
g["location"] ??= { search: "" };
g["window"] ??= { addEventListener: () => {} };
const { teamView } = await import("./team.ts");

beforeEach(installFakeDocument);

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
const lanesOf = (root: Element): FakeElement[] => {
  const team = (root as unknown as FakeElement).firstChild as FakeElement;
  const kanban = team.childNodes.find((n) => (n as FakeElement).className === "kanban") as FakeElement;
  return kanban.childNodes as FakeElement[];
};
// A lane holds its heading, then its cards.
const cardsOf = (lane: FakeElement) => lane.childNodes.slice(1);

test("lanes keep their nodes when a lane appears ahead of them and their cards change", () => {
  const root = document.createElement("div");
  patch(root, teamView([member("p1", "implementer"), member("p2", "reviewer")], app));
  const [impl, rev] = lanesOf(root);
  const cards = cardsOf(impl!);

  const next = [member("p0", "orchestrator"), member("p1", "implementer"), member("p3", "implementer")];
  patch(root, teamView([...next, member("p2", "reviewer")], app));
  const lanes = lanesOf(root);
  expect(lanes.map((l) => l.getAttribute("data-key"))).toEqual(["orchestrator", "implementer", "reviewer"]);
  expect(lanes[1]).toBe(impl!);
  expect(lanes[2]).toBe(rev!);
  expect(cardsOf(lanes[1]!)[0]).toBe(cards[0]!);
  expect(cardsOf(lanes[1]!).length).toBe(2);
});
