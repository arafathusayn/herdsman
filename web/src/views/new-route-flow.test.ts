import { beforeAll, expect, test } from "bun:test";

import type { FakeElement } from "../herdsman-blocks.testdom.ts";
import type { RouteForm } from "../new-route.ts";
import { all, byName, dispatch, dump, installDom, markup } from "../testdom.ts";
import type { Catalog, ChoicePick } from "../types.ts";

beforeAll(installDom);

const flush = () => new Promise((r) => setTimeout(r, 0));
const options = (sel: FakeElement): FakeElement[] => all(sel).filter((e) => e.tagName.toLowerCase() === "option");
/** A select's chosen value: the option the view marks selected. */
const chosen = (sel: FakeElement): string | null | undefined =>
  options(sel)
    .find((o) => o["selected"] === true)
    ?.getAttribute("value");
const click = (b: FakeElement, name: string): void => void dispatch(byName(b, name), "click");
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

const names = (b: FakeElement) => all(b).map((e) => e.getAttribute("data-field") ?? e.getAttribute("name"));

// A32: the first question is "Use the recommended setup?"; A30's previous
// choice follows it; nothing is probed until the user leaves that path.
test("the recommended setup is asked first, then the previous choice, without a probe", async () => {
  const r = await run([pick("orchestrator")], "orchestrator");
  expect(names(r.body)).toContain("use-recommended");
  expect(names(r.body)).not.toContain("use-previous");
  expect(r.probes).toEqual([]);
  click(r.body, "use-recommended");
  await flush();
  expect(names(r.body)).toContain("use-previous");
  expect(r.probes).toEqual([]);
  click(r.body, "use-previous");
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
  click(r.body, "custom-setup");
  await flush();
  expect(r.probes).toEqual([true]);
  expect(r.opened).toEqual([{ previous: false, recommended: false }]);
});

// A30 per role: the probed lists come in for the role that needs them, and
// the saved role keeps its exact saved model and effort.
test("a role without a saved pick probes the lists; the saved role keeps its pick", async () => {
  const r = await run([pick("orchestrator")], "adjutant");
  click(r.body, "use-recommended");
  await flush();
  click(r.body, "use-previous");
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
  click(again.body, "use-recommended");
  await flush();
  click(again.body, "choose-again");
  await flush();
  expect(again.probes).toEqual([true]);
  expect(again.opened).toEqual([{ previous: false, recommended: true }]);
});

// The summary before the form does not promise a checkpointer: it states
// when one runs.
test("the first summary states when the checkpointer runs", async () => {
  const r = await run([], "orchestrator");
  const dl = all(r.body).find((e) => e.getAttribute("data-field") === "recommended-summary")!;
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
  // The route form takes the flow's form's place in the dialog: the test
  // reads the dialog.
  const dialog = document.createElement("dialog") as unknown as FakeElement;
  const body = document.createElement("form") as unknown as FakeElement;
  dialog.appendChild(body);
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
  click(body, "use-recommended");
  await flush();
  click(body, "use-previous");
  await flush();
  await flush();
  await flush();
  const offered = (name: string) =>
    options(byName(dialog, name))
      .map((o) => o.getAttribute("value"))
      .filter(Boolean);
  expect(chosen(byName(dialog, "main-model"))).toBe("fable-old");
  expect(offered("orch-model")).not.toContain("fable-old");
  expect(offered("orch-model")).toContain("newer");
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
  click(body, "use-recommended");
  await flush();
  await flush();
  expect(opened).toEqual([false]);
});

// Every role saved: the form opens without the lists, and asks for them
// once when the user wants to choose again.
test("a form from saved picks can ask for the lists once", async () => {
  const r = await run([pick("orchestrator")], "orchestrator");
  click(r.body, "use-recommended");
  await flush();
  click(r.body, "use-previous");
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
  click(body, "use-recommended");
  await flush();
  click(body, "use-previous");
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

// The questions' markup, as the h() version drew them into the form.
test("the questions before the form, as markup", async () => {
  const { FIRST_SUMMARY } = await import("./new-route-flow.ts");
  const r = await run([pick("orchestrator"), { ...pick("adjutant"), runner: "tmux" }], "orchestrator");
  // The summary lists the recommended setup: data the test cannot fix.
  const summary = FIRST_SUMMARY.map(([k, v]) => `<dt>${k}</dt><dd>${v}</dd>`).join("");
  expect(dump(r.body)).toBe(
    markup`
      <form>
        <h2>New route</h2>
        <p class="muted">Use the recommended setup?</p>
        <dl class="nr-summary" data-field="recommended-summary">${summary}</dl>
        <div class="dialog-actions"
        ><button class="btn ghost" name="custom-setup" type="button">Custom setup</button
        ><button class="btn primary" name="use-recommended" type="button">Use the recommended setup</button></div>
      </form>
    `,
  );
  click(r.body, "use-recommended");
  await flush();
  // Only the picks of routes run in Herdr are offered.
  expect(dump(r.body)).toBe(
    markup`
      <form>
        <h2>New route</h2>
        <p class="muted">Use your previous choice?</p>
        <ul class="nr-previous-list"><li>orchestrator (herdr): claude fable, effort medium</li></ul>
        <div class="dialog-actions"
        ><button class="btn ghost" name="choose-again" type="button">Choose again</button
        ><button class="btn primary" name="use-previous" type="button">Use previous choice</button></div>
      </form>
    `,
  );
  click(r.body, "choose-again");
  expect(dump(r.body)).toBe('<form><h2>New route</h2><p class="nr-loading">Reading the harness lists…</p></form>');
});
