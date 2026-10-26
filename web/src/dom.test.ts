import { beforeEach, describe, expect, test } from "bun:test";

import { each, h, patch } from "./dom.ts";
import { asDom, type FakeElement, installFakeDocument } from "./herdsman-blocks.testdom.ts";

beforeEach(installFakeDocument);

describe("patch (dom.ts)", () => {
  test("keeps the live element and runs the newest handler", () => {
    const root = document.createElement("div");
    const calls: string[] = [];
    patch(root, h("button", { class: "a", onclick: () => calls.push("first") }, "Go"));
    const button = root.firstChild as unknown as FakeElement;

    patch(root, h("button", { class: "b", onclick: () => calls.push("second") }, "Go!"));
    expect(root.firstChild).toBe(asDom(button));
    expect(button.className).toBe("b");
    expect(button.textContent).toBe("Go!");
    button.fire("click");
    expect(calls).toEqual(["second"]);

    patch(root, h("button", { onmouseover: () => calls.push("hover") }));
    button.fire("click");
    button.fire("mouseover");
    expect(calls).toEqual(["second", "hover"]);
  });

  test("an identical-looking element still gets the newest handler", () => {
    const root = document.createElement("ul");
    const calls: string[] = [];
    const view = (tag: string) =>
      [1, 2, 3].map((i) => h("li", null, h("button", { onclick: () => calls.push(`${tag}${i}`) }, "Go")));
    patch(root, view("old"));
    const buttons = (root as unknown as FakeElement).childNodes.map((li) => li.firstChild as FakeElement);
    patch(root, view("new")); // same structure and attributes, new closures
    (root as unknown as FakeElement).childNodes.forEach((li, i) => expect(li.firstChild).toBe(buttons[i]!));
    buttons[1]!.fire("click");
    expect(calls).toEqual(["new2"]);
  });

  test("keyed rows from h() keep their nodes across reorders", () => {
    const root = document.createElement("ul");
    const view = (ids: string[]) => ids.map((id) => h("li", { dataset: { key: id } }, id));
    patch(root, view(["1", "2", "3"]));
    const [one, two, three] = (root as unknown as FakeElement).childNodes;
    patch(root, view(["3", "1", "2", "4"]));
    expect((root as unknown as FakeElement).childNodes.slice(0, 3)).toEqual([three!, one!, two!]);
    expect(root.textContent).toBe("3124");
  });
});

describe("each", () => {
  interface Row {
    id: number;
    label: string;
  }
  const view = (rows: Row[]) =>
    each(
      rows,
      (r) => r.id,
      (r) => h("li", null, r.label),
    );

  test("stamps keys and keeps each row's node through a reorder", () => {
    const root = document.createElement("ul");
    const rows = [1, 2, 3, 4].map((id) => ({ id, label: `row ${id}` }));
    patch(root, view(rows));
    const nodes = (root as unknown as FakeElement).childNodes;
    expect(nodes.map((n) => (n as FakeElement).getAttribute("data-key"))).toEqual(["1", "2", "3", "4"]);
    patch(root, view([rows[3]!, rows[0]!, rows[2]!, rows[1]!]));
    expect((root as unknown as FakeElement).childNodes).toEqual([nodes[3]!, nodes[0]!, nodes[2]!, nodes[1]!]);
    expect(root.textContent).toBe("row 4row 1row 3row 2");
  });

  test("an index cannot be a key", () => {
    const neverRun = (): void => {
      const indexKey = (_item: number, i: number): number => i;
      // @ts-expect-error the key function gets only the item, never its position
      each([1, 2], indexKey, () => h("li"));
    };
    void neverRun;
  });

  test("duplicate keys warn only in development", () => {
    const g = globalThis as unknown as { HERDSMAN_DEV?: boolean };
    const warn = console.warn;
    const seen: string[] = [];
    console.warn = (m: string) => void seen.push(m);
    try {
      view([
        { id: 1, label: "a" },
        { id: 1, label: "b" },
      ]);
      expect(seen).toEqual([]);
      g.HERDSMAN_DEV = true;
      view([
        { id: 1, label: "a" },
        { id: 1, label: "b" },
      ]);
      expect(seen.length).toBe(1);
      expect(seen[0]).toContain('duplicate key "1"');
    } finally {
      delete g.HERDSMAN_DEV;
      console.warn = warn;
    }
  });
});
