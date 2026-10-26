// The New route form inside its dialog. Everything outside the DOM (the
// server calls, the live state, navigation, toasts) comes in through Deps,
// so the form can be driven in tests without a server or the app's store.
import { h, icon, icons, mount } from "../dom.ts";
import {
  accountOptions,
  checklist,
  checkpointerAvailable,
  checkpointerOn,
  effortOptions,
  effortsFor,
  formField,
  freshFromLists,
  type HarnessChange,
  inboxKey,
  kindOf,
  knownFolders,
  liveCatalog,
  missingPicks,
  newForm,
  OTHER,
  pickSummary,
  preview,
  previousPicks,
  projectLabel,
  RECOMMENDED_SETUP,
  roleKind,
  type RouteForm,
  setHarness,
  setRecommended,
  setRole,
  SETUP_QUESTIONS,
  skillWarnings,
  stepNote,
  toRequest,
  update,
  usePrevious,
  validate,
} from "../new-route.ts";
import type { Catalog, GitHubAccess, InboxState, RouteRequest, RouteResult, State } from "../types.ts";

export interface Deps {
  createRoute(r: RouteRequest): Promise<RouteResult>;
  inboxState(path: string, adjutant: string, orchestrator: string): Promise<InboxState>;
  /** The live session state, for validation at submit time. */
  state(): State | null;
  copy(text: string, what: string): void;
  openPane(pane: string): void;
  close(): void;
  failed(err: unknown): void;
  /** Schedules fn every ms; returns a stop function. */
  every(ms: number, fn: () => void): () => void;
  /** Delays fn by ms; returns a cancel function. */
  later(ms: number, fn: () => void): () => void;
  /** Probes the gh accounts that can read a folder's repository (A32); absent offers none. */
  github?(folder: string): Promise<GitHubAccess>;
}

/** How often an open adjutant checklist rereads its inbox, so human steps show as they happen. */
export const INBOX_POLL_MS = 5000;
/** Typing in the inbox, folder, route name or a name waits this long before a reread. */
export const INBOX_DEBOUNCE_MS = 400;

function selectBox(
  name: string,
  value: string,
  options: [string, string][],
  onchange: (v: string) => void,
  id?: string,
): HTMLElement {
  const sel = h(
    "select",
    { name, id, onchange: (e: Event) => onchange((e.target as HTMLSelectElement).value) },
    options.map(([v, label]) => h("option", { value: v, selected: v === value }, label)),
  );
  return h("span", { class: "select" }, sel, icon(icons.chevronDown));
}

let uid = 0;

/** The checkpointer group's own messages, each shown under the group. */
const checkpointerFields = ["checkpointer", "minutes", "limit"];

/**
 * Builds the form into body and returns a stop function for its timers.
 * The returned handle also exposes the current form for tests.
 */
export function routeForm(
  body: HTMLElement,
  initial: Catalog,
  st: State | null,
  deps: Deps,
  opts: { usePrevious?: boolean; recommended?: boolean } = {},
): { stop(): void; form(): RouteForm; sent(): boolean; setCatalog(next: Catalog): void } {
  let c = initial;
  let f: RouteForm = newForm(c, st);
  // The answer to "Use the recommended setup?" (A32), asked before the form.
  if (opts.recommended === false) f = setRecommended(f, c, false);
  let errors: Record<string, string> = {};
  // "Yes" to the previous choice (A30): the saved picks fill the roles at
  // once; a role without a usable pick is asked again, by name.
  if (opts.usePrevious) {
    f = usePrevious(f, c);
    errors = missingPicks(f, c);
  }
  let busy = false;
  // The form as it was sent: the result, checklist and inbox reads describe
  // this route, whatever the fields show later.
  let submitted: RouteForm | null = null;
  const active = (): RouteForm => submitted ?? f;
  let result: RouteResult | null = null;
  // The inbox state, valid only for the key it was read for.
  let inbox: { key: string; state: InboxState } | null = null;
  let reqSeq = 0;
  let cancelDebounce = (): void => {};
  const id = `nr${++uid}`;
  const live = {
    preview: h("pre", { class: "nr-preview" }),
    label: h("small", null),
    inbox: null as HTMLInputElement | null,
    checklist: h("section", { class: "nr-checklist", "aria-label": "Channel setup" }),
  };
  // Each field's box and its error line, by field name.
  let boxes = new Map<string, HTMLElement>();
  let messages = new Map<string, HTMLElement>();
  // The typed-model inputs, so an inherited orchestrator value can be shown.
  let inputs = new Map<string, HTMLInputElement>();

  const readInbox = (): void => {
    probeGitHub();
    const r = active();
    if (r.role !== "adjutant") return;
    const key = inboxKey(r);
    const seq = ++reqSeq;
    deps.inboxState(r.inbox, r.name, r.orchName).then(
      (s) => {
        // A reply for an older request or another inbox or pair is outdated.
        if (seq !== reqSeq || key !== inboxKey(active())) return;
        inbox = { key, state: s };
        drawChecklist();
      },
      () => {
        if (seq !== reqSeq) return;
        inbox = null;
        drawChecklist();
      },
    );
  };
  const stopPoll = deps.every(INBOX_POLL_MS, readInbox);

  const set = (change: Partial<RouteForm>, redraw: boolean): void => {
    // The fields are frozen while the route is being created.
    if (busy) return draw();
    const before = inboxKey(f);
    f = update(f, change);
    for (const k of Object.keys(change)) delete errors[k];
    if (change.folder !== undefined) probeGitHub();
    if (redraw) draw();
    else refresh();
    if (inboxKey(f) !== before) {
      cancelDebounce();
      cancelDebounce = deps.later(INBOX_DEBOUNCE_MS, readInbox);
    }
  };
  const harnessChange = (which: "main" | "orch", change: HarnessChange, redraw: boolean): void => {
    if (busy) return draw();
    delete errors[which];
    if (which === "main") delete errors["orch"];
    f = setHarness(f, c, which, change);
    if (redraw) draw();
    else refresh();
  };

  const drawChecklist = (): void => {
    const r = active();
    if (r.role !== "adjutant" || (result && !result.adjutant_pane)) {
      mount(live.checklist);
      return;
    }
    const s = inbox && inbox.key === inboxKey(r) ? inbox.state : null;
    const items = checklist(r, c, s, result?.attach_command);
    mount(
      live.checklist,
      h("h3", null, "Channel setup"),
      h(
        "ol",
        null,
        items.map((it, i) =>
          h(
            "li",
            { class: it.done ? "done" : "", dataset: { item: String(i) } },
            h("span", { class: "nr-check", "aria-hidden": "true" }, it.done ? icon(icons.check) : null),
            h(
              "span",
              { class: "nr-item" },
              h("span", null, it.title),
              it.code && i === 2
                ? h(
                    "span",
                    { class: "nr-attach" },
                    h("code", null, it.code),
                    h(
                      "button",
                      {
                        type: "button",
                        class: "chip-btn",
                        title: "Copy the command",
                        "aria-label": "Copy the command",
                        onclick: () => deps.copy(it.code ?? "", "attach command"),
                      },
                      icon(icons.copy),
                    ),
                  )
                : it.code
                  ? h("code", null, it.code)
                  : null,
              it.note ? h("small", null, it.note) : null,
            ),
            h("span", { class: `nr-who${it.who === "you do this" ? " you" : ""}` }, it.done ? "done" : it.who),
          ),
        ),
      ),
      s === null ? h("small", { class: "nr-note" }, "Reading the inbox…") : null,
    );
  };

  let startButton: HTMLButtonElement | null = null;
  // Updates what follows typing without redrawing the inputs under the caret.
  const refresh = (): void => {
    // Typing a new folder starts its GitHub probe: Start waits for it.
    if (startButton) {
      startButton.disabled = busy || ghBlocks() !== "";
      startButton.title = ghBlocks();
    }
    live.preview.textContent = preview(f, c).command;
    const project = projectLabel(f.folder) || "<project>";
    live.label.textContent =
      f.role === "adjutant" ? `Tabs ${project}-adjutant and ${project}-orchestrator` : `Tab ${project}-orchestrator`;
    if (live.inbox && !f.inboxEdited) live.inbox.value = f.inbox;
    for (const [name, box] of boxes) {
      const own = name === "checkpointer" ? checkpointerFields.some((k) => errors[k]) : !!errors[name];
      box.classList.toggle("invalid", own);
    }
    // Inside the checkpointer group, only the input at fault is marked.
    for (const k of ["minutes", "limit"]) boxes.get(`${k}-input`)?.classList.toggle("invalid", !!errors[k]);
    for (const [name, msg] of messages) {
      if (!errors[name]) {
        msg.remove();
        messages.delete(name);
      }
    }
    // An orchestrator still following the adjutant shows the typed model it
    // inherits; the input under the caret is never rewritten.
    if (!f.orchEdited) {
      for (const [key, value] of [
        ["orch-model", f.orch.model],
        ["orch-other", f.orch.other],
      ] as const) {
        const el = inputs.get(key);
        if (el && el !== document.activeElement && el.value !== value) el.value = value;
      }
    }
    if (!result) drawChecklist();
  };

  const errorLine = (name: string): HTMLElement | null => {
    if (!errors[name]) return null;
    const el = h("small", { class: "error", role: "alert", dataset: { error: name } }, errors[name]);
    messages.set(name, el);
    return el;
  };

  const field = (
    name: string,
    label: string,
    control: Node,
    hint?: Node | string | null,
    wide = false,
  ): HTMLElement => {
    const box = h(
      "div",
      { class: `field${wide ? " wide" : ""}${errors[name] ? " invalid" : ""}`, dataset: { field: name } },
      h("label", { for: `${id}-${name}` }, label),
      control,
      hint ? h("small", null, hint) : null,
      errorLine(name),
    );
    boxes.set(name, box);
    return box;
  };

  const text = (name: keyof RouteForm, extra: Record<string, unknown> = {}): HTMLInputElement =>
    h("input", {
      id: `${id}-${name}`,
      name,
      value: String(f[name]),
      autocomplete: "off",
      spellcheck: false,
      oninput: (e: Event) => set({ [name]: (e.target as HTMLInputElement).value } as Partial<RouteForm>, false),
      ...extra,
    });

  const numInput = (name: "minutes" | "limit", extra: Record<string, unknown> = {}): HTMLInputElement => {
    const el = text(name, { class: `nr-num${errors[name] ? " invalid" : ""}`, ...extra });
    boxes.set(`${name}-input`, el);
    return el;
  };

  const track = (key: string, el: HTMLInputElement): HTMLInputElement => {
    inputs.set(key, el);
    return el;
  };

  const harnessFields = (which: "main" | "orch", nameField: "name" | "orchName", who: string): HTMLElement[] => {
    const hn = f[which];
    // A role keeps its own saved pick (A30, per role); it is offered with
    // the live lists, and choosing from them makes the role fresh.
    const live = liveCatalog(c).kinds;
    const k = roleKind(f, c, which);
    const kinds: [string, string][] = live.map((x) => [x.kind, x.label]);
    if (k && !live.some((x) => x.kind === k.kind)) kinds.unshift([k.kind, k.label]);
    let model: Node;
    if (!k || k.free_model) {
      model = track(
        `${which}-model`,
        h("input", {
          id: `${id}-${which}-model`,
          name: `${which}-model`,
          value: hn.model,
          placeholder: "model id (optional)",
          autocomplete: "off",
          spellcheck: false,
          oninput: (e: Event) => harnessChange(which, { model: (e.target as HTMLInputElement).value }, false),
        }),
      );
    } else {
      const listed = [...k.models, ...(k.saved ? (kindOf(liveCatalog(c), k.kind)?.models ?? []) : [])];
      const opts: [string, string][] = listed
        .filter((m, i) => listed.findIndex((x) => x.id === m.id) === i)
        .map((m) => [m.id, m.label === m.id ? m.id : `${m.label} (${m.id})`]);
      if (k.other_model) opts.push([OTHER, "Other model id…"]);
      model = selectBox(
        `${which}-model`,
        hn.model,
        opts,
        (v) => harnessChange(which, { model: v }, true),
        `${id}-${which}-model`,
      );
    }
    const efforts = effortsFor(k, hn.model);
    const offline = k?.offline
      ? h(
          "span",
          {
            class: "nr-chip",
            title: `${k?.unavailable ?? "The harness could not be asked"}: it offers no models and starts with its own default.`,
          },
          "models unavailable",
        )
      : null;
    return [
      field(
        which,
        `${who} harness`,
        selectBox(`${which}-kind`, hn.kind, kinds, (v) => harnessChange(which, { kind: v }, true), `${id}-${which}`),
        "The coding agent program this role runs in.",
      ),
      field(
        `${which}-model`,
        "Model",
        h(
          "div",
          { class: "nr-row" },
          model,
          offline,
          k?.docs_url
            ? h(
                "a",
                { href: k.docs_url, target: "_blank", rel: "noopener", title: `${k.label}'s own page of its models` },
                "Model docs",
              )
            : k?.docs_command
              ? h("small", { class: "muted", dataset: { docs: which } }, `Model details: ${k.docs_command}`)
              : null,
        ),
        "The models this harness lists now, newest first; its docs say which suits coding and review.",
      ),
      hn.model === OTHER
        ? field(
            `${which}-other`,
            "Model id",
            track(
              `${which}-other`,
              h("input", {
                id: `${id}-${which}-other`,
                name: `${which}-other`,
                value: hn.other,
                placeholder: "a model from the harness's list",
                autocomplete: "off",
                spellcheck: false,
                oninput: (e: Event) => harnessChange(which, { other: (e.target as HTMLInputElement).value }, false),
              }),
            ),
          )
        : null,
      efforts.length
        ? field(
            `${which}-effort`,
            "Effort",
            selectBox(
              `${which}-effort`,
              hn.effort,
              effortOptions(k, hn.model),
              (v) => harnessChange(which, { effort: v }, which === "main" && f.role === "adjutant"),
              `${id}-${which}-effort`,
            ),
            "How long the model thinks before it answers: more is slower and costs more.",
          )
        : null,
      field(nameField, `${who} name`, text(nameField), "The agent's name in Herdr, used in its tab and its messages."),
    ].filter((x): x is HTMLElement => !!x);
  };

  // A32: the gh accounts that can read the folder's repository; the probed
  // one fills the confirmation until the user picks another.
  // The probe and the confirmed account belong to one folder: a new folder
  // clears both, and Start waits until the new probe answers and, when it
  // lists accounts, until the user picks one.
  let gh: GitHubAccess | null = null;
  let ghFolder = "";
  let ghPending = false;
  const probeGitHub = (): void => {
    const folder = f.folder.trim();
    if (!deps.github || folder === ghFolder) return;
    ghFolder = folder;
    gh = null;
    f = { ...f, ghAccount: "" };
    if (!folder.startsWith("/")) {
      ghPending = false;
      return;
    }
    ghPending = true;
    deps.github(folder).then(
      (g) => {
        if (ghFolder !== folder) return;
        gh = g;
        ghPending = false;
        draw();
      },
      () => {
        if (ghFolder !== folder) return;
        ghPending = false;
        draw();
      },
    );
  };
  /** Why Start must wait for the GitHub account, or "". */
  const ghBlocks = (): string => {
    if (!deps.github) return "";
    if (ghPending) return "Checking which GitHub account can read this repository…";
    if (gh && accountOptions(gh).length && !f.ghAccount) return "Confirm the GitHub account.";
    return "";
  };
  const githubField = (): HTMLElement | null => {
    if (ghPending) return field("ghAccount", "GitHub account", h("span", null, "Checking…"), null, true);
    if (!gh) return null;
    const opts = accountOptions(gh);
    if (!opts.length)
      return field(
        "ghAccount",
        "GitHub account",
        h("span", null, gh.error ?? `No gh account can read ${gh.repo}.`),
        "Log in with gh auth login, then reopen this form.",
        true,
      );
    return field(
      "ghAccount",
      "GitHub account",
      selectBox(
        "ghAccount",
        f.ghAccount,
        [["", "Confirm an account…"], ...opts],
        (v) => set({ ghAccount: v }, true),
        `${id}-ghAccount`,
      ),
      `The account the route pushes and opens pull requests with; it can read ${gh.repo}.`,
    );
  };

  // A30: the last picks of this route's roles, offered before the picking questions.
  const previousRow = (): HTMLElement | null => {
    const picks = previousPicks(f, c);
    if (!picks.length) return null;
    return h(
      "div",
      { class: "nr-previous" },
      h("span", null, `Previous choice: ${picks.map(pickSummary).join("; ")}`),
      h(
        "button",
        {
          type: "button",
          class: "chip-btn",
          onclick: () => {
            if (busy) return draw();
            f = usePrevious(f, c);
            delete errors["main"];
            delete errors["orch"];
            errors = { ...errors, ...missingPicks(f, c) };
            draw();
          },
        },
        "Use previous choice",
      ),
    );
  };

  const draw = (): void => {
    // A redraw keeps the keyboard focus on the control of the same name.
    const focused = (document.activeElement as HTMLElement | null)?.getAttribute?.("name");
    boxes = new Map();
    messages = new Map();
    inputs = new Map();
    result = null;
    const ws = st?.snapshot.workspaces ?? [];
    const ck = checkpointerAvailable(f, c);
    const ckOn = checkpointerOn(f, c);
    const folders = knownFolders(st);
    const role = (r: RouteForm["role"], label: string) =>
      h(
        "button",
        {
          type: "button",
          role: "radio",
          name: `role-${r}`,
          "aria-checked": String(f.role === r),
          class: f.role === r ? "active" : "",
          onclick: () => {
            f = setRole(f, r, c);
            draw();
            readInbox();
          },
        },
        label,
      );
    const ckBox = h(
      "div",
      { class: "field wide nr-ck", dataset: { field: "checkpointer" } },
      h(
        "label",
        { class: "nr-toggle" },
        h("input", {
          type: "checkbox",
          name: "checkpointer",
          checked: ckOn,
          disabled: !ck.ok,
          onchange: (e: Event) => set({ checkpointer: (e.target as HTMLInputElement).checked }, true),
        }),
        h("span", null, "Checkpointer (Recommended)"),
        h("small", null, ck.ok ? "keeps the orchestrator's memory and compacts its context" : ck.why),
      ),
      ckOn
        ? h(
            "div",
            { class: "nr-row" },
            h("label", { class: "nr-inline" }, "every", numInput("minutes", { inputmode: "numeric" }), "min"),
            h("label", { class: "nr-inline" }, "compact at", numInput("limit"), "tokens"),
          )
        : null,
      checkpointerFields.map(errorLine),
    );
    boxes.set("checkpointer", ckBox);
    mount(
      body,
      h(
        "div",
        { class: "nr-head" },
        h("h2", null, "New route"),
        h(
          "button",
          { value: "cancel", class: "icon-btn", title: "Close", "aria-label": "Close", formnovalidate: true },
          icon(icons.close),
        ),
      ),
      h(
        "div",
        { class: "nr-body" },
        h(
          "fieldset",
          { class: "nr-fields", disabled: busy },
          h(
            "label",
            { class: "nr-toggle nr-recommended" },
            h("input", {
              type: "checkbox",
              name: "recommended",
              checked: f.recommended,
              onchange: (e: Event) => {
                if (busy) return draw();
                f = setRecommended(f, c, (e.target as HTMLInputElement).checked);
                draw();
                readInbox();
              },
            }),
            h("span", null, "Recommended setup"),
            h("small", null, "Fills the usual choices below so only the tasks and each role's model are left to pick."),
          ),
          f.recommended
            ? h(
                "dl",
                { class: "nr-summary", dataset: { field: "recommended-summary" } },
                // The effective settings: the checkpointer reads Off when the
                // route will start none.
                RECOMMENDED_SETUP.flatMap(([k, v]) => [
                  h("dt", null, k),
                  h(
                    "dd",
                    { dataset: { item: k } },
                    k === "Checkpointer" && !ckOn
                      ? `Off: ${c.checkpointer_why || "the checkpointer is not available here"}`
                      : v,
                  ),
                ]),
              )
            : h(
                "div",
                null,
                h(
                  "div",
                  { class: "nr-role", role: "radiogroup", "aria-label": "Role" },
                  role("orchestrator", "Orchestrator"),
                  role("adjutant", "Adjutant (Recommended)"),
                ),
                // The custom path asks every setup item; an empty answer lets the route ask.
                h(
                  "div",
                  { class: "nr-grid nr-custom", dataset: { field: "custom-setup" } },
                  SETUP_QUESTIONS.map(([item, recommended]) =>
                    field(
                      `custom-${item}`,
                      item,
                      h("input", {
                        id: `${id}-custom-${item}`,
                        name: `custom-${item}`,
                        value: f.custom[item] ?? "",
                        maxlength: 400,
                        autocomplete: "off",
                        placeholder: recommended,
                        oninput: (e: Event) =>
                          set({ custom: { ...f.custom, [item]: (e.target as HTMLInputElement).value } }, false),
                      }),
                      "Empty: the route asks you.",
                      true,
                    ),
                  ),
                ),
              ),
          h(
            "div",
            { class: "nr-grid" },
            field(
              "workspace",
              "Workspace",
              selectBox(
                "workspace",
                f.workspace,
                ws.map((w) => [
                  w.workspace_id,
                  `${w.label || `Workspace ${w.number}`}${w.focused ? " (focused)" : ""}`,
                ]),
                (v) => set({ workspace: v }, true),
                `${id}-workspace`,
              ),
              "The Herdr workspace the route's tabs open in.",
            ),
            field(
              "folder",
              "Folder",
              h(
                "span",
                { class: "nr-folder" },
                text("folder", { list: `${id}-folders`, placeholder: "/path/to/repo" }),
                h(
                  "datalist",
                  { id: `${id}-folders` },
                  folders.map((p) => h("option", { value: p })),
                ),
              ),
              live.label,
              true,
            ),
            githubField(),
            previousRow(),
            f.role === "adjutant"
              ? [
                  h("h3", { class: "nr-sub" }, "Adjutant"),
                  ...harnessFields("main", "name", "Adjutant"),
                  h("h3", { class: "nr-sub" }, "Orchestrator"),
                  ...harnessFields("orch", "orchName", "Orchestrator"),
                  field("routeName", "Route name", text("routeName"), "Names the route's folder and its inbox."),
                  field(
                    "inbox",
                    "Private inbox",
                    h(
                      "span",
                      { class: "nr-inbox" },
                      (live.inbox = text("inbox")),
                      h(
                        "button",
                        {
                          type: "button",
                          class: "chip-btn",
                          title: "Copy the inbox path",
                          "aria-label": "Copy the inbox path",
                          onclick: () => deps.copy(f.inbox, "inbox path"),
                        },
                        icon(icons.copy),
                      ),
                    ),
                    "One inbox per route. The app never creates or writes it.",
                    true,
                  ),
                ]
              : harnessFields("main", "name", "Orchestrator"),
            field(
              "request",
              "Route request",
              h("textarea", {
                id: `${id}-request`,
                name: "request",
                rows: 4,
                placeholder: "What should this route do?",
                value: f.request,
                oninput: (e: Event) => set({ request: (e.target as HTMLTextAreaElement).value }, false),
              }),
              "The tasks, in your words; the orchestrator plans them and asks before it starts anything.",
              true,
            ),
            f.recommended ? null : ckBox,
          ),
        ),
        skillWarnings(f, c).map((w) => h("p", { class: "nr-warn", role: "note" }, w)),
        h("div", { class: "nr-sends" }, h("span", null, "Sends"), live.preview),
        live.checklist,
      ),
      h(
        "div",
        { class: "dialog-actions nr-actions" },
        h("button", { value: "cancel", class: "btn ghost", formnovalidate: true }, "Cancel"),
        (startButton = h(
          "button",
          {
            type: "submit",
            class: "btn primary",
            disabled: busy || ghBlocks() !== "",
            title: ghBlocks(),
            name: "start",
          },
          busy ? "Starting…" : "Start route",
        ) as HTMLButtonElement),
      ),
    );
    refresh();
    if (focused) body.querySelector?.<HTMLElement>(`[name="${focused.replace(/["\\]/g, "")}"]`)?.focus();
  };

  const showResult = (r: RouteResult): void => {
    result = r;
    const mark = { ok: icons.check, failed: icons.close, skipped: null } as const;
    const pane = r.orchestrator_pane;
    mount(
      body,
      h(
        "div",
        { class: "nr-head" },
        h("h2", null, r.ok ? "Route started" : "The route stopped part way"),
        h("button", { value: "cancel", class: "icon-btn", title: "Close", "aria-label": "Close" }, icon(icons.close)),
      ),
      h(
        "div",
        { class: "nr-body" },
        r.ok ? null : h("p", { class: "error" }, r.error ?? "A step failed."),
        h(
          "ol",
          { class: "nr-steps" },
          r.steps.map((s) =>
            h(
              "li",
              { class: `s-${s.status}` },
              h(
                "span",
                { class: "nr-check", "aria-hidden": "true" },
                mark[s.status] ? icon(mark[s.status] as string[]) : "–",
              ),
              h(
                "span",
                { class: "nr-item" },
                h("span", null, s.step),
                s.status === "ok" ? null : h("small", null, stepNote(s)),
              ),
              s.id ? h("code", null, s.id) : null,
            ),
          ),
        ),
        r.ok
          ? null
          : h("p", { class: "nr-note" }, "Nothing was closed: the tabs and panes created so far stay as they are."),
        live.checklist,
      ),
      h(
        "div",
        { class: "dialog-actions nr-actions" },
        r.ok
          ? null
          : h(
              "button",
              {
                type: "button",
                class: "btn ghost",
                onclick: () => {
                  busy = false;
                  submitted = null;
                  draw();
                },
              },
              "Back to the form",
            ),
        pane
          ? h(
              "button",
              {
                type: "button",
                class: "btn ghost",
                onclick: () => {
                  deps.close();
                  deps.openPane(pane);
                },
              },
              "Open the orchestrator",
            )
          : null,
        h("button", { value: "ok", class: "btn primary" }, r.ok ? "Done" : "Close"),
      ),
    );
    drawChecklist();
    readInbox();
  };

  body.addEventListener("submit", (e) => {
    const submitter = (e as SubmitEvent).submitter as HTMLButtonElement | null;
    if (submitter?.value === "cancel" || submitter?.value === "ok") return;
    e.preventDefault();
    if (busy) return;
    if (ghBlocks()) return draw();
    errors = validate(f, c, deps.state());
    if (Object.keys(errors).length) {
      draw();
      return;
    }
    busy = true;
    submitted = f;
    draw();
    deps.createRoute(toRequest(f, c)).then(
      (r) => {
        busy = false;
        if (r.code === "invalid_route" || r.code === "bad_request") {
          submitted = null;
          errors = { [formField(r.field) || "request"]: r.error ?? "Refused." };
          draw();
          return;
        }
        showResult(r);
      },
      (err: unknown) => {
        busy = false;
        submitted = null;
        deps.failed(err);
        draw();
      },
    );
  });

  probeGitHub();
  draw();
  readInbox();
  return {
    stop: () => {
      stopPoll();
      cancelDebounce();
    },
    form: () => f,
    sent: () => busy || submitted !== null,
    // The probed lists arrived: the choices and questions change in place;
    // the draft (task, folder, names, account) stays.
    setCatalog: (next: Catalog) => {
      c = { ...next, previous: next.previous ?? c.previous ?? [] };
      f = freshFromLists(f, c);
      delete errors["main"];
      delete errors["orch"];
      errors = { ...errors, ...missingPicks(f, c) };
      draw();
    },
  };
}
