// Shared bits: status pills, toasts, the confirm and form dialogs and the theme.
import { appThemes, installAppTheme } from "./appearance.ts";
import { icon, type IconNode, icons } from "./dom.ts";
import {
  attr,
  child,
  cls,
  html,
  list,
  type ListNode,
  on,
  prop,
  render,
  text,
  type ViewNodeOf,
} from "./herdsman-blocks-view.ts";
import { type Member, statusLabel } from "./model.ts";
import { element } from "./views/element-view.ts";

const Pill = html`<span class=${cls()}><span class="dot" aria-hidden="true"></span>${text()}</span>`;

/** What `pill` returns. */
export type PillNode = ViewNodeOf<typeof Pill>;

export function pill(status: Member["status"]): PillNode {
  return Pill([`pill s-${status}`, statusLabel[status]]);
}

// Toasts come and go on timers, so each is an element the toast host owns.
const ToastHost = html`<div class="toasts" role="status" aria-live="polite"></div>`;
const Toast = html`<div class=${cls()}>${text()}</div>`;

let toastHost: HTMLDivElement | null = null;

export function toast(message: string, kind: "ok" | "error" = "ok"): void {
  if (!toastHost) {
    toastHost = element("div", ToastHost([]));
    document.body.append(toastHost);
  }
  const t = element("div", Toast([`toast ${kind}`, message]));
  toastHost.append(t);
  setTimeout(() => t.classList.add("leave"), kind === "error" ? 6000 : 3000);
  setTimeout(() => t.remove(), kind === "error" ? 6400 : 3400);
}

/**
 * Closes a modal dialog, as Cancel, on a click or tap on its backdrop. The
 * press must start and end outside the dialog's box, so a text selection
 * dragged out of it does not close it.
 */
export function dismissOnBackdrop(dlg: HTMLDialogElement): void {
  const outside = (e: MouseEvent): boolean => {
    const r = dlg.getBoundingClientRect();
    return e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom;
  };
  let pressedOutside = false;
  dlg.addEventListener("pointerdown", (e) => {
    pressedOutside = e.target === dlg && outside(e);
  });
  dlg.addEventListener("click", (e) => {
    if (pressedOutside && e.target === dlg && outside(e)) dlg.close("cancel");
    pressedOutside = false;
  });
}

// A modal dialog is an element its opener owns; its form is rendered into it.
type OnClose = (this: HTMLDialogElement, e: Event) => void;
const Dialog = html`<dialog class="dialog" onclose=${on<"close", HTMLDialogElement>()}></dialog>`;

function openDialog(onClose: OnClose): HTMLDialogElement {
  return element("dialog", Dialog([onClose]));
}

const ConfirmForm = html`<form method="dialog">
  <h2>${text()}</h2>
  <p>${text()}</p>
  <div class="dialog-actions">
    <button value="cancel" class="btn ghost">Cancel</button>
    <button value="ok" class=${cls()} autofocus>${text()}</button>
  </div>
</form>`;

/** A modal confirm that names the action; resolves false on cancel. */
export function confirmDialog(title: string, body: string, confirmLabel: string, danger = false): Promise<boolean> {
  return new Promise((resolve) => {
    const dlg = openDialog(function () {
      resolve(this.returnValue === "ok");
      this.remove();
    });
    render(dlg, ConfirmForm([title, body, danger ? "btn danger" : "btn primary", confirmLabel]));
    dismissOnBackdrop(dlg);
    document.body.append(dlg);
    dlg.showModal();
  });
}

export interface Field {
  name: string;
  label: string;
  value?: string;
  placeholder?: string;
  hint?: string;
  list?: string[];
  required?: boolean;
  pattern?: string;
}

const Suggestion = html`<option value=${attr()}></option>`;
const Suggestions = html`<datalist id=${attr()}>${child<ListNode<ViewNodeOf<typeof Suggestion>, string>>()}</datalist>`;
const Hint = html`<small>${text()}</small>`;
const FieldRow = html`<label class="field" for=${attr()}>
  <span>${text()}</span>
  <input
    id=${attr()}
    name=${attr()}
    .value=${prop<HTMLInputElement, "value">()}
    placeholder=${attr<string | null>()}
    required=${attr<boolean>()}
    pattern=${attr<string | null>()}
    autocomplete="off"
    spellcheck="false"
    list=${attr<string | null>()}
  />
  ${child<ViewNodeOf<typeof Suggestions> | null>()}${child<ViewNodeOf<typeof Hint> | null>()}
</label>`;
const FieldForm = html`<form method="dialog">
  <h2>${text()}</h2>
  ${child<ListNode<ViewNodeOf<typeof FieldRow>, string>>()}
  <div class="dialog-actions">
    <button value="cancel" class="btn ghost" formnovalidate>Cancel</button>
    <button value="ok" class="btn primary">${text()}</button>
  </div>
</form>`;

function fieldRow(f: Field): ViewNodeOf<typeof FieldRow> {
  const id = `f-${f.name}-${Math.random().toString(36).slice(2, 8)}`;
  const listId = f.list?.length ? `${id}-list` : null;
  return FieldRow([
    id,
    f.label,
    id,
    f.name,
    f.value ?? "",
    f.placeholder ?? null,
    f.required ?? false,
    f.pattern ?? null,
    listId,
    listId === null
      ? null
      : Suggestions([
          listId,
          list(
            [...new Set(f.list)], // an option is keyed by its value, so repeats go
            (v) => v,
            (v) => Suggestion([v]),
          ),
        ]),
    f.hint ? Hint([f.hint]) : null,
  ]);
}

/** A small modal form; resolves the values, or null on cancel. */
export function formDialog(
  title: string,
  fields: Field[],
  submitLabel: string,
): Promise<Record<string, string> | null> {
  return new Promise((resolve) => {
    const dlg = openDialog(function () {
      const form = this.querySelector("form");
      if (this.returnValue !== "ok" || !form) resolve(null);
      else resolve(Object.fromEntries([...new FormData(form)].map(([k, v]) => [k, String(v)])));
      this.remove();
    });
    // Field names are unique in a form (they are its values' keys).
    render(dlg, FieldForm([title, list(fields, (f) => f.name, fieldRow), submitLabel]));
    document.body.append(dlg);
    dlg.showModal();
  });
}

/** System, herdsman's own Light or Dark, or the id of a color theme (appThemes). */
export type Theme = string;

const themeKey = "herdsman.theme";

export function loadTheme(): Theme {
  try {
    const t = localStorage.getItem(themeKey);
    if (t === "light" || t === "dark" || appThemes.some((x) => x.id === t)) return t ?? "system";
  } catch {
    // Storage can be blocked; the system theme is the fallback.
  }
  return "system";
}

export function applyTheme(t: Theme): void {
  const named = t !== "system" && t !== "light" && t !== "dark";
  if (!installAppTheme(named ? t : "") && named) t = "system";
  if (t === "system") delete document.documentElement.dataset["theme"];
  else document.documentElement.dataset["theme"] = t;
  try {
    localStorage.setItem(themeKey, t);
  } catch {
    // Not remembered, still applied.
  }
}

const themes: readonly { id: Theme; label: string; glyph: readonly string[] }[] = [
  { id: "system", label: "System", glyph: icons.monitor },
  { id: "light", label: "Light", glyph: icons.sun },
  { id: "dark", label: "Dark", glyph: icons.moon },
];

const ThemeOption = html`<button
  role="radio"
  aria-checked=${attr<"true" | "false">()}
  class=${cls()}
  title=${attr()}
  onclick=${on<"click">()}
>
  ${child<IconNode>()}<span>${text()}</span>
</button>`;
const ThemeSwitch = html`<div class="theme-switch" role="radiogroup" aria-label="Theme">
  ${child<ListNode<ViewNodeOf<typeof ThemeOption>, Theme>>()}
</div>`;

/** What `themeSwitch` returns. */
export type ThemeSwitchNode = ViewNodeOf<typeof ThemeSwitch>;

/** A three-way System / Light / Dark switch. */
export function themeSwitch(current: Theme, onChange: (t: Theme) => void): ThemeSwitchNode {
  return ThemeSwitch([
    list(
      themes,
      (t) => t.id,
      (t) =>
        ThemeOption([
          t.id === current ? "true" : "false",
          t.id === current ? "active" : "",
          `${t.label} theme`,
          () => onChange(t.id),
          icon(t.glyph),
          t.label,
        ]),
    ),
  ]);
}
