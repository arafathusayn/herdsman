// The Appearance dialog: the app theme, the terminal color scheme and the code
// highlighting theme, each with a live preview. A choice applies at once.
import {
  applyCodeTheme,
  applyTermScheme,
  appThemes,
  appVars,
  codeThemes,
  codeVars,
  loadCodeTheme,
  loadTermScheme,
  matchApp,
  noHighlight,
  termSchemes,
  termVars,
} from "../appearance.ts";
import { h, icon, icons, mount } from "../dom.ts";
import { renderPieces, tokenizeLines } from "../highlight.ts";
import { store } from "../store.ts";
import { applyTheme, dismissOnBackdrop, themeSwitch } from "../ui.ts";

const sample = [
  "// Load reads a route folder.",
  "func Load(dir string) (*Route, error) {",
  '\treturn parse(dir, "00-route.md", 200)',
  "}",
];

function withVars(el: HTMLElement, vars: Record<string, string>): HTMLElement {
  for (const [k, v] of Object.entries(vars)) el.style.setProperty(k, v);
  return el;
}

/** A tiny terminal: a prompt line, then the eight base colors. */
function termPreview(id: string): HTMLElement {
  const dots = h(
    "span",
    { class: "swatches" },
    [1, 2, 3, 4, 5, 6].map((i) => h("span", { class: "dot", style: `background: var(--t${i})` })),
  );
  const el = h(
    "span",
    { class: `term-preview${id === matchApp ? " match-app" : ""}` },
    h(
      "span",
      { class: "line" },
      h("span", { style: "color: var(--t2)" }, "herdsman"),
      " on ",
      h("span", { style: "color: var(--t5)" }, " main"),
      h("span", { style: "color: var(--t1)" }, " [$]"),
    ),
    dots,
  );
  return withVars(el, termVars(id));
}

function codePreview(id: string): HTMLElement {
  const code = h("code", null);
  if (id === noHighlight) code.textContent = sample.join("\n");
  else
    tokenizeLines("go", sample).forEach((p, i) => {
      if (i) code.append("\n");
      code.append(renderPieces(p));
    });
  return withVars(h("pre", { class: `code-preview${id === matchApp ? " match-app" : ""}` }, code), codeVars(id));
}

function choices(
  label: string,
  items: { id: string; label: string }[],
  current: () => string,
  preview: (id: string) => HTMLElement,
  choose: (id: string) => void,
): HTMLElement {
  const grid = h("div", {
    class: "choice-grid",
    role: "radiogroup",
    "aria-label": label,
  });
  const draw = () =>
    mount(
      grid,
      items.map((it) => {
        const on = it.id === current();
        return h(
          "button",
          {
            type: "button",
            role: "radio",
            "aria-checked": String(on),
            class: on ? "choice active" : "choice",
            onclick: () => {
              choose(it.id);
              draw();
            },
          },
          preview(it.id),
          h("span", { class: "choice-label" }, it.label, on ? icon(icons.check) : null),
        );
      }),
    );
  draw();
  return h("section", { class: "appearance-section" }, h("h3", null, label), grid);
}

/** A tiny app window in a theme's colors: sidebar, a card, text and the accent. */
function appPreview(id: string): HTMLElement {
  return withVars(
    h(
      "span",
      { class: "app-preview", "aria-hidden": "true" },
      h("span", { class: "ap-side" }, h("span", { class: "ap-line" }), h("span", { class: "ap-line short" })),
      h(
        "span",
        { class: "ap-main" },
        h("span", { class: "ap-line" }),
        h("span", { class: "ap-line short" }),
        h("span", { class: "ap-pills" }, h("span", { class: "ap-pill" }), h("span", { class: "ap-pill working" })),
      ),
    ),
    appVars(id) ?? {},
  );
}

export function openAppearance(): void {
  const theme = h("div", { class: "appearance-theme" });
  const pick = (t: string): void => {
    applyTheme(t);
    store.set({ theme: t });
    drawTheme();
  };
  // The switch and the color themes are one choice, so both redraw together.
  const drawTheme = (): void =>
    mount(
      theme,
      themeSwitch(store.app.theme, pick),
      choices("Color themes", appThemes, () => store.app.theme, appPreview, pick),
    );
  drawTheme();
  const dlg = h(
    "dialog",
    { class: "dialog appearance" },
    h(
      "form",
      { method: "dialog" },
      h("h2", null, "Appearance"),
      h("section", { class: "appearance-section" }, h("h3", null, "Theme"), theme),
      choices(
        "Terminal colors",
        [{ id: matchApp, label: "Match app" }, ...termSchemes],
        loadTermScheme,
        termPreview,
        (id) => applyTermScheme(id),
      ),
      choices(
        "Code highlighting",
        [{ id: matchApp, label: "Match app" }, ...codeThemes, { id: noHighlight, label: "No highlighting" }],
        loadCodeTheme,
        codePreview,
        (id) => {
          applyCodeTheme(id);
          store.emit(); // views drawn with or without highlighting redraw
        },
      ),
      h("div", { class: "dialog-actions" }, h("button", { value: "ok", class: "btn primary" }, "Done")),
    ),
  );
  dlg.addEventListener("close", () => dlg.remove());
  dismissOnBackdrop(dlg);
  document.body.append(dlg);
  dlg.showModal();
}
