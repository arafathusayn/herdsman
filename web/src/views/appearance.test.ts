import { beforeEach, expect, test } from "bun:test";

import { appThemes, codeThemes, matchApp, noHighlight, termSchemes } from "../appearance.ts";
import { h, icon, icons, unkeyed } from "../dom.ts";
import { render } from "../herdsman-blocks-view.ts";
import { FakeElement, type FakeNode, installFakeDocument, ops, resetOps } from "../herdsman-blocks.testdom.ts";
import { dispatch } from "../testdom.ts";

// The store reads the URL and listens for history changes when it loads; the
// themes write to <html> and to local storage.
const g = globalThis as unknown as Record<string, unknown>;
g["location"] ??= { search: "" };
g["window"] ??= { addEventListener: () => {} };
g["requestAnimationFrame"] ??= () => 0;
const stored = new Map<string, string>();
g["localStorage"] = {
  getItem: (k: string) => stored.get(k) ?? null,
  setItem: (k: string, v: string) => void stored.set(k, v),
};
const { store } = await import("../store.ts");
const { appearanceForm } = await import("./appearance.ts");

const fake = (n: unknown): FakeElement => n as FakeElement;
beforeEach(() => {
  installFakeDocument();
  const d = g["document"] as Record<string, unknown>;
  d["documentElement"] = { style: { setProperty: () => {}, removeProperty: () => {} }, dataset: {} };
  d["getElementById"] = () => ({ textContent: "" });
  stored.clear();
});

// Elements and text with attributes sorted and the bridge's data-key left
// out; a slot's empty anchor text is not content.
const dump = (n: FakeNode): string => {
  if (n.nodeType === 3) return n.textContent;
  if (n.nodeType !== 1) return "";
  const e = fake(n);
  const attrs = e.attributes
    .filter((a) => a.name !== "data-key")
    .map((a) => `${a.name}="${a.value}"`)
    .sort()
    .join(" ");
  return `<${e.tagName.toLowerCase()} ${attrs}>${e.childNodes.map(dump).join("")}</${e.tagName.toLowerCase()}>`;
};
const elements = (n: FakeNode): FakeElement[] => n.childNodes.filter((c): c is FakeElement => c.nodeType === 1);

// A stand-in for the highlighted sample: the fake DOM has no fragments.
const sample = (id: string): HTMLElement => h("code", null, `sample ${id}`);

// The h() version this view replaced, with style.setProperty's result written
// as the attribute the browser serializes (the fake DOM has no style object).
function oldForm(theme: string, term: string, code: string): FakeElement {
  const withVars = (el: HTMLElement, vars: Record<string, string>): HTMLElement => {
    const s = Object.entries(vars)
      .map(([k, v]) => `${k}: ${v};`)
      .join(" ");
    if (s) el.setAttribute("style", s);
    return el;
  };
  const termPreview = (id: string) =>
    withVars(
      h(
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
        h(
          "span",
          { class: "swatches" },
          unkeyed([1, 2, 3, 4, 5, 6].map((i) => h("span", { class: "dot", style: `background: var(--t${i})` }))),
        ),
      ),
      termVarsOf(id),
    );
  const codePreview = (id: string) =>
    withVars(h("pre", { class: `code-preview${id === matchApp ? " match-app" : ""}` }, sample(id)), codeVarsOf(id));
  const appPreview = (id: string) =>
    withVars(
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
      appVarsOf(id) ?? {},
    );
  const choices = (
    label: string,
    items: { id: string; label: string }[],
    current: string,
    preview: (id: string) => HTMLElement,
  ) =>
    h(
      "section",
      { class: "appearance-section" },
      h("h3", null, label),
      h(
        "div",
        { class: "choice-grid", role: "radiogroup", "aria-label": label },
        unkeyed(
          items.map((it) => {
            const on = it.id === current;
            return h(
              "button",
              { type: "button", role: "radio", "aria-checked": String(on), class: on ? "choice active" : "choice" },
              preview(it.id),
              h("span", { class: "choice-label" }, it.label, on ? icon(icons.check) : null),
            );
          }),
        ),
      ),
    );
  const themeSwitch = h(
    "div",
    { class: "theme-switch", role: "radiogroup", "aria-label": "Theme" },
    unkeyed(
      (
        [
          ["system", "System", icons.monitor],
          ["light", "Light", icons.sun],
          ["dark", "Dark", icons.moon],
        ] as const
      ).map(([id, label, glyph]) =>
        h(
          "button",
          {
            role: "radio",
            "aria-checked": String(id === theme),
            class: id === theme ? "active" : "",
            title: `${label} theme`,
          },
          icon(glyph),
          h("span", null, label),
        ),
      ),
    ),
  );
  return fake(
    h(
      "form",
      { method: "dialog" },
      h("h2", null, "Appearance"),
      h(
        "section",
        { class: "appearance-section" },
        h("h3", null, "Theme"),
        h("div", { class: "appearance-theme" }, themeSwitch, choices("Color themes", appThemes, theme, appPreview)),
      ),
      choices("Terminal colors", [{ id: matchApp, label: "Match app" }, ...termSchemes], term, termPreview),
      choices(
        "Code highlighting",
        [{ id: matchApp, label: "Match app" }, ...codeThemes, { id: noHighlight, label: "No highlighting" }],
        code,
        codePreview,
      ),
      h("div", { class: "dialog-actions" }, h("button", { value: "ok", class: "btn primary" }, "Done")),
    ),
  );
}

const { termVars: termVarsOf, codeVars: codeVarsOf, appVars: appVarsOf } = await import("../appearance.ts");

const samples = new Map<string, HTMLElement>();
const code = (id: string): HTMLElement => {
  let el = samples.get(id);
  if (el === undefined) samples.set(id, (el = sample(id)));
  return el;
};

const renderForm = (): FakeElement => {
  const box = document.createElement("div");
  const redraw = (): void => render(box, appearanceForm(code, redraw));
  redraw();
  return fake(fake(box).firstChild);
};

// The sections in order: the theme's, the terminal's and the code's.
const grids = (form: FakeElement): FakeElement[] => {
  const [theme, term, codeSec] = elements(form).filter((e) => e.className === "appearance-section");
  const colors = elements(elements(theme!)[1]!)[1]!;
  return [colors, term!, codeSec!].map((s) => elements(s)[1]!);
};

test("the form renders what the h() version did", () => {
  samples.clear();
  store.set({ theme: "system" });
  expect(dump(renderForm())).toBe(dump(oldForm("system", matchApp, matchApp)));
  stored.set("herdsman.terminal-scheme", termSchemes[1]!.id);
  stored.set("herdsman.code-theme", noHighlight);
  store.set({ theme: appThemes[2]!.id });
  samples.clear();
  const form = renderForm();
  const want = oldForm(appThemes[2]!.id, termSchemes[1]!.id, noHighlight);
  // The stored keys above must be the ones the app reads, or this compares defaults.
  expect(dump(form)).toContain('aria-checked="true" class="choice active"');
  expect(dump(form)).toBe(dump(want));
});

test("a choice applies, re-renders in place and keeps every node", () => {
  samples.clear();
  store.set({ theme: "system" });
  const box = document.createElement("div");
  const redraw = (): void => render(box, appearanceForm(code, redraw));
  redraw();
  const form = fake(fake(box).firstChild);
  const [, term, codeGrid] = grids(form);
  const before = [...elements(term!), ...elements(codeGrid!)];
  const pre = elements(elements(codeGrid!)[1]!)[0]!;
  const sampleNode = pre.firstChild;

  resetOps();
  redraw();
  expect(ops).toEqual({
    inserts: 0,
    moves: 0,
    stateMoves: 0,
    removes: 0,
    clones: 0,
    classWrites: 0,
    attrWrites: 0,
    textWrites: 0,
  });

  const pick = elements(term!)[2]!;
  dispatch(pick, "click");
  expect(stored.get("herdsman.terminal-scheme")).toBe(termSchemes[1]!.id);
  expect(pick.className).toBe("choice active");
  expect(pick.getAttribute("aria-checked")).toBe("true");
  expect(elements(term!)[0]!.className).toBe("choice");
  expect([...elements(term!), ...elements(codeGrid!)]).toEqual(before);
  for (const [i, el] of before.entries()) expect(el).toBe([...elements(term!), ...elements(codeGrid!)][i]!);
  // The highlighted sample is the same node across renders.
  expect(pre.firstChild).toBe(sampleNode);
  expect(fake(fake(box).firstChild)).toBe(form);
});

test("the theme switch and the color themes are one choice", () => {
  samples.clear();
  store.set({ theme: "system" });
  const box = document.createElement("div");
  const redraw = (): void => render(box, appearanceForm(code, redraw));
  redraw();
  const form = fake(fake(box).firstChild);
  const [colors] = grids(form);
  dispatch(elements(colors!)[0]!, "click");
  expect(store.app.theme).toBe(appThemes[0]!.id);
  expect(elements(colors!)[0]!.className).toBe("choice active");
  const themeSwitch = elements(elements(elements(form)[1]!)[1]!)[0]!;
  expect(elements(themeSwitch).map((b) => b.getAttribute("aria-checked"))).toEqual(["false", "false", "false"]);
  // Its buttons have no type: a click must not submit the dialog's form.
  const e = dispatch(elements(themeSwitch)[1]!, "click");
  expect(e.defaultPrevented).toBe(true);
  expect(store.app.theme).toBe("light");
  expect(elements(themeSwitch)[1]!.getAttribute("aria-checked")).toBe("true");
});
