import { For, memo, Show } from "@reactre/core";

import { displayAnsi, type Palette } from "../ansi.ts";

/** Terminal output is native text and spans, including when output resembles markup. */
export function AnsiText(props: { text: string; palette: Palette }) {
  const text = memo(() => props.text);
  const palette = memo(() => props.palette);
  const runs = memo(() => displayAnsi(text(), palette()));
  return (
    <For each={runs()} keyed={false}>
      {(run) => (
        <Show when={run().styled} fallback={run().text}>
          <span class={run().className} style={run().css}>
            {run().text}
          </span>
        </Show>
      )}
    </For>
  );
}
