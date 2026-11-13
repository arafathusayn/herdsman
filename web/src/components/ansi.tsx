import { effect, memo } from "@reactre/core";

import { displayAnsi, type Palette } from "../ansi.ts";

/** Terminal output is native text and spans, including when output resembles markup. */
export function AnsiText(props: { text: string; palette: Palette }) {
  const text = memo(() => props.text);
  const palette = memo(() => props.palette);
  const runs = memo(() => displayAnsi(text(), palette()));
  return (
    <span
      class="ansi-output"
      ref={(host) => {
        // These are DOM handles, not another state snapshot. ANSI output has no
        // interactive children: one effect can patch its native nodes without
        // thousands of per-run owners and disposal callbacks on every panel.
        const nodes: (Text | HTMLSpanElement)[] = [];
        effect(() => {
          const next = runs();
          const added = document.createDocumentFragment();
          for (let i = 0; i < next.length; i++) {
            const run = next[i]!;
            let node = nodes[i];
            if (!node || (node.nodeType === 1) !== run.styled) {
              const replacement = run.styled ? document.createElement("span") : document.createTextNode("");
              if (node) host.replaceChild(replacement, node);
              else added.appendChild(replacement);
              nodes[i] = node = replacement;
            }
            if (node.nodeType === 3) {
              const text = node as Text;
              if (text.data !== run.text) text.data = run.text;
            }
            if (node.nodeType === 1) {
              const span = node as HTMLSpanElement;
              if (span.textContent !== run.text) span.textContent = run.text;
              if (span.className !== run.className) span.className = run.className;
              const color = run.css["color"] ?? "";
              const background = run.css["backgroundColor"] ?? "";
              const css = `color:${color};background-color:${background};`;
              if (span.getAttribute("style") !== css) span.setAttribute("style", css);
            }
          }
          if (added.childNodes.length) host.appendChild(added);
          while (nodes.length > next.length) host.removeChild(nodes.pop()!);
        });
      }}
    />
  );
}
