import { afterEach, beforeEach, expect, test } from "bun:test";

import type { FakeElement, FakeNode } from "../herdsman-blocks.testdom.ts";
import { dispatch, dump, installDom, markup } from "../testdom.ts";

// The store reads the URL and listens for history changes when it loads.
const g = globalThis as unknown as Record<string, unknown>;
g["location"] ??= { search: "" };
g["window"] ??= { addEventListener: () => {} };
const { openNewRoute } = await import("./new-route.ts");

const realFetch = globalThis.fetch;
// The test DOM gives a view's elements the dialog methods the app calls
// (showModal, append, replaceChildren, remove, querySelector).
let body: FakeElement;
beforeEach(() => {
  installDom();
  body = (g["document"] as { body: FakeElement }).body;
});
afterEach(() => {
  globalThis.fetch = realFetch;
});

const elements = (n: FakeNode): FakeElement[] => n.childNodes.filter((c): c is FakeElement => c.nodeType === 1);

test("new route dialog: the first question, loading, then the failure as the h() version showed them, and closing removes it", async () => {
  // No saved picks; the harness lists answer when the test says.
  let fail: (r: Response) => void = () => {};
  globalThis.fetch = ((url: string) =>
    String(url).endsWith("/api/choices")
      ? Promise.resolve(new Response(JSON.stringify({ picks: [] })))
      : new Promise<Response>((resolve) => (fail = resolve))) as unknown as typeof fetch;
  openNewRoute();
  const dlg = elements(body)[0]!;
  expect(dlg["open"]).toBe(true);
  // The first question, before anything is probed.
  const form = elements(dlg)[0]!;
  expect(elements(form).map((e) => e.tagName.toLowerCase())).toEqual(["h2", "p", "dl", "div"]);
  expect(elements(form)[1]!.textContent).toBe("Use the recommended setup?");
  const answer = elements(elements(form)[3]!).find((e) => e.getAttribute("name") === "use-recommended")!;
  dispatch(answer, "click");
  await new Promise((r) => setTimeout(r, 0));
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
