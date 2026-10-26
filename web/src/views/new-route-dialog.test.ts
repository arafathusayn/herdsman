import { afterEach, beforeEach, expect, test } from "bun:test";

import { FakeElement, type FakeNode, installFakeDocument } from "../herdsman-blocks.testdom.ts";
import { dispatch, dump, markup } from "../testdom.ts";

// The store reads the URL and listens for history changes when it loads.
const g = globalThis as unknown as Record<string, unknown>;
g["location"] ??= { search: "" };
g["window"] ??= { addEventListener: () => {} };
const { openNewRoute } = await import("./new-route.ts");

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

const elements = (n: FakeNode): FakeElement[] => n.childNodes.filter((c): c is FakeElement => c.nodeType === 1);

test("new route dialog: loading, then the failure as the h() version showed them, and closing removes it", async () => {
  let fail: (r: Response) => void = () => {};
  globalThis.fetch = (() => new Promise<Response>((resolve) => (fail = resolve))) as unknown as typeof fetch;
  openNewRoute();
  const dlg = elements(body)[0]!;
  expect(dlg["open"]).toBe(true);
  expect(dump(dlg)).toBe(markup`
    <dialog aria-label="New route" class="dialog new-route">
      <form method="dialog" novalidate="">
        <h2>New route</h2>
        <p class="nr-loading">Reading the harness lists…</p>
      </form>
    </dialog>
  `);

  fail(new Response("", { status: 500 }));
  await new Promise((r) => setTimeout(r, 0));
  expect(dump(dlg)).toBe(
    markup`
      <dialog aria-label="New route" class="dialog new-route">
        <form method="dialog" novalidate="">
          <h2>New route</h2>
          <p class="error">cannot read the harness list (500)</p>
          <div class="dialog-actions"><button class="btn ghost" value="cancel">Close</button></div>
        </form>
      </dialog>
    `,
  );

  dispatch(dlg, "close");
  expect(elements(body)).toEqual([]);
});
