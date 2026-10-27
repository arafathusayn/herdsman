import { beforeAll, expect, test } from "bun:test";

import type { RouteForm } from "../new-route.ts";
import { all, byName, type FakeElement, installDom } from "../testdom.ts";
import type { Catalog, ChoicePick } from "../types.ts";

beforeAll(installDom);

const flush = () => new Promise((r) => setTimeout(r, 0));
/** A select's chosen value: the selected option's, as the fake DOM keeps it. */
const chosen = (sel: FakeElement): string => sel.children.find((o) => o.selected)?.value ?? sel.value;
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
  const lists: (() => void)[] = [];
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
    (_c, previous, recommended?: boolean, needLists?: () => void) => {
      opened.push({ previous, recommended });
      if (needLists) lists.push(needLists);
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
  return { body, probes, opened, updated, lists };
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
  // The live lists only: the saved role keeps its pick from its own entry.
  const claude = r.updated[0]!.kinds.find((k) => k.kind === "claude")!;
  expect(claude.models.map((m) => m.id)).toEqual(["newer"]);
  expect(r.updated[0]!.previous).toEqual([pick("orchestrator")]);
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

// A fresh role is offered the live lists only: another role's saved model
// that the probe did not list is not among its choices.
test("a fresh role does not see another role's saved model", async () => {
  const { newRouteFlow } = await import("./new-route-flow.ts");
  const { routeForm } = await import("./route-form.ts");
  const body = document.createElement("form") as unknown as FakeElement;
  const saved: ChoicePick = {
    role: "adjutant",
    runner: "herdr",
    harness: "claude",
    model: "fable-old",
    effort: "medium",
    route: "",
    at: "",
  };
  const deps = {
    createRoute: () => new Promise<never>(() => {}),
    inboxState: () => new Promise<never>(() => {}),
    state: () => null,
    copy: () => {},
    openPane: () => {},
    close: () => {},
    failed: () => {},
    every: () => () => {},
    later: () => () => {},
  };
  const live: Catalog = {
    ...probed,
    kinds: [{ ...probed.kinds[0]!, other_model: false }],
  };
  newRouteFlow(
    body as unknown as HTMLElement,
    {
      savedChoices: () => Promise.resolve([saved]),
      catalog: (probe = true) =>
        Promise.resolve(
          probe
            ? { ...live, previous: [saved] }
            : {
                ...quick,
                from_picks: true,
                kinds: [
                  {
                    ...quick.kinds[0]!,
                    models: [{ id: "fable-old", label: "fable-old", efforts: ["medium"], default_effort: "medium" }],
                  },
                ],
                previous: [saved],
              },
        ),
    },
    (c, previous, recommended?: boolean) =>
      routeForm(body as unknown as HTMLElement, c, null, deps, {
        usePrevious: previous,
        recommended: recommended ?? true,
      }),
    () => {},
  );
  await flush();
  byName(body, "use-recommended").dispatch("click");
  await flush();
  byName(body, "use-previous").dispatch("click");
  await flush();
  await flush();
  await flush();
  const options = (name: string) =>
    byName(body, name)
      .children.map((o) => o.getAttribute?.("value") ?? (o as unknown as { value: string }).value)
      .filter(Boolean);
  expect(chosen(byName(body, "main-model"))).toBe("fable-old");
  expect(options("orch-model")).not.toContain("fable-old");
  expect(options("orch-model")).toContain("newer");
});

test("saved picks that cannot be read count as none", async () => {
  const { newRouteFlow } = await import("./new-route-flow.ts");
  const body = document.createElement("form") as unknown as FakeElement;
  const opened: boolean[] = [];
  newRouteFlow(
    body as unknown as HTMLElement,
    { savedChoices: () => Promise.reject(new Error("unreadable")), catalog: () => Promise.resolve(quick) },
    (_c, previous) => {
      opened.push(previous);
      return { stop: () => {}, form: () => ({}) as RouteForm, sent: () => false, setCatalog: () => {} };
    },
    () => {},
  );
  await flush();
  byName(body, "use-recommended").dispatch("click");
  await flush();
  await flush();
  expect(opened).toEqual([false]);
});

// Every role saved: the form opens without the lists, and asks for them
// once when the user wants to choose again.
test("a form from saved picks can ask for the lists once", async () => {
  const r = await run([pick("orchestrator")], "orchestrator");
  byName(r.body, "use-recommended").dispatch("click");
  await flush();
  byName(r.body, "use-previous").dispatch("click");
  await flush();
  expect(r.probes).toEqual([false]);
  expect(r.lists.length).toBe(1);
  r.lists[0]!();
  r.lists[0]!();
  await flush();
  expect(r.probes).toEqual([false, true]);
  expect(r.updated.length).toBe(1);
});

// A failed read of the lists can be tried again; a second request while
// one is on its way is not sent.
test("Choose again retries after a failed read, never twice at once", async () => {
  const { newRouteFlow } = await import("./new-route-flow.ts");
  const body = document.createElement("form") as unknown as FakeElement;
  const probes: boolean[] = [];
  const errors: unknown[] = [];
  const updated: Catalog[] = [];
  let lists: (() => void) | undefined;
  let fail = true;
  const h = { kind: "claude", model: "fable", other: "", effort: "medium" };
  newRouteFlow(
    body as unknown as HTMLElement,
    {
      savedChoices: () => Promise.resolve([pick("orchestrator")]),
      catalog: (probe = true) => {
        probes.push(probe);
        if (probe && fail) return Promise.reject(new Error("offline"));
        return Promise.resolve({ ...(probe ? probed : quick), previous: [pick("orchestrator")] });
      },
    },
    (_c, _previous, _recommended, needLists) => {
      lists = needLists;
      return {
        stop: () => {},
        form: () => ({ role: "orchestrator", main: h, orch: h }) as unknown as RouteForm,
        sent: () => false,
        setCatalog: (c: Catalog) => void updated.push(c),
      };
    },
    (e) => void errors.push(e),
  );
  await flush();
  byName(body, "use-recommended").dispatch("click");
  await flush();
  byName(body, "use-previous").dispatch("click");
  await flush();
  lists!();
  lists!();
  await flush();
  expect(probes).toEqual([false, true]);
  expect(errors.length).toBe(1);
  fail = false;
  lists!();
  await flush();
  expect(probes).toEqual([false, true, true]);
  expect(updated.length).toBe(1);
});
