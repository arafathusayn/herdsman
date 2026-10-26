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

const probed: Catalog = {
  ...quick,
  kinds: [
    {
      ...quick.kinds[0]!,
      saved: false,
      models: [{ id: "newer", label: "newer", efforts: ["low"], default_effort: "low" }],
      default_model: "newer",
    },
  ],
};

async function run(picks: ChoicePick[], role: "orchestrator" | "adjutant", checkpointerOk = true) {
  const { newRouteFlow } = await import("./new-route-flow.ts");
  const body = document.createElement("form") as unknown as FakeElement;
  const probes: boolean[] = [];
  const opened: { previous: boolean; recommended: unknown }[] = [];
  const updated: Catalog[] = [];
  const h = { kind: "claude", model: "fable", other: "", effort: "medium" };
  newRouteFlow(
    body as unknown as HTMLElement,
    {
      savedChoices: () => Promise.resolve(picks),
      catalog: (probe = true) => {
        probes.push(probe);
        const c = probe ? probed : quick;
        return Promise.resolve({ ...c, checkpointer_ok: checkpointerOk, previous: picks });
      },
    },
    (_c, previous, recommended?: boolean) => {
      opened.push({ previous, recommended });
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

const names = (b: FakeElement) => all(b).map((e) => e.dataset?.["field"] ?? e.getAttribute?.("name"));

// A32: the first question is "Use the recommended setup?"; A30's previous
// choice follows it; nothing is probed until the user leaves that path.
test("the recommended setup is asked first, then the previous choice, without a probe", async () => {
  const r = await run([pick("orchestrator")], "orchestrator");
  expect(names(r.body)).toContain("use-recommended");
  expect(names(r.body)).not.toContain("use-previous");
  expect(r.probes).toEqual([]);
  byName(r.body, "use-recommended").dispatch("click");
  await flush();
  expect(names(r.body)).toContain("use-previous");
  expect(r.probes).toEqual([]);
  byName(r.body, "use-previous").dispatch("click");
  await flush();
  // Yes: the saved picks open the form; every role has its pick, so no probe.
  expect(r.probes).toEqual([false]);
  expect(r.opened).toEqual([{ previous: true, recommended: true }]);
  expect(r.updated).toEqual([]);
});

test("with no saved pick the recommended question still comes before any probe", async () => {
  const r = await run([], "orchestrator");
  expect(names(r.body)).toContain("use-recommended");
  expect(r.probes).toEqual([]);
  byName(r.body, "custom-setup").dispatch("click");
  await flush();
  expect(r.probes).toEqual([true]);
  expect(r.opened).toEqual([{ previous: false, recommended: false }]);
});

// A30 per role: the probed lists come in for the role that needs them, and
// the saved role keeps its exact saved model and effort.
test("a role without a saved pick probes the lists; the saved role keeps its pick", async () => {
  const r = await run([pick("orchestrator")], "adjutant");
  byName(r.body, "use-recommended").dispatch("click");
  await flush();
  byName(r.body, "use-previous").dispatch("click");
  await flush();
  await flush();
  expect(r.probes).toEqual([false, true]);
  expect(r.opened).toEqual([{ previous: true, recommended: true }]);
  expect(r.updated.length).toBe(1);
  const claude = r.updated[0]!.kinds.find((k) => k.kind === "claude")!;
  expect(claude.models.map((m) => m.id).sort()).toEqual(["fable", "newer"]);
  expect(claude.models.find((m) => m.id === "fable")!.efforts).toEqual(["medium"]);
});

test("Choose again probes and opens the form", async () => {
  const again = await run([pick("orchestrator")], "orchestrator");
  byName(again.body, "use-recommended").dispatch("click");
  await flush();
  byName(again.body, "choose-again").dispatch("click");
  await flush();
  expect(again.probes).toEqual([true]);
  expect(again.opened).toEqual([{ previous: false, recommended: true }]);
});

// The summary before the form does not promise a checkpointer: it states
// when one runs.
test("the first summary states when the checkpointer runs", async () => {
  const r = await run([], "orchestrator");
  const dl = all(r.body).find((e) => e.dataset?.["field"] === "recommended-summary")!;
  const text = dl.textContent ?? "";
  expect(text).not.toContain("On: it keeps");
  expect(text).toContain("Checkpointer");
  expect(text).toContain("Claude Code or Codex");
});
