import { beforeAll, expect, test } from "bun:test";

import { FakeElement } from "../herdsman-blocks.testdom.ts";
import { all, byName, dispatch, dump, find, installDom, markup } from "../testdom.ts";
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

async function mountForm(answer?: Promise<RouteResult>, keepRecommended = false): Promise<Harness> {
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
  const body = fake(r.el);
  // These tests drive every field, so they start with the recommended setup off.
  if (!keepRecommended) {
    const rec = byName(body, "recommended");
    rec["checked"] = false;
    dispatch(rec, "change");
    dispatch(byName(body, "role-orchestrator"), "click");
  }
  return { ...(h as Harness), body, form: r.form };
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
  const names = [
    "folder",
    "name",
    "orchName",
    "routeName",
    "inbox",
    "request",
    "minutes",
    "limit",
    "main-kind",
    "recommended",
    "custom-Integration",
  ];
  const before = names.map((n) => byName(f.body, n));
  type(byName(f.body, "folder"), "/r/proj");
  type(byName(f.body, "request"), "do it");
  type(byName(f.body, "custom-Integration"), "one branch");
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

// #31's tests, on the view form: routeForm puts its own form element in the
// placeholder's place, so each test reads the returned element.
const disabled = (e: FakeElement): boolean => e.hasAttribute("disabled");
const options = (sel: FakeElement): FakeElement[] => all(sel).filter((e) => e.tagName.toLowerCase() === "option");
/** A select's chosen option value, as the view marks it. */
const picked = (sel: FakeElement): string | null | undefined =>
  options(sel)
    .find((o) => o["selected"] === true)
    ?.getAttribute("value");
const values = (sel: FakeElement): (string | null)[] => options(sel).map((o) => o.getAttribute("value"));
const bareDeps = {
  createRoute: () => new Promise<RouteResult>(() => {}),
  inboxState: () => new Promise<InboxState>(() => {}),
  state: () => state,
  copy: () => {},
  openPane: () => {},
  close: () => {},
  failed: () => {},
  every: () => () => {},
  later: () => () => {},
} as Deps;
// Loaded once the fake document is in place; formOf builds a form at once,
// before any probe's answer.
let routeForm: typeof import("./route-form.ts").routeForm;
beforeAll(async () => {
  ({ routeForm } = await import("./route-form.ts"));
});
function formOf(
  c: Catalog,
  deps: Deps,
  opts?: Parameters<typeof routeForm>[4],
): { body: FakeElement; r: ReturnType<typeof routeForm> } {
  const r = routeForm(document.createElement("form"), c, state, deps, opts);
  return { body: fake(r.el), r };
}
const recommendedOff = (body: FakeElement): void => {
  const rec = byName(body, "recommended");
  rec["checked"] = false;
  dispatch(rec, "change");
};

// A32: the recommended setup is on at first: its summary shows, the role and
// the checkpointer are folded, and the route it sends is the orchestrator's
// with the checkpointer on.
test("the recommended setup folds its fields into one summary", async () => {
  const f = await mountForm(undefined, true);
  expect(box(f.body, "recommended-summary")).toBeDefined();
  expect(find(f.body, (e) => e.getAttribute?.("name") === "role-adjutant")).toBeUndefined();
  expect(box(f.body, "checkpointer")).toBeUndefined();
  expect(f.form().recommended).toBe(true);
  // The recommended setup has an adjutant beside the orchestrator, with the default names.
  expect(f.form()).toMatchObject({ role: "adjutant", name: "adj-1", orchName: "orch-1" });
  recommendedOff(f.body);
  expect(box(f.body, "recommended-summary")).toBeUndefined();
  expect(byName(f.body, "role-adjutant")).toBeDefined();
  expect(box(f.body, "checkpointer")).toBeDefined();
});

// A32: the probed GitHub account fills the confirmation; the user may pick
// another account that can read the repository, and it goes with the request.
test("the probed GitHub account is offered for confirmation", async () => {
  const sent: RouteRequest[] = [];
  const asked: string[] = [];
  const { body } = formOf(catalog, {
    ...bareDeps,
    createRoute: (r) => {
      sent.push(r);
      return Promise.resolve({ ok: true, steps: [], tabs: [] } as RouteResult);
    },
    github: (folder) => {
      asked.push(folder);
      return Promise.resolve({
        repo: "o/r",
        accounts: [
          { name: "user-a", active: true, can_read: true },
          { name: "user-b", active: false, can_read: true },
        ],
        suggested: "user-a",
      });
    },
  });
  // Start waits for the probe.
  expect(disabled(byName(body, "start"))).toBe(true);
  expect(byName(body, "start").getAttribute("title")).toBe("Checking which GitHub account can read this repository…");
  submit(body);
  expect(sent.length).toBe(0);
  await flush();
  expect(asked.length).toBe(1);
  // The probed account is marked, not chosen for the user: Start waits for the pick.
  expect(picked(byName(body, "ghAccount"))).toBe("");
  expect(disabled(byName(body, "start"))).toBe(true);
  expect(byName(body, "start").getAttribute("title")).toBe("Confirm the GitHub account.");
  choose(byName(body, "ghAccount"), "user-b");
  expect(picked(byName(body, "ghAccount"))).toBe("user-b");
  expect(disabled(byName(body, "start"))).toBe(false);
  expect(byName(body, "start").hasAttribute("title")).toBe(false);
  // A new folder clears the account; its own probe runs.
  type(byName(body, "folder"), "/r/other");
  expect(asked).toEqual(["/r/repo", "/r/other"]);
  expect(disabled(byName(body, "start"))).toBe(true);
  await flush();
  expect(picked(byName(body, "ghAccount"))).toBe("");
});

test("a repository no account can read says how to log in", async () => {
  const { body } = formOf(catalog, {
    ...bareDeps,
    github: () => Promise.resolve({ repo: "o/r", accounts: [], suggested: "", error: "no origin remote" }),
  });
  await flush();
  expect(box(body, "ghAccount")?.textContent).toContain("no origin remote");
  expect(box(body, "ghAccount")?.textContent).toContain("Log in with gh auth login, then reopen this form.");
  // Nothing to confirm: Start is open.
  expect(disabled(byName(body, "start"))).toBe(false);
});

// A32: the custom path asks every setup item; the answers go in the setup.
test("the custom path asks each setup item and sends the answers", async () => {
  const f = await mountForm();
  const { SETUP_QUESTIONS } = await import("../new-route.ts");
  for (const [item] of SETUP_QUESTIONS) expect(byName(f.body, `custom-${item}`)).toBeDefined();
  type(byName(f.body, "custom-Integration"), "one combined branch");
  submit(f.body);
  const s = f.sent[0]?.setup;
  expect(s?.recommended).toBe(false);
  expect(s?.answers.find((a) => a.item === "Integration")?.answer).toBe("one combined branch");
  expect(s?.answers.find((a) => a.item === "Merge")?.answer).toBe("");
  expect(s?.roles[0]?.role).toBe("orchestrator");
});

// A30: "Yes" to the previous choice fills the roles from the saved picks at
// once; a role with no saved pick, or one the lists no longer offer, is
// asked again by name, never skipped in silence.
test("use previous choice asks again for a role without a usable pick", async () => {
  const pick = (role: string, harness: string, model: string, effort: string) => ({
    role,
    runner: "herdr",
    harness,
    model,
    effort,
    route: "",
    at: "",
  });
  // The recommended setup has an adjutant; only the orchestrator was saved.
  let { body, r } = formOf({ ...catalog, previous: [pick("orchestrator", "codex", "gpt-5.6-luna", "low")] }, bareDeps, {
    usePrevious: true,
  });
  expect(r.form().orch).toMatchObject({ kind: "codex", model: "gpt-5.6-luna", effort: "low" });
  expect(box(body, "main")?.textContent).toContain("No saved choice for the adjutant");
  // A saved model the lists no longer offer is asked again too.
  ({ body, r } = formOf(
    {
      ...catalog,
      previous: [pick("adjutant", "claude", "fable", "high"), pick("orchestrator", "pi", "typed", "high")],
    },
    bareDeps,
    { usePrevious: true },
  ));
  expect(r.form().main).toMatchObject({ kind: "claude", model: "fable", effort: "high" });
  expect(box(body, "orch")?.textContent).toContain("does not say how to pass its effort: pick again");
  expect(box(body, "main")?.textContent ?? "").not.toContain("pick");
});

// P2: the recommended summary shows the effective checkpointer: Off when
// the script is missing, as the request sends it.
test("the recommended summary says the checkpointer is off when unavailable", async () => {
  const { body } = formOf({ ...catalog, checkpointer_ok: false, checkpointer_why: "no checkpointer.sh" }, bareDeps);
  const dd = find(body, (e) => e.getAttribute("data-item") === "Checkpointer");
  expect(dd?.textContent).toBe("Off: no checkpointer.sh");
});

// review-3: the probed lists update the form in place; the draft stays.
test("setCatalog keeps the draft and each role's saved pick", async () => {
  const pick = {
    role: "orchestrator",
    runner: "herdr",
    harness: "claude",
    model: "fable",
    effort: "low",
    route: "",
    at: "",
  };
  const { body, r } = formOf({ ...catalog, previous: [pick] }, bareDeps);
  recommendedOff(body);
  dispatch(byName(body, "role-orchestrator"), "click");
  type(byName(body, "request"), "keep this task");
  type(byName(body, "folder"), "/r/kept");
  const request = byName(body, "request");
  const claude = catalog.kinds[0]!;
  r.setCatalog({
    ...catalog,
    kinds: [
      { ...claude, models: claude.models.map((m) => ({ ...m, efforts: ["medium", "high"] })) },
      ...catalog.kinds.slice(1),
    ],
  });
  expect(r.form().request).toBe("keep this task");
  expect(r.form().folder).toBe("/r/kept");
  // The new lists patch the form in place: the field being typed in stays.
  expect(byName(body, "request")).toBe(request);
  expect(valueOf(request)).toBe("keep this task");
  // Each role keeps its own saved pick exactly, whatever the new lists say.
  expect(r.form().main).toMatchObject({ kind: "claude", model: "fable" });
  expect(box(body, "main")?.textContent ?? "").not.toContain("pick again");
});

// A29 step 2: a harness listed through its own command shows that command
// as its model docs when it has no web page.
test("a kind listed through its own command shows that command as its model docs", async () => {
  const muse = {
    ...catalog.kinds[1]!,
    kind: "muse",
    label: "Muse",
    docs_command: "muse model-profile show <model>",
  };
  const { body } = formOf({ ...catalog, kinds: [muse] }, bareDeps);
  const docs = all(body).find((e) => e.getAttribute("data-docs") === "main");
  expect(docs?.textContent).toContain("muse model-profile show <model>");
});

// A harness with a web page of its models links to it; only a web address
// becomes a link.
test("a kind with a docs page links to it", async () => {
  const withPage = { ...catalog.kinds[0]!, docs_url: "https://example.com/models" };
  const { body } = formOf({ ...catalog, kinds: [withPage] }, bareDeps);
  const row = find(box(body, "main-model")!, (e) => hasClass(e, "nr-row"))!;
  expect(dump(find(row, (e) => e.tagName.toLowerCase() === "a")!)).toBe(
    '<a href="https://example.com/models" rel="noopener" target="_blank" title="Claude\'s own page of its models">Model docs</a>',
  );
  const odd = { ...catalog.kinds[0]!, docs_url: "javascript:alert(1)" };
  const other = formOf({ ...catalog, kinds: [odd] }, bareDeps);
  expect(find(other.body, (e) => e.tagName.toLowerCase() === "a")).toBeUndefined();
});

// A harness that could not be asked offers no models: the chip says so.
test("a harness whose models are unavailable says it starts with its own default", async () => {
  const off = { ...catalog.kinds[0]!, offline: true, unavailable: "claude did not answer" };
  const { body } = formOf({ ...catalog, kinds: [off] }, bareDeps);
  const chip = find(body, (e) => hasClass(e, "nr-chip"))!;
  expect(dump(chip)).toBe(
    '<span class="nr-chip" title="claude did not answer: it offers no models and starts with its own default.">models unavailable</span>',
  );
});

// A32: the answer to "Use the recommended setup?" carries into the form.
test("a custom setup answer opens the form with the recommended setup off", async () => {
  const off = formOf(catalog, bareDeps, { recommended: false });
  expect(byName(off.body, "recommended")["checked"]).toBe(false);
  const on = formOf(catalog, bareDeps, { recommended: true });
  expect(byName(on.body, "recommended")["checked"]).toBe(true);
});

// The real form: after "Use previous choice" with only the adjutant saved,
// choosing the orchestrator role does not offer the adjutant's model.
test("the orchestrator role is not offered the adjutant's saved model", async () => {
  const adjPick = {
    role: "adjutant",
    runner: "herdr",
    harness: "claude",
    model: "adj-only",
    effort: "low",
    route: "",
    at: "",
  };
  const { body, r } = formOf({ ...catalog, previous: [adjPick] }, bareDeps, {
    usePrevious: true,
    recommended: false,
  });
  expect(values(byName(body, "main-model"))).toContain("adj-only");
  dispatch(byName(body, "role-orchestrator"), "click");
  expect(values(byName(body, "main-model"))).not.toContain("adj-only");
  expect(r.form().main.model).not.toBe("adj-only");
});

// A form opened from the saved picks alone can still change them: it
// offers to read the harness lists, and asks for them itself when a role
// without a saved pick is chosen.
test("a form from saved picks alone asks for the lists when a fresh choice is needed", async () => {
  const orch = {
    role: "orchestrator",
    runner: "herdr",
    harness: "claude",
    model: "fable",
    effort: "medium",
    route: "",
    at: "",
  };
  const fromPicks: Catalog = {
    ...catalog,
    from_picks: true,
    kinds: [{ ...catalog.kinds[0]!, saved: true, models: catalog.kinds[0]!.models.slice(0, 1) }],
    previous: [orch],
  };
  let asked = 0;
  const { body, r } = formOf(fromPicks, bareDeps, {
    usePrevious: true,
    recommended: false,
    needLists: () => void asked++,
  });
  dispatch(byName(body, "role-orchestrator"), "click");
  expect(asked).toBe(0);
  dispatch(byName(body, "choose-lists"), "click");
  expect(asked).toBe(1);
  dispatch(byName(body, "role-adjutant"), "click");
  expect(asked).toBe(2);
  r.setCatalog({ ...catalog, previous: [orch] });
  expect(all(body).some((e) => e.getAttribute?.("name") === "choose-lists")).toBe(false);
});

// The form's new parts, as markup: the recommended switch and summary, and
// the custom path's role and setup questions with their hints.
test("the recommended switch, its summary and the custom questions", async () => {
  const { RECOMMENDED_SETUP, SETUP_QUESTIONS } = await import("../new-route.ts");
  const { body } = formOf(catalog, bareDeps);
  const fields = find(body, (e) => e.tagName.toLowerCase() === "fieldset")!;
  const [toggle, summary] = fields.childNodes.filter((n): n is FakeElement => n instanceof FakeElement);
  expect(dump(toggle!)).toBe(
    markup`
      <label class="nr-toggle nr-recommended"><input checked="" name="recommended" type="checkbox"></input
      ><span>Recommended setup</span
      ><small>Fills the usual choices below so only the tasks and each role's model are left to pick.</small></label>
    `,
  );
  // The summary lists the recommended setup as it is: data the test cannot fix.
  expect(dump(summary!)).toBe(
    `<dl class="nr-summary" data-field="recommended-summary">${RECOMMENDED_SETUP.map(
      ([k, v]) => `<dt>${k}</dt><dd data-item="${k}">${v}</dd>`,
    ).join("")}</dl>`,
  );
  recommendedOff(body);
  const custom = fields.childNodes.filter((n): n is FakeElement => n instanceof FakeElement)[1]!;
  const roles = find(custom, (e) => e.getAttribute("role") === "radiogroup")!;
  expect(dump(roles)).toBe(
    markup`
      <div aria-label="Role" class="nr-role" role="radiogroup"
      ><button aria-checked="false" name="role-orchestrator" role="radio" type="button">Orchestrator</button
      ><button aria-checked="true" class="active" name="role-adjutant" role="radio" type="button"
      >Adjutant (Recommended)</button></div>
    `,
  );
  const [first] = SETUP_QUESTIONS[0]!;
  const id = byName(body, `custom-${first}`).getAttribute("id");
  expect(dump(box(custom, `custom-${first}`)!)).toBe(
    markup`
      <div class="field wide" data-field="custom-${first}"
      ><label for="${id ?? ""}">${first}</label
      ><input autocomplete="off" id="${id ?? ""}" maxlength="400" name="custom-${first}" placeholder="${SETUP_QUESTIONS[0]![1]}"></input
      ><small>Empty: the route asks you.</small></div>
    `,
  );
  expect(find(box(body, "checkpointer")!, (e) => e.tagName.toLowerCase() === "span")?.textContent).toBe(
    "Checkpointer (Recommended)",
  );
});
