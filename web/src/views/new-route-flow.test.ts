import { beforeAll, expect, test } from "bun:test";

import type { RouteForm } from "../new-route.ts";
import { all, byName, type FakeElement, installDom } from "../testdom.ts";
import type { Catalog, ChoicePick } from "../types.ts";

beforeAll(installDom);

const flush = () => new Promise((r) => setTimeout(r, 0));
const pick = (role: string): ChoicePick => ({
  role,
  runner: "herdr",
  harness: "claude",
  model: "fable",
  effort: "medium",
  route: "",
  at: "",
});
const quick: Catalog = {
  today: "2026-10-25",
  home: "/h",
  checkpointer_ok: true,
  kinds: [
    {
      kind: "claude",
      label: "claude",
      models: [{ id: "fable", label: "fable", efforts: ["medium"], default_effort: "medium" }],
      default_model: "fable",
      other_model: false,
      free_model: false,
      checkpointer: true,
      skill_prefix: "/",
      skill_installed: true,
      offline: false,
      saved: true,
    },
  ],
};

async function run(picks: ChoicePick[], role: "orchestrator" | "adjutant") {
  const { newRouteFlow } = await import("./new-route-flow.ts");
  const body = document.createElement("form") as unknown as FakeElement;
  const probes: boolean[] = [];
  const opened: boolean[] = [];
  const updated: Catalog[] = [];
  const h = { kind: "claude", model: "fable", other: "", effort: "medium" };
  newRouteFlow(
    body as unknown as HTMLElement,
    {
      savedChoices: () => Promise.resolve(picks),
      catalog: (probe = true) => {
        probes.push(probe);
        return Promise.resolve({ ...quick, previous: picks });
      },
    },
    (_c, previous) => {
      opened.push(previous);
      return {
        stop: () => {},
        form: () => ({ role, main: h, orch: h }) as unknown as RouteForm,
        sent: () => false,
        setCatalog: (c: Catalog) => void updated.push(c),
      };
    },
    () => {},
  );
  await flush();
  return { body, probes, opened, updated };
}

// A32/A30: the recommended setup shows first, then the previous-choice
// question; nothing is probed until the user leaves that path.
test("the previous choice is asked after the recommended setup, without a probe", async () => {
  const r = await run([pick("orchestrator")], "orchestrator");
  const names = all(r.body).map((e) => e.dataset?.["field"] ?? e.getAttribute?.("name"));
  expect(names.indexOf("recommended-summary")).toBeLessThan(names.indexOf("use-previous"));
  expect(r.probes).toEqual([]);
  byName(r.body, "use-previous").dispatch("click");
  await flush();
  // Yes: the saved picks open the form; every role has its pick, so no probe.
  expect(r.probes).toEqual([false]);
  expect(r.opened).toEqual([true]);
  expect(r.updated).toEqual([]);
});

test("a role without a saved pick probes the lists and updates the form in place", async () => {
  const r = await run([pick("orchestrator")], "adjutant");
  byName(r.body, "use-previous").dispatch("click");
  await flush();
  await flush();
  expect(r.probes).toEqual([false, true]);
  expect(r.opened).toEqual([true]);
  expect(r.updated.length).toBe(1);
});

test("no saved pick, or Choose again, probes and opens the form", async () => {
  const none = await run([], "orchestrator");
  expect(none.probes).toEqual([true]);
  expect(none.opened).toEqual([false]);
  const again = await run([pick("orchestrator")], "orchestrator");
  byName(again.body, "choose-again").dispatch("click");
  await flush();
  expect(again.probes).toEqual([true]);
  expect(again.opened).toEqual([false]);
});
