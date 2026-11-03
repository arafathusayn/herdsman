import { For, memo, Show } from "@reactre/core";

/** Fuzzy-match highlights are text and compiled marks, never HTML from the input. */
export function Highlight(props: { value: string; hits: readonly number[] }) {
  const pieces = memo(() => {
    const hits = new Set(props.hits);
    const parts: { hit: boolean; text: string }[] = [];
    for (let i = 0; i < props.value.length; i++) {
      const hit = hits.has(i);
      const last = parts.at(-1);
      if (last?.hit === hit) last.text += props.value[i];
      else parts.push({ hit, text: props.value[i]! });
    }
    return parts;
  });
  return (
    <For each={pieces()} keyed={false}>
      {(piece) => (
        <Show when={piece().hit} fallback={piece().text}>
          <mark>{piece().text}</mark>
        </Show>
      )}
    </For>
  );
}
