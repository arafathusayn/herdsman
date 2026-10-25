// The New route dialog: pick a folder, type the request, press Start. It
// starts an orchestrator (or an adjutant with its orchestrator) in new
// herdr tabs through the server, then shows each step and, for an adjutant,
// the channel setup checklist. The steps that belong to the human stay the
// human's: the app never approves a pair, writes the inbox or types into
// the adjutant's pane.
import { AuthError, catalog, createRoute, inboxState } from "../api.ts";
import { h, icon, icons, mount } from "../dom.ts";
import {
  checkpointerAvailable,
  checkpointerOn,
  effortsFor,
  formField,
  type Harness,
  initialForm,
  kindOf,
  knownFolders,
  OTHER,
  pickHarness,
  preview,
  projectLabel,
  type RouteForm,
  setRole,
  skillWarnings,
  toRequest,
  update,
  validate,
} from "../new-route.ts";
import { store } from "../store.ts";
import type { Catalog, InboxState, RouteResult, State } from "../types.ts";
import { dismissOnBackdrop, toast } from "../ui.ts";

async function copy(text: string, what: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text);
    toast(`Copied the ${what}.`);
  } catch {
    toast("The browser blocked the clipboard.", "error");
  }
}

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

/** Opens the New route dialog. */
export function openNewRoute(): void {
  const dlg = h("dialog", { class: "dialog new-route", "aria-label": "New route" });
  const body = h("form", { method: "dialog", novalidate: true });
  dlg.append(body);
  dlg.addEventListener("close", () => dlg.remove());
  dismissOnBackdrop(dlg);
  document.body.append(dlg);
  mount(body, h("h2", null, "New route"), h("p", { class: "nr-loading" }, "Reading the harness lists…"));
  dlg.showModal();
  catalog().then(
    (c) => newRouteForm(dlg, body, c, store.app.server),
    (e: unknown) => {
      if (e instanceof AuthError) store.set({ fatal: e.message });
      mount(
        body,
        h("h2", null, "New route"),
        h("p", { class: "error" }, e instanceof Error ? e.message : String(e)),
        h("div", { class: "dialog-actions" }, h("button", { value: "cancel", class: "btn ghost" }, "Close")),
      );
    },
  );
}

function newRouteForm(dlg: HTMLDialogElement, body: HTMLFormElement, c: Catalog, st: State | null): void {
  let f: RouteForm = initialForm(c, st);
  let errors: Record<string, string> = {};
  let busy = false;
  let inbox: InboxState | null = null;
  let inboxFor = "";
  const id = `nr${++uid}`;
  const live = {
    preview: h("pre", { class: "nr-preview" }),
    label: h("small", null),
    inbox: null as HTMLInputElement | null,
  };

  const set = (change: Partial<RouteForm>, redraw: boolean): void => {
    f = update(f, change);
    for (const k of Object.keys(change)) delete errors[k];
    if (redraw) draw();
    else refresh();
  };
  const setHarness = (which: "main" | "orch", change: Partial<Pick<Harness, "kind" | "model">>): void => {
    delete errors[which];
    f = { ...f, [which]: pickHarness(c, f[which], change) };
    draw();
  };

  // Updates what follows typing without redrawing the inputs under the caret.
  const refresh = (): void => {
    const p = preview(f, c);
    live.preview.textContent = p.command;
    const project = projectLabel(f.folder) || "<project>";
    live.label.textContent =
      f.role === "adjutant" ? `Tabs ${project}-adjutant and ${project}-orchestrator` : `Tab ${project}-orchestrator`;
    if (live.inbox && !f.inboxEdited) live.inbox.value = f.inbox;
    for (const el of body.querySelectorAll<HTMLElement>("[data-field]")) {
      const msg = errors[el.dataset["field"] ?? ""];
      el.classList.toggle("invalid", !!msg);
      const small = el.querySelector(":scope > small.error");
      if (!msg) small?.remove();
    }
  };

  const field = (name: string, label: string, control: Node, hint?: Node | string | null, wide = false): HTMLElement =>
    h(
      "div",
      { class: `field${wide ? " wide" : ""}${errors[name] ? " invalid" : ""}`, dataset: { field: name } },
      h("label", { for: `${id}-${name}` }, label),
      control,
      hint ? h("small", null, hint) : null,
      errors[name] ? h("small", { class: "error", role: "alert" }, errors[name]) : null,
    );

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

  const harnessFields = (which: "main" | "orch", nameField: "name" | "orchName", who: string): HTMLElement[] => {
    const hn = f[which];
    const k = kindOf(c, hn.kind);
    const kinds: [string, string][] = c.kinds.map((x) => [x.kind, x.label]);
    let model: Node;
    if (!k || k.free_model) {
      model = h("input", {
        id: `${id}-${which}-model`,
        name: `${which}-model`,
        value: hn.model,
        placeholder: "model id (optional)",
        autocomplete: "off",
        spellcheck: false,
        oninput: (e: Event) => {
          f = { ...f, [which]: { ...f[which], model: (e.target as HTMLInputElement).value } };
          delete errors[which];
          refresh();
        },
      });
    } else {
      const opts: [string, string][] = k.models.map((m) => [m.id, m.label === m.id ? m.id : `${m.label} (${m.id})`]);
      if (k.other_model) opts.push([OTHER, "Other model id…"]);
      model = selectBox(
        `${which}-model`,
        hn.model,
        opts,
        (v) => setHarness(which, { model: v }),
        `${id}-${which}-model`,
      );
    }
    const efforts = effortsFor(k, hn.model);
    const offline = k?.offline
      ? h(
          "span",
          { class: "nr-chip", title: "The harness could not be asked; this is the last-known list." },
          "offline list",
        )
      : null;
    return [
      field(
        which,
        `${who} harness`,
        selectBox(`${which}-kind`, hn.kind, kinds, (v) => setHarness(which, { kind: v }), `${id}-${which}`),
      ),
      field(`${which}-model`, "Model", h("div", { class: "nr-row" }, model, offline)),
      hn.model === OTHER
        ? field(
            `${which}-other`,
            "Model id",
            h("input", {
              id: `${id}-${which}-other`,
              name: `${which}-other`,
              value: hn.other,
              placeholder: "claude-opus-4-1",
              autocomplete: "off",
              spellcheck: false,
              oninput: (e: Event) => {
                f = { ...f, [which]: { ...f[which], other: (e.target as HTMLInputElement).value } };
                delete errors[which];
                refresh();
              },
            }),
          )
        : null,
      efforts.length
        ? field(
            `${which}-effort`,
            "Effort",
            selectBox(
              `${which}-effort`,
              hn.effort,
              efforts.map((e) => [e, e]),
              (v) => {
                f = { ...f, [which]: { ...f[which], effort: v } };
                delete errors[which];
                refresh();
              },
              `${id}-${which}-effort`,
            ),
          )
        : null,
      field(nameField, `${who} name`, text(nameField)),
    ].filter((x): x is HTMLElement => !!x);
  };

  const checklist = (result: RouteResult | null): HTMLElement => {
    const attach = result?.attach_command ?? preview(f, c).attach ?? "";
    const s = inbox;
    const item = (done: boolean | null, title: string, who: string, extra?: Node | null): HTMLElement =>
      h(
        "li",
        { class: done ? "done" : "" },
        h("span", { class: "nr-check", "aria-hidden": "true" }, done ? icon(icons.check) : null),
        h("span", { class: "nr-item" }, h("span", null, title), extra ?? null),
        h("span", { class: `nr-who${who === "you do this" ? " you" : ""}` }, done ? "done" : who),
      );
    return h(
      "section",
      { class: "nr-checklist", "aria-label": "Channel setup" },
      h("h3", null, "Channel setup"),
      h(
        "ol",
        null,
        item(
          s ? s.private : null,
          "Create the private inbox, mode 700",
          "you do this",
          h("code", null, `mkdir -m 700 -p ${f.inbox}`),
        ),
        item(s ? s.setup : null, "Write the setup proof (setup.json)", "you do this"),
        item(
          null,
          "Attach the adjutant: type this into its pane",
          "you do this",
          h(
            "span",
            { class: "nr-attach" },
            h("code", null, attach),
            h(
              "button",
              {
                type: "button",
                class: "chip-btn",
                title: "Copy the command",
                "aria-label": "Copy the command",
                onclick: () => void copy(attach, "attach command"),
              },
              icon(icons.copy),
            ),
          ),
        ),
        item(
          s ? s.proposed.length >= 2 : null,
          "Both sides propose",
          "after start",
          s?.proposed.length ? h("small", null, s.proposed.join(", ")) : null,
        ),
        item(s ? s.approved.length > 0 : null, "Approve the pair", "you do this"),
      ),
      s === null ? h("small", { class: "nr-note" }, "State unknown: the inbox could not be read.") : null,
    );
  };

  const loadInbox = (redraw: () => void): void => {
    if (f.role !== "adjutant" || inboxFor === f.inbox) return;
    inboxFor = f.inbox;
    inboxState(f.inbox).then(
      (s) => {
        if (inboxFor === s.path || inboxFor === f.inbox) {
          inbox = s;
          redraw();
        }
      },
      () => {
        inbox = null;
      },
    );
  };

  const draw = (): void => {
    const focused = (document.activeElement as HTMLElement | null)?.getAttribute("name");
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
            f = setRole(f, r);
            draw();
            if (r === "adjutant") loadInbox(draw);
          },
        },
        label,
      );
    const warnings = skillWarnings(f, c);
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
          "div",
          { class: "nr-role", role: "radiogroup", "aria-label": "Role" },
          role("orchestrator", "Orchestrator"),
          role("adjutant", "Adjutant"),
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
              ws.map((w) => [w.workspace_id, `${w.label || `Workspace ${w.number}`}${w.focused ? " (focused)" : ""}`]),
              (v) => set({ workspace: v }, true),
              `${id}-workspace`,
            ),
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
          f.role === "adjutant"
            ? [
                h("h3", { class: "nr-sub" }, "Adjutant"),
                ...harnessFields("main", "name", "Adjutant"),
                h("h3", { class: "nr-sub" }, "Orchestrator"),
                ...harnessFields("orch", "orchName", "Orchestrator"),
                field("routeName", "Route name", text("routeName")),
                field(
                  "inbox",
                  "Private inbox",
                  h(
                    "span",
                    { class: "nr-inbox" },
                    (live.inbox = text("inbox", {
                      oninput: (e: Event) => set({ inbox: (e.target as HTMLInputElement).value }, false),
                      onchange: () => loadInbox(draw),
                    })),
                    h(
                      "button",
                      {
                        type: "button",
                        class: "chip-btn",
                        title: "Copy the inbox path",
                        "aria-label": "Copy the inbox path",
                        onclick: () => void copy(f.inbox, "inbox path"),
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
            null,
            true,
          ),
          h(
            "div",
            {
              class: `field wide nr-ck${errors["minutes"] || errors["limit"] || errors["checkpointer"] ? " invalid" : ""}`,
              dataset: { field: "checkpointer" },
            },
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
              h("span", null, "Checkpointer"),
              h("small", null, ck.ok ? "keeps the orchestrator's memory and compacts its context" : ck.why),
            ),
            ckOn
              ? h(
                  "div",
                  { class: "nr-row" },
                  h(
                    "label",
                    { class: "nr-inline" },
                    "every",
                    text("minutes", { inputmode: "numeric", class: "nr-num" }),
                    "min",
                  ),
                  h("label", { class: "nr-inline" }, "compact at", text("limit", { class: "nr-num" }), "tokens"),
                )
              : null,
            ["checkpointer", "minutes", "limit"].map((k) =>
              errors[k] ? h("small", { class: "error", role: "alert" }, errors[k]) : null,
            ),
          ),
        ),
        warnings.map((w) => h("p", { class: "nr-warn", role: "note" }, w)),
        h("div", { class: "nr-sends" }, h("span", null, "Sends"), live.preview),
        f.role === "adjutant" ? checklist(null) : null,
      ),
      h(
        "div",
        { class: "dialog-actions nr-actions" },
        h("button", { value: "cancel", class: "btn ghost", formnovalidate: true }, "Cancel"),
        h(
          "button",
          { type: "submit", class: "btn primary", disabled: busy, name: "start" },
          busy ? "Starting…" : "Start route",
        ),
      ),
    );
    refresh();
    if (focused) body.querySelector<HTMLElement>(`[name="${CSS.escape(focused)}"]`)?.focus();
  };

  const showResult = (r: RouteResult): void => {
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
                s.status === "ok"
                  ? null
                  : h(
                      "small",
                      null,
                      s.status === "failed"
                        ? `${s.code ? `${s.code}: ` : ""}${s.reason ?? ""}`
                        : "skipped: an earlier step failed",
                    ),
              ),
              s.id ? h("code", null, s.id) : null,
            ),
          ),
        ),
        r.ok
          ? null
          : h("p", { class: "nr-note" }, "Nothing was closed: the tabs and panes created so far stay as they are."),
        f.role === "adjutant" && r.adjutant_pane ? checklist(r) : null,
      ),
      h(
        "div",
        { class: "dialog-actions nr-actions" },
        r.ok
          ? null
          : h(
              "button",
              { type: "button", class: "btn ghost", onclick: () => ((busy = false), draw()) },
              "Back to the form",
            ),
        pane
          ? h(
              "button",
              {
                type: "button",
                class: r.ok ? "btn ghost" : "btn ghost",
                onclick: () => {
                  dlg.close();
                  store.go({ pane });
                },
              },
              "Open the orchestrator",
            )
          : null,
        h("button", { value: "ok", class: "btn primary" }, r.ok ? "Done" : "Close"),
      ),
    );
  };

  body.addEventListener("submit", (e) => {
    const submitter = (e as SubmitEvent).submitter as HTMLButtonElement | null;
    if (submitter?.value === "cancel" || submitter?.value === "ok") return;
    e.preventDefault();
    if (busy) return;
    errors = validate(f, c, store.app.server);
    if (Object.keys(errors).length) {
      draw();
      body.querySelector<HTMLElement>(".field.invalid input, .field.invalid select, .field.invalid textarea")?.focus();
      return;
    }
    busy = true;
    draw();
    createRoute(toRequest(f, c)).then(
      (r) => {
        busy = false;
        if (r.code === "invalid_route" || r.code === "bad_request") {
          errors = { [formField(r.field) || "request"]: r.error ?? "Refused." };
          draw();
          return;
        }
        if (f.role === "adjutant") {
          inboxFor = "";
          loadInbox(() => showResult(r));
        }
        showResult(r);
      },
      (err: unknown) => {
        busy = false;
        if (err instanceof AuthError) store.set({ fatal: err.message });
        toast(err instanceof Error ? err.message : String(err), "error");
        draw();
      },
    );
  });

  draw();
  body.querySelector<HTMLElement>(f.folder ? "textarea" : "input[name=folder]")?.focus();
}
