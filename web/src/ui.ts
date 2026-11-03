// Shared bits: status pills, toasts, the confirm and form dialogs and the theme.
import { appThemes, installAppTheme } from "./appearance.ts";
import { attr, child, cls, html, list, type ListNode, on, text, type ViewNodeOf } from "./herdsman-blocks-view.ts";
import { icon, type IconNode, icons } from "./icons.ts";
import { type Member, statusLabel } from "./model.ts";

export { confirmDialog, formDialog, type Field } from "./components/dialogs.tsx";
export { toast } from "./components/toasts.tsx";
export { dismissOnBackdrop } from "./dialog-backdrop.ts";

const Pill = html`<span class=${cls()}><span class="dot" aria-hidden="true"></span>${text()}</span>`;

/** What `pill` returns. */
export type PillNode = ViewNodeOf<typeof Pill>;

export function pill(status: Member["status"]): PillNode {
  return Pill([`pill s-${status}`, statusLabel[status]]);
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
  type="button"
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
