// The New route form on its own: roles and harness choices change the
// fields, refusals show by their fields, a sent form locks, the result
// screen lists the steps, and typing keeps focus and caret.
import type { Catalog, GitHubAccess, RouteResult } from "../../src/types.ts";
import type { Deps } from "../../src/views/route-form.ts";
import { routeForm } from "../../src/views/route-form.ts";
import { check, equal, flush, pick, q, run, typeIn } from "./lib.ts";

const efforts = ["low", "medium", "high"];
const catalog = {
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
      skill_installed: false,
      offline: true,
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
  herdr: { version: "0.9", protocol: 1 },
  routes: [],
  snapshot: {
    workspaces: [
      { workspace_id: "w1", label: "a", focused: true, number: 1 },
      { workspace_id: "w2", label: "", focused: false, number: 2 },
    ],
    panes: [{ cwd: "/r/repo" }, { cwd: "/r/two" }],
    agents: [],
  },
} as unknown as Parameters<typeof routeForm>[2];

interface Run {
  root: () => HTMLFormElement;
  reads: ((s: unknown) => void)[];
  finish: (r: RouteResult) => void;
  sent: unknown[];
}

/** A form in a fresh container, with answers the run controls. */
function start(
  o: {
    custom?: boolean;
    c?: Catalog;
    opts?: Parameters<typeof routeForm>[4];
    github?: (folder: string) => Promise<GitHubAccess>;
  } = {},
): Run {
  for (const old of document.querySelectorAll(".form-box")) old.remove();
  localStorage.clear(); // the form remembers the last choices
  const box = document.createElement("div");
  box.className = "form-box";
  document.body.append(box);
  const body = document.createElement("form");
  box.append(body);
  const reads: ((s: unknown) => void)[] = [];
  const sent: unknown[] = [];
  let finish: (r: RouteResult) => void = () => {};
  const deps: Deps = {
    createRoute: (r) => {
      sent.push(r);
      return new Promise((res) => (finish = res));
    },
    inboxState: () => new Promise((res) => reads.push(res as (s: unknown) => void)),
    state: () => state,
    copy: () => {},
    openPane: () => {},
    close: () => {},
    failed: () => {},
    every: () => () => {},
    later: () => () => {},
    ...(o.github ? { github: o.github } : {}),
  };
  routeForm(body, o.c ?? catalog, state, deps, o.opts);
  const root = () => q<HTMLFormElement>(box, "form");
  // Most checks drive every field: they start with the recommended setup
  // off and the orchestrator role, as the custom path does.
  if (o.custom ?? true) {
    const rec = q<HTMLInputElement>(root(), "[name=recommended]");
    if (rec.checked) rec.click();
    q(root(), "[name=role-orchestrator]").click();
  }
  return {
    root,
    reads,
    finish: (r) => finish(r),
    sent,
  };
}

const field = <E extends Element = HTMLInputElement>(r: Run, name: string): E => q<E>(r.root(), `[name="${name}"]`);
const has = (r: Run, name: string): boolean => r.root().querySelector(`[name="${name}"]`) !== null;
const options = (s: HTMLSelectElement): string[] => [...s.options].map((o) => o.value);
const errorOf = (r: Run, name: string): string | null =>
  r.root().querySelector(`[data-error="${name}"]`)?.textContent ?? null;
const submit = (r: Run): void => r.root().requestSubmit(field<HTMLButtonElement>(r, "start"));

await run(async () => {
  // The initial form: the orchestrator role, the first harness and its models, focus on the folder.
  let r = start();
  check("the form replaces its placeholder", r.root().isConnected && r.root().querySelector("[name=start]") !== null);
  // The folder comes from the focused workspace, so the first empty field is the request.
  equal("the folder is the workspace's", field<HTMLInputElement>(r, "folder").value, "/r/repo");
  check("the first empty field is focused", document.activeElement === field(r, "request"));
  equal("harnesses", options(field<HTMLSelectElement>(r, "main-kind")), ["claude", "codex", "pi"]);
  equal("models", options(field<HTMLSelectElement>(r, "main-model")), ["fable", "sonnet", "__other__"]);
  check("the role is a radio group", field<HTMLInputElement>(r, "role-adjutant").closest("[role=radiogroup]") !== null);
  check("no orchestrator name field without an adjutant", !has(r, "orchName"));

  // The adjutant role adds the orchestrator's own fields.
  field(r, "role-adjutant").click();
  check("an adjutant adds the orchestrator name", has(r, "orchName"));

  // A harness choice changes the models; another model opens a free field.
  r = start();
  pick(field<HTMLSelectElement>(r, "main-kind"), "codex");
  equal("codex models", options(field<HTMLSelectElement>(r, "main-model")), ["gpt-5.6-luna"]);
  r = start();
  check("no free model field at first", !has(r, "main-other"));
  pick(field<HTMLSelectElement>(r, "main-model"), "__other__");
  check("Other model opens its field", has(r, "main-other"));
  typeIn(field(r, "main-other"), "m-1");
  equal("the free model keeps what was typed", field<HTMLInputElement>(r, "main-other").value, "m-1");
  r = start();
  pick(field<HTMLSelectElement>(r, "main-kind"), "pi");
  equal("a harness with free models takes a typed model", field(r, "main-model").tagName, "INPUT");

  // Refusals show under their fields and clear as each is fixed.
  r = start();
  typeIn(field(r, "minutes"), "1");
  typeIn(field(r, "limit"), "x");
  submit(r);
  equal("nothing is sent", r.sent.length, 0);
  equal("minutes refused", errorOf(r, "minutes"), "5 to 240 minutes.");
  check("limit refused", (errorOf(r, "limit") ?? "").includes("50k to 2m"), String(errorOf(r, "limit")));
  check(
    "the checkpointer group is marked",
    q(r.root(), '[data-field="checkpointer"]').className.split(" ").includes("invalid"),
  );
  const minutes = field(r, "minutes");
  typeIn(minutes, "15");
  equal("a fixed field clears its message", errorOf(r, "minutes"), null);
  check("the other message stays", errorOf(r, "limit") !== null);
  check("fixing keeps the field's node", field(r, "minutes") === minutes);

  // Without a checkpointer its numbers are gone.
  r = start();
  field(r, "checkpointer").click();
  check("no checkpointer hides its numbers", !has(r, "minutes") && !has(r, "limit"));
  field(r, "checkpointer").click();
  equal("turned on again, the numbers are back", field<HTMLInputElement>(r, "minutes").value, "15");

  // The adjutant's inbox checklist shows what the inbox read says.
  r = start();
  field(r, "role-adjutant").click();
  r.reads.at(-1)?.({
    path: "x",
    exists: true,
    private: true,
    setup: false,
    proposed: ["a"],
    approved: [],
    pair_proposed: true,
    pair_approved: false,
  });
  await flush();
  check("the inbox checklist lists items", r.root().querySelectorAll("[data-item]").length > 0);

  // A sent form locks while the route starts, then shows the steps.
  r = start();
  typeIn(field(r, "folder"), "/r/repo");
  submit(r);
  equal("the form is sent", r.sent.length, 1);
  check("the fields lock while it starts", q<HTMLFieldSetElement>(r.root(), "fieldset.nr-fields").disabled);
  check("Start is off while it starts", field<HTMLButtonElement>(r, "start").disabled);
  r.finish({
    ok: false,
    error: "boom",
    steps: [
      { step: "create tab x", method: "tab.create", status: "ok", id: "w1:t1" },
      { step: "start a", method: "agent.start", status: "failed", reason: "no" },
      { step: "send", method: "agent.prompt", status: "skipped" },
    ],
    tabs: [],
    orchestrator_pane: "w1:p2",
  } as unknown as RouteResult);
  await flush();
  equal(
    "the result marks each step by its status",
    [...r.root().querySelectorAll(".nr-steps li")].map((li) => li.className),
    ["s-ok", "s-failed", "s-skipped"],
  );
  check("the failure says why", (q(r.root(), ".nr-body .error").textContent ?? "").includes("boom"));
  // Back to the form keeps what was typed.
  q(r.root(), ".nr-actions .btn.ghost").click();
  await flush();
  equal("back to the form keeps the folder", field<HTMLInputElement>(r, "folder").value, "/r/repo");

  // A started adjutant route shows the command to attach.
  r = start();
  field(r, "role-adjutant").click();
  typeIn(field(r, "folder"), "/r/repo");
  typeIn(field(r, "orchName"), "o-2");
  submit(r);
  r.finish({
    ok: true,
    steps: [],
    tabs: [],
    adjutant_pane: "w1:p1",
    orchestrator_pane: "w1:p2",
    attach_command: "/herdsman adjutant x",
  } as unknown as RouteResult);
  await flush();
  equal("the attach command", q(r.root(), ".nr-attach code").textContent, "/herdsman adjutant x");

  // Typing keeps the field, its focus and its caret.
  r = start();
  const req = field<HTMLTextAreaElement>(r, "request");
  req.focus();
  req.value = "hello";
  req.setSelectionRange(2, 2);
  req.dispatchEvent(new Event("input", { bubbles: true }));
  check(
    "typing keeps focus and caret",
    document.activeElement === req && req.selectionStart === 2 && field(r, "request") === (req as unknown),
  );

  // A32: the recommended setup is on at first: one summary in place of the
  // role and the checkpointer; turned off, the role and every setup item
  // show.
  r = start({ custom: false });
  check("the recommended switch is on", field<HTMLInputElement>(r, "recommended").checked);
  const summary = q(r.root(), '[data-field="recommended-summary"]');
  check("its summary shows", summary.checkVisibility() && summary.querySelectorAll("dt").length > 0);
  equal(
    "the summary names the checkpointer as on",
    (summary.querySelector('[data-item="Checkpointer"]')?.textContent ?? "").startsWith("Off"),
    false,
  );
  check("no role or checkpointer while it is on", !has(r, "role-adjutant") && !has(r, "checkpointer"));
  check("Start is open", !field<HTMLButtonElement>(r, "start").disabled);
  field(r, "recommended").click();
  check("off: the role and the checkpointer show", has(r, "role-adjutant") && has(r, "checkpointer"));
  check("off: the summary is gone", r.root().querySelector('[data-field="recommended-summary"]') === null);
  const integration = field<HTMLInputElement>(r, "custom-Integration");
  check("off: each setup item is asked", integration.checkVisibility() && integration.maxLength === 400);
  // Typing an answer keeps the field, its focus and its caret.
  integration.focus();
  integration.value = "one branch";
  integration.setSelectionRange(3, 3);
  integration.dispatchEvent(new Event("input", { bubbles: true }));
  check(
    "typing a setup answer keeps focus and caret",
    document.activeElement === integration &&
      integration.selectionStart === 3 &&
      field(r, "custom-Integration") === integration,
  );
  typeIn(field(r, "folder"), "/r/repo");
  submit(r);
  const setup = (r.sent[0] as { setup?: { recommended: boolean; answers: { item: string; answer: string }[] } })?.setup;
  equal("the answer is sent", setup?.answers.find((a) => a.item === "Integration")?.answer, "one branch");
  equal("the setup is the custom one", setup?.recommended, false);

  // A32: Start waits for the GitHub probe and for a confirmed account; a
  // new folder starts over.
  let answer: (g: GitHubAccess) => void = () => {};
  const asked: string[] = [];
  r = start({
    custom: false,
    github: (folder) => {
      asked.push(folder);
      return new Promise((res) => (answer = res));
    },
  });
  equal("the folder is probed", asked, ["/r/repo"]);
  check("Start waits for the probe", field<HTMLButtonElement>(r, "start").disabled);
  equal(
    "Start says why",
    field<HTMLButtonElement>(r, "start").title,
    "Checking which GitHub account can read this repository…",
  );
  answer({
    repo: "o/r",
    accounts: [
      { name: "user-a", active: true, can_read: true },
      { name: "user-b", active: false, can_read: true },
    ],
    suggested: "user-a",
  });
  await flush();
  const account = field<HTMLSelectElement>(r, "ghAccount");
  equal("the accounts", options(account), ["", "user-a", "user-b"]);
  equal("none is chosen for the user", account.value, "");
  check("Start waits for the pick", field<HTMLButtonElement>(r, "start").disabled);
  pick(account, "user-b");
  check("a picked account opens Start", !field<HTMLButtonElement>(r, "start").disabled);
  equal("Start has no reason left", field<HTMLButtonElement>(r, "start").title, "");
  typeIn(field(r, "folder"), "/r/two");
  equal("a new folder is probed", asked, ["/r/repo", "/r/two"]);
  check("a new folder waits again", field<HTMLButtonElement>(r, "start").disabled);

  // A30: the previous choice fills the roles; a form from the saved picks
  // alone offers to read the lists.
  const orchPick = {
    role: "orchestrator",
    runner: "herdr",
    harness: "codex",
    model: "gpt-5.6-luna",
    effort: "low",
    route: "",
    at: "",
  };
  let lists = 0;
  r = start({
    custom: false,
    c: { ...catalog, from_picks: true, previous: [orchPick] } as unknown as Catalog,
    opts: { needLists: () => void lists++ },
  });
  field(r, "recommended").click();
  q(r.root(), "[name=role-orchestrator]").click();
  const previous = q(r.root(), ".nr-previous");
  check("the previous choice shows", (previous.textContent ?? "").includes("orchestrator (herdr): codex gpt-5.6-luna"));
  q(r.root(), "[name=choose-lists]").click();
  equal("Choose again asks for the lists", lists, 1);
  [...previous.querySelectorAll("button")].find((b) => b.textContent?.trim() === "Use previous choice")?.click();
  equal("the previous choice fills the role", field<HTMLSelectElement>(r, "main-kind").value, "codex");
  equal("with its effort", field<HTMLSelectElement>(r, "main-effort").value, "low");

  // A29: a harness with a page of its models links to it.
  r = start({
    c: {
      ...catalog,
      kinds: [{ ...catalog.kinds[1]!, docs_url: "https://example.com/models" }],
    } as unknown as Catalog,
  });
  const docs = q<HTMLAnchorElement>(r.root(), '[data-field="main-model"] a');
  equal(
    "the docs link",
    [docs.href, docs.target, docs.rel, docs.textContent],
    ["https://example.com/models", "_blank", "noopener", "Model docs"],
  );
});
