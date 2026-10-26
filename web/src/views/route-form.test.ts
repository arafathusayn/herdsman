import { beforeAll, expect, test } from "bun:test";

import { FakeElement } from "../herdsman-blocks.testdom.ts";
import { all, byName, dispatch, find, installDom } from "../testdom.ts";
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
    {
      kind: "pi",
      label: "Pi",
      models: [],
      default_model: "",
      other_model: false,
      free_model: true,
      checkpointer: false,
      skill_prefix: "/",
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

async function mountForm(answer?: Promise<RouteResult>): Promise<Harness> {
  const { routeForm } = await import("./route-form.ts");
  const placeholder = document.createElement("form");
  const h: Partial<Harness> = { sent: [], reads: [], debounce: [] };
  const deps: Deps = {
    createRoute: (r) => {
      h.sent!.push(r);
      return answer ?? Promise.resolve({ ok: true, steps: [], tabs: [] } as RouteResult);
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
  const r = routeForm(placeholder, catalog, state, deps);
  return { ...(h as Harness), body: fake(r.el), form: r.form };
}

// The form is rendered from views, so its elements are the fake DOM's own
// (attributes, no dataset or classList): these helpers read attributes.
// A checked narrowing, not a cast: the fake document makes FakeElements.
const fake = (n: unknown): FakeElement => {
  if (!(n instanceof FakeElement)) throw new Error("not a fake element");
  return n;
};
const hasClass = (e: FakeElement | undefined, c: string): boolean => !!e?.className.split(" ").includes(c);
const valueOf = (e: FakeElement): unknown => e["value"];
const type = (el: FakeElement, v: string): void => {
  el["value"] = v;
  dispatch(el, "input");
};
const choose = (el: FakeElement, v: string): void => {
  el["value"] = v;
  dispatch(el, "change");
};
// A submit by the Start button.
const submit = (b: FakeElement): void => {
  dispatch(b, "submit", { submitter: byName(b, "start") });
};
const errorOf = (b: FakeElement, name: string): string | undefined =>
  find(b, (e) => e.getAttribute("data-error") === name)?.textContent;
const box = (b: FakeElement, name: string): FakeElement | undefined =>
  find(b, (e) => e.getAttribute("data-field") === name);
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
    .filter((e) => e.getAttribute("data-item") !== null)
    .map((e) => ({ text: e.textContent, done: hasClass(e, "done") }));

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
    expect(hasClass(byName(f.body, field), "invalid")).toBe(true);
    expect(hasClass(byName(f.body, field === "minutes" ? "limit" : "minutes"), "invalid")).toBe(false);
    expect(hasClass(box(f.body, "checkpointer"), "invalid")).toBe(true);
    // Typing elsewhere keeps the message; fixing the field clears it.
    type(byName(f.body, "request"), "go");
    expect(errorOf(f.body, field)).toBe(message);
    type(byName(f.body, field), field === "minutes" ? "15" : "250k");
    expect(errorOf(f.body, field)).toBeUndefined();
    expect(hasClass(box(f.body, "checkpointer"), "invalid")).toBe(false);
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
  expect(hasClass(box(f.body, "checkpointer"), "invalid")).toBe(true);
});

test("the orchestrator's harness follows the adjutant's until edited", async () => {
  const f = await mountForm();
  dispatch(byName(f.body, "role-adjutant"), "click");
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
  dispatch(byName(f.body, "role-adjutant"), "click");
  expect(f.form().orch).toEqual({ kind: "claude", model: "sonnet", other: "", effort: "high" });
});

test("the checklist follows the inbox and names as they are typed, and drops outdated replies", async () => {
  const f = await mountForm();
  dispatch(byName(f.body, "role-adjutant"), "click");
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
  dispatch(byName(f.body, "role-adjutant"), "click");
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

test("edits made while a route is being created do not change what the result and checklist describe", async () => {
  let finish: (r: RouteResult) => void = () => {};
  const answer = new Promise<RouteResult>((r) => {
    finish = r;
  });
  const f = await mountForm(answer);
  dispatch(byName(f.body, "role-adjutant"), "click");
  type(byName(f.body, "orchName"), "orch-2");
  type(byName(f.body, "inbox"), "~/inbox-a");
  submit(f.body);
  expect(f.sent[0]?.inbox).toBe("~/inbox-a");
  // An attempted edit during the launch is refused.
  type(byName(f.body, "inbox"), "~/inbox-b");
  type(byName(f.body, "name"), "adj-9");
  expect(f.form().inbox).toBe("~/inbox-a");
  expect(f.form().name).toBe("adj-1");
  finish({
    ok: true,
    steps: [],
    tabs: ["w1:t1", "w1:t2"],
    adjutant_pane: "w1:p1",
    orchestrator_pane: "w1:p2",
    attach_command: "/herdsman adjutant --name adj-1 --orchestrator w1:p2 --inbox /Users/me/inbox-a",
  });
  await flush();
  const it = items(f.body);
  expect(it[0]?.text).toContain("mkdir -m 700 -p ~/inbox-a");
  expect(it[2]?.text).toContain("--inbox /Users/me/inbox-a");
  expect(f.reads.at(-1)?.path).toBe("~/inbox-a");
  f.poll();
  expect([f.reads.at(-1)?.path, f.reads.at(-1)?.adj, f.reads.at(-1)?.orch]).toEqual(["~/inbox-a", "adj-1", "orch-2"]);
});

test("a typed model id the orchestrator inherits shows in its own input and is what is sent", async () => {
  const f = await mountForm();
  dispatch(byName(f.body, "role-adjutant"), "click");
  choose(byName(f.body, "main-model"), "__other__");
  type(byName(f.body, "main-other"), "claude-a");
  choose(byName(f.body, "main-effort"), "high"); // redraws the orchestrator's fields
  expect(valueOf(byName(f.body, "orch-other"))).toBe("claude-a");
  type(byName(f.body, "main-other"), "claude-b");
  expect(valueOf(byName(f.body, "orch-other"))).toBe("claude-b");
  type(byName(f.body, "orchName"), "orch-2");
  submit(f.body);
  expect([f.sent[0]?.model, f.sent[0]?.orch_model]).toEqual(["claude-b", "claude-b"]);
});

test("a free-text model the orchestrator inherits shows in its own input and is what is sent", async () => {
  const f = await mountForm();
  dispatch(byName(f.body, "role-adjutant"), "click");
  choose(byName(f.body, "main-kind"), "pi");
  type(byName(f.body, "main-model"), "m-a");
  expect(valueOf(byName(f.body, "orch-model"))).toBe("m-a");
  type(byName(f.body, "main-model"), "m-b");
  expect(valueOf(byName(f.body, "orch-model"))).toBe("m-b");
  // Once the orchestrator's own model is typed, it no longer follows.
  type(byName(f.body, "orch-model"), "own");
  type(byName(f.body, "main-model"), "m-c");
  expect(valueOf(byName(f.body, "orch-model"))).toBe("own");
  type(byName(f.body, "orchName"), "orch-2");
  submit(f.body);
  expect([f.sent[0]?.model, f.sent[0]?.orch_model]).toEqual(["m-c", "own"]);
});

test("typing keeps every control's node, and the inbox follows the folder until edited", async () => {
  const f = await mountForm();
  dispatch(byName(f.body, "role-adjutant"), "click");
  const names = ["folder", "name", "orchName", "routeName", "inbox", "request", "minutes", "limit", "main-kind"];
  const before = names.map((n) => byName(f.body, n));
  type(byName(f.body, "folder"), "/r/proj");
  type(byName(f.body, "request"), "do it");
  expect(names.map((n) => byName(f.body, n))).toEqual(before);
  for (const [i, n] of names.entries()) expect(byName(f.body, n)).toBe(before[i]!);
  expect(valueOf(byName(f.body, "folder"))).toBe("/r/proj");
  expect(valueOf(byName(f.body, "inbox"))).toBe(f.form().inbox);
  expect(f.form().inbox).toContain("proj");
  type(byName(f.body, "inbox"), "~/mine");
  type(byName(f.body, "folder"), "/r/other");
  expect(valueOf(byName(f.body, "inbox"))).toBe("~/mine");
  // Switching back to the orchestrator role keeps the fields both roles share.
  dispatch(byName(f.body, "role-orchestrator"), "click");
  expect(byName(f.body, "folder")).toBe(before[0]!);
  expect(byName(f.body, "name")).toBe(before[1]!);
  expect(byName(f.body, "request")).toBe(before[5]!);
  expect(() => byName(f.body, "inbox")).toThrow();
});

test("an edit made while the route starts is undone in the control", async () => {
  const f = await mountForm(new Promise<RouteResult>(() => {}));
  type(byName(f.body, "folder"), "/r/repo");
  submit(f.body);
  expect(f.sent.length).toBe(1);
  expect(byName(f.body, "start").getAttribute("disabled")).toBe("");
  type(byName(f.body, "request"), "late");
  expect(valueOf(byName(f.body, "request"))).toBe("");
  expect(f.form().request).toBe("");
});
