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
import { icon, type IconNode, icons } from "../dom.ts";
import {
  attr,
  child,
  cls,
  html,
  list,
  type ListNode,
  on,
  render,
  runs,
  type RunsNode,
  text,
  type ViewNodeOf,
} from "../herdsman-blocks-view.ts";
import { tokenizeLines } from "../highlight.ts";
import { store } from "../store.ts";
import { applyTheme, dismissOnBackdrop, themeSwitch, type ThemeSwitchNode } from "../ui.ts";
import { element } from "./element-view.ts";

const sample = [
  "// Load reads a route folder.",
  "func Load(dir string) (*Route, error) {",
  '\treturn parse(dir, "00-route.md", 200)',
  "}",
];

/** CSS variables as a style attribute, as style.setProperty writes them; null when there are none. */
function styleOf(vars: Record<string, string>): string | null {
  const decls = Object.entries(vars).map(([k, v]) => `${k}: ${v};`);
  return decls.length ? decls.join(" ") : null;
}

const previewClass = (base: string, id: string): string => `${base}${id === matchApp ? " match-app" : ""}`;

// The prompt's branch: a private-use character (a Powerline branch glyph) and the name.
const branch = " main";

/** A tiny terminal: a prompt line, then the eight base colors. */
const TermPreview = html`<span class=${cls()} style=${attr<string | null>()}
  ><span class="line"
    ><span style="color: var(--t2)">herdsman</span> on <span style="color: var(--t5)">${text()}</span
    ><span style="color: var(--t1)"> [$]</span></span
  ><span class="swatches"
    ><span class="dot" style="background: var(--t1)"></span><span class="dot" style="background: var(--t2)"></span
    ><span class="dot" style="background: var(--t3)"></span><span class="dot" style="background: var(--t4)"></span
    ><span class="dot" style="background: var(--t5)"></span
    ><span class="dot" style="background: var(--t6)"></span></span
></span>`;

/** What `termPreview` returns. */
export type TermPreviewNode = ViewNodeOf<typeof TermPreview>;

function termPreview(id: string): TermPreviewNode {
  return TermPreview([previewClass("term-preview", id), styleOf(termVars(id)), branch]);
}

// The sample, highlighted: text runs between token spans, as markdown's code
// blocks are. It is the same for every theme; only the colors differ.
const Token = html`<span class=${cls()}>${text()}</span>`;
type SampleNode = string | RunsNode<ViewNodeOf<typeof Token>>;

let highlighted: RunsNode<ViewNodeOf<typeof Token>> | null = null;

function sampleCode(id: string): SampleNode {
  if (id === noHighlight) return sample.join("\n");
  if (highlighted === null) {
    const out: (string | ViewNodeOf<typeof Token>)[] = [];
    tokenizeLines("go", sample).forEach((pieces, i) => {
      if (i) out.push("\n");
      for (const [k, t] of pieces) out.push(k ? Token([`hl-${k}`, t]) : t);
    });
    highlighted = runs(out);
  }
  return highlighted;
}

const CodePreview = html`<pre class=${cls()} style=${attr<string | null>()}><code>${child<SampleNode>()}</code></pre>`;

/** What `codePreview` returns. */
export type CodePreviewNode = ViewNodeOf<typeof CodePreview>;

/** The sample code in a theme's colors. */
function codePreview(id: string): CodePreviewNode {
  return CodePreview([previewClass("code-preview", id), styleOf(codeVars(id)), sampleCode(id)]);
}

/** A tiny app window in a theme's colors: sidebar, a card, text and the accent. */
const AppPreview = html`<span class="app-preview" aria-hidden="true" style=${attr<string | null>()}
  ><span class="ap-side"><span class="ap-line"></span><span class="ap-line short"></span></span
  ><span class="ap-main"
    ><span class="ap-line"></span><span class="ap-line short"></span
    ><span class="ap-pills"><span class="ap-pill"></span><span class="ap-pill working"></span></span></span
></span>`;

/** What `appPreview` returns. */
export type AppPreviewNode = ViewNodeOf<typeof AppPreview>;

function appPreview(id: string): AppPreviewNode {
  return AppPreview([styleOf(appVars(id) ?? {})]);
}

type PreviewNode = TermPreviewNode | CodePreviewNode | AppPreviewNode;

const Choice = html`<button
  type="button"
  role="radio"
  aria-checked=${attr<"true" | "false">()}
  class=${cls()}
  onclick=${on<"click">()}
>
  ${child<PreviewNode>()}<span class="choice-label">${text()}${child<IconNode | null>()}</span>
</button>`;
const Choices = html`<section class="appearance-section">
  <h3>${text()}</h3>
  <div class="choice-grid" role="radiogroup" aria-label=${attr()}>
    ${child<ListNode<ViewNodeOf<typeof Choice>, string>>()}
  </div>
</section>`;

/** What `choices` returns. */
export type ChoicesNode = ViewNodeOf<typeof Choices>;

/** A radio group of themes, each shown by its preview; ids are unique within a group. */
function choices(
  label: string,
  items: readonly { id: string; label: string }[],
  current: string,
  preview: (id: string) => PreviewNode,
  choose: (id: string) => void,
): ChoicesNode {
  return Choices([
    label,
    label,
    list(
      items,
      (it) => it.id,
      (it) => {
        const on = it.id === current;
        return Choice([
          on ? "true" : "false",
          on ? "choice active" : "choice",
          () => choose(it.id),
          preview(it.id),
          it.label,
          on ? icon(icons.check) : null,
        ]);
      },
    ),
  ]);
}

const AppearanceDialog = html`<dialog class="dialog appearance" onclose=${on<"close", HTMLDialogElement>()}></dialog>`;
const AppearanceForm = html`<form method="dialog">
  <h2>Appearance</h2>
  <section class="appearance-section">
    <h3>Theme</h3>
    <div class="appearance-theme" onclick=${on<"click">()}>${child<ThemeSwitchNode>()}${child<ChoicesNode>()}</div>
  </section>
  ${child<ChoicesNode>()}${child<ChoicesNode>()}
  <div class="dialog-actions"><button value="ok" class="btn primary">Done</button></div>
</form>`;

/** What `appearanceForm` returns. */
export type AppearanceFormNode = ViewNodeOf<typeof AppearanceForm>;

const termItems = [{ id: matchApp, label: "Match app" }, ...termSchemes];
const codeItems = [{ id: matchApp, label: "Match app" }, ...codeThemes, { id: noHighlight, label: "No highlighting" }];

/** The dialog's form for the current choices; `redraw` renders it again. */
export function appearanceForm(redraw: () => void): AppearanceFormNode {
  const pick = (t: string): void => {
    applyTheme(t);
    store.set({ theme: t });
    redraw();
  };
  return AppearanceForm([
    // The theme switch's buttons have no type, so in this form they would
    // submit it and close the dialog: a choice here never submits.
    (e) => e.preventDefault(),
    themeSwitch(store.app.theme, pick),
    choices("Color themes", appThemes, store.app.theme, appPreview, pick),
    choices("Terminal colors", termItems, loadTermScheme(), termPreview, (id) => {
      applyTermScheme(id);
      redraw();
    }),
    choices("Code highlighting", codeItems, loadCodeTheme(), codePreview, (id) => {
      applyCodeTheme(id);
      store.emit(); // views drawn with or without highlighting redraw
      redraw();
    }),
  ]);
}

export function openAppearance(): void {
  const dlg = element(
    "dialog",
    AppearanceDialog([
      function () {
        this.remove();
      },
    ]),
  );
  const redraw = (): void => render(dlg, appearanceForm(redraw));
  redraw();
  dismissOnBackdrop(dlg);
  document.body.append(dlg);
  dlg.showModal();
}
