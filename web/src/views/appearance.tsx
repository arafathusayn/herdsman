import { attach, For, memo, Show } from "@reactre/core";
import type { Child } from "@reactre/core/jsx-runtime";

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
import { Icon } from "../components/icons.tsx";
import { ThemeSwitch } from "../components/theme-switch.tsx";
import { dismissOnBackdrop } from "../dialog-backdrop.ts";
import { tokenizeLines } from "../highlight.ts";
import { icons } from "../icons.ts";
import { valueState } from "../state/value.ts";
import { store } from "../store.ts";
import { applyTheme } from "../ui.ts";

const sample = [
  "// Load reads a route folder.",
  "func Load(dir string) (*Route, error) {",
  '\treturn parse(dir, "00-route.md", 200)',
  "}",
];
const termItems = [{ id: matchApp, label: "Match app" }, ...termSchemes];
const codeItems = [{ id: matchApp, label: "Match app" }, ...codeThemes, { id: noHighlight, label: "No highlighting" }];
const previewClass = (base: string, id: string): string => `${base}${id === matchApp ? " match-app" : ""}`;
const styleOf = (vars: Record<string, string>): string | null => {
  const declarations = Object.entries(vars).map(([key, value]) => `${key}: ${value};`);
  return declarations.length ? declarations.join(" ") : null;
};

function AppPreview(props: { id: string }) {
  return (
    <span class="app-preview" aria-hidden="true" style={styleOf(appVars(props.id) ?? {})}>
      <span class="ap-side">
        <span class="ap-line" />
        <span class="ap-line short" />
      </span>
      <span class="ap-main">
        <span class="ap-line" />
        <span class="ap-line short" />
        <span class="ap-pills">
          <span class="ap-pill" />
          <span class="ap-pill working" />
        </span>
      </span>
    </span>
  );
}

function TermPreview(props: { id: string }) {
  return (
    <span class={previewClass("term-preview", props.id)} style={styleOf(termVars(props.id))}>
      <span class="line">
        <span style="color: var(--t2)">herdsman</span> on <span style="color: var(--t5)"> main</span>
        <span style="color: var(--t1)"> [$]</span>
      </span>
      <span class="swatches">
        <span class="dot" style="background: var(--t1)" />
        <span class="dot" style="background: var(--t2)" />
        <span class="dot" style="background: var(--t3)" />
        <span class="dot" style="background: var(--t4)" />
        <span class="dot" style="background: var(--t5)" />
        <span class="dot" style="background: var(--t6)" />
      </span>
    </span>
  );
}

function sampleCode(id: string): Child {
  if (id === noHighlight) return sample.join("\n");
  const out: Child[] = [];
  tokenizeLines("go", sample).forEach((pieces, i) => {
    if (i) out.push("\n");
    for (const [kind, text] of pieces) out.push(kind ? <span class={`hl-${kind}`}>{text}</span> : text);
  });
  return out;
}

function CodePreview(props: { id: string }) {
  return (
    <pre class={previewClass("code-preview", props.id)} style={styleOf(codeVars(props.id))}>
      <code>
        <>{sampleCode(props.id)}</>
      </code>
    </pre>
  );
}

function Choices(props: {
  label: string;
  items: readonly { id: string; label: string }[];
  current: string;
  preview: (id: string) => Child;
  choose: (id: string) => void;
}) {
  return (
    <section class="appearance-section">
      <h3>{props.label}</h3>
      <div class="choice-grid" role="radiogroup" aria-label={props.label}>
        <For each={props.items} key={(item) => item.id}>
          {(item) => (
            <button
              type="button"
              role="radio"
              aria-checked={item.id === props.current ? "true" : "false"}
              class={item.id === props.current ? "choice active" : "choice"}
              onClick={() => props.choose(item.id)}
            >
              <>{props.preview(item.id)}</>
              <span class="choice-label">
                {item.label}
                <Show when={item.id === props.current}>
                  <Icon paths={icons.check} />
                </Show>
              </span>
            </button>
          )}
        </For>
      </div>
    </section>
  );
}

/** One mounted form: selection signals update controls without recreating previews. */
export function AppearanceForm() {
  const theme = memo(() => store.state().theme);
  const [terminal, setTerminal] = valueState(loadTermScheme());
  const [code, setCode] = valueState(loadCodeTheme());
  const pick = (id: string): void => {
    applyTheme(id);
    store.set({ theme: id });
  };
  return (
    <form method="dialog">
      <h2>Appearance</h2>
      <section class="appearance-section">
        <h3>Theme</h3>
        <div class="appearance-theme">
          <ThemeSwitch current={theme()} choose={pick} />
          <Choices
            label="Color themes"
            items={appThemes}
            current={theme()}
            preview={(id) => <AppPreview id={id} />}
            choose={pick}
          />
        </div>
      </section>
      <Choices
        label="Terminal colors"
        items={termItems}
        current={terminal()}
        preview={(id) => <TermPreview id={id} />}
        choose={(id) => {
          applyTermScheme(id);
          setTerminal(id);
        }}
      />
      <Choices
        label="Code highlighting"
        items={codeItems}
        current={code()}
        preview={(id) => <CodePreview id={id} />}
        choose={(id) => {
          applyCodeTheme(id);
          setCode(id);
        }}
      />
      <div class="dialog-actions">
        <button value="ok" class="btn primary">
          Done
        </button>
      </div>
    </form>
  );
}

export function openAppearance(): void {
  let dialog!: HTMLDialogElement;
  let dispose = (): void => {};
  dispose = attach(
    () => (
      <dialog
        class="dialog appearance"
        ref={(element) => {
          dialog = element;
        }}
        onClose={() => dispose()}
      >
        <AppearanceForm />
      </dialog>
    ),
    document.body,
  );
  dismissOnBackdrop(dialog);
  dialog.showModal();
}
