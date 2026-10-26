// The New route form's logic, apart from the DOM: defaults, the folder
// list, labels, validation and the exact text the app will send. The server
// checks everything again; this keeps the form honest and quick.
import type {
  Catalog,
  CatalogKind,
  ChoicePick,
  GitHubAccess,
  InboxState,
  RouteRequest,
  RouteRole,
  RouteSetup,
  RouteStep,
  State,
} from "./types.ts";

export const CHECKPOINT_MIN_MINUTES = 5;
export const CHECKPOINT_MAX_MINUTES = 240;
export const CHECKPOINT_MIN_TOKENS = 50000;
export const CHECKPOINT_MAX_TOKENS = 2000000;
export const MAX_REQUEST = 8000;

const agentName = /^[a-z][a-z0-9_-]{0,31}$/;
const modelId = /^[A-Za-z0-9._:[\]-]{1,64}(\/[A-Za-z0-9._:[\]-]{1,64})?$/;
const routeNameRe = /^[a-z0-9][a-z0-9-]{0,47}$/;
const inboxRe = /^[A-Za-z0-9._/@+~-]+$/;

/** A coordinator's harness choice. `other` holds a typed model id (Claude's Other model id). */
export interface Harness {
  kind: string;
  model: string;
  other: string;
  effort: string;
}

export interface RouteForm {
  role: RouteRole;
  workspace: string;
  folder: string;
  /** The coordinator of an orchestrator route, or the adjutant of an adjutant route. */
  main: Harness;
  name: string;
  request: string;
  checkpointer: boolean;
  minutes: string;
  limit: string;
  /** The orchestrator an adjutant route starts beside the adjutant. */
  orch: Harness;
  /** The orchestrator's harness follows the adjutant's until the user edits it. */
  orchEdited: boolean;
  orchName: string;
  routeName: string;
  inbox: string;
  /** The inbox stops following the folder and route name once the user edits it. */
  inboxEdited: boolean;
  /** The recommended setup (A32): its fields are filled and folded into one summary. */
  recommended: boolean;
  /** The GitHub account the user confirmed for the route, or "" (A32). */
  ghAccount: string;
  /** The custom path's answer to each setup item, by item name (A32). */
  custom: Record<string, string>;
  /**
   * The saved pick a role keeps exactly (A30, per role): it is checked
   * against that pick alone; a role without one is a fresh choice from the
   * live lists. Editing the role's harness makes it fresh.
   */
  saved: { main?: ChoicePick; orch?: ChoicePick };
}

/**
 * The recommended setup (A32), each item with one plain sentence of what it
 * means. It names no model, effort, runner or task: those are asked.
 */
export const RECOMMENDED_SETUP: [string, string][] = [
  ["Integration", "One pull request per task; the implementers publish their own."],
  ["Worktrees", "Each task gets its own worktree, named by the task id."],
  [
    "Push policy",
    "Two gates: a local review passes, then a draft pull request goes through the review bots, then it is marked ready.",
  ],
  ["Merge", "You merge; branches stay until the route closes."],
  [
    "Severity",
    "P0 and P1 block: data loss, a security hole, a crash, a wrong result or a broken feature. P2 (clarity or a minor defect) is reported, with one fix round after the pass.",
  ],
  [
    "Review skills",
    "A code-quality skill matched to the diff's languages plus a security pass, chosen from the installed skills.",
  ],
  ["Techniques", "A skill navigator and a progress file per task."],
  ["Adjutant", "On: you talk to an adjutant, which relays to the orchestrator through one private inbox per route."],
  ["Context policy", "Compact before a follow-up of an agent's own work; clear before a new task."],
  [
    "Checkpointer",
    "On: it keeps the orchestrator's memory and compacts its context (the app starts agents inside Herdr).",
  ],
  ["Test services", "None, unless the repository declares a database or service; then the route asks about it."],
  ["GitHub account", "The active gh account, unless it cannot see the repository; then the route asks."],
];

/**
 * Turns the recommended setup on or off. On fills its fields: an adjutant
 * beside the orchestrator (the app starts both through Herdr, so both can
 * enroll), the default names and inbox, and the checkpointer on at its
 * usual interval when the skill's script is there. Off keeps the values.
 */
export function setRecommended(f: RouteForm, c: Catalog, on: boolean): RouteForm {
  if (!on) return { ...f, recommended: false };
  return {
    ...setRole(f, "adjutant"),
    recommended: true,
    checkpointer: c.checkpointer_ok,
    minutes: "15",
    limit: "250k",
  };
}

/** Marks the "Other model id" choice in the model list. */
export const OTHER = "__other__";

/** The folder's base name cut to a safe label, as the server cuts it. */
export function projectLabel(folder: string): string {
  const base = folder.replace(/\/+$/, "").split("/").pop() ?? "";
  let s = base
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
  if (s.length > 24) s = s.slice(0, 24).replace(/-+$/, "");
  return /^[a-z0-9][a-z0-9-]*$/.test(s) ? s : "";
}

/** The folders the form offers: workspace checkouts first, then the folders panes run in. */
export function knownFolders(st: State | null): string[] {
  const out: string[] = [];
  const add = (p: string | null | undefined): void => {
    if (p && p.startsWith("/") && !out.includes(p)) out.push(p);
  };
  const ws = [...(st?.snapshot.workspaces ?? [])].sort((a, b) => Number(b.focused) - Number(a.focused));
  for (const w of ws) {
    add(w.worktree?.checkout_path);
    add(w.worktree?.repo_root);
  }
  for (const p of st?.snapshot.panes ?? []) add(p.cwd);
  return out;
}

export function kindOf(c: Catalog, kind: string): CatalogKind | undefined {
  return c.kinds.find((k) => k.kind === kind);
}

/** The efforts of a model: the listed model's own, the kind's for a typed id, none for a free model. */
export function effortsFor(k: CatalogKind | undefined, model: string): string[] {
  if (!k || k.free_model) return [];
  const m = k.models.find((x) => x.id === model);
  if (m) return m.efforts;
  return model === OTHER && k.other_model ? (k.models[0]?.efforts ?? []) : [];
}

/** The model's default as the harness reports it, else none: the form then asks for a choice. */
export function defaultEffort(k: CatalogKind | undefined, model: string): string {
  const efforts = effortsFor(k, model);
  const m = k?.models.find((x) => x.id === model);
  if (m && m.default_effort !== "" && efforts.includes(m.default_effort)) return m.default_effort;
  return "";
}

/**
 * The effort choices of a model, in the harness's order: the default the
 * harness reports is marked; with none reported, a blank first choice asks
 * the user to pick (no level is preferred by name).
 */
export function effortOptions(k: CatalogKind | undefined, model: string): [string, string][] {
  const efforts = effortsFor(k, model);
  const def = defaultEffort(k, model);
  const opts: [string, string][] = efforts.map((e) => [e, e === def ? `${e} (harness default)` : e]);
  return def === "" && opts.length ? [["", "Choose an effort…"], ...opts] : opts;
}

/** A harness with its newest model and default effort. */
export function harness(c: Catalog, kind: string): Harness {
  const k = kindOf(c, kind);
  const model = k?.free_model ? "" : (k?.default_model ?? "");
  return { kind, model, other: "", effort: defaultEffort(k, model) };
}

/** Switches the harness's kind or model and keeps the effort when the new list has it. */
export function pickHarness(c: Catalog, h: Harness, change: Partial<Pick<Harness, "kind" | "model">>): Harness {
  if (change.kind !== undefined && change.kind !== h.kind) return harness(c, change.kind);
  const model = change.model ?? h.model;
  const k = kindOf(c, h.kind);
  const effort = effortsFor(k, model).includes(h.effort) ? h.effort : defaultEffort(k, model);
  return { ...h, model, effort };
}

/** The saved picks of the roles this route needs, in the form's order; a role with none is left out. */
export function previousPicks(f: RouteForm, c: Catalog): ChoicePick[] {
  const roles = f.role === "adjutant" ? ["adjutant", "orchestrator"] : ["orchestrator"];
  return roles
    .map((r) => (c.previous ?? []).find((p) => p.role === r && p.runner === "herdr"))
    .filter((p): p is ChoicePick => !!p);
}

/** One line for a saved pick, as the "Use previous choice" option shows it. */
export function pickSummary(p: ChoicePick): string {
  return `${p.role} (${p.runner}): ${p.harness}${p.model ? ` ${p.model}` : ""}${p.effort ? `, effort ${p.effort}` : ""}`;
}

/** A saved pick that can start as saved: an effort of a harness other than Claude and Codex needs its option. */
export function usablePick(p: ChoicePick): boolean {
  return !p.effort || !!p.effort_flag || p.harness === "claude" || p.harness === "codex";
}

/** The catalog entry of one role's own saved pick: its model, effort and effort option, nothing of another role's. */
export function savedKind(c: Catalog, p: ChoicePick): CatalogKind {
  const known = kindOf(c, p.harness);
  const flag = p.effort_flag ? { effort_flag: p.effort_flag } : {};
  return {
    kind: p.harness,
    label: known?.label ?? p.harness,
    models: p.model
      ? [{ id: p.model, label: p.model, efforts: p.effort ? [p.effort] : [], default_effort: p.effort }]
      : [],
    default_model: p.model,
    other_model: false,
    free_model: false,
    checkpointer: p.harness === "claude" || p.harness === "codex",
    skill_prefix: p.harness === "codex" ? "$" : "/",
    skill_installed: known?.skill_installed ?? true,
    offline: false,
    saved: true,
    ...flag,
  };
}

/** The live lists a fresh role picks from: none while only the saved picks are known. */
export function liveCatalog(c: Catalog): Catalog {
  return c.from_picks ? { ...c, kinds: [] } : c;
}

/** The catalog one role is checked against: its own saved pick, or the live lists. */
export function roleCatalog(f: RouteForm, c: Catalog, which: "main" | "orch"): Catalog {
  const p = f.saved[which];
  return p ? { ...c, kinds: [savedKind(c, p)] } : liveCatalog(c);
}

/** A saved pick as the role's exact harness choice; null when it cannot start as saved. */
export function harnessFromPick(p: ChoicePick): Harness | null {
  if (!usablePick(p)) return null;
  return { kind: p.harness, model: p.model, other: "", effort: p.effort };
}

/**
 * When the live lists arrive, a fresh role whose harness they do not list
 * takes their first harness and its newest model; saved roles and valid
 * fresh choices stay.
 */
export function freshFromLists(f: RouteForm, c: Catalog): RouteForm {
  const live = liveCatalog(c);
  const fix = (which: "main" | "orch", h: Harness): Harness => {
    if (f.saved[which]) return h;
    const k = kindOf(live, h.kind);
    if (k && (k.free_model || k.models.some((m) => m.id === h.model) || h.model === OTHER)) return h;
    return harness(live, k ? k.kind : (live.kinds[0]?.kind ?? h.kind));
  };
  return { ...f, main: fix("main", f.main), orch: fix("orch", f.orch) };
}

/** Fills each role of the form from its own saved pick, which it then keeps exactly. */
export function usePrevious(f: RouteForm, c: Catalog): RouteForm {
  let out = f;
  for (const p of previousPicks(f, c)) {
    const h = harnessFromPick(p);
    if (!h) continue;
    if (p.role === "orchestrator" && f.role === "adjutant")
      out = { ...out, orch: h, orchEdited: true, saved: { ...out.saved, orch: p } };
    else out = { ...out, main: h, saved: { ...out.saved, main: p } };
  }
  return out;
}

/**
 * The form's harness fields whose role has no usable saved Herdr pick,
 * each with the question to show (A30): such a role is asked again from
 * the live lists, never skipped in silence. Each role reads its own pick.
 */
export function missingPicks(f: RouteForm, c: Catalog): Record<string, string> {
  const roles: [string, "main" | "orch"][] =
    f.role === "adjutant"
      ? [
          ["adjutant", "main"],
          ["orchestrator", "orch"],
        ]
      : [["orchestrator", "main"]];
  const out: Record<string, string> = {};
  for (const [role, field] of roles) {
    const p = (c.previous ?? []).find((x) => x.role === role && x.runner === "herdr");
    if (!p) out[field] = `No saved choice for the ${role}: pick its harness, model and effort.`;
    else if (!usablePick(p))
      out[field] = `The saved ${role} choice (${pickSummary(p)}) does not say how to pass its effort: pick again.`;
  }
  return out;
}

/** A change to one coordinator's harness: kind, model, effort or typed model id. */
export type HarnessChange = Partial<Harness>;

/**
 * Applies a harness change. The adjutant's choice (main) carries over to the
 * orchestrator until the user edits the orchestrator's own choice.
 */
export function setHarness(f0: RouteForm, c0: Catalog, which: "main" | "orch", change: HarnessChange): RouteForm {
  // An edited role is a fresh choice from the live lists; so is the
  // orchestrator that follows the adjutant.
  const c = liveCatalog(c0);
  const saved = { ...f0.saved };
  delete saved[which];
  if (which === "main" && !f0.orchEdited) delete saved.orch;
  const f = { ...f0, saved };
  const cur = f[which];
  let h: Harness;
  if (change.kind !== undefined && change.kind !== cur.kind) h = harness(c, change.kind);
  else if (change.model !== undefined && !kindOf(c, cur.kind)?.free_model)
    h = pickHarness(c, cur, { model: change.model });
  else h = { ...cur, ...(change.model !== undefined ? { model: change.model } : {}) };
  if (change.effort !== undefined) h = { ...h, effort: change.effort };
  if (change.other !== undefined) h = { ...h, other: change.other };
  if (which === "orch") return { ...f, orch: h, orchEdited: true };
  return { ...f, main: h, orch: f.orchEdited ? f.orch : { ...h } };
}

export function inboxDefault(project: string, routeName: string): string {
  return `~/.herdsman-private/${project || "project"}-${routeName}`;
}

/** The form a quick start needs: pick a folder, type the request, press Start. */
export function initialForm(c: Catalog, st: State | null): RouteForm {
  const ws = st?.snapshot.workspaces ?? [];
  const focused = ws.find((w) => w.focused) ?? ws[0];
  const folder = knownFolders(st)[0] ?? "";
  // A fresh role starts from the live lists, never from a saved pick.
  const live = liveCatalog(c);
  const first = live.kinds[0]?.kind ?? "claude";
  const routeName = `route-${c.today}`;
  return {
    role: "orchestrator",
    workspace: focused?.workspace_id ?? "",
    folder,
    main: harness(live, first),
    name: "orch-1",
    request: "",
    checkpointer: c.checkpointer_ok,
    minutes: "15",
    limit: "250k",
    orch: harness(live, first),
    orchEdited: false,
    orchName: "orch-1",
    routeName,
    inbox: inboxDefault(projectLabel(folder), routeName),
    inboxEdited: false,
    recommended: false,
    ghAccount: "",
    custom: {},
    saved: {},
  };
}

/** The form a new route opens with: the recommended setup on (A32). */
export function newForm(c: Catalog, st: State | null): RouteForm {
  return setRecommended(initialForm(c, st), c, true);
}

/** Switches the role and keeps every field the user typed; untouched default names follow. */
export function setRole(f: RouteForm, role: RouteRole): RouteForm {
  if (role === f.role) return f;
  let name = f.name;
  if (role === "adjutant" && name === "orch-1") name = "adj-1";
  if (role === "orchestrator" && name === "adj-1") name = "orch-1";
  return { ...f, role, name, orch: f.orchEdited ? f.orch : { ...f.main } };
}

/** Applies a change and keeps the inbox following the folder and route name until edited. */
export function update(f: RouteForm, change: Partial<RouteForm>): RouteForm {
  const next = { ...f, ...change };
  if (change.inbox !== undefined) next.inboxEdited = true;
  else if (!next.inboxEdited) next.inbox = inboxDefault(projectLabel(next.folder), next.routeName);
  return next;
}

/** Reads 250k, 1m or a plain number of tokens; null when it is none of these. */
export function parseLimit(s: string): number | null {
  const m = /^\s*(\d+(?:\.\d+)?)\s*([km]?)\s*$/i.exec(s);
  if (!m) return null;
  const mult = { "": 1, k: 1000, m: 1000000 }[(m[2] ?? "").toLowerCase()] ?? 1;
  const n = Math.round(Number(m[1]) * mult);
  return Number.isSafeInteger(n) ? n : null;
}

/** The route request as one double-quoted argument, as the server quotes it. */
export function quoteArg(s: string): string {
  return `"${s.replace(/[\\"]/g, (c) => `\\${c}`)}"`;
}

function modelOf(h: Harness): string {
  return h.model === OTHER ? h.other.trim() : h.model;
}

/** A role's catalog entry: its saved pick's, or the live list's. */
export function roleKind(f: RouteForm, c: Catalog, which: "main" | "orch"): CatalogKind | undefined {
  return kindOf(roleCatalog(f, c, which), f[which].kind);
}

/** The coordinator's entry: the route command and the checkpointer follow its kind. */
function coordKind(f: RouteForm, c: Catalog): CatalogKind | undefined {
  return roleKind(f, c, f.role === "adjutant" ? "orch" : "main");
}

/** The exact text sent to the orchestrator, and the adjutant's attach command for the human. */
export function preview(f: RouteForm, c: Catalog): { command: string; attach?: string } {
  const prefix = `${coordKind(f, c)?.skill_prefix ?? "/"}herdsman`;
  const req = f.request === "" ? "" : ` ${quoteArg(f.request)}`;
  if (f.role === "orchestrator") return { command: `${prefix} run${req}` };
  const adjPrefix = `${roleKind(f, c, "main")?.skill_prefix ?? "/"}herdsman`;
  return {
    command: `${prefix} orchestrator --adjutant ${f.name} --name ${f.orchName} --inbox ${f.inbox}${req}`,
    attach: `${adjPrefix} adjutant --name ${f.name} --orchestrator <orchestrator pane> --inbox ${f.inbox}`,
  };
}

/** Whether the checkpointer can run for this route, and why not. */
export function checkpointerAvailable(f: RouteForm, c: Catalog): { ok: boolean; why: string } {
  const k = coordKind(f, c);
  if (!k?.checkpointer) return { ok: false, why: "the checkpointer reads Claude Code and Codex transcripts only" };
  if (!c.checkpointer_ok) return { ok: false, why: c.checkpointer_why ?? "the checkpointer script was not found" };
  return { ok: true, why: "" };
}

/** The checkpointer runs when it is on and can run; the form shows why it cannot. */
export function checkpointerOn(f: RouteForm, c: Catalog): boolean {
  return f.checkpointer && checkpointerAvailable(f, c).ok;
}

/** The warnings that do not block a start: a harness without the herdsman skill. */
export function skillWarnings(f: RouteForm, c: Catalog): string[] {
  const which: ("main" | "orch")[] = f.role === "adjutant" ? ["main", "orch"] : ["main"];
  const kinds = which.map((w) => roleKind(f, c, w)).filter((k): k is CatalogKind => !!k && !k.skill_installed);
  return [...new Map(kinds.map((k) => [k.kind, k])).values()].map(
    (k) => `herdsman skill not found for ${k.kind}: the route command will not run until it is installed`,
  );
}

function checkHarness(h: Harness, c: Catalog, field: string, out: Record<string, string>, saved: boolean): void {
  const k = kindOf(c, h.kind);
  if (!k) {
    out[field] = "Pick a harness.";
    return;
  }
  const model = modelOf(h);
  if (!k.free_model && !k.other_model && k.models.length === 0) {
    // A29: no list was read, so there is no model and effort to choose; only
    // a saved default pick (no model) starts again as saved.
    if (!saved || h.model || h.effort)
      out[field] = `${k.label}'s model list could not be read: pick a harness whose list was read.`;
    return;
  }
  if (k.free_model) {
    if (model && !modelId.test(model)) out[field] = "A model id uses letters, digits and . _ : [ ] - (at most 64).";
    return;
  }
  if (h.model === OTHER) {
    if (!modelId.test(model)) out[field] = "A model id uses letters, digits and . _ : [ ] - (at most 64).";
  } else if (!k.models.some((m) => m.id === h.model)) out[field] = `Pick a model from ${k.label}'s list.`;
  // A model that lists no efforts takes none, as the server accepts.
  const efforts = effortsFor(k, h.model);
  if (efforts.length ? !efforts.includes(h.effort) : h.effort !== "") out[field] ??= "Pick an effort from the list.";
}

/** Field → message for every problem the form can see. Empty when it can be sent. */
export function validate(f: RouteForm, c: Catalog, st: State | null): Record<string, string> {
  const out: Record<string, string> = {};
  const live = new Set((st?.snapshot.agents ?? []).map((a) => a.name).filter(Boolean));
  if (!f.workspace) out["workspace"] = "Pick a workspace.";
  if (!f.folder.startsWith("/")) out["folder"] = "Type an absolute folder path.";
  else if (!projectLabel(f.folder)) out["folder"] = "The folder name needs letters or digits for the tab label.";
  const name = (field: string, n: string): void => {
    if (!agentName.test(n)) out[field] = "Start with a letter; then a-z, 0-9, - or _ (at most 32).";
    else if (live.has(n)) out[field] = `An agent named ${n} is already running.`;
  };
  name("name", f.name);
  checkHarness(f.main, roleCatalog(f, c, "main"), "main", out, !!f.saved.main);
  if (f.role === "adjutant") {
    name("orchName", f.orchName);
    if (f.orchName === f.name) out["orchName"] = "The orchestrator needs a name of its own.";
    checkHarness(f.orch, roleCatalog(f, c, "orch"), "orch", out, !!f.saved.orch);
    if (!routeNameRe.test(f.routeName)) out["routeName"] = "a-z, 0-9 and -, starting with a letter or digit.";
    if (
      !(f.inbox.startsWith("~/") || f.inbox.startsWith(`${c.home}/`)) ||
      !inboxRe.test(f.inbox) ||
      f.inbox.includes("..")
    )
      out["inbox"] = "The inbox must be a plain path inside your home folder.";
  }
  if (f.request.length > MAX_REQUEST) out["request"] = `At most ${MAX_REQUEST} characters.`;
  else if ([...f.request].some((ch) => ch !== "\n" && ch !== "\t" && /\p{Cc}/u.test(ch)))
    out["request"] = "Only newlines and tabs may stand among the text: remove the control character.";
  if (checkpointerOn(f, c)) {
    const m = Number(f.minutes);
    if (!Number.isInteger(m) || m < CHECKPOINT_MIN_MINUTES || m > CHECKPOINT_MAX_MINUTES)
      out["minutes"] = `${CHECKPOINT_MIN_MINUTES} to ${CHECKPOINT_MAX_MINUTES} minutes.`;
    const l = parseLimit(f.limit);
    if (l === null || l < CHECKPOINT_MIN_TOKENS || l > CHECKPOINT_MAX_TOKENS)
      out["limit"] = "50k to 2m tokens (for example 250k).";
  }
  return out;
}

/** The server field a 400 names, mapped to the form field that shows it. */
export function formField(serverField: string | undefined): string {
  const map: Record<string, string> = {
    workspace_id: "workspace",
    model: "main",
    orch_model: "orch",
    orch_name: "orchName",
    route_name: "routeName",
  };
  return map[serverField ?? ""] ?? serverField ?? "";
}

/** The setup items the custom path asks: the form's own fields cover the adjutant, checkpointer and account. */
export const SETUP_QUESTIONS: [string, string][] = RECOMMENDED_SETUP.filter(
  ([item]) => !["Adjutant", "Checkpointer", "GitHub account"].includes(item),
);

/** The setup the orchestrator receives (A32), with the effective checkpointer. */
export function setupOf(f: RouteForm, c: Catalog): RouteSetup {
  const roles = [
    { role: f.role, runner: "herdr", harness: f.main.kind, model: modelOf(f.main), effort: f.main.effort },
  ];
  if (f.role === "adjutant") {
    roles.push({
      role: "orchestrator",
      runner: "herdr",
      harness: f.orch.kind,
      model: modelOf(f.orch),
      effort: f.orch.effort,
    });
  }
  const s: RouteSetup = {
    recommended: f.recommended,
    checkpointer: checkpointerOn(f, c),
    roles,
    answers: SETUP_QUESTIONS.map(([item, text]) => ({
      item,
      answer: f.recommended ? text : (f.custom[item] ?? "").trim(),
    })),
  };
  if (f.ghAccount) s.github_account = f.ghAccount;
  return s;
}

/** The account choices of a probe: the accounts that can read the repository, the probed one marked. */
export function accountOptions(g: GitHubAccess): [string, string][] {
  return g.accounts
    .filter((a) => a.can_read)
    .map((a) => [a.name, a.name === g.suggested ? `${a.name} (probed)` : a.name]);
}

/** The request body for POST /api/routes. */
export function toRequest(f: RouteForm, c: Catalog): RouteRequest {
  const r: RouteRequest = {
    role: f.role,
    workspace_id: f.workspace,
    folder: f.folder.trim(),
    kind: f.main.kind,
    model: modelOf(f.main),
    effort: f.main.effort,
    name: f.name,
    request: f.request,
    checkpointer: { on: checkpointerOn(f, c), minutes: Number(f.minutes), limit: parseLimit(f.limit) ?? 0 },
    setup: setupOf(f, c),
  };
  if (f.role === "adjutant") {
    r.orch_name = f.orchName;
    r.orch_kind = f.orch.kind;
    r.orch_model = modelOf(f.orch);
    r.orch_effort = f.orch.effort;
    r.route_name = f.routeName;
    r.inbox = f.inbox;
  }
  return r;
}

/** Shows a home path with ~. */
export function tilde(p: string, home: string): string {
  return home && (p === home || p.startsWith(`${home}/`)) ? `~${p.slice(home.length)}` : p;
}

/** The line under a step that did not pass: herdr's error (which starts with its code), or why it was skipped. */
export function stepNote(s: RouteStep): string {
  if (s.status === "skipped") return `skipped: ${s.reason ?? "an earlier step failed"}`;
  if (s.status !== "failed") return "";
  const reason = s.reason ?? "";
  return s.code && !reason.startsWith(`${s.code}:`) ? `${s.code}: ${reason}` : reason;
}

/** One line of the channel setup checklist. done is null while its state cannot be read. */
export interface ChecklistItem {
  title: string;
  who: "you do this" | "after start";
  done: boolean | null;
  code?: string;
  note?: string;
}

/**
 * The channel setup checklist for the form as it is now: the commands follow
 * the current inbox and names, and the inbox state counts only when it was
 * read for this inbox and pair (stateFor); anything else shows as unknown.
 */
export function checklist(f: RouteForm, c: Catalog, s: InboxState | null, attach?: string): ChecklistItem[] {
  const known = s !== null;
  const items: ChecklistItem[] = [
    {
      title: "Create the private inbox, mode 700",
      who: "you do this",
      done: known ? s.private : null,
      code: `mkdir -m 700 -p ${f.inbox}`,
    },
    { title: "Write the setup proof (setup.json)", who: "you do this", done: known ? s.setup : null },
    {
      title: "Attach the adjutant: type this into its pane",
      who: "you do this",
      done: null,
      code: attach ?? preview(f, c).attach ?? "",
    },
    { title: "Both sides propose", who: "after start", done: known ? s.pair_proposed : null },
    { title: "Approve the pair", who: "you do this", done: known ? s.pair_approved : null },
  ];
  if (s?.proposed.length) items[3] = { ...items[3]!, note: s.proposed.join(", ") };
  return items;
}

/** The key an inbox read is bound to: a reply for another key is outdated. */
export function inboxKey(f: RouteForm): string {
  return `${f.inbox}\n${f.name}\n${f.orchName}`;
}
