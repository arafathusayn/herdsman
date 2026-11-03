// Shared bits: status pills, toasts, the confirm and form dialogs and the theme.
import { appThemes, installAppTheme } from "./appearance.ts";

export { confirmDialog, formDialog, type Field } from "./components/dialogs.tsx";
export { toast } from "./components/toasts.tsx";
export { dismissOnBackdrop } from "./dialog-backdrop.ts";

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
