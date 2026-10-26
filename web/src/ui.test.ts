import { afterEach, beforeEach, describe, expect, test } from "bun:test";

import { icon, icons } from "./dom.ts";
import { render, type ViewNode } from "./herdsman-blocks-view.ts";
import { FakeElement, type FakeNode, installFakeDocument } from "./herdsman-blocks.testdom.ts";
import { type Member, statusLabel } from "./model.ts";
import { dispatch } from "./testdom.ts";
import { h, unkeyed } from "./testref.ts";
import { confirmDialog, formDialog, pill, themeSwitch } from "./ui.ts";

const fake = (n: unknown): FakeElement => n as FakeElement;
const g = globalThis as unknown as { document: Record<string, unknown> };
// What a dialog needs beyond the fake DOM: a body, showModal, append and remove.
const proto = FakeElement.prototype as unknown as Record<string, unknown>;
let body: FakeElement;
beforeEach(() => {
  installFakeDocument();
  body = new FakeElement("BODY");
  g.document["body"] = body;
  proto["showModal"] = function (this: FakeElement) {
    this["open"] = true;
  };
  proto["append"] = function (this: FakeElement, n: FakeNode) {
    this.appendChild(n);
  };
  proto["remove"] = function (this: FakeElement) {
    this.parentNode?.removeChild(this);
  };
});
afterEach(() => {
  delete proto["showModal"];
  delete proto["remove"];
  delete proto["append"];
});

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
const rendered = (v: ViewNode): FakeElement => {
  const box = document.createElement("div");
  render(box, v);
  return fake(box).firstChild as FakeElement;
};
const elements = (n: FakeNode): FakeElement[] => n.childNodes.filter((c): c is FakeElement => c.nodeType === 1);

describe("ui views render what the h() versions did", () => {
  test("pill", () => {
    const statuses: Member["status"][] = ["working", "idle", "blocked", "done", "unknown", "gone"];
    for (const s of statuses) {
      const old = h(
        "span",
        { class: `pill s-${s}` },
        h("span", { class: "dot", "aria-hidden": "true" }),
        statusLabel[s],
      );
      expect(dump(rendered(pill(s)))).toBe(dump(fake(old)));
    }
  });

  test("theme switch, and its buttons call back with their theme", () => {
    const picked: string[] = [];
    const old = (current: string) =>
      h(
        "div",
        { class: "theme-switch", role: "radiogroup", "aria-label": "Theme" },
        unkeyed(
          (
            [
              ["system", "System", icons.monitor],
              ["light", "Light", icons.sun],
              ["dark", "Dark", icons.moon],
            ] as const
          ).map(([id, label, glyph]) =>
            h(
              "button",
              {
                role: "radio",
                "aria-checked": String(id === current),
                class: id === current ? "active" : "",
                title: `${label} theme`,
              },
              icon(glyph),
              h("span", null, label),
            ),
          ),
        ),
      );
    const box = document.createElement("div");
    render(
      box,
      themeSwitch("light", (t) => picked.push(`a:${t}`)),
    );
    const sw = fake(box).firstChild as FakeElement;
    expect(dump(sw)).toBe(dump(fake(old("light"))));
    const buttons = elements(sw);
    render(
      box,
      themeSwitch("dark", (t) => picked.push(`b:${t}`)),
    );
    expect(elements(sw)).toEqual(buttons); // patched in place
    expect(dump(sw)).toBe(dump(fake(old("dark"))));
    dispatch(buttons[0]!, "click");
    expect(picked).toEqual(["b:system"]);
  });

  test("confirm dialog: markup, and closing resolves with the button's value", async () => {
    const old = h(
      "dialog",
      { class: "dialog" },
      h(
        "form",
        { method: "dialog" },
        h("h2", null, "Delete this pane?"),
        h("p", null, "It is gone for good."),
        h(
          "div",
          { class: "dialog-actions" },
          h("button", { value: "cancel", class: "btn ghost" }, "Cancel"),
          h("button", { value: "ok", class: "btn danger", autofocus: true }, "Delete pane"),
        ),
      ),
    );
    const answer = confirmDialog("Delete this pane?", "It is gone for good.", "Delete pane", true);
    const dlg = elements(body)[0]!;
    expect(dump(dlg)).toBe(dump(fake(old)));
    expect(dlg["open"]).toBe(true);
    dlg["returnValue"] = "ok";
    dispatch(dlg, "close");
    expect(await answer).toBe(true);
    expect(elements(body)).toEqual([]);
  });

  test("form dialog markup", () => {
    const random = Math.random;
    Math.random = () => 0.5;
    try {
      const id = `f-name-${(0.5).toString(36).slice(2, 8)}`;
      // The browser reflects the old h() properties (value aside) as these attributes.
      const old = h(
        "dialog",
        { class: "dialog" },
        h(
          "form",
          { method: "dialog" },
          h("h2", null, "Rename"),
          h(
            "label",
            { class: "field", for: id },
            h("span", null, "Name"),
            h("input", {
              id,
              name: "name",
              placeholder: "orch-2",
              required: true,
              autocomplete: "off",
              spellcheck: "false",
              list: `${id}-list`,
            }),
            h("datalist", { id: `${id}-list` }, h("option", { value: "a" }), h("option", { value: "b" })),
            h("small", null, "A short name"),
          ),
          h(
            "div",
            { class: "dialog-actions" },
            h("button", { value: "cancel", class: "btn ghost", formnovalidate: true }, "Cancel"),
            h("button", { value: "ok", class: "btn primary" }, "Rename"),
          ),
        ),
      );
      void formDialog(
        "Rename",
        [
          {
            name: "name",
            label: "Name",
            value: "orch",
            placeholder: "orch-2",
            hint: "A short name",
            list: ["a", "b", "a"],
            required: true,
          },
        ],
        "Rename",
      );
      const dlg = elements(body)[0]!;
      expect(dump(dlg)).toBe(dump(fake(old)));
      const input = elements(elements(elements(dlg)[0]!)[1]!)[1]!;
      expect(input["value"]).toBe("orch"); // a property, as h() set it
    } finally {
      Math.random = random;
    }
  });
});
