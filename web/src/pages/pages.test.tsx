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
const { launchCommand, launchRequest } = await import("./new-route/new-route.tsx");

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
  fill(all(form, "input")[1]!, " go, format ,, ");
  fill(all(form, "textarea")[0]!, "notes");
  dispatch(form, "submit");
  await settle();
  expect(calls.find((call) => call.path.endsWith("/Add"))?.body).toEqual({
    kind: "tip",
    title: "Formatter",
    tags: ["go", "format"],
    body: "notes",
  });
});

test("suggestions offers only the kinds the catalog accepts", async () => {
  daemon({ "suggest.v1.SuggestService/Search": { items: [] } });
  const root = await render("suggestions");
  dispatch(
    all(root, "button").find((button) => text(button) === "Add an entry")!,
    "click",
  );
  await settle();
  const form = all(root, "form")[1]!;
  expect(all(all(form, "select")[0]!, "option").map(text)).toEqual(["tip", "mcp", "skill", "plugin", "mod", "repo"]);
});

test("suggestions waits for a tag before saving", async () => {
  daemon({ "suggest.v1.SuggestService/Search": { items: [] } });
  const root = await render("suggestions");
  dispatch(
    all(root, "button").find((button) => text(button) === "Add an entry")!,
    "click",
  );
  await settle();
  const form = all(root, "form")[1]!;
  fill(all(form, "input")[0]!, "Formatter");
  await settle();
  const save = all(form, "button").find((button) => text(button) === "Save entry")!;
  expect(save.getAttribute("disabled") !== null || (save as unknown as { disabled?: boolean }).disabled === true).toBe(
    true,
  );
  fill(all(form, "input")[1]!, " , ");
  fill(all(form, "textarea")[0]!, "notes");
  await settle();
  expect(save.getAttribute("disabled") !== null || (save as unknown as { disabled?: boolean }).disabled === true).toBe(
    true,
  );
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

test("route starts show each execution's runtime fields and its backend's controls", async () => {
  const calls = daemon({
    "daemon.v1.RouteService/List": {
      starts: [{ operation: "o1", route: { value: "auth" }, state: "done", backend: "herdr-core" }],
    },
    "daemon.v1.RuntimeService/ListExecutions": {
      executions: [
        {
          ref: { backend: "herdr-core", executionId: "e1", generation: "2", terminalAddress: "t-7" },
          route: { value: "auth" },
          agent: "proj-auth-orchestrator",
          role: "orchestrator",
          observation: {
            process: "live",
            readiness: "needs_attention",
            program: "idle",
            visibility: "verified",
            app: "closed",
          },
          attention: [
            { reason: "notice_waiting", noticeId: "n-1", line: "Run `herdsman notice read` and do what it prints" },
          ],
          unresolved: [
            { kind: "typed_submit", id: "s-0", state: "unresolved", choices: ["keep_waiting", "send_again"] },
          ],
          notices: { issued: 1, heldGate: "route_held" },
          actions: ["show", "submit"],
        },
        { ref: { backend: "herdr-tui" }, route: { value: "other" }, agent: "elsewhere", source: "legacy" },
      ],
    },
    "daemon.v1.RuntimeService/Show": { backend: "herdr-core" },
    "daemon.v1.RuntimeService/Submit": { submitId: "s-1", state: "typed" },
  });
  const root = await render("route");
  for (const expected of [
    "proj-auth-orchestrator",
    "Process: live",
    "Readiness: needs_attention",
    "Program: idle",
    "Visibility: verified, app closed",
    "Needs a person: notice_waiting",
    "Unresolved typed_submit s-0: unresolved",
    "keep_waiting, send_again",
    "Notices: issued 1, claimed 0, handled 0, returned 0, held by route_held",
  ]) {
    expect(text(root)).toContain(expected);
  }
  expect(text(root)).not.toContain("elsewhere");
  const buttons = all(root, "button");
  dispatch(
    buttons.find((b) => text(b) === "Show in the app")!,
    "click",
  );
  await settle();
  expect(calls.find((call) => call.path.endsWith("RuntimeService/Show"))?.body).toEqual({ executionId: "e1" });
  // A typed submit: the warning first, then the person's own confirmation.
  dispatch(
    all(root, "button").find((b) => text(b) === "Type a line")!,
    "click",
  );
  await settle();
  expect(text(root)).toContain("nothing can recall it");
  expect(calls.some((call) => call.path.endsWith("RuntimeService/Submit"))).toBe(false);
  fill(all(root, "input")[0]!, " go on ");
  dispatch(all(root, "form")[0]!, "submit");
  await settle();
  expect(calls.find((call) => call.path.endsWith("RuntimeService/Submit"))?.body).toEqual({
    executionId: "e1",
    text: "go on",
    confirmed: true,
  });
  expect(text(root)).toContain("Typed submit s-1: typed");
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
    adjutantKind: " ak ",
    orchestrator: " o ",
    orchestratorKind: " ok ",
    noAdjutant: false,
  };
  expect(launchRequest(fields)).toEqual({
    route: { value: "r" },
    repo: "/x",
    prompt: " go ",
    orchestratorCommand: "o",
    orchestratorKind: "ok",
    adjutantCommand: "a",
    adjutantKind: "ak",
  });
  expect(launchRequest({ ...fields, noAdjutant: true })).toEqual({
    route: { value: "r" },
    repo: "/x",
    prompt: " go ",
    orchestratorCommand: "o",
    orchestratorKind: "ok",
    noAdjutant: true,
  });
});

/** A daemon whose RouteService.Up replies only when the test says so. */
function heldUp() {
  const held: { body: Record<string, unknown>; reply: (status: number, body: unknown) => void }[] = [];
  bindHost({ url: "http://host", device: "d" }, (_url, init) => {
    return new Promise((resolve) => {
      held.push({
        body: JSON.parse(String(init.body)) as Record<string, unknown>,
        reply: (status, body) => resolve(Response.json(body, { status })),
      });
    });
  });
  return held;
}
const planned = (plan: string) => ({ start: { route: { value: "r" }, state: "planned", plan, folder: "/f" } });
function launchInputs(root: FakeElement): FakeElement[] {
  return all(root, "input").filter((input) => input.getAttribute("type") !== "checkbox");
}
async function fillAndPreview(root: FakeElement): Promise<void> {
  ["r", "/x", "orch", "codex", "adj", "claude"].forEach((value, i) => fill(launchInputs(root)[i]!, value));
  dispatch(all(root, "form")[0]!, "submit");
  await settle();
}

test("new route shows the preview, and an edit takes it away", async () => {
  const calls = daemon({ "daemon.v1.RouteService/Up": planned("step one") });
  const root = await render("new-route");
  await fillAndPreview(root);
  expect(calls[0]?.body).toMatchObject({
    route: { value: "r" },
    dryRun: true,
    orchestratorKind: "codex",
    adjutantKind: "claude",
  });
  expect(text(root)).toContain("step one");
  fill(launchInputs(root)[0]!, "changed");
  await settle();
  expect(text(root)).not.toContain("step one");
});

test("new route never shows a preview that an edit overtook", async () => {
  const held = heldUp();
  const root = await render("new-route");
  await fillAndPreview(root);
  fill(launchInputs(root)[0]!, "changed");
  held[0]!.reply(200, planned("old plan"));
  await settle();
  expect(text(root)).not.toContain("old plan");
  expect(text(root)).not.toContain("herdsman up");
});

test("new route drops the failure of a preview that an edit overtook", async () => {
  const held = heldUp();
  const root = await render("new-route");
  await fillAndPreview(root);
  fill(launchInputs(root)[0]!, "changed");
  held[0]!.reply(400, { code: "invalid_argument", message: "old problem" });
  await settle();
  expect(text(root)).not.toContain("old problem");
});

test("new route can preview again while an overtaken preview is still out", async () => {
  const held = heldUp();
  const root = await render("new-route");
  await fillAndPreview(root);
  fill(launchInputs(root)[0]!, "changed");
  dispatch(all(root, "form")[0]!, "submit");
  await settle();
  expect(held.length).toBe(2);
  held[1]!.reply(200, planned("new plan"));
  await settle();
  held[0]!.reply(200, planned("old plan"));
  await settle();
  expect(text(root)).toContain("new plan");
  expect(text(root)).not.toContain("old plan");
});

test("new route shows a refused preview", async () => {
  daemon({ "daemon.v1.RouteService/Up": { status: 400, body: { code: "invalid_argument", message: "bad route" } } });
  const root = await render("new-route");
  await fillAndPreview(root);
  expect(text(root)).toContain("bad route");
});

test("new route never asks the daemon for a real start, and says where to start one", async () => {
  const calls = daemon({ "daemon.v1.RouteService/Up": planned("p") });
  const root = await render("new-route");
  await fillAndPreview(root);
  expect(all(root, "button").map(text)).toEqual(["Preview"]);
  expect(calls.every((call) => (call.body as { dryRun?: boolean }).dryRun === true)).toBe(true);
  expect(text(root)).toContain("A browser cannot start a route");
  expect(text(root)).toContain(
    "cd /x && herdsman up r -p '' -a adj --adjutant-kind claude -o orch --orchestrator-kind codex",
  );
});

test("launchCommand quotes what the shell would read", () => {
  const fields = {
    route: "r",
    repo: "/my repo",
    prompt: "it's $HOME",
    adjutant: "claude --x",
    adjutantKind: "claude",
    orchestrator: "codex",
    orchestratorKind: "codex",
    noAdjutant: false,
  };
  expect(launchCommand(fields)).toBe(
    "cd '/my repo' && herdsman up r -p 'it'\\''s $HOME' -a 'claude --x' --adjutant-kind claude -o codex --orchestrator-kind codex",
  );
  expect(launchCommand({ ...fields, noAdjutant: true })).toBe(
    "cd '/my repo' && herdsman up r -p 'it'\\''s $HOME' --no-adjutant -o codex --orchestrator-kind codex",
  );
});

test("new route can run without an adjutant", async () => {
  const calls = daemon({ "daemon.v1.RouteService/Up": { start: { state: "planned", plan: "p" } } });
  const root = await render("new-route");
  const box = all(root, "input").find((input) => input.getAttribute("type") === "checkbox")!;
  (box as unknown as { checked: boolean }).checked = true;
  dispatch(box, "change");
  await settle();
  const inputs = all(root, "input").filter((input) => input.getAttribute("type") !== "checkbox");
  expect(inputs.length).toBe(4);
  ["r", "/x", "orch", "codex"].forEach((value, i) => fill(inputs[i]!, value));
  dispatch(all(root, "form")[0]!, "submit");
  await settle();
  expect(calls[0]?.body).toMatchObject({ noAdjutant: true });
  expect(calls[0]?.body).not.toHaveProperty("adjutantCommand");
});
