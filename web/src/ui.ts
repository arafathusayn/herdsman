// Shared bits: status pills, toasts, the confirm dialog and the theme.
import { appThemes, installAppTheme } from "./appearance.ts";
import { h, icon, icons } from "./dom.ts";
import { type Member, statusLabel } from "./model.ts";

export function pill(status: Member["status"]): HTMLElement {
  return h(
    "span",
    { class: `pill s-${status}` },
    h("span", { class: "dot", "aria-hidden": "true" }),
    statusLabel[status],
  );
}

let toastHost: HTMLElement | null = null;

export function toast(text: string, kind: "ok" | "error" = "ok"): void {
  if (!toastHost) {
    toastHost = h("div", {
      class: "toasts",
      role: "status",
      "aria-live": "polite",
    });
    document.body.append(toastHost);
  }
  const t = h("div", { class: `toast ${kind}` }, text);
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

/** A modal confirm that names the action; resolves false on cancel. */
export function confirmDialog(title: string, body: string, confirmLabel: string, danger = false): Promise<boolean> {
  return new Promise((resolve) => {
    const dlg = h(
      "dialog",
      { class: "dialog" },
      h(
        "form",
        { method: "dialog" },
        h("h2", null, title),
        h("p", null, body),
        h(
          "div",
          { class: "dialog-actions" },
          h("button", { value: "cancel", class: "btn ghost" }, "Cancel"),
          h(
            "button",
            {
              value: "ok",
              class: danger ? "btn danger" : "btn primary",
              autofocus: true,
            },
            confirmLabel,
          ),
        ),
      ),
    );
    dlg.addEventListener("close", () => {
      resolve(dlg.returnValue === "ok");
      dlg.remove();
    });
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

/** A small modal form; resolves the values, or null on cancel. */
export function formDialog(
  title: string,
  fields: Field[],
  submitLabel: string,
): Promise<Record<string, string> | null> {
  return new Promise((resolve) => {
    const inputs = fields.map((f) => {
      const id = `f-${f.name}-${Math.random().toString(36).slice(2, 8)}`;
      const listId = f.list?.length ? `${id}-list` : undefined;
      const input = h("input", {
        id,
        name: f.name,
        value: f.value ?? "",
        placeholder: f.placeholder,
        required: f.required,
        pattern: f.pattern,
        autocomplete: "off",
        spellcheck: false,
      });
      if (listId) input.setAttribute("list", listId);
      return h(
        "label",
        { class: "field", for: id },
        h("span", null, f.label),
        input,
        listId
          ? h(
              "datalist",
              { id: listId },
              (f.list ?? []).map((v) => h("option", { value: v })),
            )
          : null,
        f.hint ? h("small", null, f.hint) : null,
      );
    });
    const form = h(
      "form",
      { method: "dialog" },
      h("h2", null, title),
      inputs,
      h(
        "div",
        { class: "dialog-actions" },
        h("button", { value: "cancel", class: "btn ghost", formnovalidate: true }, "Cancel"),
        h("button", { value: "ok", class: "btn primary" }, submitLabel),
      ),
    );
    const dlg = h("dialog", { class: "dialog" }, form);
    dlg.addEventListener("close", () => {
      if (dlg.returnValue !== "ok") resolve(null);
      else resolve(Object.fromEntries([...new FormData(form)].map(([k, v]) => [k, String(v)])));
      dlg.remove();
    });
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

const themes: { id: Theme; label: string; glyph: string[] }[] = [
  { id: "system", label: "System", glyph: icons.monitor },
  { id: "light", label: "Light", glyph: icons.sun },
  { id: "dark", label: "Dark", glyph: icons.moon },
];

/** A three-way System / Light / Dark switch. */
export function themeSwitch(current: Theme, onChange: (t: Theme) => void): HTMLElement {
  return h(
    "div",
    { class: "theme-switch", role: "radiogroup", "aria-label": "Theme" },
    themes.map((t) =>
      h(
        "button",
        {
          role: "radio",
          "aria-checked": String(t.id === current),
          class: t.id === current ? "active" : "",
          title: `${t.label} theme`,
          onclick: () => onChange(t.id),
        },
        icon(t.glyph),
        h("span", null, t.label),
      ),
    ),
  );
}
