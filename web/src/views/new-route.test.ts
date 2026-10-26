import { beforeAll, expect, mock, test } from "bun:test";

import { all, byName, type FakeElement, find, installDom } from "../testdom.ts";
import type { Catalog, ChoicePick } from "../types.ts";

beforeAll(installDom);

const flush = () => new Promise((r) => setTimeout(r, 0));
/** A select's chosen value: the selected option's, as the fake DOM keeps it. */
const chosen = (sel: FakeElement): string => sel.children.find((o) => o.selected)?.value ?? sel.value;
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

void mock.module("../api.ts", () => ({
  AuthError: class extends Error {},
  catalog: (probe = true) => {
    probes.push(probe);
    return failCatalog ? Promise.reject(new Error("lists unreadable")) : Promise.resolve({ ...live, previous: saved });
  },
  createRoute: () => new Promise(() => {}),
  githubAccess: () => new Promise(() => {}),
  inboxState: () => new Promise(() => {}),
  savedChoices: () => Promise.resolve(saved),
}));
void mock.module("../store.ts", () => ({ store: { app: { server: null }, set: () => {}, go: () => {} } }));
void mock.module("../ui.ts", () => ({ dismissOnBackdrop: () => {}, toast: (m: string) => void toasts.push(m) }));

const dialog = (): FakeElement => {
  const body = (document as unknown as { body: FakeElement }).body;
  return body.children[body.children.length - 1]!;
};

// The dialog wires the answers to the form: the custom setup opens the
// form with the recommended setup off and the previous choice filled in.
test("the New route dialog carries the setup answer and the previous choice into the form", async () => {
  const { openNewRoute } = await import("./new-route.ts");
  openNewRoute();
  const dlg = dialog();
  expect(dlg.open).toBe(true);
  byName(dlg, "custom-setup").dispatch("click");
  await flush();
  await flush();
  byName(dlg, "use-previous").dispatch("click");
  await flush();
  await flush();
  // The adjutant has no saved pick, so the lists are read for it alone.
  expect(probes).toEqual([false, true]);
  expect(byName(dlg, "recommended").checked).toBe(false);
  expect(chosen(byName(dlg, "main-model"))).toBe("fable");
  dlg.close();
  expect(find(dlg, (e) => e.name === "main-model")).toBeDefined();
});

test("a catalog that cannot be read shows the error in the dialog", async () => {
  const { openNewRoute } = await import("./new-route.ts");
  failCatalog = true;
  openNewRoute();
  const dlg = dialog();
  byName(dlg, "use-recommended").dispatch("click");
  await flush();
  byName(dlg, "choose-again").dispatch("click");
  await flush();
  await flush();
  expect(all(dlg).some((e) => e.classList.contains("error"))).toBe(true);
  expect(toasts).toContain("lists unreadable");
  failCatalog = false;
});
