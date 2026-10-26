import { afterEach, beforeEach, expect, test } from "bun:test";

import { h } from "../dom.ts";
import { FakeElement, type FakeNode, installFakeDocument } from "../herdsman-blocks.testdom.ts";
import { dispatch } from "../testdom.ts";

// The store reads the URL and listens for history changes when it loads.
const g = globalThis as unknown as Record<string, unknown>;
g["location"] ??= { search: "" };
g["window"] ??= { addEventListener: () => {} };
const { openNewRoute } = await import("./new-route.ts");

const fake = (n: unknown): FakeElement => n as FakeElement;
const doc = (): Record<string, unknown> => g["document"] as Record<string, unknown>;
const proto = FakeElement.prototype as unknown as Record<string, unknown>;
const realFetch = globalThis.fetch;
let body: FakeElement;
beforeEach(() => {
  installFakeDocument();
  body = new FakeElement("BODY");
  doc()["body"] = body;
  proto["showModal"] = function (this: FakeElement) {
    this["open"] = true;
  };
  proto["append"] = function (this: FakeElement, ...ns: FakeNode[]) {
    for (const n of ns) this.appendChild(n);
  };
  proto["replaceChildren"] = function (this: FakeElement, ...ns: FakeNode[]) {
    this.textContent = "";
    for (const n of ns) this.appendChild(n);
  };
  proto["remove"] = function (this: FakeElement) {
    this.parentNode?.removeChild(this);
  };
});
afterEach(() => {
  for (const k of ["showModal", "append", "replaceChildren", "remove"]) delete proto[k];
  globalThis.fetch = realFetch;
});

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
const elements = (n: FakeNode): FakeElement[] => n.childNodes.filter((c): c is FakeElement => c.nodeType === 1);

// The old h() dialog, with the form's method property written as the
// attribute the browser reflects it to.
const oldDialog = (...content: HTMLElement[]): HTMLElement =>
  h(
    "dialog",
    { class: "dialog new-route", "aria-label": "New route" },
    h("form", { method: "dialog", novalidate: true }, h("h2", null, "New route"), ...content),
  );

test("new route dialog: loading, then the failure as the h() version showed them, and closing removes it", async () => {
  let fail: (r: Response) => void = () => {};
  globalThis.fetch = (() => new Promise<Response>((resolve) => (fail = resolve))) as unknown as typeof fetch;
  openNewRoute();
  const dlg = elements(body)[0]!;
  expect(dlg["open"]).toBe(true);
  expect(dump(dlg)).toBe(dump(fake(oldDialog(h("p", { class: "nr-loading" }, "Reading the harness lists…")))));

  fail(new Response("", { status: 500 }));
  await new Promise((r) => setTimeout(r, 0));
  expect(dump(dlg)).toBe(
    dump(
      fake(
        oldDialog(
          h("p", { class: "error" }, "cannot read the harness list (500)"),
          h("div", { class: "dialog-actions" }, h("button", { value: "cancel", class: "btn ghost" }, "Close")),
        ),
      ),
    ),
  );

  dispatch(dlg, "close");
  expect(elements(body)).toEqual([]);
});
