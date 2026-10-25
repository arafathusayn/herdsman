import { beforeAll, expect, test } from "bun:test";

import { all, byName, type FakeElement, find, installDom } from "../testdom.ts";
import type { Catalog, InboxState, RouteRequest, RouteResult, State } from "../types.ts";
import type { Deps } from "./route-form.ts";

beforeAll(installDom);

const efforts = ["low", "medium", "high"];
const catalog: Catalog = {
  today: "2026-10-25",
  home: "/Users/me",
  checkpointer_ok: true,
  kinds: [
    {
      kind: "claude",
      label: "Claude",
      models: ["fable", "sonnet"].map((id) => ({ id, label: id, efforts, default_effort: "medium" })),
      default_model: "fable",
      other_model: true,
      free_model: false,
      checkpointer: true,
      skill_prefix: "/",
      skill_installed: true,
      offline: false,
    },
    {
      kind: "codex",
      label: "Codex",
      models: [
        { id: "gpt-5.6-luna", label: "GPT-5.6-Luna", efforts: ["low", "medium", "xhigh"], default_effort: "medium" },
      ],
      default_model: "gpt-5.6-luna",
      other_model: false,
      free_model: false,
      checkpointer: true,
      skill_prefix: "$",
      skill_installed: true,
      offline: false,
    },
  ],
};

const state = {
  snapshot: {
    workspaces: [{ workspace_id: "w1", label: "a", focused: true, number: 1 }],
    panes: [{ cwd: "/r/repo" }],
    agents: [],
  },
} as unknown as State;

interface Harness {
  body: FakeElement;
  form: () => import("../new-route.ts").RouteForm;
  sent: RouteRequest[];
  reads: { path: string; adj: string; orch: string; resolve(s: InboxState): void }[];
  poll: () => void;
  debounce: (() => void)[];
}

async function mountForm(): Promise<Harness> {
  const { routeForm } = await import("./route-form.ts");
  const body = document.createElement("form") as unknown as FakeElement;
  const h: Partial<Harness> = { sent: [], reads: [], debounce: [] };
  const deps: Deps = {
    createRoute: (r) => {
      h.sent!.push(r);
      return Promise.resolve({ ok: true, steps: [], tabs: [] } as RouteResult);
    },
    inboxState: (path, adj, orch) =>
      new Promise((resolve) => {
        h.reads!.push({ path, adj, orch, resolve });
      }),
    state: () => state,
    copy: () => {},
    openPane: () => {},
    close: () => {},
    failed: () => {},
    every: (_ms, fn) => {
      h.poll = fn;
      return () => {};
    },
    later: (_ms, fn) => {
      h.debounce!.push(fn);
      return () => {};
    },
  };
  const r = routeForm(body as unknown as HTMLElement, catalog, state, deps);
  return { ...(h as Harness), body, form: r.form };
}

const type = (el: FakeElement, v: string): void => {
  el.value = v;
  el.dispatch("input");
};
const choose = (el: FakeElement, v: string): void => {
  el.value = v;
  el.dispatch("change");
};
const submit = (b: FakeElement): void => {
  b.dispatch("submit", { submitter: byName(b, "start") });
};
const errorOf = (b: FakeElement, name: string): string | undefined =>
  find(b, (e) => e.dataset["error"] === name)?.textContent;
const box = (b: FakeElement, name: string): FakeElement | undefined => find(b, (e) => e.dataset["field"] === name);
const flush = () => new Promise((r) => setTimeout(r, 0));
const inbox = (over: Partial<InboxState> = {}): InboxState => ({
  path: "/Users/me/x",
  exists: true,
  private: true,
  setup: false,
  proposed: [],
  approved: [],
  pair_proposed: false,
  pair_approved: false,
  ...over,
});
const items = (b: FakeElement) =>
  all(b)
    .filter((e) => e.dataset["item"] !== undefined)
    .map((e) => ({ text: e.textContent, done: e.classList.contains("done") }));

test("each checkpointer bound refusal shows its own message and marks the group", async () => {
  for (const [field, value, message] of [
    ["minutes", "4", "5 to 240 minutes."],
    ["minutes", "241", "5 to 240 minutes."],
    ["limit", "9m", "50k to 2m tokens (for example 250k)."],
    ["limit", "10k", "50k to 2m tokens (for example 250k)."],
  ] as const) {
    const f = await mountForm();
    type(byName(f.body, field), value);
    submit(f.body);
    expect(f.sent).toEqual([]);
    expect(errorOf(f.body, field)).toBe(message);
    expect(box(f.body, "checkpointer")?.classList.contains("invalid")).toBe(true);
    // Typing elsewhere keeps the message; fixing the field clears it.
    type(byName(f.body, "request"), "go");
    expect(errorOf(f.body, field)).toBe(message);
    type(byName(f.body, field), field === "minutes" ? "15" : "250k");
    expect(errorOf(f.body, field)).toBeUndefined();
    expect(box(f.body, "checkpointer")?.classList.contains("invalid")).toBe(false);
  }
});

test("both checkpointer messages show together, and a fixed one leaves the other", async () => {
  const f = await mountForm();
  type(byName(f.body, "minutes"), "1");
  type(byName(f.body, "limit"), "x");
  submit(f.body);
  expect(errorOf(f.body, "minutes")).toBe("5 to 240 minutes.");
  expect(errorOf(f.body, "limit")).toContain("50k to 2m");
  type(byName(f.body, "minutes"), "15");
  expect(errorOf(f.body, "limit")).toContain("50k to 2m");
  expect(box(f.body, "checkpointer")?.classList.contains("invalid")).toBe(true);
});

test("the orchestrator's harness follows the adjutant's until edited", async () => {
  const f = await mountForm();
  byName(f.body, "role-adjutant").dispatch("click");
  choose(byName(f.body, "main-kind"), "codex");
  expect(f.form().orch).toEqual({ kind: "codex", model: "gpt-5.6-luna", other: "", effort: "medium" });
  choose(byName(f.body, "main-effort"), "xhigh");
  expect(f.form().orch.effort).toBe("xhigh");
  choose(byName(f.body, "orch-effort"), "low");
  choose(byName(f.body, "main-kind"), "claude");
  expect(f.form().orch).toEqual({ kind: "codex", model: "gpt-5.6-luna", other: "", effort: "low" });
  type(byName(f.body, "orchName"), "orch-2");
  type(byName(f.body, "folder"), "/r/repo");
  submit(f.body);
  expect(f.sent[0]?.orch_kind).toBe("codex");
  expect(f.sent[0]?.kind).toBe("claude");
});

test("a choice made before the role switch carries to the orchestrator", async () => {
  const f = await mountForm();
  choose(byName(f.body, "main-model"), "sonnet");
  choose(byName(f.body, "main-effort"), "high");
  byName(f.body, "role-adjutant").dispatch("click");
  expect(f.form().orch).toEqual({ kind: "claude", model: "sonnet", other: "", effort: "high" });
});

test("the checklist follows the inbox and names as they are typed, and drops outdated replies", async () => {
  const f = await mountForm();
  byName(f.body, "role-adjutant").dispatch("click");
  const a = f.reads.at(-1)!;
  type(byName(f.body, "inbox"), "~/inbox-b");
  // The commands change at once, before any reread.
  expect(items(f.body)[0]?.text).toContain("mkdir -m 700 -p ~/inbox-b");
  expect(items(f.body)[2]?.text).toContain("--inbox ~/inbox-b");
  type(byName(f.body, "name"), "adj-7");
  expect(items(f.body)[2]?.text).toContain("--name adj-7");
  f.debounce.at(-1)!();
  const b = f.reads.at(-1)!;
  expect([b.path, b.adj, b.orch]).toEqual(["~/inbox-b", "adj-7", "orch-1"]);
  b.resolve(inbox({ setup: true }));
  await flush();
  a.resolve(inbox({ setup: false, private: false })); // late reply for the old inbox
  await flush();
  expect(items(f.body)[1]?.done).toBe(true);
  expect(items(f.body)[0]?.done).toBe(true);
  // Human steps show up on the next poll.
  f.poll();
  f.reads.at(-1)!.resolve(inbox({ setup: true, pair_proposed: true, pair_approved: true }));
  await flush();
  expect(items(f.body).map((i) => i.done)).toEqual([true, true, false, true, true]);
});

test("an approval that is not current for this pair is not shown as done", async () => {
  const f = await mountForm();
  byName(f.body, "role-adjutant").dispatch("click");
  f.reads.at(-1)!.resolve(
    inbox({
      approved: ["adj-1 (adjutant)", "orch-1 (orchestrator)"],
      proposed: ["adj-1 (adjutant)"],
      pair_approved: false,
    }),
  );
  await flush();
  const approve = items(f.body)[4]!;
  expect(approve.done).toBe(false);
  expect(approve.text).toContain("you do this");
});
