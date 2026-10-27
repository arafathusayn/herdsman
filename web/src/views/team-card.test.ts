import { beforeAll, expect, test } from "bun:test";

import { render } from "../herdsman-blocks-view.ts";
import type { FakeElement } from "../herdsman-blocks.testdom.ts";
import type { Member } from "../model.ts";
import { find, installDom } from "../testdom.ts";

beforeAll(() => {
  installDom();
  // The store reads the address and listens for history moves when loaded.
  const g = globalThis as unknown as Record<string, unknown>;
  g["location"] = { search: "" };
  g["window"] = { addEventListener() {} };
});

const long = "teaching-lab-scm627-implementers-with-a-tab-label-longer-than-any-card";

const member = {
  pane_id: "w6:p3R",
  role: "implementer",
  title: "im-config-with-a-name-longer-than-the-card",
  status: "working",
  task: { task: "config", branch: "arafat-ll/scm-574-sown-to-grow-integration" },
  agent: { agent: "claude", terminal_title_stripped: "✳ Implementing the config loader" },
  pane: null,
  tab: { label: long },
} as unknown as Member;

test("an agent card's one-line texts clip, each with its full text as a tooltip", async () => {
  const { card } = await import("./team.ts");
  const { store } = await import("../store.ts");
  // card() returns a view: render it and read the button it made.
  const box = document.createElement("div");
  render(box, card(member, store.app));
  const el = box.firstChild as unknown as FakeElement;
  const byClass = (c: string) => find(el, (e) => e.className.split(" ").includes(c));
  expect(byClass("card-title")?.getAttribute("title")).toBe(member.title);
  expect(byClass("card-sub")?.getAttribute("title")).toBe("claude · arafat-ll/scm-574-sown-to-grow-integration");
  const tab = byClass("card-tab");
  expect(tab?.className).toContain("clip");
  expect(tab?.getAttribute("title")).toBe(long);
  expect(find(el, (e) => e.tagName.toLowerCase() === "code")?.textContent).toBe("w6:p3R");
});
