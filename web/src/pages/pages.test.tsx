import { beforeEach, expect, test } from "bun:test";

import { bindHost } from "../sdk/index.ts";
import { type FakeElement, type FakeNode, installFakeDocument } from "../test-dom-core.ts";
import { mount } from "../test-mount.ts";
import { dispatch } from "../testdom.ts";

const g = globalThis as unknown as Record<string, unknown>;
g["location"] ??= { search: "", pathname: "/" };
g["window"] ??= { addEventListener: () => {} };
g["history"] ??= { pushState: () => {}, replaceState: () => {} };
g["requestAnimationFrame"] ??= () => 0;

const { pageById, pages } = await import("./index.tsx");
const { launchRequest } = await import("./new-route/new-route.tsx");

beforeEach(installFakeDocument);

type Reply = unknown | { status: number; body: unknown };
const SERVICE = "/rpc/herdsman.";

/** Serves SDK calls from a table of method paths; the rest are unimplemented. */
function daemon(replies: Record<string, Reply>) {
  const calls: { path: string; body: unknown }[] = [];
  bindHost({ url: "http://host", device: "d" }, (url, init) => {
    const path = url.slice("http://host".length + SERVICE.length);
    calls.push({ path, body: JSON.parse(String(init.body)) });
    const reply = replies[path];
    if (reply === undefined) {
      return Promise.resolve(Response.json({ code: "unimplemented", message: "no" }, { status: 501 }));
    }
    const failure = reply as { status?: number; body?: unknown };
    return Promise.resolve(Response.json(failure.status ? failure.body : reply, { status: failure.status ?? 200 }));
  });
  return calls;
}

const settle = async () => {
  for (let i = 0; i < 6; i++) await new Promise((resolve) => setTimeout(resolve, 0));
};
const text = (node: FakeNode): string =>
  node.nodeType === 3 ? ((node as unknown as { data: string }).data ?? "") : node.childNodes.map(text).join("");
function all(node: FakeNode, tag: string): FakeElement[] {
  const found: FakeElement[] = [];
  for (const child of node.childNodes) {
    if (child.nodeType !== 1) continue;
    const element = child as FakeElement;
    if (element.tagName.toLowerCase() === tag) found.push(element);
    found.push(...all(element, tag));
  }
  return found;
}
async function render(id: string): Promise<FakeElement> {
  const page = pageById(id);
  if (!page) throw new Error(`no page ${id}`);
  const root = mount(() => page.view());
  await settle();
  return root;
}
function fill(field: FakeElement, value: string): void {
  (field as unknown as { value: string }).value = value;
  dispatch(field, "input");
}

test("every page has an id and a label", () => {
  expect(pages.map((page) => page.id)).toEqual([
    "projects",
    "tasks",
    "route",
    "worktrees",
    "suggestions",
    "cost",
    "traces",
    "new-route",
  ]);
  expect(pageById("nope")).toBeUndefined();
});

test.each([
  ["projects", "The project list"],
  ["tasks", "The task list"],
  ["route", "The route list"],
  ["suggestions", "The suggestion catalog"],
])("%s says so when the daemon does not serve its method", async (id, what) => {
  daemon({});
  expect(text(await render(id))).toContain(`${what} is not served by this daemon yet.`);
});

test("a failed read shows the reason and can be retried", async () => {
  const replies: Record<string, Reply> = {
    "daemon.v1.ProjectService/List": { status: 401, body: { code: "unauthenticated", message: "x" } },
  };
  daemon(replies);
  const root = await render("projects");
  expect(text(root)).toContain("not paired");
  replies["daemon.v1.ProjectService/List"] = { projects: [{ name: "alpha" }] };
  dispatch(all(root, "button")[0]!, "click");
  await settle();
  expect(text(root)).toContain("alpha");
});

test("projects lists and creates", async () => {
  const calls = daemon({
    "daemon.v1.ProjectService/List": {
      projects: [{ id: { value: "p1" }, name: "alpha", description: "first", openTasks: 2, repos: [{ name: "api" }] }],
    },
    "daemon.v1.ProjectService/Create": { project: { name: "beta" } },
  });
  const root = await render("projects");
  expect(text(root)).toContain("alpha");
  expect(text(root)).toContain("2 open");
  expect(text(root)).toContain("api");
  fill(all(root, "input")[0]!, " beta ");
  dispatch(all(root, "form")[0]!, "submit");
  await settle();
  expect(calls.find((call) => call.path.endsWith("/Create"))?.body).toEqual({ name: "beta", description: "" });
  expect(calls.filter((call) => call.path.endsWith("/List")).length).toBe(2);
});

test("projects shows a refused create", async () => {
  daemon({
    "daemon.v1.ProjectService/List": { projects: [] },
    "daemon.v1.ProjectService/Create": { status: 409, body: { code: "already_exists", message: "taken" } },
  });
  const root = await render("projects");
  expect(text(root)).toContain("No projects yet.");
  fill(all(root, "input")[0]!, "beta");
  dispatch(all(root, "form")[0]!, "submit");
  await settle();
  expect(text(root)).toContain("taken");
});

test("tasks lists every project by default and updates a status", async () => {
  const calls = daemon({
    "daemon.v1.ProjectService/List": { projects: [{ name: "alpha" }] },
    "daemon.v1.TaskService/List": {
      tasks: [{ id: { value: "t1" }, project: { value: "alpha" }, slug: "one", title: "First", status: "todo" }],
    },
    "daemon.v1.TaskService/Update": {},
  });
  const root = await render("tasks");
  expect(calls.find((call) => call.path.endsWith("TaskService/List"))?.body).toEqual({ all: true });
  expect(text(root)).toContain("First");
  const status = all(root, "select")[1]!;
  (status as unknown as { value: string }).value = "doing";
  dispatch(status, "change");
  await settle();
  expect(calls.find((call) => call.path.endsWith("/Update"))?.body).toEqual({
    scope: { project: "alpha" },
    task: "one",
    status: "doing",
  });
});

test("tasks narrows to a project and adds to it", async () => {
  const calls = daemon({
    "daemon.v1.ProjectService/List": { projects: [{ name: "alpha" }] },
    "daemon.v1.TaskService/List": { tasks: [] },
    "daemon.v1.TaskService/Create": {},
  });
  const root = await render("tasks");
  const project = all(root, "select")[0]!;
  (project as unknown as { value: string }).value = "alpha";
  dispatch(project, "change");
  await settle();
  expect(calls.filter((call) => call.path.endsWith("TaskService/List")).at(-1)?.body).toEqual({
    scope: { project: "alpha" },
  });
  fill(all(root, "input")[0]!, "Second");
  dispatch(all(root, "form")[0]!, "submit");
  await settle();
  expect(calls.find((call) => call.path.endsWith("/Create"))?.body).toEqual({
    scope: { project: "alpha" },
    title: "Second",
  });
});

test("suggestions searches, opens the entry's Markdown and points apply at a terminal", async () => {
  const calls = daemon({
    "suggest.v1.SuggestService/Search": {
      items: [{ entry: { id: "s1", title: "Linter", kind: "tool" }, reasons: [{ code: "ecosystem" }] }],
    },
    "suggest.v1.SuggestService/Get": { entry: { id: "s1", body: "# Heading" } },
  });
  const root = await render("suggestions");
  expect(text(root)).toContain("Linter");
  expect(text(root)).toContain("ecosystem");
  fill(all(root, "input")[0]!, " lint ");
  dispatch(all(root, "form")[0]!, "submit");
  await settle();
  expect(calls.filter((call) => call.path.endsWith("/Search")).at(-1)?.body).toEqual({ text: "lint" });
  dispatch(
    all(root, "button").find((button) => text(button).includes("Linter"))!,
    "click",
  );
  await settle();
  expect(text(root)).toContain("Heading");
  expect(text(root)).toContain("herdsman suggest apply s1");
});

test("suggestions adds an entry", async () => {
  const calls = daemon({
    "suggest.v1.SuggestService/Search": { items: [] },
    "suggest.v1.SuggestService/Add": { id: "new" },
  });
  const root = await render("suggestions");
  dispatch(
    all(root, "button").find((button) => text(button) === "Add an entry")!,
    "click",
  );
  await settle();
  const form = all(root, "form")[1]!;
  fill(all(form, "input")[0]!, "Formatter");
  fill(all(form, "textarea")[0]!, "notes");
  dispatch(form, "submit");
  await settle();
  expect(calls.find((call) => call.path.endsWith("/Add"))?.body).toEqual({
    kind: "tool",
    title: "Formatter",
    body: "notes",
  });
});

test("route starts lists each start with its steps and names the missing controls", async () => {
  daemon({
    "daemon.v1.RouteService/List": {
      starts: [
        {
          operation: "o1",
          route: { value: "auth" },
          state: "halted",
          reason: "no herdr",
          steps: [{ ordinal: 1, name: "doctor", state: "done" }],
        },
      ],
    },
  });
  const root = await render("route");
  for (const expected of ["auth", "halted", "no herdr", "doctor", "Pausing, resuming, stopping and deleting a route"]) {
    expect(text(root)).toContain(expected);
  }
});

test("worktrees reads the ledger and its plan, and names the missing actions", async () => {
  const calls = daemon({
    "daemon.v1.LedgerService/Resources": {
      records: [{ kind: "process", id: "42", command: "bun dev", adopted: true }],
      plan: { lines: [{ kind: "process", id: "42", class: "owned", action: "signal" }] },
    },
  });
  const root = await render("worktrees");
  expect(text(root)).toContain("Queueing teardown, clean, down and undo is not served");
  fill(all(root, "input")[0]!, "/r/.git");
  fill(all(root, "input")[1]!, "fix");
  dispatch(all(root, "form")[0]!, "submit");
  await settle();
  expect(calls[0]?.body).toEqual({ commonDir: "/r/.git", task: "fix" });
  for (const expected of ["bun dev", "adopted", "owned", "signal"]) expect(text(root)).toContain(expected);
});

test("worktrees shows a failed read", async () => {
  daemon({
    "daemon.v1.LedgerService/Resources": { status: 404, body: { code: "not_found", message: "no such task" } },
  });
  const root = await render("worktrees");
  fill(all(root, "input")[0]!, "/r/.git");
  fill(all(root, "input")[1]!, "fix");
  dispatch(all(root, "form")[0]!, "submit");
  await settle();
  expect(text(root)).toContain("no such task");
});

test.each([
  ["cost", "Cost reporting is not served", "herdsman cost"],
  ["traces", "Trace reading is not served", "herdsman trace"],
])("%s renders its notice without calling anything", async (id, notice, terminal) => {
  const calls = daemon({});
  const root = await render(id);
  expect(text(root)).toContain(notice);
  expect(text(root)).toContain(terminal);
  expect(calls).toEqual([]);
});

test("launchRequest trims and leaves out the adjutant when there is none", () => {
  const fields = {
    route: " r ",
    repo: " /x ",
    prompt: " go ",
    adjutant: " a ",
    orchestrator: " o ",
    noAdjutant: false,
  };
  expect(launchRequest(fields)).toEqual({
    route: { value: "r" },
    repo: "/x",
    prompt: " go ",
    orchestratorCommand: "o",
    adjutantCommand: "a",
  });
  expect(launchRequest({ ...fields, noAdjutant: true })).toEqual({
    route: { value: "r" },
    repo: "/x",
    prompt: " go ",
    orchestratorCommand: "o",
    noAdjutant: true,
  });
});

test("new route previews before it can start, and an edit asks for a new preview", async () => {
  const calls = daemon({
    "daemon.v1.RouteService/Up": { start: { route: { value: "r" }, state: "planned", plan: "step one", folder: "/f" } },
  });
  const root = await render("new-route");
  const start = () => all(root, "button").find((button) => text(button) === "Start route")!;
  const disabled = (element: FakeElement) => (element as unknown as { disabled: boolean }).disabled;
  expect(disabled(start())).toBe(true);
  const inputs = all(root, "input").filter((input) => input.getAttribute("type") !== "checkbox");
  ["r", "/x", "orch", "adj"].forEach((value, i) => fill(inputs[i]!, value));
  dispatch(all(root, "form")[0]!, "submit");
  await settle();
  expect(calls[0]?.body).toMatchObject({ route: { value: "r" }, dryRun: true });
  expect(text(root)).toContain("step one");
  expect(disabled(start())).toBe(false);

  fill(inputs[0]!, "changed");
  await settle();
  expect(text(root)).not.toContain("step one");
  expect(disabled(start())).toBe(true);
});

test("new route starts after a preview and reports a refusal", async () => {
  const replies: Record<string, Reply> = {
    "daemon.v1.RouteService/Up": { start: { route: { value: "r" }, state: "planned", plan: "p" } },
  };
  const calls = daemon(replies);
  const root = await render("new-route");
  const inputs = all(root, "input").filter((input) => input.getAttribute("type") !== "checkbox");
  ["r", "/x", "orch", "adj"].forEach((value, i) => fill(inputs[i]!, value));
  dispatch(all(root, "form")[0]!, "submit");
  await settle();
  replies["daemon.v1.RouteService/Up"] = { start: { route: { value: "r" }, state: "running", reason: "on its way" } };
  dispatch(
    all(root, "button").find((button) => text(button) === "Start route")!,
    "click",
  );
  await settle();
  expect(calls.at(-1)?.body).toMatchObject({ dryRun: false });
  expect(text(root)).toContain("r: running");

  replies["daemon.v1.RouteService/Up"] = { status: 403, body: { code: "permission_denied", message: "no pair" } };
  dispatch(
    all(root, "button").find((button) => text(button) === "Start route")!,
    "click",
  );
  await settle();
  expect(text(root)).toContain("no pair");
});

test("new route can run without an adjutant", async () => {
  const calls = daemon({ "daemon.v1.RouteService/Up": { start: { state: "planned", plan: "p" } } });
  const root = await render("new-route");
  const box = all(root, "input").find((input) => input.getAttribute("type") === "checkbox")!;
  (box as unknown as { checked: boolean }).checked = true;
  dispatch(box, "change");
  await settle();
  const inputs = all(root, "input").filter((input) => input.getAttribute("type") !== "checkbox");
  expect(inputs.length).toBe(3);
  ["r", "/x", "orch"].forEach((value, i) => fill(inputs[i]!, value));
  dispatch(all(root, "form")[0]!, "submit");
  await settle();
  expect(calls[0]?.body).toMatchObject({ noAdjutant: true });
  expect(calls[0]?.body).not.toHaveProperty("adjutantCommand");
});
