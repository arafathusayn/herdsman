// The New route form's logic, apart from the DOM: defaults, the folder
// list, labels, validation and the exact text the app will send. The server
// checks everything again; this keeps the form honest and quick.
import type { Catalog, CatalogKind, InboxState, RouteRequest, RouteRole, RouteStep, State } from "./types.ts";

export const CHECKPOINT_MIN_MINUTES = 5;
export const CHECKPOINT_MAX_MINUTES = 240;
export const CHECKPOINT_MIN_TOKENS = 50000;
export const CHECKPOINT_MAX_TOKENS = 2000000;
export const MAX_REQUEST = 8000;

const agentName = /^[a-z][a-z0-9_-]{0,31}$/;
const modelId = /^[A-Za-z0-9._:[\]-]{1,64}$/;
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

/** Medium when the list has it, else the model's default, else the first. */
export function defaultEffort(k: CatalogKind | undefined, model: string): string {
  const efforts = effortsFor(k, model);
  if (efforts.includes("medium")) return "medium";
  const m = k?.models.find((x) => x.id === model);
  if (m && efforts.includes(m.default_effort)) return m.default_effort;
  return efforts[0] ?? "";
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

/** A change to one coordinator's harness: kind, model, effort or typed model id. */
export type HarnessChange = Partial<Harness>;

/**
 * Applies a harness change. The adjutant's choice (main) carries over to the
 * orchestrator until the user edits the orchestrator's own choice.
 */
export function setHarness(f: RouteForm, c: Catalog, which: "main" | "orch", change: HarnessChange): RouteForm {
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
  const first = c.kinds[0]?.kind ?? "claude";
  const routeName = `route-${c.today}`;
  return {
    role: "orchestrator",
    workspace: focused?.workspace_id ?? "",
    folder,
    main: harness(c, first),
    name: "orch-1",
    request: "",
    checkpointer: c.checkpointer_ok,
    minutes: "15",
    limit: "250k",
    orch: harness(c, first),
    orchEdited: false,
    orchName: "orch-1",
    routeName,
    inbox: inboxDefault(projectLabel(folder), routeName),
    inboxEdited: false,
  };
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

/** The coordinator whose kind the route command and the checkpointer follow. */
function coordinator(f: RouteForm): Harness {
  return f.role === "adjutant" ? f.orch : f.main;
}

/** The exact text sent to the orchestrator, and the adjutant's attach command for the human. */
export function preview(f: RouteForm, c: Catalog): { command: string; attach?: string } {
  const prefix = `${kindOf(c, coordinator(f).kind)?.skill_prefix ?? "/"}herdsman`;
  const req = f.request === "" ? "" : ` ${quoteArg(f.request)}`;
  if (f.role === "orchestrator") return { command: `${prefix} run${req}` };
  const adjPrefix = `${kindOf(c, f.main.kind)?.skill_prefix ?? "/"}herdsman`;
  return {
    command: `${prefix} orchestrator --adjutant ${f.name} --name ${f.orchName} --inbox ${f.inbox}${req}`,
    attach: `${adjPrefix} adjutant --name ${f.name} --orchestrator <orchestrator pane> --inbox ${f.inbox}`,
  };
}

/** Whether the checkpointer can run for this route, and why not. */
export function checkpointerAvailable(f: RouteForm, c: Catalog): { ok: boolean; why: string } {
  const k = kindOf(c, coordinator(f).kind);
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
  const kinds = f.role === "adjutant" ? [f.main.kind, f.orch.kind] : [f.main.kind];
  return [...new Set(kinds)]
    .map((k) => kindOf(c, k))
    .filter((k): k is CatalogKind => !!k && !k.skill_installed)
    .map((k) => `herdsman skill not found for ${k.kind}: the route command will not run until it is installed`);
}

function checkHarness(h: Harness, c: Catalog, field: string, out: Record<string, string>): void {
  const k = kindOf(c, h.kind);
  if (!k) {
    out[field] = "Pick a harness.";
    return;
  }
  const model = modelOf(h);
  if (k.free_model) {
    if (model && !modelId.test(model)) out[field] = "A model id uses letters, digits and . _ : [ ] - (at most 64).";
    return;
  }
  if (h.model === OTHER) {
    if (!modelId.test(model)) out[field] = "A model id uses letters, digits and . _ : [ ] - (at most 64).";
  } else if (!k.models.some((m) => m.id === h.model)) out[field] = `Pick a model from ${k.label}'s list.`;
  if (!effortsFor(k, h.model).includes(h.effort)) out[field] ??= "Pick an effort from the list.";
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
  checkHarness(f.main, c, "main", out);
  if (f.role === "adjutant") {
    name("orchName", f.orchName);
    if (f.orchName === f.name) out["orchName"] = "The orchestrator needs a name of its own.";
    checkHarness(f.orch, c, "orch", out);
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
