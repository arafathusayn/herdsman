import { afterAll, beforeAll, expect, mock, test } from "bun:test";

import type { FakeElement } from "../herdsman-blocks.testdom.ts";
import { all, byName, dispatch, find, installDom } from "../testdom.ts";
import type { Catalog, ChoicePick } from "../types.ts";

beforeAll(installDom);

const flush = () => new Promise((r) => setTimeout(r, 0));
/** A select's chosen value: the option the view marks selected. */
const chosen = (sel: FakeElement): string | null | undefined =>
  all(sel)
    .find((o) => o.tagName.toLowerCase() === "option" && o["selected"] === true)
    ?.getAttribute("value");
const click = (b: FakeElement, name: string): void => void dispatch(byName(b, name), "click");
const saved: ChoicePick[] = [
  { role: "orchestrator", runner: "herdr", harness: "claude", model: "fable", effort: "medium", route: "", at: "" },
];
const live: Catalog = {
  today: "2026-10-26",
  home: "/h",
  checkpointer_ok: true,
  kinds: [
    {
      kind: "claude",
      label: "Claude",
      models: [{ id: "fable", label: "fable", efforts: ["medium"], default_effort: "medium" }],
      default_model: "fable",
      other_model: false,
      free_model: false,
      checkpointer: true,
      skill_prefix: "/",
      skill_installed: true,
      offline: false,
    },
  ],
};
const probes: boolean[] = [];
let failCatalog = false;
const toasts: string[] = [];

// The server, as the real api.ts asks it: the harness lists (probed or the
// saved picks alone), the saved picks, and calls that never answer. A fetch
// stub, not a module mock: a module mock would last for the whole run.
const realFetch = globalThis.fetch;
globalThis.fetch = ((url: string) => {
  const path = String(url);
  const json = (v: unknown) => Promise.resolve(new Response(JSON.stringify(v)));
  if (path.endsWith("/api/choices")) return json({ picks: saved });
  if (path.includes("/api/catalog")) {
    const probe = !path.includes("probe=0");
    probes.push(probe);
    return failCatalog ? Promise.reject(new Error("lists unreadable")) : json({ ...live, previous: saved });
  }
  return new Promise(() => {});
}) as unknown as typeof fetch;
afterAll(() => {
  globalThis.fetch = realFetch;
});
// The store reads the address and listens for history moves when loaded.
const g = globalThis as unknown as Record<string, unknown>;
g["location"] ??= { search: "" };
g["window"] ??= { addEventListener() {} };
// A module mock lasts for the whole run, so it keeps ui.ts's other exports
// for the test files that load after this one.
const realUi = { ...(await import("../ui.ts")) };
void mock.module("../ui.ts", () => ({
  ...realUi,
  dismissOnBackdrop: () => {},
  toast: (m: string) => void toasts.push(m),
}));

const dialog = (): FakeElement => {
  const body = (document as unknown as { body: FakeElement }).body;
  return body.lastChild as FakeElement;
};

// The dialog wires the answers to the form: the custom setup opens the
// form with the recommended setup off and the previous choice filled in.
test("the New route dialog carries the setup answer and the previous choice into the form", async () => {
  const { openNewRoute } = await import("./new-route.ts");
  openNewRoute();
  const dlg = dialog();
  expect(dlg["open"]).toBe(true);
  click(dlg, "custom-setup");
  await flush();
  await flush();
  click(dlg, "use-previous");
  await flush();
  await flush();
  // The adjutant has no saved pick, so the lists are read for it alone.
  expect(probes).toEqual([false, true]);
  expect(byName(dlg, "recommended")["checked"]).toBe(false);
  expect(chosen(byName(dlg, "main-model"))).toBe("fable");
  // The route form is the dialog's only form now.
  const forms = all(dlg).filter((e) => e.tagName.toLowerCase() === "form");
  expect(forms.length).toBe(1);
  expect(find(forms[0]!, (e) => e.getAttribute("name") === "start")).toBeDefined();
  (dlg as unknown as HTMLDialogElement).close();
  // Closing removes the dialog; its form stays as it was.
  expect(dlg.parentNode).toBeNull();
  expect(find(dlg, (e) => e.getAttribute("name") === "main-model")).toBeDefined();
});

test("a catalog that cannot be read shows the error in the dialog", async () => {
  const { openNewRoute } = await import("./new-route.ts");
  failCatalog = true;
  openNewRoute();
  const dlg = dialog();
  click(dlg, "use-recommended");
  await flush();
  click(dlg, "choose-again");
  await flush();
  await flush();
  expect(all(dlg).some((e) => e.className.split(" ").includes("error"))).toBe(true);
  expect(all(dlg).some((e) => e.className.split(" ").includes("nr-loading"))).toBe(false);
  expect(toasts).toContain("lists unreadable");
  failCatalog = false;
});
