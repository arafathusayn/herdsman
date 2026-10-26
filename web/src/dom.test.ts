import { beforeEach, describe, expect, test } from "bun:test";

import { h, patch } from "./dom.ts";
import { asDom, type FakeElement, installFakeDocument } from "./herdsman-dom-diff.testdom.ts";

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
