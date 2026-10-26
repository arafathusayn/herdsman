import { afterEach, beforeEach, describe, expect, test } from "bun:test";

import { render, type ViewNode } from "./herdsman-blocks-view.ts";
import { FakeElement, type FakeNode, installFakeDocument } from "./herdsman-blocks.testdom.ts";
import { icon, icons } from "./icons.ts";
import type { Member } from "./model.ts";
import { dispatch, markup } from "./testdom.ts";
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
const iconMarkup = (paths: readonly string[]): string => dump(rendered(icon(paths)));
const elements = (n: FakeNode): FakeElement[] => n.childNodes.filter((c): c is FakeElement => c.nodeType === 1);

describe("ui views render what the h() versions did", () => {
  test("pill", () => {
    const labels: [Member["status"], string][] = [
      ["working", "Working"],
      ["idle", "Idle"],
      ["blocked", "Needs input"],
      ["done", "Done"],
      ["unknown", "Unknown"],
      ["gone", "Pane gone"],
    ];
    for (const [s, label] of labels) {
      expect(dump(rendered(pill(s)))).toBe(
        `<span class="pill s-${s}"><span aria-hidden="true" class="dot"></span>${label}</span>`,
      );
    }
  });

  test("theme switch, and its buttons call back with their theme", () => {
    const picked: string[] = [];
    const box = document.createElement("div");
    render(
      box,
      themeSwitch("light", (t) => picked.push(`a:${t}`)),
    );
    const sw = fake(box).firstChild as FakeElement;
    expect(dump(sw)).toBe(markup`
      <div aria-label="Theme" class="theme-switch" role="radiogroup">
        <button aria-checked="false" role="radio" title="System theme" type="button">
          ${iconMarkup(icons.monitor)}
          <span >System</span>
        </button>
        <button aria-checked="true" class="active" role="radio" title="Light theme" type="button">
          ${iconMarkup(icons.sun)}
          <span >Light</span>
        </button>
        <button aria-checked="false" role="radio" title="Dark theme" type="button">
          ${iconMarkup(icons.moon)}
          <span >Dark</span>
        </button>
      </div>
    `);
    const buttons = elements(sw);
    render(
      box,
      themeSwitch("dark", (t) => picked.push(`b:${t}`)),
    );
    expect(elements(sw)).toEqual(buttons); // patched in place
    expect(dump(sw)).toBe(markup`
      <div aria-label="Theme" class="theme-switch" role="radiogroup">
        <button aria-checked="false" role="radio" title="System theme" type="button">
          ${iconMarkup(icons.monitor)}
          <span >System</span>
        </button>
        <button aria-checked="false" role="radio" title="Light theme" type="button">
          ${iconMarkup(icons.sun)}
          <span >Light</span>
        </button>
        <button aria-checked="true" class="active" role="radio" title="Dark theme" type="button">
          ${iconMarkup(icons.moon)}
          <span >Dark</span>
        </button>
      </div>
    `);
    dispatch(buttons[0]!, "click");
    expect(picked).toEqual(["b:system"]);
  });

  test("confirm dialog: markup, and closing resolves with the button's value", async () => {
    const answer = confirmDialog("Delete this pane?", "It is gone for good.", "Delete pane", true);
    const dlg = elements(body)[0]!;
    expect(dump(dlg)).toBe(markup`
      <dialog class="dialog">
        <form method="dialog">
          <h2 >Delete this pane?</h2>
          <p >It is gone for good.</p>
          <div class="dialog-actions">
            <button class="btn ghost" value="cancel">Cancel</button>
            <button autofocus="" class="btn danger" value="ok">Delete pane</button>
          </div>
        </form>
      </dialog>
    `);
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
      // Math.random is 0.5, so the field's id is f-name-i.
      expect(dump(dlg)).toBe(markup`
        <dialog class="dialog">
          <form method="dialog">
            <h2 >Rename</h2>
            <label class="field" for="f-name-i">
              <span >Name</span>
              <input autocomplete="off" id="f-name-i" list="f-name-i-list" name="name" placeholder="orch-2" required="" spellcheck="false"></input>
              <datalist id="f-name-i-list"><option value="a"></option><option value="b"></option></datalist>
              <small >A short name</small>
            </label>
            <div class="dialog-actions">
              <button class="btn ghost" formnovalidate="" value="cancel">Cancel</button>
              <button class="btn primary" value="ok">Rename</button>
            </div>
          </form>
        </dialog>
      `);
      const input = elements(elements(elements(dlg)[0]!)[1]!)[1]!;
      expect(input["value"]).toBe("orch"); // a property, as h() set it
    } finally {
      Math.random = random;
    }
  });
});
