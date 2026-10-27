import { beforeEach, expect, test } from "bun:test";

import { appThemes, codeThemes, matchApp, noHighlight, termSchemes } from "../appearance.ts";
import { render } from "../herdsman-blocks-view.ts";
import { FakeElement, type FakeNode, installFakeDocument, ops, resetOps } from "../herdsman-blocks.testdom.ts";
import { icon, icons } from "../icons.ts";
import { dispatch, dump, markup } from "../testdom.ts";

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

const elements = (n: FakeNode): FakeElement[] => n.childNodes.filter((c): c is FakeElement => c.nodeType === 1);

const { termVars: termVarsOf, codeVars: codeVarsOf, appVars: appVarsOf } = await import("../appearance.ts");

// The form as the h() version drew it. Its grids show every theme the app
// ships, each with its colors in a style attribute (written as the browser
// serializes style.setProperty's result), so the expected markup is built
// from the theme lists rather than written out in full.
const iconMarkup = (paths: readonly string[]): string => {
  const box = document.createElement("div");
  render(box, icon(paths));
  return dump(fake(box).firstChild as FakeNode);
};
const styleOf = (vars: Record<string, string> | null | undefined): string => {
  const s = Object.entries(vars ?? {})
    .map(([k, v]) => `${k}: ${v};`)
    .join(" ");
  return s ? ` style="${s}"` : "";
};
const matchClass = (id: string): string => (id === matchApp ? " match-app" : "");
// The code sample, highlighted as Go and plain.
const highlighted = markup`
  <span class="hl-comment">// Load reads a route folder.</span>\n
  <span class="hl-keyword">func</span> <span class="hl-func">Load</span>(dir <span class="hl-type">string</span>) (*Route, <span class="hl-type">error</span>) {\n
  \t<span class="hl-keyword">return</span> <span class="hl-func">parse</span>(dir, <span class="hl-string">"00-route.md"</span>, <span class="hl-number">200</span>)\n
  }
`;
const plain =
  '// Load reads a route folder.\nfunc Load(dir string) (*Route, error) {\n\treturn parse(dir, "00-route.md", 200)\n}';
const appPreview = (id: string): string => markup`
  <span aria-hidden="true" class="app-preview"${styleOf(appVarsOf(id))}>
    <span class="ap-side"><span class="ap-line"></span><span class="ap-line short"></span></span>
    <span class="ap-main">
      <span class="ap-line"></span>
      <span class="ap-line short"></span>
      <span class="ap-pills"><span class="ap-pill"></span><span class="ap-pill working"></span></span>
    </span>
  </span>
`;
const termPreview = (id: string): string => markup`
  <span class="term-preview${matchClass(id)}"${styleOf(termVarsOf(id))}>
    <span class="line">
      <span style="color: var(--t2)">herdsman</span> on <span style="color: var(--t5)">\ue0a0 main</span>
      <span style="color: var(--t1)"> [$]</span>
    </span>
    <span class="swatches">
      ${[1, 2, 3, 4, 5, 6].map((i) => `<span class="dot" style="background: var(--t${i})"></span>`).join("")}
    </span>
  </span>
`;
const codePreview = (id: string): string => markup`
  <pre class="code-preview${matchClass(id)}"${styleOf(codeVarsOf(id))}>
    <code>${id === noHighlight ? plain : highlighted}</code>
  </pre>
`;
const choicesMarkup = (
  label: string,
  items: { id: string; label: string }[],
  current: string,
  preview: (id: string) => string,
): string => markup`
  <section class="appearance-section">
    <h3>${label}</h3>
    <div aria-label="${label}" class="choice-grid" role="radiogroup">
      ${items
        .map((it) => {
          const on = it.id === current;
          return markup`
            <button aria-checked="${String(on)}" class="choice${on ? " active" : ""}" role="radio" type="button">
              ${preview(it.id)}
              <span class="choice-label">${it.label}${on ? iconMarkup(icons.check) : ""}</span>
            </button>
          `;
        })
        .join("")}
    </div>
  </section>
`;
const themeButton = (theme: string, id: string, label: string, glyph: readonly string[]): string => markup`
  <button aria-checked="${String(id === theme)}"${id === theme ? ' class="active"' : ""} role="radio" title="${label} theme" type="button">
    ${iconMarkup(glyph)}
    <span>${label}</span>
  </button>
`;
const formMarkup = (theme: string, term: string, code: string): string => markup`
  <form method="dialog">
    <h2>Appearance</h2>
    <section class="appearance-section">
      <h3>Theme</h3>
      <div class="appearance-theme">
        <div aria-label="Theme" class="theme-switch" role="radiogroup">
          ${themeButton(theme, "system", "System", icons.monitor)}
          ${themeButton(theme, "light", "Light", icons.sun)}
          ${themeButton(theme, "dark", "Dark", icons.moon)}
        </div>
        ${choicesMarkup("Color themes", appThemes, theme, appPreview)}
      </div>
    </section>
    ${choicesMarkup("Terminal colors", [{ id: matchApp, label: "Match app" }, ...termSchemes], term, termPreview)}
    ${choicesMarkup(
      "Code highlighting",
      [{ id: matchApp, label: "Match app" }, ...codeThemes, { id: noHighlight, label: "No highlighting" }],
      code,
      codePreview,
    )}
    <div class="dialog-actions"><button class="btn primary" value="ok">Done</button></div>
  </form>
`;

const renderForm = (): FakeElement => {
  const box = document.createElement("div");
  const redraw = (): void => render(box, appearanceForm(redraw));
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
  store.set({ theme: "system" });
  expect(dump(renderForm())).toBe(formMarkup("system", matchApp, matchApp));
  stored.set("herdsman.terminal-scheme", termSchemes[1]!.id);
  stored.set("herdsman.code-theme", noHighlight);
  store.set({ theme: appThemes[2]!.id });
  const form = renderForm();
  const want = formMarkup(appThemes[2]!.id, termSchemes[1]!.id, noHighlight);
  // The stored keys above must be the ones the app reads, or this compares defaults.
  expect(dump(form)).toContain('aria-checked="true" class="choice active"');
  expect(dump(form)).toBe(want);
});

test("a choice applies, re-renders in place and keeps every node", () => {
  store.set({ theme: "system" });
  const box = document.createElement("div");
  const redraw = (): void => render(box, appearanceForm(redraw));
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
  // The highlighted sample keeps its nodes across renders.
  expect(pre.firstChild).toBe(sampleNode);
  expect(elements(sampleNode as FakeNode).length).toBeGreaterThan(5);
  expect(fake(fake(box).firstChild)).toBe(form);
});

test("the theme switch and the color themes are one choice", () => {
  store.set({ theme: "system" });
  const box = document.createElement("div");
  const redraw = (): void => render(box, appearanceForm(redraw));
  redraw();
  const form = fake(fake(box).firstChild);
  const [colors] = grids(form);
  dispatch(elements(colors!)[0]!, "click");
  expect(store.app.theme).toBe(appThemes[0]!.id);
  expect(elements(colors!)[0]!.className).toBe("choice active");
  const themeSwitch = elements(elements(elements(form)[1]!)[1]!)[0]!;
  expect(elements(themeSwitch).map((b) => b.getAttribute("aria-checked"))).toEqual(["false", "false", "false"]);
  // Its buttons are type="button": a click picks the theme and does not submit the dialog's form.
  expect(elements(themeSwitch).map((b) => b.getAttribute("type"))).toEqual(["button", "button", "button"]);
  dispatch(elements(themeSwitch)[1]!, "click");
  expect(store.app.theme).toBe("light");
  expect(elements(themeSwitch)[1]!.getAttribute("aria-checked")).toBe("true");
});
