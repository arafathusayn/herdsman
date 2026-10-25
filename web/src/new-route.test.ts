import { expect, test } from "bun:test";

import {
  checkpointerAvailable,
  checkpointerOn,
  defaultEffort,
  effortsFor,
  formField,
  initialForm,
  kindOf,
  knownFolders,
  OTHER,
  parseLimit,
  pickHarness,
  preview,
  projectLabel,
  quoteArg,
  type RouteForm,
  setRole,
  skillWarnings,
  tilde,
  toRequest,
  update,
  validate,
} from "./new-route.ts";
import type { Catalog, State } from "./types.ts";

const efforts = ["low", "medium", "high", "xhigh", "max"];
const catalog: Catalog = {
  today: "2026-10-25",
  home: "/Users/me",
  checkpointer_ok: true,
  kinds: [
    {
      kind: "claude",
      label: "Claude",
      models: ["fable", "opus", "sonnet"].map((id) => ({ id, label: id, efforts, default_effort: "medium" })),
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
        { id: "gpt-6.1-sol", label: "GPT-6.1-Sol", efforts: ["low", "high"], default_effort: "high" },
        { id: "gpt-5.6-luna", label: "GPT-5.6-Luna", efforts, default_effort: "medium" },
      ],
      default_model: "gpt-6.1-sol",
      other_model: false,
      free_model: false,
      checkpointer: true,
      skill_prefix: "$",
      skill_installed: false,
      offline: true,
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
      skill_installed: false,
      offline: false,
    },
  ],
};

const state = {
  snapshot: {
    workspaces: [
      {
        workspace_id: "w2",
        label: "b",
        focused: false,
        worktree: { repo_root: "/r/other", checkout_path: "/r/other" },
      },
      {
        workspace_id: "w1",
        label: "a",
        focused: true,
        worktree: { repo_root: "/r/main", checkout_path: "/r/main-wt" },
      },
    ],
    panes: [{ cwd: "/r/main" }, { cwd: "/tmp/x" }, { cwd: null }, { cwd: "relative" }],
    agents: [{ name: "orch-1" }, { name: null }],
  },
} as unknown as State;

const fresh = (): RouteForm =>
  update(initialForm(catalog, null), { workspace: "w1", folder: "/r/herdsman", name: "orch-9" });

test("defaults let a quick start finish in a few clicks", () => {
  const f = initialForm(catalog, state);
  expect(f.role).toBe("orchestrator");
  expect(f.workspace).toBe("w1");
  expect(f.folder).toBe("/r/main-wt");
  expect(f.main).toEqual({ kind: "claude", model: "fable", other: "", effort: "medium" });
  expect([f.name, f.orchName, f.routeName]).toEqual(["orch-1", "orch-1", "route-2026-10-25"]);
  expect([f.checkpointer, f.minutes, f.limit]).toEqual([true, "15", "250k"]);
  expect(f.inbox).toBe("~/.herdsman-private/main-wt-route-2026-10-25");
  expect(initialForm({ ...catalog, checkpointer_ok: false }, null).checkpointer).toBe(false);
  expect(initialForm(catalog, null).workspace).toBe("");
});

test("known folders put the focused workspace's checkout first, then pane folders", () => {
  expect(knownFolders(state)).toEqual(["/r/main-wt", "/r/main", "/r/other", "/tmp/x"]);
  expect(knownFolders(null)).toEqual([]);
});

test("project labels follow the server's cut", () => {
  expect(projectLabel("/x/herdsman")).toBe("herdsman");
  expect(projectLabel("/x/My_Repo.git/")).toBe("my-repo-git");
  expect(projectLabel("/x/a-very-long-repository-name-here")).toBe("a-very-long-repository-n");
  expect(projectLabel("/x/___")).toBe("");
});

test("models and efforts come from the harness's own list", () => {
  const cx = kindOf(catalog, "codex");
  expect(effortsFor(cx, "gpt-6.1-sol")).toEqual(["low", "high"]);
  expect(defaultEffort(cx, "gpt-6.1-sol")).toBe("high");
  expect(defaultEffort(cx, "gpt-5.6-luna")).toBe("medium");
  expect(effortsFor(kindOf(catalog, "claude"), OTHER)).toEqual(efforts);
  expect(effortsFor(cx, OTHER)).toEqual([]);
  expect(effortsFor(kindOf(catalog, "pi"), "x")).toEqual([]);
  expect(defaultEffort(undefined, "x")).toBe("");
  const h = pickHarness(catalog, fresh().main, { kind: "codex" });
  expect(h).toEqual({ kind: "codex", model: "gpt-6.1-sol", other: "", effort: "high" });
  expect(pickHarness(catalog, h, { model: "gpt-5.6-luna" }).effort).toBe("high");
  expect(pickHarness(catalog, { ...h, effort: "low" }, { model: "gpt-5.6-luna" }).effort).toBe("low");
  expect(pickHarness(catalog, h, { kind: "pi" })).toEqual({ kind: "pi", model: "", other: "", effort: "" });
});

test("the role switch keeps typed fields and swaps untouched default names", () => {
  const f = { ...initialForm(catalog, state), request: "keep me" };
  const a = setRole(f, "adjutant");
  expect([a.role, a.name, a.request]).toEqual(["adjutant", "adj-1", "keep me"]);
  expect(setRole(a, "orchestrator").name).toBe("orch-1");
  expect(setRole({ ...f, name: "boss" }, "adjutant").name).toBe("boss");
  expect(setRole(f, "orchestrator")).toBe(f);
});

test("the inbox follows the folder and route name until the user edits it", () => {
  let f = update(fresh(), { folder: "/r/web" });
  expect(f.inbox).toBe("~/.herdsman-private/web-route-2026-10-25");
  f = update(f, { routeName: "route-x" });
  expect(f.inbox).toBe("~/.herdsman-private/web-route-x");
  f = update(f, { inbox: "~/mine" });
  f = update(f, { folder: "/r/other" });
  expect([f.inbox, f.inboxEdited]).toEqual(["~/mine", true]);
  expect(update(fresh(), { folder: "/" }).inbox).toBe("~/.herdsman-private/project-route-2026-10-25");
});

test("limits read 250k, 1m or a number", () => {
  expect(parseLimit("250k")).toBe(250000);
  expect(parseLimit("1m")).toBe(1000000);
  expect(parseLimit("1.5M")).toBe(1500000);
  expect(parseLimit(" 300000 ")).toBe(300000);
  expect(parseLimit("lots")).toBeNull();
  expect(parseLimit("")).toBeNull();
});

test("the preview shows the exact text sent, with each harness's skill prefix", () => {
  const f = { ...fresh(), request: 'fix "auth" \\ now' };
  expect(preview(f, catalog).command).toBe('/herdsman run "fix \\"auth\\" \\\\ now"');
  expect(preview({ ...f, request: "" }, catalog).command).toBe("/herdsman run");
  const cx = { ...f, main: pickHarness(catalog, f.main, { kind: "codex" }) };
  expect(preview(cx, catalog).command.startsWith("$herdsman run ")).toBe(true);
  const a = {
    ...setRole({ ...f, name: "orch-1" }, "adjutant"),
    request: "go",
    orch: pickHarness(catalog, f.orch, { kind: "codex" }),
  };
  const p = preview(a, catalog);
  expect(p.command).toBe(
    '$herdsman orchestrator --adjutant adj-1 --name orch-1 --inbox ~/.herdsman-private/herdsman-route-2026-10-25 "go"',
  );
  expect(p.attach).toBe(
    "/herdsman adjutant --name adj-1 --orchestrator <orchestrator pane> --inbox ~/.herdsman-private/herdsman-route-2026-10-25",
  );
  expect(quoteArg("a\nb")).toBe('"a\nb"');
  expect(preview({ ...f, main: { ...f.main, kind: "gone" } }, catalog).command.startsWith("/herdsman")).toBe(true);
});

test("a valid form has no messages", () => {
  expect(validate(fresh(), catalog, state)).toEqual({});
  const a = setRole(fresh(), "adjutant");
  expect(validate({ ...a, orchName: "orch-2" }, catalog, state)).toEqual({});
});

test("validation messages name each problem", () => {
  const v = (change: Partial<RouteForm>) => validate({ ...fresh(), ...change }, catalog, state);
  expect(v({ workspace: "" })["workspace"]).toBe("Pick a workspace.");
  expect(v({ folder: "rel" })["folder"]).toBe("Type an absolute folder path.");
  expect(v({ folder: "/x/___" })["folder"]).toContain("letters or digits");
  expect(v({ name: "Orch" })["name"]).toContain("Start with a letter");
  expect(v({ name: "orch-1" })["name"]).toBe("An agent named orch-1 is already running.");
  expect(v({ main: { kind: "nope", model: "", other: "", effort: "" } })["main"]).toBe("Pick a harness.");
  expect(v({ main: { kind: "claude", model: "fable-9", other: "", effort: "low" } })["main"]).toBe(
    "Pick a model from Claude's list.",
  );
  expect(v({ main: { kind: "claude", model: "fable", other: "", effort: "ultra" } })["main"]).toBe(
    "Pick an effort from the list.",
  );
  expect(v({ main: { kind: "claude", model: OTHER, other: "bad id", effort: "low" } })["main"]).toContain("model id");
  expect(v({ main: { kind: "claude", model: OTHER, other: "claude-opus-5-5[1m]", effort: "low" } })["main"]).toBe(
    undefined,
  );
  expect(v({ main: { kind: "pi", model: "x y", other: "", effort: "" } })["main"]).toContain("model id");
  expect(v({ main: { kind: "pi", model: "", other: "", effort: "" } })["main"]).toBe(undefined);
  expect(v({ request: "a\u001bb" })["request"]).toContain("control character");
  expect(v({ request: "a\nb\tc" })["request"]).toBe(undefined);
  expect(v({ request: "x".repeat(8001) })["request"]).toBe("At most 8000 characters.");
  expect(v({ minutes: "4" })["minutes"]).toBe("5 to 240 minutes.");
  expect(v({ minutes: "1.5" })["minutes"]).toBe("5 to 240 minutes.");
  expect(v({ limit: "3m" })["limit"]).toContain("50k to 2m");
  expect(v({ limit: "x" })["limit"]).toContain("50k to 2m");
  expect(v({ checkpointer: false, limit: "x" })["limit"]).toBe(undefined);
  const a = (change: Partial<RouteForm>) => validate({ ...setRole(fresh(), "adjutant"), ...change }, catalog, state);
  expect(a({})["orchName"]).toBe("An agent named orch-1 is already running.");
  expect(a({ orchName: "orch-9" })["orchName"]).toBe("The orchestrator needs a name of its own.");
  expect(a({ orchName: "o2", routeName: "Route" })["routeName"]).toContain("a-z");
  expect(a({ orchName: "o2", inbox: "/tmp/x" })["inbox"]).toContain("inside your home");
  expect(a({ orchName: "o2", inbox: "~/../x" })["inbox"]).toContain("inside your home");
  expect(a({ orchName: "o2", inbox: "/Users/me/in box" })["inbox"]).toContain("inside your home");
  expect(a({ orchName: "o2", inbox: "/Users/me/inbox" })["inbox"]).toBe(undefined);
  expect(a({ orchName: "o2", orch: { kind: "codex", model: "gpt-x", other: "", effort: "low" } })["orch"]).toBe(
    "Pick a model from Codex's list.",
  );
});

test("the checkpointer runs for Claude and Codex coordinators only, and says why not", () => {
  const f = fresh();
  expect(checkpointerAvailable(f, catalog)).toEqual({ ok: true, why: "" });
  const pi = { ...f, main: pickHarness(catalog, f.main, { kind: "pi" }) };
  expect(checkpointerAvailable(pi, catalog).why).toBe("the checkpointer reads Claude Code and Codex transcripts only");
  expect(checkpointerOn(pi, catalog)).toBe(false);
  expect(toRequest(pi, catalog).checkpointer.on).toBe(false);
  const missing = { ...catalog, checkpointer_ok: false, checkpointer_why: "no script" };
  expect(checkpointerAvailable(f, missing)).toEqual({ ok: false, why: "no script" });
  expect(checkpointerAvailable(f, { ...catalog, checkpointer_ok: false }).why).toContain("not found");
  // On an adjutant route the checkpointer follows the orchestrator's harness.
  const a = { ...setRole(f, "adjutant"), main: pickHarness(catalog, f.main, { kind: "pi" }) };
  expect(checkpointerOn(a, catalog)).toBe(true);
});

test("a harness without the herdsman skill warns without blocking", () => {
  expect(skillWarnings(fresh(), catalog)).toEqual([]);
  const a = { ...setRole(fresh(), "adjutant"), orch: pickHarness(catalog, fresh().orch, { kind: "codex" }) };
  expect(skillWarnings(a, catalog)).toEqual([
    "herdsman skill not found for codex: the route command will not run until it is installed",
  ]);
  expect(validate({ ...a, orchName: "o2" }, catalog, state)).toEqual({});
});

test("the request body carries the form's values", () => {
  const f = { ...fresh(), main: { kind: "claude", model: OTHER, other: " claude-x ", effort: "low" }, limit: "1m" };
  expect(toRequest(f, catalog)).toEqual({
    role: "orchestrator",
    workspace_id: "w1",
    folder: "/r/herdsman",
    kind: "claude",
    model: "claude-x",
    effort: "low",
    name: "orch-9",
    request: "",
    checkpointer: { on: true, minutes: 15, limit: 1000000 },
  });
  const a = toRequest({ ...setRole(fresh(), "adjutant"), orchName: "o2" }, catalog);
  expect([a.orch_name, a.orch_kind, a.orch_model, a.orch_effort, a.route_name, a.inbox]).toEqual([
    "o2",
    "claude",
    "fable",
    "medium",
    "route-2026-10-25",
    "~/.herdsman-private/herdsman-route-2026-10-25",
  ]);
  expect(toRequest({ ...fresh(), limit: "x" }, catalog).checkpointer.limit).toBe(0);
});

test("server fields map to form fields, and home paths show ~", () => {
  expect(formField("workspace_id")).toBe("workspace");
  expect(formField("orch_model")).toBe("orch");
  expect(formField("folder")).toBe("folder");
  expect(formField(undefined)).toBe("");
  expect(tilde("/Users/me/x", "/Users/me")).toBe("~/x");
  expect(tilde("/Users/meme", "/Users/me")).toBe("/Users/meme");
  expect(tilde("/x", "")).toBe("/x");
});
