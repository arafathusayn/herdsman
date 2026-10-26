// The New route form inside its dialog. Everything outside the DOM (the
// server calls, the live state, navigation, toasts) comes in through Deps,
// so the form can be driven in tests without a server or the app's store.
import { icon, type IconNode, icons } from "../dom.ts";
import {
  attr,
  child,
  cls,
  html,
  list,
  type ListNode,
  on,
  prop,
  renderDirect,
  text,
  type ViewNodeOf,
} from "../herdsman-blocks-view.ts";
import {
  checklist,
  type ChecklistItem,
  checkpointerAvailable,
  checkpointerOn,
  effortsFor,
  formField,
  type HarnessChange,
  inboxKey,
  initialForm,
  kindOf,
  knownFolders,
  OTHER,
  preview,
  projectLabel,
  type RouteForm,
  setHarness,
  setRole,
  skillWarnings,
  stepNote,
  toRequest,
  update,
  validate,
} from "../new-route.ts";
import type { Catalog, InboxState, RouteRequest, RouteResult, RouteStep, State } from "../types.ts";

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
}

/** How often an open adjutant checklist rereads its inbox, so human steps show as they happen. */
export const INBOX_POLL_MS = 5000;
/** Typing in the inbox, folder, route name or a name waits this long before a reread. */
export const INBOX_DEBOUNCE_MS = 400;

// ---------------------------------------------------------------------------
// Controls
// ---------------------------------------------------------------------------

const Option = html`<option value=${attr()} .selected=${prop<HTMLOptionElement, "selected">()}>${text()}</option>`;
const SelectBox = html`<span class="select"
  ><select name=${attr()} id=${attr()} onchange=${on<"change", HTMLSelectElement>()}>
    ${child<ListNode<ViewNodeOf<typeof Option>, string>>()}</select
  >${child<IconNode>()}</span
>`;
type SelectNode = ViewNodeOf<typeof SelectBox>;

/** A select of [value, label] options, keyed by value; onchange gets the chosen value. */
function selectBox(
  name: string,
  id: string,
  value: string,
  options: readonly (readonly [string, string])[],
  onchange: (this: HTMLSelectElement) => void,
): SelectNode {
  return SelectBox([
    name,
    id,
    onchange,
    list(
      // An option is keyed by its value, so a repeated value shows once.
      [...new Map(options.map(([v, label]) => [v, label] as const))],
      ([v]) => v,
      ([v, label]) => Option([v, v === value, label]),
    ),
    icon(icons.chevronDown),
  ]);
}

// One template for every text input; the optional attributes are null when absent.
const TextInput = html`<input
  id=${attr()}
  name=${attr()}
  class=${cls()}
  placeholder=${attr<string | null>()}
  list=${attr<string | null>()}
  inputmode=${attr<"numeric" | null>()}
  autocomplete="off"
  .value=${prop<HTMLInputElement, "value">()}
  oninput=${on<"input", HTMLInputElement>()}
/>`;
type InputNode = ViewNodeOf<typeof TextInput>;

interface InputOpts {
  cls?: string;
  placeholder?: string;
  list?: string;
  inputmode?: "numeric";
}

function textInput(
  id: string,
  name: string,
  value: string,
  oninput: (this: HTMLInputElement) => void,
  o: InputOpts = {},
): InputNode {
  return TextInput([id, name, o.cls ?? "", o.placeholder ?? null, o.list ?? null, o.inputmode ?? null, value, oninput]);
}

const Request = html`<textarea
  id=${attr()}
  name="request"
  rows="4"
  placeholder="What should this route do?"
  .value=${prop<HTMLTextAreaElement, "value">()}
  oninput=${on<"input", HTMLTextAreaElement>()}
></textarea>`;

const FolderOption = html`<option value=${attr()}></option>`;
const Folder = html`<span class="nr-folder"
  >${child<InputNode>()}<datalist id=${attr()}>
    ${child<ListNode<ViewNodeOf<typeof FolderOption>, string>>()}
  </datalist></span
>`;

const CopyButton = html`<button
  type="button"
  class="chip-btn"
  title=${attr()}
  aria-label=${attr()}
  onclick=${on<"click">()}
>
  ${child<IconNode>()}
</button>`;
const Inbox = html`<span class="nr-inbox">${child<InputNode>()}${child<ViewNodeOf<typeof CopyButton>>()}</span>`;

const Offline = html`<span class="nr-chip" title="The harness could not be asked; this is the last-known list."
  >offline list</span
>`;
const ModelRow = html`<div class="nr-row">
  ${child<SelectNode | InputNode>()}${child<ViewNodeOf<typeof Offline> | null>()}
</div>`;

type Control =
  | SelectNode
  | InputNode
  | ViewNodeOf<typeof Request>
  | ViewNodeOf<typeof Folder>
  | ViewNodeOf<typeof Inbox>
  | ViewNodeOf<typeof ModelRow>;

// ---------------------------------------------------------------------------
// Fields
// ---------------------------------------------------------------------------

const Hint = html`<small>${text()}</small>`;
// The folder's hint is the tab label, a <small> in the hint's <small>, as before.
const TabsHint = html`<small><small>${text()}</small></small>`;
const ErrorLine = html`<small class="error" role="alert" data-error=${attr()}>${text()}</small>`;
type ErrorNode = ViewNodeOf<typeof ErrorLine>;

const Field = html`<div class=${cls()} data-field=${attr()}>
  <label for=${attr()}>${text()}</label>${child<Control>()}${child<
    ViewNodeOf<typeof Hint> | ViewNodeOf<typeof TabsHint> | null
  >()}${child<ErrorNode | null>()}
</div>`;
type FieldNode = ViewNodeOf<typeof Field>;

const Sub = html`<h3 class="nr-sub">${text()}</h3>`;

const CkRow = html`<div class="nr-row">
  <label class="nr-inline">every${child<InputNode>()}min</label
  ><label class="nr-inline">compact at${child<InputNode>()}tokens</label>
</div>`;
const CkBox = html`<div class=${cls()} data-field="checkpointer">
  <label class="nr-toggle"
    ><input
      type="checkbox"
      name="checkpointer"
      disabled=${attr<boolean>()}
      .checked=${prop<HTMLInputElement, "checked">()}
      onchange=${on<"change", HTMLInputElement>()}
    /><span>Checkpointer</span><small>${text()}</small></label
  >${child<ViewNodeOf<typeof CkRow> | null>()}${child<ListNode<ErrorNode, string>>()}
</div>`;

type GridNode = FieldNode | ViewNodeOf<typeof Sub> | ViewNodeOf<typeof CkBox>;

// ---------------------------------------------------------------------------
// The channel checklist
// ---------------------------------------------------------------------------

const Code = html`<code>${text()}</code>`;
const Attach = html`<span class="nr-attach"><code>${text()}</code>${child<ViewNodeOf<typeof CopyButton>>()}</span>`;
const CheckItem = html`<li class=${cls()} data-item=${attr()}>
  <span class="nr-check" aria-hidden="true">${child<IconNode | null>()}</span
  ><span class="nr-item"
    ><span>${text()}</span>${child<ViewNodeOf<typeof Attach> | ViewNodeOf<typeof Code> | null>()}${child<ViewNodeOf<
      typeof Hint
    > | null>()}</span
  ><span class=${cls()}>${text()}</span>
</li>`;
const CheckTitle = html`<h3>Channel setup</h3>`;
const CheckList = html`<ol>
  ${child<ListNode<ViewNodeOf<typeof CheckItem>, string>>()}
</ol>`;
const Reading = html`<small class="nr-note">Reading the inbox…</small>`;
const Checklist = html`<section class="nr-checklist" aria-label="Channel setup">
  ${child<ViewNodeOf<typeof CheckTitle> | null>()}${child<ViewNodeOf<typeof CheckList> | null>()}${child<ViewNodeOf<
    typeof Reading
  > | null>()}
</section>`;
type ChecklistNode = ViewNodeOf<typeof Checklist>;

// ---------------------------------------------------------------------------
// The two screens: the form, and the result of a start
// ---------------------------------------------------------------------------

const Head = html`<div class="nr-head">
  <h2>${text()}</h2>
  <button value="cancel" class="icon-btn" title="Close" aria-label="Close" formnovalidate=${attr<boolean>()}>
    ${child<IconNode>()}
  </button>
</div>`;

const RoleButton = html`<button
  type="button"
  role="radio"
  name=${attr()}
  aria-checked=${attr<"true" | "false">()}
  class=${cls()}
  onclick=${on<"click">()}
>
  ${text()}
</button>`;
const Warning = html`<p class="nr-warn" role="note">${text()}</p>`;
const FormBody = html`<div class="nr-body">
  <fieldset class="nr-fields" disabled=${attr<boolean>()}>
    <div class="nr-role" role="radiogroup" aria-label="Role">
      ${child<ListNode<ViewNodeOf<typeof RoleButton>, RouteForm["role"]>>()}
    </div>
    <div class="nr-grid">${child<ListNode<GridNode, string>>()}</div>
  </fieldset>
  ${child<ListNode<ViewNodeOf<typeof Warning>, string>>()}
  <div class="nr-sends">
    <span>Sends</span>
    <pre class="nr-preview">${text()}</pre>
  </div>
  ${child<ChecklistNode>()}
</div>`;
const FormActions = html`<div class="dialog-actions nr-actions">
  <button value="cancel" class="btn ghost" formnovalidate>Cancel</button>
  <button type="submit" class="btn primary" disabled=${attr<boolean>()} name="start">${text()}</button>
</div>`;

const Step = html`<li class=${cls()}>
  <span class="nr-check" aria-hidden="true">${child<IconNode | string>()}</span
  ><span class="nr-item"><span>${text()}</span>${child<ViewNodeOf<typeof Hint> | null>()}</span
  >${child<ViewNodeOf<typeof Code> | null>()}
</li>`;
const Failed = html`<p class="error">${text()}</p>`;
// Prose is a text hole: the formatter wraps text in the markup, and a wrapped
// line keeps its line break and indent as text.
const Note = html`<p class="nr-note">${text()}</p>`;
const ResultBody = html`<div class="nr-body">
  ${child<ViewNodeOf<typeof Failed> | null>()}
  <ol class="nr-steps">
    ${child<ListNode<ViewNodeOf<typeof Step>, string>>()}
  </ol>
  ${child<ViewNodeOf<typeof Note> | null>()}${child<ChecklistNode>()}
</div>`;
const ActionButton = html`<button type="button" class="btn ghost" onclick=${on<"click">()}>${text()}</button>`;
const ResultActions = html`<div class="dialog-actions nr-actions">
  ${child<ViewNodeOf<typeof ActionButton> | null>()}${child<ViewNodeOf<typeof ActionButton> | null>()}
  <button value="ok" class="btn primary">${text()}</button>
</div>`;

// The form element: the dialog's form, with the head, body and actions of
// one screen. The submit handler is typed for HTMLElement: on<"submit",
// HTMLFormElement>() does not type-check (the form's string index signature
// hides its handler names).
const Root = html`<form method="dialog" novalidate onsubmit=${on<"submit">()}>
  ${child<ViewNodeOf<typeof Head>>()}${child<ViewNodeOf<typeof FormBody> | ViewNodeOf<typeof ResultBody>>()}${child<
    ViewNodeOf<typeof FormActions> | ViewNodeOf<typeof ResultActions>
  >()}
</form>`;

/** What `routeForm` renders: the dialog's form. */
export type RouteFormNode = ViewNodeOf<typeof Root>;

// A checked narrowing, not a cast.
const isForm = (n: Node | null): n is HTMLFormElement => n !== null && n.nodeName === "FORM";

let uid = 0;

/** The checkpointer group's own messages, each shown under the group. */
const checkpointerFields = ["checkpointer", "minutes", "limit"] as const;

/** The fields typed into a text input. */
type TextKey = "folder" | "name" | "orchName" | "routeName" | "inbox" | "minutes" | "limit";

/**
 * Builds the form and puts it in body's place (body is the dialog's
 * placeholder form; it is left detached), focuses its first empty field and
 * returns the form element, a stop function for its timers and, for tests,
 * the current form.
 */
export function routeForm(
  body: HTMLElement,
  c: Catalog,
  st: State | null,
  deps: Deps,
): { el: HTMLFormElement; stop(): void; form(): RouteForm } {
  let f: RouteForm = initialForm(c, st);
  let errors: Record<string, string> = {};
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

  const readInbox = (): void => {
    const r = active();
    if (r.role !== "adjutant") return;
    const key = inboxKey(r);
    const seq = ++reqSeq;
    deps.inboxState(r.inbox, r.name, r.orchName).then(
      (s) => {
        // A reply for an older request or another inbox or pair is outdated.
        if (seq !== reqSeq || key !== inboxKey(active())) return;
        inbox = { key, state: s };
        draw();
      },
      () => {
        if (seq !== reqSeq) return;
        inbox = null;
        draw();
      },
    );
  };
  const stopPoll = deps.every(INBOX_POLL_MS, readInbox);

  const set = (change: Partial<RouteForm>): void => {
    const before = inboxKey(f);
    f = update(f, change);
    for (const k of Object.keys(change)) delete errors[k];
    draw();
    if (inboxKey(f) !== before) {
      cancelDebounce();
      cancelDebounce = deps.later(INBOX_DEBOUNCE_MS, readInbox);
    }
  };
  // The fields are frozen while the route is being created: an edit then is
  // undone in the control, since the form's state did not change.
  const typed = (name: TextKey) =>
    function (this: HTMLInputElement): void {
      if (busy) {
        this.value = f[name];
        return;
      }
      const change: Partial<RouteForm> = {};
      change[name] = this.value;
      set(change);
    };
  const harnessChange = (which: "main" | "orch", change: HarnessChange): void => {
    delete errors[which];
    if (which === "main") delete errors["orch"];
    f = setHarness(f, c, which, change);
    draw();
  };
  const harnessSelect = (which: "main" | "orch", key: "kind" | "model" | "effort") =>
    function (this: HTMLSelectElement): void {
      if (busy) {
        this.value = f[which][key];
        return;
      }
      harnessChange(which, { [key]: this.value });
    };
  const harnessTyped = (which: "main" | "orch", key: "model" | "other") =>
    function (this: HTMLInputElement): void {
      if (busy) {
        this.value = f[which][key];
        return;
      }
      harnessChange(which, { [key]: this.value });
    };

  const errorLine = (name: string): ErrorNode | null => {
    const e = errors[name];
    return e ? ErrorLine([name, e]) : null;
  };

  const field = (
    name: string,
    label: string,
    control: Control,
    hint: ViewNodeOf<typeof Hint> | ViewNodeOf<typeof TabsHint> | null = null,
    wide = false,
  ): { key: string; node: GridNode } => ({
    key: name,
    node: Field([
      `field${wide ? " wide" : ""}${errors[name] ? " invalid" : ""}`,
      name,
      `${id}-${name}`,
      label,
      control,
      hint,
      errorLine(name),
    ]),
  });

  const input = (name: TextKey, o: InputOpts = {}): InputNode =>
    textInput(`${id}-${name}`, name, f[name], typed(name), o);

  const numInput = (name: "minutes" | "limit", o: InputOpts = {}): InputNode =>
    input(name, { cls: `nr-num${errors[name] ? " invalid" : ""}`, ...o });

  const harnessFields = (
    which: "main" | "orch",
    nameField: "name" | "orchName",
    who: string,
  ): { key: string; node: GridNode }[] => {
    const hn = f[which];
    const k = kindOf(c, hn.kind);
    let model: SelectNode | InputNode;
    if (!k || k.free_model) {
      model = textInput(`${id}-${which}-model`, `${which}-model`, hn.model, harnessTyped(which, "model"), {
        placeholder: "model id (optional)",
      });
    } else {
      const opts: [string, string][] = k.models.map((m) => [m.id, m.label === m.id ? m.id : `${m.label} (${m.id})`]);
      if (k.other_model) opts.push([OTHER, "Other model id…"]);
      model = selectBox(`${which}-model`, `${id}-${which}-model`, hn.model, opts, harnessSelect(which, "model"));
    }
    const efforts = effortsFor(k, hn.model);
    const out = [
      field(
        which,
        `${who} harness`,
        selectBox(
          `${which}-kind`,
          `${id}-${which}`,
          hn.kind,
          c.kinds.map((x) => [x.kind, x.label]),
          harnessSelect(which, "kind"),
        ),
      ),
      field(`${which}-model`, "Model", ModelRow([model, k?.offline ? Offline([]) : null])),
    ];
    if (hn.model === OTHER)
      out.push(
        field(
          `${which}-other`,
          "Model id",
          textInput(`${id}-${which}-other`, `${which}-other`, hn.other, harnessTyped(which, "other"), {
            placeholder: "claude-opus-4-1",
          }),
        ),
      );
    if (efforts.length)
      out.push(
        field(
          `${which}-effort`,
          "Effort",
          selectBox(
            `${which}-effort`,
            `${id}-${which}-effort`,
            hn.effort,
            efforts.map((e) => [e, e]),
            harnessSelect(which, "effort"),
          ),
        ),
      );
    out.push(field(nameField, `${who} name`, input(nameField)));
    return out;
  };

  const checklistView = (): ChecklistNode => {
    const r = active();
    if (r.role !== "adjutant" || (result && !result.adjutant_pane)) return Checklist([null, null, null]);
    const s = inbox && inbox.key === inboxKey(r) ? inbox.state : null;
    const items = checklist(r, c, s, result?.attach_command).map((it, i) => ({ it, i }));
    const item = ({ it, i }: { it: ChecklistItem; i: number }): ViewNodeOf<typeof CheckItem> =>
      CheckItem([
        it.done ? "done" : "",
        String(i),
        it.done ? icon(icons.check) : null,
        it.title,
        it.code && i === 2
          ? Attach([
              it.code,
              CopyButton([
                "Copy the command",
                "Copy the command",
                () => deps.copy(it.code ?? "", "attach command"),
                icon(icons.copy),
              ]),
            ])
          : it.code
            ? Code([it.code])
            : null,
        it.note ? Hint([it.note]) : null,
        `nr-who${it.who === "you do this" ? " you" : ""}`,
        it.done ? "done" : it.who,
      ]);
    return Checklist([
      CheckTitle([]),
      CheckList([list(items, ({ it }) => it.title, item)]),
      s === null ? Reading([]) : null,
    ]);
  };

  const ckBox = (): { key: string; node: GridNode } => {
    const ck = checkpointerAvailable(f, c);
    const ckOn = checkpointerOn(f, c);
    const own = checkpointerFields.some((k) => errors[k]);
    return {
      key: "checkpointer",
      node: CkBox([
        `field wide nr-ck${own ? " invalid" : ""}`,
        !ck.ok,
        ckOn,
        function () {
          if (busy) {
            this.checked = ckOn;
            return;
          }
          set({ checkpointer: this.checked });
        },
        ck.ok ? "keeps the orchestrator's memory and compacts its context" : ck.why,
        ckOn ? CkRow([numInput("minutes", { inputmode: "numeric" }), numInput("limit")]) : null,
        list(
          checkpointerFields.filter((k) => errors[k]),
          (k) => k,
          (k) => ErrorLine([k, errors[k] ?? ""]),
        ),
      ]),
    };
  };

  const roles = [
    { r: "orchestrator", label: "Orchestrator" },
    { r: "adjutant", label: "Adjutant" },
  ] as const;

  const formScreen = (): RouteFormNode => {
    const ws = st?.snapshot.workspaces ?? [];
    const project = projectLabel(f.folder) || "<project>";
    const grid: { key: string; node: GridNode }[] = [
      field(
        "workspace",
        "Workspace",
        selectBox(
          "workspace",
          `${id}-workspace`,
          f.workspace,
          ws.map((w) => [w.workspace_id, `${w.label || `Workspace ${w.number}`}${w.focused ? " (focused)" : ""}`]),
          function () {
            if (busy) {
              this.value = f.workspace;
              return;
            }
            set({ workspace: this.value });
          },
        ),
      ),
      field(
        "folder",
        "Folder",
        Folder([
          input("folder", { list: `${id}-folders`, placeholder: "/path/to/repo" }),
          `${id}-folders`,
          list(
            [...new Set(knownFolders(st))],
            (p) => p,
            (p) => FolderOption([p]),
          ),
        ]),
        TabsHint([
          f.role === "adjutant"
            ? `Tabs ${project}-adjutant and ${project}-orchestrator`
            : `Tab ${project}-orchestrator`,
        ]),
        true,
      ),
    ];
    if (f.role === "adjutant") {
      grid.push(
        { key: "sub-adjutant", node: Sub(["Adjutant"]) },
        ...harnessFields("main", "name", "Adjutant"),
        { key: "sub-orchestrator", node: Sub(["Orchestrator"]) },
        ...harnessFields("orch", "orchName", "Orchestrator"),
        field("routeName", "Route name", input("routeName")),
        field(
          "inbox",
          "Private inbox",
          Inbox([
            input("inbox"),
            CopyButton([
              "Copy the inbox path",
              "Copy the inbox path",
              () => deps.copy(f.inbox, "inbox path"),
              icon(icons.copy),
            ]),
          ]),
          Hint(["One inbox per route. The app never creates or writes it."]),
          true,
        ),
      );
    } else grid.push(...harnessFields("main", "name", "Orchestrator"));
    grid.push(
      field(
        "request",
        "Route request",
        Request([
          `${id}-request`,
          f.request,
          function () {
            if (busy) {
              this.value = f.request;
              return;
            }
            set({ request: this.value });
          },
        ]),
        null,
        true,
      ),
      ckBox(),
    );
    return Root([
      submit,
      Head(["New route", true, icon(icons.close)]),
      FormBody([
        busy,
        list(
          roles,
          (x) => x.r,
          (x) =>
            RoleButton([
              `role-${x.r}`,
              f.role === x.r ? "true" : "false",
              f.role === x.r ? "active" : "",
              () => {
                f = setRole(f, x.r);
                draw();
                readInbox();
              },
              x.label,
            ]),
        ),
        list(
          grid,
          (g) => g.key,
          (g) => g.node,
        ),
        list(
          [...new Set(skillWarnings(f, c))],
          (w) => w,
          (w) => Warning([w]),
        ),
        preview(f, c).command,
        checklistView(),
      ]),
      FormActions([busy, busy ? "Starting…" : "Start route"]),
    ]);
  };

  const mark = { ok: icons.check, failed: icons.close, skipped: null } as const;
  const step = (s: RouteStep): ViewNodeOf<typeof Step> => {
    const glyph = mark[s.status];
    return Step([
      `s-${s.status}`,
      glyph ? icon(glyph) : "–",
      s.step,
      s.status === "ok" ? null : Hint([stepNote(s)]),
      s.id ? Code([s.id]) : null,
    ]);
  };

  const resultScreen = (r: RouteResult): RouteFormNode => {
    const pane = r.orchestrator_pane;
    return Root([
      submit,
      Head([r.ok ? "Route started" : "The route stopped part way", false, icon(icons.close)]),
      ResultBody([
        r.ok ? null : Failed([r.error ?? "A step failed."]),
        // Step names are unique in a start: each names its tab, agent or pane.
        list(r.steps, (s) => `${s.method}|${s.step}`, step),
        r.ok ? null : Note(["Nothing was closed: the tabs and panes created so far stay as they are."]),
        checklistView(),
      ]),
      ResultActions([
        r.ok
          ? null
          : ActionButton([
              () => {
                busy = false;
                submitted = null;
                result = null;
                draw();
              },
              "Back to the form",
            ]),
        pane
          ? ActionButton([
              () => {
                deps.close();
                deps.openPane(pane);
              },
              "Open the orchestrator",
            ])
          : null,
        r.ok ? "Done" : "Close",
      ]),
    ]);
  };

  // The form is the view's root (its head, body and actions are the dialog
  // form's own children: a view cannot fill a container with several roots),
  // so it is rendered in a holder and put in body's place. renderDirect, not
  // render: render's delegated handlers listen on the holder, which the form
  // leaves. Later renders of the same view patch it where it is.
  const holder = document.createElement("div");
  const draw = (): void => {
    // A control a render replaced hands the keyboard focus to its successor of the same name.
    const was = document.activeElement;
    const focused = was?.getAttribute?.("name");
    renderDirect(holder, result ? resultScreen(result) : formScreen());
    if (focused && was && !was.isConnected) {
      el.querySelector?.<HTMLElement>(`[name="${focused.replace(/["\\]/g, "")}"]`)?.focus();
    }
  };

  function submit(this: HTMLElement, e: SubmitEvent): void {
    const v = e.submitter?.getAttribute("value");
    if (v === "cancel" || v === "ok") return;
    e.preventDefault();
    if (busy) return;
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
        result = r;
        draw();
        readInbox();
      },
      (err: unknown) => {
        busy = false;
        submitted = null;
        deps.failed(err);
        draw();
      },
    );
  }

  renderDirect(holder, formScreen());
  const root = holder.firstChild;
  if (!isForm(root)) throw new Error("route form: the view's root is not a form");
  const el = root;
  body.parentNode?.replaceChild(el, body);
  el.querySelector?.<HTMLElement>(f.folder ? "textarea" : "input[name=folder]")?.focus();
  readInbox();
  return {
    el,
    stop: () => {
      stopPoll();
      cancelDebounce();
    },
    form: () => f,
  };
}
