import { afterEach, beforeEach, expect, test } from "bun:test";

import { h, icon, icons } from "../dom.ts";
import { render } from "../herdsman-blocks-view.ts";
import { FakeElement, type FakeNode, installFakeDocument, ops, resetOps } from "../herdsman-blocks.testdom.ts";
import { dispatch, dump } from "../testdom.ts";
import { hostsDialog, hostsState } from "./hosts.ts";

const g = globalThis as unknown as Record<string, unknown>;
const stored = new Map<string, string>();
g["localStorage"] = {
  getItem: (k: string) => stored.get(k) ?? null,
  setItem: (k: string, v: string) => void stored.set(k, v),
};
const assigned: string[] = [];
g["location"] = {
  protocol: "http:",
  origin: "http://127.0.0.1:7878",
  pathname: "/",
  assign: (u: string) => void assigned.push(u),
};

const fake = (n: unknown): FakeElement => n as FakeElement;
// What the dialog needs beyond the fake DOM: a body for toasts, focus and select.
const proto = FakeElement.prototype as unknown as Record<string, unknown>;
let focused: unknown[] = [];
beforeEach(() => {
  installFakeDocument();
  const body = new FakeElement("BODY");
  (g["document"] as Record<string, unknown>)["body"] = body;
  proto["append"] = function (this: FakeElement, ...ns: FakeNode[]) {
    for (const n of ns) this.appendChild(n);
  };
  proto["focus"] = function (this: FakeElement) {
    focused.push(this);
  };
  proto["select"] = function () {};
  focused = [];
  assigned.length = 0;
  stored.set(
    "herdsman.hosts",
    JSON.stringify({
      hosts: [
        { id: "a", url: "http://127.0.0.1:7878", name: "laptop", device: "s" },
        { id: "b", url: "https://box.example.com", name: "box" },
        { id: "c", url: "http://192.168.1.20:7878", name: "lan" },
      ],
      current: "a",
    }),
  );
});
afterEach(() => {
  delete proto["append"];
  delete proto["focus"];
  delete proto["select"];
});

const elements = (n: FakeNode): FakeElement[] => n.childNodes.filter((c): c is FakeElement => c.nodeType === 1);
const all = (n: FakeElement): FakeElement[] => [n, ...elements(n).flatMap(all)];
const byAttr = (n: FakeElement, k: string, v: string): FakeElement => {
  const el = all(n).find((e) => e.getAttribute(k) === v);
  if (!el) throw new Error(`no element with ${k}="${v}"`);
  return el;
};
const byClass = (n: FakeElement, c: string): FakeElement[] => all(n).filter((e) => e.className.split(" ").includes(c));

function open(): { dlg: FakeElement; redraw: () => void } {
  const box = document.createElement("div");
  const s = hostsState();
  const redraw = (): void => render(box, hostsDialog(s, redraw));
  redraw();
  return { dlg: fake(fake(box).firstChild), redraw };
}

const typeIn = (el: FakeElement, v: string): void => {
  el["value"] = v;
  dispatch(el, "input");
};

test("a host row renders what the h() version did", () => {
  const { dlg } = open();
  const rows = byClass(dlg, "host");
  // The h() row of the second host.
  const old = h(
    "li",
    { class: "host" },
    h(
      "div",
      { class: "host-main" },
      h("div", { class: "host-title" }, h("span", { class: "host-name" }, "box")),
      h("code", { class: "host-url" }, "https://box.example.com"),
      h("span", { class: "host-state" }, "Not paired"),
    ),
    h(
      "div",
      { class: "host-actions" },
      h("button", { type: "button", class: "btn" }, "Connect"),
      h(
        "button",
        { type: "button", class: "icon-btn", "aria-label": "Rename box", "data-tip": "Rename" },
        icon(icons.tag),
      ),
      h(
        "button",
        { type: "button", class: "icon-btn", "aria-label": "Forget box", "data-tip": "Forget" },
        icon(icons.trash),
      ),
    ),
  );
  expect(dump(rows[1]!)).toBe(dump(fake(old)));
  expect(rows[0]!.className).toBe("host current");
  expect(dump(rows[0]!)).toContain('<span class="host-chip">Current</span>');
  expect(dump(rows[0]!)).toContain('<span class="host-state paired">Paired</span>');
  // https from an http page is fine; the LAN host is not blocked either.
  expect(byClass(dlg, "error").length).toBe(0);
});

test("typing in the add form keeps the inputs, and an unchanged render writes nothing", () => {
  const { dlg, redraw } = open();
  const input = byAttr(dlg, "name", "host");
  typeIn(input, "10.0.");
  typeIn(input, "10.0.0.9:7878");
  expect(byAttr(dlg, "name", "host")).toBe(input);
  expect(input["value"]).toBe("10.0.0.9:7878");
  resetOps();
  redraw();
  expect(Object.values(ops).every((n) => n === 0)).toBe(true);
});

test("a refused address shows the error note; an added one clears the fields", () => {
  const { dlg } = open();
  const host = byAttr(dlg, "name", "host");
  const name = byAttr(dlg, "name", "name");
  const form = byClass(dlg, "hosts-add")[0]!;
  typeIn(host, "ftp://x");
  expect(dispatch(form, "submit").defaultPrevented).toBe(true);
  const note = byAttr(dlg, "role", "status");
  expect(note.className).toBe("hosts-note error");
  expect(note.textContent).toBe("That is not a host address or a pairing link.");
  expect(host["value"]).toBe("ftp://x");

  typeIn(host, "10.0.0.9:7878");
  typeIn(name, "nine");
  dispatch(form, "submit");
  expect(note.className).toBe("hosts-note");
  expect(note.textContent).toBe("Added nine. Paste its pairing link to pair this browser with it.");
  expect([host["value"], name["value"]]).toEqual(["", ""]);
  expect(byAttr(dlg, "name", "host")).toBe(host);
  // The address alone does not switch hosts; the new row is added after the others.
  const rows = byClass(dlg, "host");
  expect(rows.map((r) => byClass(r, "host-name")[0]?.textContent)).toEqual(["laptop", "box", "lan", "nine"]);
  expect(rows[0]!.className).toBe("host current");
  expect(assigned).toEqual([]);
});

test("rename swaps in a focused field; Enter saves, Escape does not", () => {
  const { dlg } = open();
  const row = byClass(dlg, "host")[1]!;
  dispatch(byAttr(row, "data-tip", "Rename"), "click");
  const field = byClass(row, "host-rename")[0]!;
  expect(focused).toEqual([field]);
  expect(field["value"]).toBe("box");
  expect(byClass(row, "host-name").length).toBe(0);
  field["value"] = "renamed";
  dispatch(field, "keydown", { key: "Enter" });
  // The row is kept; the name is back with the new text, and the blur that follows does nothing.
  expect(byClass(dlg, "host")[1]).toBe(row);
  expect(byClass(row, "host-name")[0]!.textContent).toBe("renamed");
  dispatch(field, "blur");
  expect(JSON.parse(stored.get("herdsman.hosts")!).hosts[1].name).toBe("renamed");

  dispatch(byAttr(row, "data-tip", "Rename"), "click");
  const again = byClass(row, "host-rename")[0]!;
  again["value"] = "nope";
  const escape = dispatch(again, "keydown", { key: "Escape" });
  dispatch(again, "blur");
  // Escape stays in the field: the dialog does not see it and close.
  expect(escape.cancelBubble).toBe(true);
  expect(byClass(row, "host-name")[0]!.textContent).toBe("renamed");
  expect(JSON.parse(stored.get("herdsman.hosts")!).hosts[1].name).toBe("renamed");
});

test("forgetting a host removes its row and keeps the others' nodes", () => {
  const { dlg } = open();
  const [first, , third] = byClass(dlg, "host");
  dispatch(byAttr(byClass(dlg, "host")[1]!, "data-tip", "Forget"), "click");
  const rows = byClass(dlg, "host");
  expect(rows.length).toBe(2);
  expect(rows[0]).toBe(first!);
  expect(rows[1]).toBe(third!);
  expect(assigned).toEqual([]);
  // Forgetting the current host starts the app over.
  dispatch(byAttr(rows[0]!, "data-tip", "Forget"), "click");
  expect(assigned).toEqual(["/"]);
});

test("an empty list says so", () => {
  stored.set("herdsman.hosts", JSON.stringify({ hosts: [], current: "" }));
  const { dlg } = open();
  expect(dump(byClass(dlg, "hosts-list-wrap")[0]!)).toBe(
    '<div class="hosts-list-wrap"><p class="hosts-empty">No hosts yet.</p></div>',
  );
});
