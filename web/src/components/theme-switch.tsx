import { For } from "@reactre/core";

import { icons } from "../icons.ts";
import { Icon } from "./icons.tsx";

const themes = [
  { id: "system", label: "System", glyph: icons.monitor },
  { id: "light", label: "Light", glyph: icons.sun },
  { id: "dark", label: "Dark", glyph: icons.moon },
];

export function ThemeSwitch(props: { current: string; choose: (theme: string) => void }) {
  return (
    <div class="theme-switch" role="radiogroup" aria-label="Theme">
      <For each={themes} key={(theme) => theme.id}>
        {(theme) => (
          <button
            type="button"
            role="radio"
            aria-checked={theme.id === props.current ? "true" : "false"}
            class={theme.id === props.current ? "active" : ""}
            title={`${theme.label} theme`}
            onClick={() => props.choose(theme.id)}
          >
            <Icon paths={theme.glyph} />
            <span>{theme.label}</span>
          </button>
        )}
      </For>
    </div>
  );
}
